import { useMemo, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import {
  App,
  Button,
  Input,
  Popconfirm,
  Segmented,
  Select,
  Skeleton,
  Switch,
  Tooltip,
} from 'antd';
import { SearchOutlined, ThunderboltOutlined } from '@ant-design/icons';
import { endpoints } from '@/api/endpoints';
import { keys } from '@/api/query';
import type { AggregatedTool, ServerView, Tool, UsageEntry } from '@/api/types';
import { Panel } from '@/components/Panel';
import { PageHeading } from '@/components/PageHeading';
import { StateBadge } from '@/components/StateBadge';
import { ErrorNotice } from '@/components/ErrorNotice';
import { Nothing } from '@/components/Nothing';
import { ToolCallDialog } from '@/components/ToolCallDialog';
import { useExposure } from '@/hooks/use-exposure';
import { useToolLayoutStore, type ToolLayout } from '@/stores/tool-layout';
import { SYSTEM_GROUP, useToolCollapseStore } from '@/stores/tool-collapse';
import { cx } from '@/lib/cx';
import { fullTime } from '@/lib/format';
import { stagger } from '@/lib/motion';
import motion from '@/styles/motion.module.css';
import styles from './Tools.module.css';

/**
 * Every tool on offer, grouped by where it comes from.
 *
 * One group per server, and one for the gateway's own tools — list_servers
 * and the rest, which belong to no server and are called through a route
 * of their own. A single list interleaves servers, and the question
 * someone brings to this page is nearly always about one of them: what
 * does this server offer, did it come up, why is it not contributing
 * anything.
 *
 * That last question is why groups with nothing in them are kept. A
 * server contributing no tools is indistinguishable from a server that
 * was never configured if it simply does not appear, so an empty group
 * says which of the reasons applies instead of leaving a gap.
 */

type Translate = ReturnType<typeof useTranslation>['t'];

/** How many forwarded tools to ask for. The endpoint reports the count it
 *  returned rather than the count it had, so a full page is the only sign
 *  that there was more — which is why the number is needed here and not
 *  only in the request. */
const LIMIT = 200;

/** A server's tools, or the gateway's own. */
interface Group {
  /** The server's name, or '' for the gateway itself. */
  server: string;
  tools: AggregatedTool[];
  /** The server as configured and as connected. Absent for the gateway,
   *  which is not a server. */
  view?: ServerView | undefined;
  /** Where this group's best tool sits in the gateway's ordering. Under a
   *  search that ordering is a relevance ranking, and it is what puts the
   *  server that actually answered the search at the top. */
  rank: number;
}

/** What the tools are ordered by within a group. */
type ToolOrder = 'name' | 'usage';

/** Usage counts keyed by origin, which is how a tool is identified: every
 *  unexposed tool shares the empty exposed name. */
type UsageMap = Map<string, UsageEntry>;

function originKey(server: string, tool: string): string {
  return `${server}/${tool}`;
}

/**
 * How much a model has used this tool, in a phrase.
 *
 * The counts are what the page is ultimately for: an operator deciding
 * what to expose wants to know what gets reached for, and one wondering
 * why a tool is never chosen wants to know whether it is ever found. A
 * tool with no entry has never been either, and says so rather than
 * showing zeros that look like a measurement.
 */
function UsageBadge({ entry }: { entry: UsageEntry | undefined }) {
  const { t } = useTranslation();
  if (!entry) {
    return <span className={styles.usage}>{t('tools.usageNever')}</span>;
  }
  const phrase = t('tools.usage', { called: entry.called, searched: entry.searched });
  return (
    <Tooltip title={entry.lastCalled ? fullTime(entry.lastCalled) : undefined}>
      <span className={styles.usage}>
        {phrase}
        {entry.failed > 0 ? (
          <>
            {' · '}
            <span className={styles.usageFailed}>
              {t('tools.usageFailed', { count: entry.failed })}
            </span>
          </>
        ) : null}
      </span>
    </Tooltip>
  );
}

/**
 * Orders a group's tools by how much they are used, most first. Ties,
 * and every tool with no use at all, keep the order they arrived in —
 * which is the gateway's relevance order under a search and its name
 * order otherwise.
 */
function byUsage(tools: AggregatedTool[], usage: UsageMap): AggregatedTool[] {
  const weight = (tool: AggregatedTool) => {
    const entry = usage.get(originKey(tool.server, tool.tool));
    return entry ? entry.called * 1000 + entry.searched : 0;
  };
  return tools
    .map((tool, index) => ({ tool, index, weight: weight(tool) }))
    .sort((a, b) => b.weight - a.weight || a.index - b.index)
    .map(({ tool }) => tool);
}

/** Turns an aggregated tool into what the call dialog takes. The dialog
 *  works in terms of one server's tool, since that is what the call
 *  endpoint addresses. */
function asTool(aggregated: AggregatedTool): Tool {
  return {
    name: aggregated.tool,
    ...(aggregated.description !== undefined ? { description: aggregated.description } : {}),
    ...(aggregated.inputSchema !== undefined ? { inputSchema: aggregated.inputSchema } : {}),
  };
}

/**
 * Filters the gateway's own tools by the search text.
 *
 * The forwarded tools are searched by the gateway, which scores and
 * orders them. That endpoint aggregates upstream servers and so does not
 * see these, which leaves the choice between filtering them here and
 * letting a search quietly apply to only half of what is on offer. Every
 * term has to match, which is the rule the gateway's own search uses.
 */
function matching(tools: AggregatedTool[], search: string): AggregatedTool[] {
  const terms = search.toLowerCase().split(/\s+/).filter(Boolean);
  if (terms.length === 0) return tools;

  return tools.filter((tool) => {
    const haystack = `${tool.exposed} ${tool.description ?? ''}`.toLowerCase();
    return terms.every((term) => haystack.includes(term));
  });
}

/**
 * Why a server's group has nothing in it.
 *
 * Four facts leading to four different things to do about it: enable the
 * server, read the error, wait for the connection, or accept that it has
 * no tools. "No tools" alone would leave all four looking the same.
 *
 * "Has tools but exposes none" is not among them, because this page lists
 * every tool a server offers rather than only the exposed ones — that is
 * the state most servers are in, and it is a state with something to click
 * rather than something to explain.
 *
 * A group without a server view only exists because it has tools in it,
 * so there is nothing to explain in that case.
 */
function emptyHint(
  view: ServerView | undefined,
  truncated: boolean,
  t: Translate,
): string | undefined {
  if (!view) return undefined;

  // The list stops at a limit, and it is ordered by exposed name — so on
  // a large installation the servers late in the alphabet are cut off
  // wholesale. Any of the answers below would be a confident lie.
  if (truncated) return t('tools.groupTruncated');

  const { config, status } = view;
  if (!config.enabled) return t('tools.groupDisabled');
  if (status.state === 'failed') return t('tools.groupFailed');
  if (status.state !== 'connected') return t('tools.groupOffline');
  if (!status.hasTools) return t('tools.groupNoCapability');
  return t('tools.groupNoTools');
}

/**
 * The exposure switch, in whichever layout is on screen.
 *
 * One component because the two layouts must not drift into disagreeing
 * about what a switch does — and because what it writes is the server's
 * whole allow list, which is a thing to get wrong in exactly one place.
 */
function ExposeSwitch({ tool, view }: { tool: AggregatedTool; view: ServerView }) {
  const { t } = useTranslation();
  const expose = useExposure();

  const on = tool.exposed !== '';
  const busy = expose.isPending && expose.variables?.tool === tool.tool;

  return (
    <Tooltip title={on ? t('tools.unexpose') : t('tools.expose')}>
      <Switch
        size="small"
        checked={on}
        loading={busy}
        disabled={expose.isPending}
        onChange={(next) =>
          expose.mutate({ server: view.name, config: view.config, tool: tool.tool, on: next })
        }
        aria-label={`${t('tools.expose')} ${tool.tool}`}
      />
    </Tooltip>
  );
}

function ToolCard({
  tool,
  index,
  view,
  usage,
  onCall,
}: {
  tool: AggregatedTool;
  index: number;
  /** The server this tool belongs to, absent for the gateway's own. */
  view?: ServerView | undefined;
  /** How models have used it; absent for the gateway's own, which are
   *  not counted. */
  usage?: UsageEntry | undefined;
  onCall: () => void;
}) {
  const { t } = useTranslation();

  // The gateway prefixes and, on a collision, renames. What a client must
  // call is `exposed`, which is not always derivable from the upstream
  // name. A gateway tool has no server and is never renamed.
  const renamed =
    tool.server !== '' && tool.exposed !== '' && !tool.exposed.endsWith(tool.tool);
  const on = tool.exposed !== '';

  return (
    <div
      className={cx(styles.card, motion.enter, !on && tool.server !== '' && styles.cardOff)}
      style={stagger(index)}
    >
      <div className={styles.head}>
        {/* The name a client calls, where there is one. An unexposed
              tool has none, so its own name is the headline instead —
              rather than an empty line where a name should be. */}
        <span className={styles.exposed}>{tool.exposed || tool.tool}</span>
        {view ? <ExposeSwitch tool={tool} view={view} /> : null}
      </div>

      {tool.description ? <p className={styles.description}>{tool.description}</p> : null}

      {tool.server !== '' ? (
        <div className={styles.cardUsage}>
          <UsageBadge entry={usage} />
        </div>
      ) : null}

      <div className={styles.foot}>
        {tool.server === '' ? (
          <span className={styles.origin}>{t('tools.builtIn')}</span>
        ) : on ? (
          <Tooltip title={`${t('tools.from')} ${tool.server} · ${tool.tool}`}>
            <span className={cx(styles.origin, renamed && styles.renamed)}>{tool.tool}</span>
          </Tooltip>
        ) : (
          // The upstream name is already the headline for an unexposed
          // tool, so repeating it here would say nothing. What is worth
          // saying is the state, in words rather than only as a switch
          // and a dashed border.
          <span className={styles.origin}>{t('server.notExposed')}</span>
        )}
        <Button size="small" icon={<ThunderboltOutlined aria-hidden />} onClick={onCall}>
          {t('tools.call')}
        </Button>
      </div>
    </div>
  );
}

/**
 * One tool per row.
 *
 * The same facts as a card, in the order they are scanned rather than
 * read: the name a client calls, the name on the server, then what it
 * does. A description is one line here — someone in this layout is
 * looking for a tool, and the full text is a click away in the call
 * dialog.
 */
function ToolRow({
  tool,
  index,
  view,
  usage,
  onCall,
}: {
  tool: AggregatedTool;
  index: number;
  view?: ServerView | undefined;
  usage?: UsageEntry | undefined;
  onCall: () => void;
}) {
  const { t } = useTranslation();

  const on = tool.exposed !== '';
  const renamed = tool.server !== '' && on && !tool.exposed.endsWith(tool.tool);

  return (
    <div
      role="listitem"
      className={cx(styles.row, motion.fade, !on && tool.server !== '' && styles.rowOff)}
      style={stagger(index)}
    >
      <span className={styles.rowName}>{tool.exposed || tool.tool}</span>

      {/* The provenance column. A gateway tool has no server; an unexposed
          one has no second name to show, since the headline is already its
          own — so it says what its state is instead. */}
      {tool.server === '' ? (
        <span className={styles.rowOrigin}>{t('tools.builtIn')}</span>
      ) : on ? (
        <span className={cx(styles.rowOrigin, renamed && styles.renamed)}>{tool.tool}</span>
      ) : (
        <span className={styles.rowOrigin}>{t('server.notExposed')}</span>
      )}

      <span className={styles.rowDescription} title={tool.description}>
        {tool.description ?? ''}
      </span>

      <span className={styles.rowUsage}>
        {tool.server !== '' ? <UsageBadge entry={usage} /> : null}
      </span>

      <span className={styles.rowActions}>
        {view ? <ExposeSwitch tool={tool} view={view} /> : null}
        <Button size="small" icon={<ThunderboltOutlined aria-hidden />} onClick={onCall}>
          {t('tools.call')}
        </Button>
      </span>
    </div>
  );
}

/**
 * A group's tools, in whichever layout was chosen.
 *
 * Both layouts show every tool and offer the same two actions on each. A
 * layout that quietly left something out would make the choice between
 * them a choice about what the page tells you, which is not what a view
 * toggle is for.
 */
function ToolGroup({
  tools,
  view,
  layout,
  usage,
  onCall,
}: {
  tools: AggregatedTool[];
  view?: ServerView | undefined;
  layout: ToolLayout;
  usage: UsageMap;
  onCall: (tool: AggregatedTool) => void;
}) {
  const { t } = useTranslation();
  const usageOf = (tool: AggregatedTool) => usage.get(originKey(tool.server, tool.tool));
  // Keyed by origin, not by exposed name: every unexposed tool has the
  // same empty one.
  if (layout === 'list') {
    return (
      <div className={styles.list}>
        <div className={styles.listHead} aria-hidden="true">
          <span>{t('tools.name')}</span>
          <span className={styles.rowOrigin}>{t('tools.upstreamName')}</span>
          <span>{t('tools.summary')}</span>
          <span className={styles.rowUsage}>{t('tools.usageColumn')}</span>
          <span>{t('servers.actions')}</span>
        </div>
        <div role="list">
          {tools.map((tool, index) => (
            <ToolRow
              key={`${tool.server}/${tool.tool}`}
              tool={tool}
              index={index}
              view={view}
              usage={usageOf(tool)}
              onCall={() => onCall(tool)}
            />
          ))}
        </div>
      </div>
    );
  }

  return (
    <div className={styles.grid}>
      {tools.map((tool, index) => (
        <ToolCard
          key={`${tool.server}/${tool.tool}`}
          tool={tool}
          index={index}
          view={view}
          usage={usageOf(tool)}
          onCall={() => onCall(tool)}
        />
      ))}
    </div>
  );
}

export default function Tools() {
  const { t } = useTranslation();

  const [search, setSearch] = useState('');
  const [server, setServer] = useState('');
  const [order, setOrder] = useState<ToolOrder>('name');
  const [calling, setCalling] = useState<AggregatedTool | null>(null);
  const layout = useToolLayoutStore((state) => state.layout);
  const setLayout = useToolLayoutStore((state) => state.setLayout);
  const collapsed = useToolCollapseStore((state) => state.collapsed);
  const toggleGroup = useToolCollapseStore((state) => state.toggle);
  const queryClient = useQueryClient();
  const { message } = App.useApp();

  // The search runs on the gateway rather than here: it scores matches
  // across every connected server, and the result order is that score.
  const tools = useQuery({
    queryKey: keys.tools.aggregated(search, true),
    queryFn: () => endpoints.tools({ ...(search ? { search } : {}), limit: LIMIT, all: true }),
  });

  // What the models have done with the tools. Not on the critical path:
  // a page without the counts is still the page, so its failure is not
  // one of the page's.
  const usageQuery = useQuery({
    queryKey: keys.gateway.usage(),
    queryFn: () => endpoints.gatewayUsage(),
  });
  const usage = useMemo<UsageMap>(() => {
    const map: UsageMap = new Map();
    for (const entry of usageQuery.data?.entries ?? []) {
      map.set(originKey(entry.server, entry.tool), entry);
    }
    return map;
  }, [usageQuery.data]);

  const resetUsage = useMutation({
    mutationFn: () => endpoints.resetGatewayUsage(),
    onSuccess: () => {
      message.success(t('tools.resetUsageDone'));
      void queryClient.invalidateQueries({ queryKey: keys.gateway.usage() });
    },
    onError: (error: unknown) => {
      message.error(error instanceof Error ? error.message : t('error.unknown'));
    },
  });

  // The gateway's own tools come from the endpoint that reports what its
  // MCP server publishes, because they are on no upstream server and the
  // aggregated endpoint therefore cannot see them.
  const gateway = useQuery({
    queryKey: keys.gateway.tools(),
    queryFn: () => endpoints.gatewayTools(),
  });

  // The server list, for the groups. The tools alone would name only the
  // servers that contributed one, which is precisely the set that needs
  // no explanation.
  const servers = useQuery({
    queryKey: keys.servers.list(),
    queryFn: () => endpoints.listServers(),
  });

  const system = useMemo<AggregatedTool[]>(() => {
    if (!gateway.data) return [];
    const own = new Set(gateway.data.systemTools);
    const listed = gateway.data.tools
      .filter((tool) => own.has(tool.name))
      .map((tool) => ({
        server: '',
        tool: tool.name,
        exposed: tool.name,
        ...(tool.description !== undefined ? { description: tool.description } : {}),
        ...(tool.inputSchema !== undefined ? { inputSchema: tool.inputSchema } : {}),
      }));
    return matching(listed, search);
  }, [gateway.data, search]);

  const groups = useMemo<Group[]>(() => {
    const forwarded = tools.data ?? [];
    const views = servers.data ?? [];

    // Bucketed in arrival order, so the gateway's ordering survives
    // inside each group as well as between them.
    const buckets = new Map<string, AggregatedTool[]>();
    const ranks = new Map<string, number>();

    forwarded.forEach((tool, index) => {
      const bucket = buckets.get(tool.server);
      if (bucket) {
        bucket.push(tool);
        return;
      }
      buckets.set(tool.server, [tool]);
      ranks.set(tool.server, index);
    });

    // A tool whose server is not in the list would otherwise vanish.
    // That should not happen, and dropping tools without saying so would
    // be a worse way to find out than an unadorned group.
    const known = new Set(views.map((view) => view.name));
    const orphans = [...buckets.keys()]
      .filter((name) => name !== '' && !known.has(name))
      .sort();

    const named: { name: string; view?: ServerView | undefined }[] = [
      ...views.map((view) => ({ name: view.name, view })),
      ...orphans.map((name) => ({ name })),
    ];

    return named.map(({ name, view }) => ({
      server: name,
      tools: order === 'usage' ? byUsage(buckets.get(name) ?? [], usage) : (buckets.get(name) ?? []),
      view,
      // A server that contributed nothing sorts last under a search,
      // which is where a group with no answer in it belongs.
      rank: ranks.get(name) ?? Number.MAX_SAFE_INTEGER,
    }));
  }, [tools.data, servers.data, order, usage]);

  const shown = useMemo(() => {
    // Picking a server asks about that server, so the others go — empty
    // or not.
    const picked = server === '' ? groups : groups.filter((group) => group.server === server);

    // A search asks a question, and a server with no answer to it is not
    // part of the answer. Without one, every group stays: an empty one is
    // then reporting the state of its server rather than a lack of match.
    if (search.trim() === '') return picked;

    return picked
      .filter((group) => group.tools.length > 0)
      .sort((left, right) => left.rank - right.rank);
  }, [groups, server, search]);

  // The gateway's own tools are on no server, so a picked server is not a
  // question they answer.
  const shownSystem = server === '' ? system : [];

  const pending = tools.isPending || gateway.isPending || servers.isPending;
  const failure = tools.error ?? gateway.error ?? servers.error;
  const truncated = (tools.data ?? []).length >= LIMIT;

  // A search asks to see something, so a group holding a match is opened
  // whatever it was left at. Folding is about putting away what is not the
  // question; under a search everything on screen is the question. The
  // remembered state is untouched, so clearing the search puts each group
  // back where it was.
  const searching = search.trim() !== '';
  const isOpen = (key: string) => searching || !collapsed.includes(key);
  const heading = <PageHeading title={t('tools.title')} description={t('tools.description')} />;

  if (failure) {
    return (
      <div className={styles.page}>
        {heading}
        <ErrorNotice
          error={failure}
          onRetry={() => {
            void tools.refetch();
            void gateway.refetch();
            void servers.refetch();
          }}
        />
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
          aria-label={t('tools.search')}
          allowClear
          placeholder={t('tools.search')}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className={cx(styles.search)}
        />
        <Select
          aria-label={t('logs.server')}
          value={server}
          onChange={setServer}
          style={{ minWidth: 170 }}
          options={[
            { label: `${t('logs.server')}: ${t('logs.all')}`, value: '' },
            ...groups.map((group) => ({ label: group.server, value: group.server })),
          ]}
        />
        <span className={styles.spacer} />
        <Segmented<ToolOrder>
          value={order}
          onChange={setOrder}
          aria-label={t('tools.order')}
          options={[
            { label: t('tools.byName'), value: 'name' },
            { label: t('tools.byUsage'), value: 'usage' },
          ]}
        />
        <Segmented<ToolLayout>
          value={layout}
          onChange={setLayout}
          aria-label={t('tools.layout')}
          options={[
            { label: t('tools.asCards'), value: 'cards' },
            { label: t('tools.asList'), value: 'list' },
          ]}
        />
      </div>

      {pending ? (
        <Panel title={t('tools.title')}>
          <Skeleton active paragraph={{ rows: 5 }} title={false} />
        </Panel>
      ) : (
        <>
          {shownSystem.length > 0 ? (
            // Collapsible like the rest, but nothing puts it away by
            // default: under progressive disclosure these are the only
            // tools a client is shown without being asked, so they are
            // what a first visit should land on.
            <Panel
              title={t('tools.system')}
              flush={layout === 'list'}
              count={shownSystem.length}
              actions={<span className={styles.mark}>{t('tools.builtIn')}</span>}
              collapsible
              open={isOpen(SYSTEM_GROUP)}
              onToggle={() => toggleGroup(SYSTEM_GROUP)}
            >
              <ToolGroup tools={shownSystem} layout={layout} usage={usage} onCall={setCalling} />
            </Panel>
          ) : null}

          {shown.map((group) => (
            <Panel
              key={group.server}
              flush={layout === 'list'}
              title={<span className={styles.serverName}>{group.server}</span>}
              // An empty group's body is the sentence explaining why it is
              // empty, which is the only thing it has to say. Folding that
              // away would leave a heading and no answer.
              collapsible={group.tools.length > 0}
              open={isOpen(group.server)}
              onToggle={() => toggleGroup(group.server)}
              // The pair, not the count: the gateway offers nothing it has
              // not been asked to, so "3" alone reads as all this server
              // has. "3 / 9" says how much was asked for.
              count={
                group.view
                  ? t('tools.exposedRatio', {
                      count: group.view.exposedCount,
                      total: group.view.status.toolCount,
                    })
                  : group.tools.length
              }
              actions={
                group.view ? (
                  <StateBadge
                    state={group.view.status.state}
                    error={group.view.status.error}
                    enabled={group.view.config.enabled}
                  />
                ) : undefined
              }
            >
              {group.tools.length === 0 ? (
                <Nothing
                  title={t('tools.groupEmpty')}
                  hint={emptyHint(group.view, truncated, t)}
                />
              ) : (
                <ToolGroup
                  tools={group.tools}
                  view={group.view}
                  layout={layout}
                  usage={usage}
                  onCall={setCalling}
                />
              )}
            </Panel>
          ))}

          {/* Where the counts come from and what they leave out, so that a
              small number is not read as a small installation. Offered
              only once there is something to reset. */}
          {usageQuery.data && usageQuery.data.entries.length > 0 ? (
            <p className={styles.usageNote}>
              <span>{t('tools.usageSince', { since: fullTime(usageQuery.data.since) })}</span>
              <Popconfirm
                title={t('tools.resetUsage')}
                okText={t('common.confirm')}
                cancelText={t('common.cancel')}
                onConfirm={() => resetUsage.mutate()}
              >
                <Button size="small" type="text" loading={resetUsage.isPending}>
                  {t('tools.resetUsage')}
                </Button>
              </Popconfirm>
            </p>
          ) : null}

          {/* No group at all is the first-run case; no group shown while
              some exist means the search or the filter matched nothing.
              They are different things to say. */}
          {groups.length === 0 ? (
            <Panel title={t('tools.fromServers')}>
              <Nothing title={t('tools.empty')} hint={t('tools.emptyHint')} />
            </Panel>
          ) : shown.length === 0 ? (
            <Panel title={t('tools.fromServers')}>
              <Nothing title={t('tools.noMatch')} />
            </Panel>
          ) : null}

          {/* A list that stopped at the limit looks exactly like a
              complete one, and every count above it would be wrong. */}
          {truncated ? (
            <p className={styles.truncated}>{t('tools.truncated', { count: LIMIT })}</p>
          ) : null}
        </>
      )}

      <ToolCallDialog
        open={calling !== null}
        onClose={() => setCalling(null)}
        server={calling?.server ?? ''}
        tool={calling ? asTool(calling) : null}
      />
    </div>
  );
}
