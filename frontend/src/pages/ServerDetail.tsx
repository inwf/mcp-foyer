import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { Button, Popconfirm, Skeleton, Switch, Table, Tabs, Tooltip } from 'antd';
import type { ColumnsType } from 'antd/es/table';
import {
  ArrowLeftOutlined,
  DeleteOutlined,
  EditOutlined,
  EyeOutlined,
  PlayCircleOutlined,
  ReloadOutlined,
  StopOutlined,
  ThunderboltOutlined,
} from '@ant-design/icons';
import { endpoints } from '@/api/endpoints';
import { keys } from '@/api/query';
import type { Resource, ServerView, Tool } from '@/api/types';
import { useServerActions } from '@/hooks/use-server-actions';
import { useExposure } from '@/hooks/use-exposure';
import { DisableToolButton } from '@/components/DisableToolButton';
import { IconButton } from '@/components/IconButton';
import { Panel } from '@/components/Panel';
import { StateBadge } from '@/components/StateBadge';
import { ErrorNotice } from '@/components/ErrorNotice';
import { Nothing } from '@/components/Nothing';
import { LogView } from '@/components/LogView';
import { ServerForm } from '@/components/ServerForm';
import { ToolCallDialog } from '@/components/ToolCallDialog';
import { endpointOf, fullTime, relative } from '@/lib/format';
import { cx } from '@/lib/cx';
import { stagger } from '@/lib/motion';
import motion from '@/styles/motion.module.css';
import styles from './ServerDetail.module.css';

const TABS = ['overview', 'tools', 'resources', 'logs'] as const;
type Tab = (typeof TABS)[number];

function isTab(value: string | undefined): value is Tab {
  return TABS.includes((value ?? '') as Tab);
}

function Fact({ label, value, muted }: { label: string; value: string; muted?: boolean }) {
  return (
    <div className={styles.fact}>
      <span className="label">{label}</span>
      <span className={cx(styles.factValue, muted && styles.factMuted)}>{value}</span>
    </div>
  );
}

function OverviewTab({ server }: { server: ServerView }) {
  const { t } = useTranslation();
  const { status, config } = server;

  const caps: Array<[string, boolean]> = [
    ['tools', status.hasTools],
    ['resources', status.hasResources],
    ['prompts', status.hasPrompts],
    ['logging', status.hasLogging],
  ];

  return (
    <div style={{ display: 'grid', gap: 'var(--space-4)' }}>
      {status.error ? <p className={styles.failure}>{status.error}</p> : null}

      <Panel title={t('server.overview')}>
        <div className={styles.facts}>
          <Fact label={t('form.transport')} value={config.transport} />
          <Fact label={t('form.timeout')} value={config.timeout} />
          <Fact
            label={config.url ? t('form.url') : t('form.command')}
            value={endpointOf(config)}
          />
          <Fact
            label={t('form.enabled')}
            value={config.enabled ? t('common.yes') : t('common.no')}
          />

          <Fact
            label={t('server.pid')}
            value={status.pid ? String(status.pid) : t('server.noProcess')}
            muted={!status.pid}
          />
          <Tooltip title={fullTime(status.startedAt)}>
            <div>
              <Fact
                label={t('server.startedAt')}
                value={status.startedAt ? relative(status.startedAt) : '—'}
                muted={!status.startedAt}
              />
            </div>
          </Tooltip>
          <Tooltip title={fullTime(status.lastCheck)}>
            <div>
              <Fact
                label={t('server.lastCheck')}
                value={status.lastCheck ? relative(status.lastCheck) : t('server.neverChecked')}
                muted={!status.lastCheck}
              />
            </div>
          </Tooltip>

          <Fact
            label={t('server.upstreamName')}
            value={status.serverName ?? '—'}
            muted={!status.serverName}
          />
          <Fact
            label={t('server.upstreamVersion')}
            value={status.serverVersion ?? '—'}
            muted={!status.serverVersion}
          />
          <Fact
            label={t('server.protocol')}
            value={status.protocolVersion ?? '—'}
            muted={!status.protocolVersion}
          />
        </div>
      </Panel>

      <Panel title={t('server.capabilities')}>
        {/* Reported because they explain an empty list: a server may not
            support resources at all, which is not the same as having
            none. */}
        <div className={styles.caps}>
          {caps.map(([name, on]) => (
            <span key={name} className={cx(styles.cap, on && styles.capOn)}>
              {name}
            </span>
          ))}
        </div>
      </Panel>

    </div>
  );
}

