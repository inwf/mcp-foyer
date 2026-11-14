import { useEffect, useMemo, useRef, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { App, Popconfirm, Segmented, Select, Skeleton, Switch, Tooltip } from 'antd';
import { ClearOutlined, CopyOutlined, ReloadOutlined } from '@ant-design/icons';
import { endpoints } from '@/api/endpoints';
import { keys } from '@/api/query';
import { LOG_LEVELS, LOG_MODULES, type LogEntry, type LogLevel } from '@/api/types';
import { ErrorNotice } from '@/components/ErrorNotice';
import { IconButton } from '@/components/IconButton';
import { Nothing } from '@/components/Nothing';
import { clockTime } from '@/lib/format';
import { cx } from '@/lib/cx';
import styles from './LogView.module.css';

/** How often the log is re-read while following. Logs are the one thing
 *  here the event stream does not push — a gateway writing a line per
 *  request would flood it — so this is the one place that polls. */
const FOLLOW_MS = 2_000;

const LEVEL_CLASS: Record<LogLevel, string | undefined> = {
  debug: styles.debug,
  info: styles.info,
  warn: styles.warn,
  error: styles.error,
};

function Line({ entry }: { entry: LogEntry }) {
  const attrs = Object.entries(entry.attrs ?? {});

  return (
    <div className={cx(styles.line, LEVEL_CLASS[entry.level])}>
      <span className={styles.at}>{clockTime(entry.time)}</span>
      <span className={styles.level}>{entry.level}</span>
      <span className={styles.origin}>{entry.server ?? entry.module ?? ''}</span>
      <span className={styles.message}>
        {entry.message}
        {attrs.length > 0 ? (
          <span className={styles.attrs}>
            {attrs.map(([key, value]) => (
              <span key={key}>
                {' '}
                <span className={styles.attrKey}>{key}</span>={value}
              </span>
            ))}
          </span>
        ) : null}
      </span>
    </div>
  );
}

/**
 * The log, filtered.
 *
 * Used both on its own page and on a server's detail page. When a server
 * is given, its filter is fixed and the server selector is hidden —
 * otherwise the page for `files` would offer to show the log for
 * `fetch`, which is not what someone navigated there for.
 */
export function LogView({
  server,
  actions,
}: {
  server?: string | undefined;
  actions?: boolean;
}) {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const queryClient = useQueryClient();

  const [level, setLevel] = useState<LogLevel | ''>('');
  const [module, setModule] = useState<string>('');
  const [pickedServer, setPickedServer] = useState<string>('');
  const [following, setFollowing] = useState(true);
  const [limit, setLimit] = useState(500);

  const stream = useRef<HTMLDivElement>(null);

  const filter = useMemo(
    () => ({
      ...(server ? { server } : pickedServer ? { server: pickedServer } : {}),
      ...(module ? { module } : {}),
      ...(level ? { level } : {}),
      limit,
    }),
    [server, pickedServer, module, level, limit],
  );

  const logs = useQuery({
    queryKey: keys.logs.query(filter),
    queryFn: () => endpoints.logs(filter),
    ...(following ? { refetchInterval: FOLLOW_MS } : {}),
  });

  const clear = useMutation({
    mutationFn: () => endpoints.clearLogs(server ?? pickedServer ?? undefined),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: keys.logs.all }),
  });

  const entries = logs.data?.entries ?? [];

  // Following means the newest line stays in view. Only while following:
  // scrolling back to read something and being yanked to the bottom two
  // seconds later is the single most annoying thing a log viewer can do.
  useEffect(() => {
    if (!following || !stream.current) return;
    stream.current.scrollTop = stream.current.scrollHeight;
  }, [entries.length, following]);

  const copy = () => {
    const text = entries
      .map(
        (e) => `${e.time} ${e.level.toUpperCase()} ${e.server ?? e.module ?? ''} ${e.message}`,
      )
      .join('\n');
    void navigator.clipboard.writeText(text).then(
      () => void message.success(t('call.copied')),
      () => void message.error(t('error.unknown')),
    );
  };

  return (
    <div className={styles.view}>
      <div className={styles.bar}>
        <Segmented
          aria-label={t('logs.level')}
          value={level}
          onChange={(value) => setLevel(value as LogLevel | '')}
          options={[
            { label: t('logs.all'), value: '' },
            ...LOG_LEVELS.map((value) => ({ label: value, value })),
          ]}
        />

        <Select
          aria-label={t('logs.module')}
          value={module}
          onChange={setModule}
          style={{ minWidth: 136 }}
          options={[
            { label: `${t('logs.module')}: ${t('logs.all')}`, value: '' },
            ...LOG_MODULES.map((value) => ({ label: value, value })),
          ]}
        />

        {server ? null : (
          <Select
            aria-label={t('logs.server')}
            value={pickedServer}
            onChange={setPickedServer}
            style={{ minWidth: 152 }}
            options={[
              { label: `${t('logs.server')}: ${t('logs.all')}`, value: '' },
              ...(logs.data?.servers ?? []).map((value) => ({ label: value, value })),
            ]}
          />
        )}

        <Select
          aria-label={t('logs.limit')}
          value={limit}
          onChange={setLimit}
          style={{ minWidth: 112 }}
          options={[200, 500, 1000, 2000].map((value) => ({
            label: `${t('logs.limit')} ${value}`,
            value,
          }))}
        />

        <span className={styles.spacer} />

        <Tooltip title={t('logs.follow')}>
          <span style={{ display: 'inline-flex', alignItems: 'center', gap: 'var(--space-2)' }}>
            <Switch
              aria-label={t('logs.follow')}
              size="small"
              checked={following}
              onChange={setFollowing}
            />
            <span className="label">{t('logs.follow')}</span>
          </span>
        </Tooltip>

        <IconButton
          label={t('common.refresh')}
          icon={<ReloadOutlined />}
          loading={logs.isFetching && !following}
          onClick={() => void logs.refetch()}
        />

        <IconButton
          label={t('logs.copy')}
          icon={<CopyOutlined />}
          onClick={copy}
          disabled={entries.length === 0}
        />

        {actions !== false ? (
          <Popconfirm
            title={t('logs.clearConfirm')}
            okText={t('common.confirm')}
            cancelText={t('common.cancel')}
            okButtonProps={{ danger: true }}
            onConfirm={() => clear.mutate()}
          >
            <IconButton
              label={t('logs.clear')}
              icon={<ClearOutlined />}
              danger
              loading={clear.isPending}
            />
          </Popconfirm>
        ) : null}
      </div>

      {logs.isError ? (
        <ErrorNotice error={logs.error} onRetry={() => void logs.refetch()} />
      ) : (
        <div
          className={styles.stream}
          ref={stream}
          role="region"
          aria-label={t('logs.stream')}
          tabIndex={0}
        >
          {logs.isPending ? (
            <div className={styles.loading}>
              <Skeleton active paragraph={{ rows: 5 }} title={false} />
            </div>
          ) : entries.length === 0 ? (
            <Nothing title={t('logs.empty')} />
          ) : (
            entries.map((entry, index) => (
              // The store has no id for a log record, and two records
              // can share a timestamp; the index within a fetched page
              // is stable for as long as that page is on screen.
              <Line key={`${entry.time}-${index}`} entry={entry} />
            ))
          )}
        </div>
      )}
    </div>
  );
}
