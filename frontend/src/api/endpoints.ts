import { api } from './client';
import type {
  AggregatedResource,
  AggregatedTool,
  Config,
  ConfigResponse,
  ConfigWriteResponse,
  GatewayStatus,
  GatewayTools,
  UsageSnapshot,
  Health,
  ImportSummary,
  LogQuery,
  LogsResponse,
  MCPServer,
  Resource,
  ResourceReadResult,
  ServerStatus,
  ServerView,
  SessionInfo,
  Tool,
  ToolCallResult,
  ValidationResponse,
} from './types';

const BASE = '/api';

/*
 * One function per endpoint, and the only place a URL is written down.
 * Components ask for what they want; nothing outside this file needs to
 * know that servers live at /api/servers.
 */

export const endpoints = {
  health: () => api.get<Health>(`${BASE}/health`),

  // ===== configuration =====

  getConfig: () => api.get<ConfigResponse>(`${BASE}/config`),

  putConfig: (config: Config) => api.put<ConfigWriteResponse>(`${BASE}/config`, { config }),

  /** Checks without saving, so the settings form can mark bad fields as
   *  they are edited rather than only on submission. */
  validateConfig: (config: Config) =>
    api.post<ValidationResponse>(`${BASE}/config/validate`, { config }),

  // ===== servers =====

  listServers: () =>
    api.get<{ servers: ServerView[] }>(`${BASE}/servers`).then((r) => r.servers),

  getServer: (name: string) =>
    api.get<ServerView>(`${BASE}/servers/${encodeURIComponent(name)}`),

  createServer: (name: string, server: MCPServer) =>
    api.post<ServerView>(`${BASE}/servers`, { name, server }),

  updateServer: (name: string, server: MCPServer) =>
    api.put<ServerView>(`${BASE}/servers/${encodeURIComponent(name)}`, { server }),

  deleteServer: (name: string) =>
    api.delete<void>(`${BASE}/servers/${encodeURIComponent(name)}`),

  connectServer: (name: string) =>
    api.post<ServerStatus>(`${BASE}/servers/${encodeURIComponent(name)}/connect`, undefined, {
      // Connecting starts a child process and waits for its handshake,
      // which is slower than an ordinary request but not unbounded.
      timeoutMs: 120_000,
    }),

  disconnectServer: (name: string) =>
    api.post<ServerStatus>(`${BASE}/servers/${encodeURIComponent(name)}/disconnect`),

  serverTools: (name: string) =>
    api
      .get<{ tools: Tool[] }>(`${BASE}/servers/${encodeURIComponent(name)}/tools`)
      .then((r) => r.tools),

  serverResources: (name: string) =>
    api
      .get<{ resources: Resource[] }>(`${BASE}/servers/${encodeURIComponent(name)}/resources`)
      .then((r) => r.resources),

  // The arguments travel under a key rather than as the body itself.
  // The gateway takes {"arguments": {...}} and rejects unknown fields, so
  // posting the arguments bare fails the request before the tool is ever
  // reached.
  callTool: (server: string, tool: string, args: unknown) =>
    api.post<ToolCallResult>(
      `${BASE}/servers/${encodeURIComponent(server)}/tools/${encodeURIComponent(tool)}/call`,
      { arguments: args },
    ),

  // The gateway's own tools have a route of their own: they belong to no
  // server, so there is no name to put in the path above.
  callGatewayTool: (tool: string, args: unknown) =>
    api.post<ToolCallResult>(`${BASE}/gateway/tools/${encodeURIComponent(tool)}/call`, {
      arguments: args,
    }),

  readResource: (server: string, uri: string) =>
    api.get<ResourceReadResult>(`${BASE}/servers/${encodeURIComponent(server)}/resource`, {
      query: { uri },
    }),

  // The document is sent exactly as pasted. Which fields need
  // translating — "type" against "transport", a stdio server recognised
  // by having a command — is the gateway's business, so that the same
  // file works through curl as through the web interface.
  importServers: (document: string) =>
    api.post<ImportSummary>(`${BASE}/servers/import`, JSON.parse(document) as unknown),

  // ===== aggregated =====

  /** `all` asks for every upstream tool rather than only the exposed
   *  ones. The management interface wants that: it is where exposure is
   *  decided, and it cannot offer a choice about tools it does not show. */
  tools: (options: { search?: string; limit?: number; all?: boolean } = {}) => {
    const query: Record<string, string | number> = {};
    if (options.search) query['q'] = options.search;
    if (options.limit) query['limit'] = options.limit;
    if (options.all) query['all'] = 'true';
    return api
      .get<{ tools: AggregatedTool[]; total: number }>(`${BASE}/tools`, { query })
      .then((r) => r.tools);
  },

  resources: () =>
    api
      .get<{ resources: AggregatedResource[]; total: number }>(
        `${BASE}/resources`,
      )
      .then((r) => r.resources),

  // ===== gateway =====

  gatewayStatus: () => api.get<GatewayStatus>(`${BASE}/gateway/status`),

  gatewaySessions: () =>
    api
      .get<{ sessions: SessionInfo[]; total: number }>(`${BASE}/gateway/sessions`)
      .then((r) => r.sessions),

  gatewayTools: () => api.get<GatewayTools>(`${BASE}/gateway/tools`),

  gatewayUsage: () => api.get<UsageSnapshot>(`${BASE}/gateway/usage`),

  resetGatewayUsage: () => api.delete<void>(`${BASE}/gateway/usage`),

  // ===== logs =====

  logs: (query: LogQuery = {}) =>
    api.get<LogsResponse>(`${BASE}/logs`, {
      query: {
        server: query.server,
        module: query.module,
        level: query.level,
        since: query.since,
        limit: query.limit,
      },
    }),

  clearLogs: (server?: string) =>
    api.delete<void>(`${BASE}/logs`, { query: server ? { server } : {} }),
};