function ToolsTab({ server }: { server: ServerView }) {
  const { t } = useTranslation();
  const [selected, setSelected] = useState<string | null>(null);
  const [calling, setCalling] = useState<Tool | null>(null);

  const connected = server.status.state === 'connected';

  const tools = useQuery({
    queryKey: keys.servers.tools(server.name),
    queryFn: () => endpoints.serverTools(server.name),
    enabled: connected,
  });

  const expose = useExposure();

  if (!connected) {
    return <Nothing title={t('state.disconnected')} hint={t('tools.emptyHint')} />;
  }
  if (tools.isPending) return <Skeleton active paragraph={{ rows: 4 }} title={false} />;
  if (tools.isError) {
    return <ErrorNotice error={tools.error} onRetry={() => void tools.refetch()} />;
  }
  if (tools.data.length === 0) return <Nothing title={t('tools.empty')} />;

  const active = tools.data.find((tool) => tool.name === selected) ?? tools.data[0];
  const exposedCount = tools.data.filter((tool) => tool.exposed).length;

  return (
    <div className={styles.tools}>
      <Panel
        title={t('server.tools')}
        count={t('tools.exposedRatio', { count: exposedCount, total: tools.data.length })}
        flush
        className={styles.toolList}
      >
        {tools.data.map((tool, index) => (
          <div
            key={tool.name}
            className={cx(
              styles.toolRow,
              motion.fade,
              tool.name === active?.name && styles.toolOn,
            )}
            style={stagger(index)}
          >
            <button
              type="button"
              className={styles.toolPick}
              aria-pressed={tool.name === active?.name}
              onClick={() => setSelected(tool.name)}
            >
              <span className={styles.toolRowName}>{tool.disabled ? <del>{tool.name}</del> : tool.name}</span>
              {tool.disabled ? <span>{t('tools.disabled')}</span> : null}
              {/* The exposed name is worth showing where it exists, because
                  it is what a client has to call and it is not always the
                  upstream name. */}
              {tool.exposed ? <span className={styles.toolExposed}>{tool.exposed}</span> : null}
            </button>
            <Switch
              size="small"
              checked={Boolean(tool.exposed)}
              loading={expose.isPending && expose.variables?.tool === tool.name}
              disabled={expose.isPending || Boolean(tool.disabled)}
              onChange={(on) =>
                expose.mutate({
                  server: server.name,
                  config: server.config,
                  tool: tool.name,
                  on,
                })
              }
              aria-label={`${t('server.expose')} ${tool.name}`}
            />
            <DisableToolButton server={server} tool={tool.name} disabled={Boolean(tool.disabled)} />
          </div>
        ))}
      </Panel>

      {active ? (
        <Panel title={t('tools.schema')}>
          <div key={active.name} className={motion.fade}>
            <div className={styles.toolHead}>
              <h3 className={styles.toolName}>{active.title ?? active.name}</h3>
              <span style={{ flex: 1 }} />
              <Button
                type="primary"
                icon={<ThunderboltOutlined aria-hidden />}
                disabled={Boolean(active.disabled)}
                onClick={() => setCalling(active)}
              >
                {t('tools.call')}
              </Button>
            </div>
            {active.description ? (
              <p className={styles.toolDescription}>{active.description}</p>
            ) : null}
            <pre className={styles.schema}>
              {JSON.stringify(active.inputSchema ?? {}, null, 2)}
            </pre>
          </div>
        </Panel>
      ) : null}

      <ToolCallDialog
        open={calling !== null}
        onClose={() => setCalling(null)}
        server={server.name}
        tool={calling}
      />
    </div>
  );
}

