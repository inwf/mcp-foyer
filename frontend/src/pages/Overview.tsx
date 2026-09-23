import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Link } from 'react-router-dom';
import { App, Button, Skeleton } from 'antd';
import {
  ArrowRightOutlined,
  CodeOutlined,
  CopyOutlined,
  GlobalOutlined,
} from '@ant-design/icons';
import { endpoints } from '@/api/endpoints';
import { keys } from '@/api/query';
import type { EventKind } from '@/api/stream';
import { useEventStore } from '@/stores/events';
import { PageHeading } from '@/components/PageHeading';
import { Panel } from '@/components/Panel';
import { StatCard } from '@/components/StatCard';
import { StateBadge } from '@/components/StateBadge';
import { ErrorNotice } from '@/components/ErrorNotice';
import { Nothing } from '@/components/Nothing';
import { clockTime, endpointOf, fullTime, uptime } from '@/lib/format';
import { cx } from '@/lib/cx';
import { stagger } from '@/lib/motion';
import motion from '@/styles/motion.module.css';
import styles from './Overview.module.css';

const TONE: Partial<Record<EventKind, string | undefined>> = {
  'server.connected': styles.toneOk,
  'server.disconnected': styles.toneWarn,
  'server.failed': styles.toneDanger,
  'toolcall.failed': styles.toneDanger,
};

function Stats() {
  const { t } = useTranslation();
  const status = useQuery({
    queryKey: keys.gateway.status(),
    queryFn: endpoints.gatewayStatus,
  });

  if (status.isPending) {
    return (
      <div className={styles.loading}>
        <Skeleton active paragraph={{ rows: 2 }} title={false} />
      </div>
    );
  }
  if (status.isError) {
    return <ErrorNotice error={status.error} onRetry={() => void status.refetch()} />;
  }

  const { servers, tools, sessions, sessionMode } = status.data;
  return (
    <dl className={styles.stats} aria-label={t('overview.metrics')}>
      <StatCard
        label={t('overview.servers')}
        value={servers.configured}
        note={t('overview.configured')}
      />
      <StatCard
        label={t('overview.connected')}
        value={servers.connected}
        index={1}
        note={
          servers.failed > 0 ? t('overview.failureCount', { count: servers.failed }) : undefined
        }
        noteTone={servers.failed > 0 ? 'danger' : 'plain'}
      />
      <StatCard
        label={t('overview.tools')}
        value={tools}
        index={2}
        note={t('overview.toolsHint')}
      />
      <StatCard label={t('overview.sessions')} value={sessions} index={3} note={sessionMode} />
    </dl>
  );
}

function Activity() {
  const { t } = useTranslation();
  const events = useEventStore((s) => s.events);

  return (
    <Panel
      title={t('overview.activity')}
      actions={
        <Link className={styles.textLink} to="/logs">
          {t('overview.viewLogs')}
        </Link>
      }
      flush
    >
      {events.length === 0 ? (
        <Nothing title={t('overview.noActivity')} hint={t('overview.noActivityHint')} />
      ) : (
        <ol className={styles.events}>
          {events.slice(0, 8).map((event) => (
            <li key={event.id} className={cx(styles.event, motion.slide)}>
              <span className={cx(styles.eventDot, TONE[event.kind])} aria-hidden="true" />
              <div className={styles.eventText}>
                <span>{t(`event.${event.kind}`)}</span>
                {event.server ? (
                  <span className={styles.eventServer}>{event.server}</span>
                ) : null}
              </div>
              <time className={styles.at} dateTime={event.at} title={fullTime(event.at)}>
                {clockTime(event.at)}
              </time>
            </li>
          ))}
        </ol>
      )}
    </Panel>
  );
}

