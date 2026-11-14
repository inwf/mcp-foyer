import { useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { Button, Input, Popconfirm, Skeleton, Table, Tooltip } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import {
  DeleteOutlined,
  EditOutlined,
  PlayCircleOutlined,
  ImportOutlined,
  PlusOutlined,
  ReloadOutlined,
  SearchOutlined,
  StopOutlined,
} from '@ant-design/icons';
import { endpoints } from '@/api/endpoints';
import { keys } from '@/api/query';
import type { ServerState, ServerView } from '@/api/types';
import { useServerActions } from '@/hooks/use-server-actions';
import { IconButton } from '@/components/IconButton';
import { Panel } from '@/components/Panel';
import { PageHeading } from '@/components/PageHeading';
import { StateBadge } from '@/components/StateBadge';
import { ErrorNotice } from '@/components/ErrorNotice';
import { Nothing } from '@/components/Nothing';
import { ServerForm } from '@/components/ServerForm';
import { ImportServers } from '@/components/ImportServers';
import { ellipsize, endpointOf } from '@/lib/format';
import { cx } from '@/lib/cx';
import { stagger } from '@/lib/motion';
import motion from '@/styles/motion.module.css';
import styles from './Servers.module.css';

const FILTERS: Array<'all' | ServerState> = [
  'all',
  'connected',
  'connecting',
  'failed',
  'disconnected',
];

/** Matches a server against what was typed. Name, description and
 *  endpoint are all searched, because all three are things someone
 *  might remember a server by. */
function matches(server: ServerView, needle: string): boolean {
  if (!needle) return true;
  const term = needle.toLowerCase();
  return (
    server.name.toLowerCase().includes(term) ||
    (server.config.description ?? '').toLowerCase().includes(term) ||
    endpointOf(server.config).toLowerCase().includes(term)
  );
}

export default function Servers() {
  const { t } = useTranslation();
  const { connect, disconnect, remove } = useServerActions();

  const [search, setSearch] = useState('');
  const [status, setStatus] = useState<'all' | ServerState>('all');
  const [editing, setEditing] = useState<ServerView | null>(null);
  const [adding, setAdding] = useState(false);
  const [importing, setImporting] = useState(false);

  const servers = useQuery({ queryKey: keys.servers.list(), queryFn: endpoints.listServers });

  const shown = useMemo(() => {
    const all = servers.data ?? [];
    return all.filter(
      (server) =>
        (status === 'all' || server.status.state === status) &&
        matches(server, search),
    );
  }, [servers.data, search, status]);

  const filtered = status !== 'all' || search !== '';
  const clearFilters = () => {
    setStatus('all');
    setSearch('');
  };

  const columns: ColumnsType<ServerView> = [
    {
      title: t('servers.name'),
      key: 'name',
      width: 240,
      render: (_, server) => (
        <span className={styles.name}>
          <Link
            to={`/servers/${encodeURIComponent(server.name)}/overview`}
            className={styles.nameLink}
          >
            {server.name}
          </Link>
          {server.config.description ? (
            <span className={styles.description}>{server.config.description}</span>
          ) : null}
        </span>
      ),
    },
    {
      title: t('servers.status'),
      key: 'status',
      width: 136,
      render: (_, server) => (
        <StateBadge
          state={server.status.state}
          error={server.status.error}
          enabled={server.config.enabled}
        />
      ),
    },
    {
      title: t('servers.transport'),
      key: 'transport',
      width: 192,
      render: (_, server) => (
        <span className={styles.name}>
          <span className={styles.transport}>{server.config.transport}</span>
          <Tooltip title={endpointOf(server.config)}>
            <span className={styles.endpoint}>{ellipsize(endpointOf(server.config), 34)}</span>
          </Tooltip>
        </span>
      ),
    },
    {
      title: `${t('servers.tools')} / ${t('servers.resources')}`,
      key: 'counts',
      width: 112,
      render: (_, server) => (
        <span className={styles.counts}>
          <span className={cx(server.status.toolCount === 0 && styles.countZero)}>
            {server.status.toolCount}
          </span>
          <span className={styles.countSeparator}>/</span>
          <span className={cx(server.status.resourceCount === 0 && styles.countZero)}>
            {server.status.resourceCount}
          </span>
        </span>
      ),
    },
    {
      title: t('servers.actions'),
      key: 'actions',
      width: 192,
      align: 'right',
      render: (_, server) => {
        const live = server.status.state === 'connected';
        const busy =
          (connect.isPending && connect.variables === server.name) ||
          (disconnect.isPending && disconnect.variables === server.name);

        return (
          <span className={styles.actions}>
            {live ? (
              <>
                <IconButton
                  label={t('servers.reconnect')}
                  icon={<ReloadOutlined />}
                  loading={busy}
                  onClick={() => connect.mutate(server.name)}
                />
                <IconButton
                  label={t('servers.disconnect')}
                  icon={<StopOutlined />}
                  loading={busy}
                  onClick={() => disconnect.mutate(server.name)}
                />
              </>
            ) : (
              <Button
                size="small"
                icon={<PlayCircleOutlined aria-hidden />}
                loading={busy}
                onClick={() => connect.mutate(server.name)}
              >
                {t('servers.connect')}
              </Button>
            )}

            <Button
              type="text"
              size="small"
              icon={<EditOutlined aria-hidden />}
              onClick={() => setEditing(server)}
            >
              {t('servers.edit')}
            </Button>

            {/* Deleting stops a running process and drops the
                configuration, neither of which can be undone from
                here, so it asks. */}
            <Popconfirm
              title={t('servers.deleteConfirm', { name: server.name })}
              description={t('servers.deleteHint')}
              okText={t('common.confirm')}
              cancelText={t('common.cancel')}
              okButtonProps={{ danger: true }}
              onConfirm={() => remove.mutate(server.name)}
            >
              <IconButton label={t('servers.delete')} icon={<DeleteOutlined />} danger />
            </Popconfirm>
          </span>
        );
      },
    },
  ];

  const addButton = (
    <Button type="primary" icon={<PlusOutlined aria-hidden />} onClick={() => setAdding(true)}>
      {t('servers.add')}
    </Button>
  );

  // Pasting an existing configuration is how most installations start, so
  // it sits beside the add button rather than behind a menu.
  const importButton = (
    <Button icon={<ImportOutlined aria-hidden />} onClick={() => setImporting(true)}>
      {t('servers.import')}
    </Button>
  );

  return (
    <div className={styles.page}>
      <PageHeading
        title={t('servers.title')}
        description={t('servers.description')}
        actions={
          <>
            {importButton}
            {addButton}
          </>
        }
      />
      <div className={styles.bar}>
        <div className={styles.filters} role="group" aria-label={t('servers.filterStatus')}>
          {FILTERS.map((value) => (
            <button
              key={value}
              type="button"
              className={cx(styles.filter, status === value && styles.filterOn)}
              aria-pressed={status === value}
              onClick={() => setStatus(value)}
            >
              {value === 'all' ? t('servers.all') : t(`state.${value}`)}
              <span className={styles.filterCount}>
                {servers.data
                  ? servers.data.filter(
                      (server) => value === 'all' || server.status.state === value,
                    ).length
                  : '—'}
              </span>
            </button>
          ))}
        </div>
        <Input
          type="search"
          prefix={<SearchOutlined aria-hidden />}
          allowClear
          aria-label={t('servers.search')}
          placeholder={t('servers.search')}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className={cx(styles.search)}
        />
      </div>

      {filtered ? (
        <div className={styles.activeFilters}>
          <span className={styles.filterSummary}>
            {t('servers.filtered', { shown: shown.length, total: servers.data?.length ?? 0 })}
          </span>
          <Button type="text" size="small" onClick={clearFilters}>
            {t('servers.clearFilters')}
          </Button>
        </div>
      ) : null}

      <Panel title={t('servers.inventory')} count={shown.length} flush>
        {servers.isPending ? (
          <div style={{ padding: 'var(--space-4)' }}>
            <Skeleton active paragraph={{ rows: 5 }} title={false} />
          </div>
        ) : servers.isError ? (
          <ErrorNotice error={servers.error} onRetry={() => void servers.refetch()} />
        ) : (servers.data ?? []).length === 0 ? (
          <Nothing
            title={t('servers.empty')}
            hint={t('servers.emptyHint')}
            action={
              <span className={styles.emptyActions}>
                {importButton}
                {addButton}
              </span>
            }
          />
        ) : shown.length === 0 ? (
          <Nothing
            title={t('servers.noMatch')}
            action={<Button onClick={clearFilters}>{t('servers.clearFilters')}</Button>}
          />
        ) : (
          <Table
            dataSource={shown}
            columns={columns}
            rowKey="name"
            rowClassName={cx(motion.fade)}
            onRow={(_, index) => ({ style: stagger(index ?? 0) })}
            pagination={false}
            size="middle"
            scroll={{ x: 860 }}
          />
        )}
      </Panel>

      <ImportServers open={importing} onClose={() => setImporting(false)} />
      <ServerForm open={adding} onClose={() => setAdding(false)} />
      <ServerForm
        open={editing !== null}
        onClose={() => setEditing(null)}
        {...(editing ? { editing: { name: editing.name, server: editing.config } } : {})}
      />
    </div>
  );
}
