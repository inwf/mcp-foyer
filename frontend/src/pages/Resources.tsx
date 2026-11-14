import { useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { useSearchParams } from 'react-router-dom';
import { Input, Select, Skeleton } from 'antd';
import { CopyOutlined, SearchOutlined } from '@ant-design/icons';
import { App } from 'antd';
import { endpoints } from '@/api/endpoints';
import { keys } from '@/api/query';
import type { AggregatedResource, ContentBlock } from '@/api/types';
import { Panel } from '@/components/Panel';
import { PageHeading } from '@/components/PageHeading';
import { ErrorNotice } from '@/components/ErrorNotice';
import { Nothing } from '@/components/Nothing';
import { IconButton } from '@/components/IconButton';
import { cx } from '@/lib/cx';
import { stagger } from '@/lib/motion';
import motion from '@/styles/motion.module.css';
import styles from './Resources.module.css';

/** Renders one piece of a resource. Text is the common case; anything
 *  else is shown as it arrived rather than hidden, because a server is
 *  free to return a type this UI has not been taught. */
function Content({ block }: { block: ContentBlock }) {
  if (typeof block.text === 'string')
    return <pre className={cx(styles.content, motion.fade)}>{block.text}</pre>;

  if (typeof block.blob === 'string' && block.mimeType?.startsWith('image/')) {
    return (
      <img
        className={cx(styles.image, motion.fade)}
        src={`data:${block.mimeType};base64,${block.blob}`}
        alt=""
      />
    );
  }
  return (
    <pre className={cx(styles.content, motion.fade)}>{JSON.stringify(block, null, 2)}</pre>
  );
}

function Viewer({ resource }: { resource: AggregatedResource }) {
  const { t } = useTranslation();
  const { message } = App.useApp();

  const read = useQuery({
    queryKey: [...keys.resources.all, 'read', resource.server, resource.uri] as const,
    queryFn: () => endpoints.readResource(resource.server, resource.uri),
  });

  const copy = () => {
    const text = (read.data?.contents ?? [])
      .map((block) => (typeof block.text === 'string' ? block.text : JSON.stringify(block)))
      .join('\n');
    void navigator.clipboard.writeText(text).then(
      () => void message.success(t('call.copied')),
      () => void message.error(t('error.unknown')),
    );
  };

  return (
    <Panel
      title={resource.name || t('resources.content')}
      actions={
        <IconButton
          label={t('call.copy')}
          icon={<CopyOutlined />}
          onClick={copy}
          disabled={!read.data}
        />
      }
    >
      <div className={cx(styles.facts, motion.fade)}>
        <span className={styles.resourceUri}>{resource.uri}</span>
        <span>{resource.server}</span>
        <span>{resource.mimeType ?? '—'}</span>
      </div>

      {read.isPending ? (
        <Skeleton active paragraph={{ rows: 6 }} title={false} />
      ) : read.isError ? (
        <ErrorNotice error={read.error} onRetry={() => void read.refetch()} />
      ) : read.data.contents.length === 0 ? (
        <Nothing title={t('call.empty')} />
      ) : (
        read.data.contents.map((block, index) => <Content key={index} block={block} />)
      )}
    </Panel>
  );
}

export default function Resources() {
  const { t } = useTranslation();

  // The URL carries the selection, so a link to one resource is a link
  // someone can send — which is what the server detail page links to.
  const [params, setParams] = useSearchParams();
  const [search, setSearch] = useState('');

  const fromServer = params.get('server') ?? '';
  const selectedUri = params.get('uri') ?? '';

  const resources = useQuery({
    queryKey: keys.resources.aggregated(),
    queryFn: () => endpoints.resources(),
  });

  const servers = useMemo(
    () => [...new Set((resources.data ?? []).map((r) => r.server))].sort(),
    [resources.data],
  );

  const shown = useMemo(() => {
    const term = search.toLowerCase();
    return (resources.data ?? []).filter(
      (r) =>
        (!fromServer || r.server === fromServer) &&
        (!term ||
          r.uri.toLowerCase().includes(term) ||
          (r.name ?? '').toLowerCase().includes(term) ||
          (r.description ?? '').toLowerCase().includes(term)),
    );
  }, [resources.data, fromServer, search]);

  const selected =
    shown.find((r) => r.uri === selectedUri && (!fromServer || r.server === fromServer)) ??
    shown[0];

  const select = (resource: AggregatedResource) => {
    const next = new URLSearchParams(params);
    next.set('server', resource.server);
    next.set('uri', resource.uri);
    setParams(next, { replace: true });
  };

  const heading = (
    <PageHeading title={t('resources.title')} description={t('resources.description')} />
  );
  if (resources.isPending)
    return (
      <div className={styles.page}>
        {heading}
        <Skeleton active paragraph={{ rows: 6 }} />
      </div>
    );
  if (resources.isError) {
    return (
      <div className={styles.page}>
        {heading}
        <ErrorNotice error={resources.error} onRetry={() => void resources.refetch()} />
      </div>
    );
  }
  if (resources.data.length === 0) {
    return (
      <div className={styles.page}>
        {heading}
        <Panel title={t('resources.title')} count={0}>
          <Nothing title={t('resources.empty')} hint={t('resources.emptyHint')} />
        </Panel>
      </div>
    );
  }

  return (
    <div className={styles.page}>
      {heading}
      <div className={styles.bar}>
        <Input
          type="search"
          prefix={<SearchOutlined aria-hidden />}
          aria-label={t('resources.search')}
          allowClear
          placeholder={t('resources.search')}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className={cx(styles.search)}
        />
        <Select
          aria-label={t('logs.server')}
          value={fromServer}
          onChange={(value) => {
            const next = new URLSearchParams(params);
            if (value) next.set('server', value);
            else next.delete('server');
            // The previous selection may belong to another server.
            next.delete('uri');
            setParams(next, { replace: true });
          }}
          style={{ minWidth: 176 }}
          options={[
            { label: `${t('logs.server')}: ${t('logs.all')}`, value: '' },
            ...servers.map((value) => ({ label: value, value })),
          ]}
        />
      </div>

      <div className={styles.split}>
        <Panel
          title={t('resources.title')}
          count={shown.length}
          className={styles.catalog}
          flush
        >
          {shown.length === 0 ? (
            <Nothing title={t('common.noMatches')} />
          ) : (
            shown.map((resource, index) => (
              <button
                key={`${resource.server}/${resource.uri}`}
                type="button"
                aria-pressed={
                  resource.uri === selected?.uri && resource.server === selected?.server
                }
                className={cx(
                  styles.row,
                  motion.fade,
                  resource.uri === selected?.uri &&
                    resource.server === selected?.server &&
                    styles.rowOn,
                )}
                style={stagger(index)}
                onClick={() => select(resource)}
              >
                <span className={styles.itemText}>
                  <span className={styles.name}>{resource.name || resource.uri}</span>
                  {resource.name ? <span className={styles.uri}>{resource.uri}</span> : null}
                </span>
                <span className={styles.from}>{resource.server}</span>
              </button>
            ))
          )}
        </Panel>

        {selected ? (
          <Viewer key={`${selected.server}/${selected.uri}`} resource={selected} />
        ) : null}
      </div>
    </div>
  );
}