function Servers() {
  const { t } = useTranslation();
  const servers = useQuery({ queryKey: keys.servers.list(), queryFn: endpoints.listServers });
  // Failed connections deserve the first rows; the full inventory remains
  // on the servers page, so a large installation cannot bury the overview.
  const shown = [...(servers.data ?? [])]
    .sort(
      (a, b) =>
        Number(b.status.state === 'failed') - Number(a.status.state === 'failed') ||
        a.name.localeCompare(b.name),
    )
    .slice(0, 8);

  return (
    <Panel
      title={t('overview.upstreams')}
      count={servers.data?.length}
      actions={
        <Link className={styles.textLink} to="/servers">
          {t('overview.viewAll')} <ArrowRightOutlined aria-hidden />
        </Link>
      }
      flush
    >
      {servers.isPending ? (
        <div className={styles.loading}>
          <Skeleton active paragraph={{ rows: 4 }} title={false} />
        </div>
      ) : servers.isError ? (
        <ErrorNotice error={servers.error} onRetry={() => void servers.refetch()} />
      ) : shown.length === 0 ? (
        <Nothing
          title={t('servers.empty')}
          hint={t('servers.emptyHint')}
          action={
            <Link className={styles.textLink} to="/servers">
              {t('servers.add')} <ArrowRightOutlined aria-hidden />
            </Link>
          }
        />
      ) : (
        <div>
          <div className={styles.listHead} aria-hidden="true">
            <span>{t('servers.name')}</span>
            <span>{t('servers.tools')}</span>
            <span>{t('servers.status')}</span>
          </div>
          {shown.map((server, index) => (
            <Link
              key={server.name}
              to={`/servers/${encodeURIComponent(server.name)}/overview`}
              className={cx(styles.serverRow, motion.fade)}
              style={stagger(index)}
            >
              <span className={styles.serverInfo}>
                <span className={styles.serverIcon} aria-hidden="true">
                  {server.config.transport === 'stdio' ? <CodeOutlined /> : <GlobalOutlined />}
                </span>
                <span className={styles.serverText}>
                  <span className={styles.serverName}>{server.name}</span>
                  <span className={styles.serverEndpoint} title={endpointOf(server.config)}>
                    {server.config.description || endpointOf(server.config)}
                  </span>
                </span>
              </span>
              <span className={styles.serverCount}>{server.status.toolCount}</span>
              <StateBadge
                state={server.status.state}
                error={server.status.error}
                enabled={server.config.enabled}
              />
            </Link>
          ))}
        </div>
      )}
    </Panel>
  );
}

function ClientConnection() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  // Use the browser-visible origin so deployments behind a reverse proxy
  // advertise the address clients can actually reach.
  const endpoint = new URL('/mcp', window.location.href).href;
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(endpoint);
      void message.success(t('call.copied'));
    } catch {
      void message.error(t('common.copyFailed'));
    }
  };

  return (
    <Panel title={t('overview.connectClient')}>
      <p className={styles.connectHint}>{t('overview.connectHint')}</p>
      <input
        className={styles.endpoint}
        aria-label={t('overview.endpoint')}
        value={endpoint}
        readOnly
        spellCheck={false}
        onFocus={(event) => event.currentTarget.select()}
      />
      <Button
        className={cx(styles.copy)}
        icon={<CopyOutlined aria-hidden />}
        onClick={() => void copy()}
        block
      >
        {t('overview.copyEndpoint')}
      </Button>
      <p className={styles.endpointHint}>{t('overview.endpointHint')}</p>
    </Panel>
  );
}

function Meta() {
  const { t } = useTranslation();
  const health = useQuery({
    queryKey: keys.health,
    queryFn: endpoints.health,
    refetchInterval: 30_000,
  });
  if (!health.data) return null;
  return (
    <footer className={styles.meta}>
      <span>
        mcp-foyer <strong>{health.data.version}</strong>
      </span>
      <span>
        {t('overview.uptime')} <strong>{uptime(health.data.uptimeSeconds)}</strong>
      </span>
    </footer>
  );
}

export default function Overview() {
  const { t } = useTranslation();
  return (
    <div className={styles.page}>
      <PageHeading
        title={t('overview.title')}
        description={t('overview.description')}
        actions={
          <Link className={styles.primaryLink} to="/servers">
            {t('overview.manageServers')} <ArrowRightOutlined aria-hidden />
          </Link>
        }
      />
      <Stats />
      <div className={styles.workspace}>
        <Servers />
        <div className={styles.aside}>
          <ClientConnection />
          <Activity />
        </div>
      </div>
      <Meta />
    </div>
  );
}