function ResourcesTab({ server }: { server: ServerView }) {
  const { t } = useTranslation();
  const connected = server.status.state === 'connected';

  const resources = useQuery({
    queryKey: keys.servers.resources(server.name),
    queryFn: () => endpoints.serverResources(server.name),
    enabled: connected,
  });

  if (!connected) {
    return <Nothing title={t('state.disconnected')} hint={t('resources.emptyHint')} />;
  }
  if (resources.isPending) return <Skeleton active paragraph={{ rows: 4 }} title={false} />;
  if (resources.isError) {
    return <ErrorNotice error={resources.error} onRetry={() => void resources.refetch()} />;
  }
  if (resources.data.length === 0) {
    return (
      <Nothing
        title={t('resources.empty')}
        hint={server.status.hasResources ? undefined : t('resources.emptyHint')}
      />
    );
  }

  const columns: ColumnsType<Resource> = [
    {
      title: t('resources.uri'),
      dataIndex: 'uri',
      render: (uri: string) => <span className="mono">{uri}</span>,
    },
    { title: t('servers.name'), render: (_, r) => r.name ?? r.title ?? '—' },
    {
      title: t('resources.mimeType'),
      dataIndex: 'mimeType',
      width: 160,
      render: (value: string | undefined) => <span className="mono">{value ?? '—'}</span>,
    },
    {
      key: 'actions',
      width: 60,
      align: 'right',
      render: (_, resource) => (
        <Link
          to={`/resources?server=${encodeURIComponent(server.name)}&uri=${encodeURIComponent(resource.uri)}`}
        >
          <IconButton label={t('resources.view')} icon={<EyeOutlined aria-hidden />} />
        </Link>
      ),
    },
  ];

  return (
    <Panel title={t('server.resources')} count={resources.data.length} flush>
      <Table
        dataSource={resources.data}
        columns={columns}
        rowKey="uri"
        rowClassName={cx(motion.fade)}
        onRow={(_, index) => ({ style: stagger(index ?? 0) })}
        pagination={false}
      />
    </Panel>
  );
}

export default function ServerDetail() {
  const { t } = useTranslation();
  const { name = '', tab } = useParams();
  const navigate = useNavigate();
  const { connect, disconnect, remove } = useServerActions();
  const [editing, setEditing] = useState(false);

  const server = useQuery({
    queryKey: keys.servers.one(name),
    queryFn: () => endpoints.getServer(name),
    enabled: name !== '',
  });

  const current: Tab = isTab(tab) ? tab : 'overview';

  if (server.isPending) return <Skeleton active paragraph={{ rows: 6 }} />;
  if (server.isError) {
    return <ErrorNotice error={server.error} onRetry={() => void server.refetch()} />;
  }

  const view = server.data;
  const live = view.status.state === 'connected';

  return (
    <div className={styles.page}>
      <div className={styles.head}>
        <Link to="/servers" className={styles.back} aria-label={t('error.back')}>
          <ArrowLeftOutlined />
        </Link>
        <h1 className={styles.name}>{view.name}</h1>
        <StateBadge
          state={view.status.state}
          error={view.status.error}
          enabled={view.config.enabled}
        />

        <span className={styles.spacer} />

        {live ? (
          <>
            <Button
              icon={<ReloadOutlined aria-hidden />}
              loading={connect.isPending}
              onClick={() => connect.mutate(view.name)}
            >
              {t('servers.reconnect')}
            </Button>
            <Button
              icon={<StopOutlined aria-hidden />}
              loading={disconnect.isPending}
              onClick={() => disconnect.mutate(view.name)}
            >
              {t('servers.disconnect')}
            </Button>
          </>
        ) : (
          <Button
            type="primary"
            icon={<PlayCircleOutlined aria-hidden />}
            loading={connect.isPending}
            onClick={() => connect.mutate(view.name)}
          >
            {t('servers.connect')}
          </Button>
        )}

        <Button icon={<EditOutlined aria-hidden />} onClick={() => setEditing(true)}>
          {t('servers.edit')}
        </Button>

        <Popconfirm
          title={t('servers.deleteConfirm', { name: view.name })}
          description={t('servers.deleteHint')}
          okText={t('common.confirm')}
          cancelText={t('common.cancel')}
          okButtonProps={{ danger: true }}
          onConfirm={() =>
            remove.mutate(view.name, {
              // The page it was showing no longer exists.
              onSuccess: () => void navigate('/servers'),
            })
          }
        >
          <Button danger icon={<DeleteOutlined aria-hidden />}>
            {t('servers.delete')}
          </Button>
        </Popconfirm>
      </div>

      {/* The tab is in the URL, so a link to a server's logs is a link
          someone can send. */}
      <Tabs
        activeKey={current}
        onChange={(key) => void navigate(`/servers/${encodeURIComponent(name)}/${key}`)}
        items={[
          {
            key: 'overview',
            label: t('server.overview'),
            children: <OverviewTab server={view} />,
          },
          { key: 'tools', label: t('server.tools'), children: <ToolsTab server={view} /> },
          {
            key: 'resources',
            label: t('server.resources'),
            children: <ResourcesTab server={view} />,
          },
          { key: 'logs', label: t('server.logs'), children: <LogView server={view.name} /> },
        ]}
      />

      <ServerForm
        open={editing}
        onClose={() => setEditing(false)}
        editing={{ name: view.name, server: view.config }}
      />
    </div>
  );
}
