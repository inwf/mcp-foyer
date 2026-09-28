/*
 * The shapes the management API speaks.
 *
 * These mirror the configuration file exactly: the same field names, and
 * durations as the strings a person writes rather than as numbers. The
 * backend serves the configuration in its file shape for this reason —
 * a duration as an integer has no unit attached to it, and the settings
 * form and the raw-YAML editor would otherwise disagree about what a
 * field is called.
 *
 * A field that is optional here is one the server omits when it has no
 * value, so `undefined` means "not set" rather than "set to nothing".
 */

/** A duration as it is written in the configuration: `30s`, `5m0s`, `168h0m0s`. */
export type Duration = string;

/** An RFC 3339 timestamp. */
export type Timestamp = string;

export const TRANSPORTS = ['stdio', 'streamable-http'] as const;
export type Transport = (typeof TRANSPORTS)[number];

export const LOG_LEVELS = ['debug', 'info', 'warn', 'error'] as const;
export type LogLevel = (typeof LOG_LEVELS)[number];

export const SERVER_STATES = ['disconnected', 'connecting', 'connected', 'failed'] as const;
export type ServerState = (typeof SERVER_STATES)[number];

export type SessionMode = 'stateful' | 'stateless';

// ===== configuration =====

export interface MCPServer {
  transport: Transport;
  enabled: boolean;
  description?: string;
  timeout: Duration;

  /** For stdio, which runs the server as a child process. */
  command?: string;
  args?: string[];
  env?: Record<string, string>;

  /** For streamable-http, which dials a server someone else runs. */
  url?: string;
  headers?: Record<string, string>;
  proxy?: string;


  /** Empty or absent exposes no tools. */
  exposedTools?: string[];
  disabledTools?: string[];
  readyPatterns?: string[];
  readyTimeout?: Duration;
}

export interface Config {
  version: number;
  listen: { host: string; port: number };
  logging: {
    level: LogLevel;
    format: 'console' | 'json';
    maxAge: Duration;
    maxSizeMB: number;
    mcpWireDebug: boolean;
    apiDebug: boolean;
    gatewayDebug: boolean;
    showTraceContext: boolean;
  };
  security: {
    allowedNetworks: string[];
    allowedOrigins?: string[];
    maxConnections: number;
    maxConcurrentRequests: number;
    connectionTimeout: Duration;
    idleConnectionTimeout: Duration;
  };
  gateway: {
    defaultSessionMode: SessionMode;
    sessionModeRules: { stateful?: string[]; stateless?: string[] };
    sessionTimeout: Duration;
    notifyDebounce: Duration;
    keepAlive: Duration;
    keepAliveFailureThreshold: number;
  };
  startup: {
    connectDelay: Duration;
    maxRetries: number;
    retryBackoff: Duration;
  };
  mcpServers: Record<string, MCPServer>;
}

/** The placeholder a hidden secret is handed out as. Sending it back
 *  unchanged means "leave this alone"; the server restores the real
 *  value. Anything else is taken as a new secret. */
export const REDACTED = '[redacted]';

/** The username a hidden URL credential is handed out as. */
export const REDACTED_URL_USER = 'redacted';

export interface ConfigResponse {
  config: Config;
  /** Where the file lives, so the UI can say what it is editing. */
  path: string;
}

export interface ConfigChange {
  /** The dotted path of the setting that changed, the same one the
   *  backend uses in a validation error. */
  field: string;
  /** The previous value, or "(unset)" if the field was absent before. */
  old: string;
  /** The new value, or "(unset)" if the field was removed. */
  new: string;
}

export interface ConfigWriteResponse {
  config: Config;
  changes: ConfigChange[];
}

export interface ValidationResponse {
  valid: boolean;
  fields: FieldError[];
}

// ===== servers =====

export interface ServerStatus {
  name: string;
  state: ServerState;
  /** Set when the state is `failed`. */
  error?: string;
  /** Absent when the server has never been reached for. */
  lastCheck?: Timestamp;
  toolCount: number;
  resourceCount: number;
  /** Set only for transports that run the server as a child process. */
  pid?: number;
  startedAt?: Timestamp;
  /** What the far side called itself during the handshake. */
  serverName?: string;
  serverVersion?: string;
  protocolVersion?: string;
  /** Declared capabilities. They explain an empty list: a server may
   *  not support resources at all, which is not the same as having
   *  none. */
  hasTools: boolean;
  hasResources: boolean;
  hasPrompts: boolean;
  hasLogging: boolean;
}

export interface ServerView {
  name: string;
  config: MCPServer;
  status: ServerStatus;
  /** How many of the server's tools the gateway actually offers in its
   *  `tools/list`. The gateway computes this: it needs the configured
   *  list and the tools the server currently has, and only it holds
   *  both. Normally well below `status.toolCount` — that is the design. */
  exposedCount: number;
  disabledCount?: number;
}

// ===== tools and resources =====

export interface Tool {
  name: string;
  title?: string;
  description?: string;
  inputSchema?: unknown;
  disabled?: boolean;
  /** The name the gateway offers this tool under, absent when it is not
   *  offered at all. The gateway works it out — collisions between
   *  servers are resolved by renaming, so only something holding every
   *  server's configuration can say what a tool is called. */
  exposed?: string;
}

export interface Resource {
  uri: string;
  name?: string;
  title?: string;
  description?: string;
  mimeType?: string;
}

export interface AggregatedTool {
  /** Where the tool came from, and its name there. */
  server: string;
  tool: string;
  /** The name the gateway offers it under, after conflict resolution. */
  exposed: string;
  description?: string;
  inputSchema?: unknown;
  disabled?: boolean;
  /** Set when the list came from a search, and orders it. */
  score?: number;
}

export interface AggregatedResource {
  server: string;
  uri: string;
  exposed: string;
  name?: string;
  description?: string;
  mimeType?: string;
}

/** One piece of a tool result or a resource read. The shape is the MCP
 *  protocol's, which is a union discriminated by `type`. */
export interface ContentBlock {
  type?: 'text' | 'image' | 'audio' | 'resource' | 'resource_link' | string;
  text?: string;
  /** Base64, on a tool result's image or audio block. */
  data?: string;
  /** Base64, on a resource whose contents are not text. */
  blob?: string;
  mimeType?: string;
  uri?: string;
  [key: string]: unknown;
}

export interface ToolCallResult {
  /** A tool that ran and reported a problem is a successful call with a
   *  failed result. Both need showing, and differently. */
  isError: boolean;
  content: ContentBlock[];
  structuredContent?: unknown;
}

export interface ResourceReadResult {
  contents: ContentBlock[];
}

// ===== gateway =====

export interface GatewayStatus {
  servers: { configured: number; connected: number; failed: number };
  /** How many tools the gateway publishes, which is not the sum of the
   *  upstreams': it adds its own and applies each server's exposure
   *  rules. */
  tools: number;
  sessions: number;
  sessionMode: SessionMode;
}

export interface SessionInfo {
  id: string;
  clientName?: string;
  clientVersion?: string;
  protocolVersion?: string;
}

/** One entry of an imported document: the server's name, and why it was
 *  not imported when it was not. */
export interface ImportResult {
  name: string;
  error?: string;
}

/** What a partially successful import answers with. A document from which
 *  nothing could be imported is a failed request instead, and its per-entry
 *  reasons arrive as the envelope's field errors. */
export interface ImportSummary {
  results: ImportResult[];
  imported: number;
  failed: number;
}

export interface GatewayTools {
  tools: Tool[];
  total: number;
  systemTools: string[];
}

// ===== usage =====

/** How the connected models have used one tool. Calls made from this
 *  interface are not counted: the question is what a model reaches for. */
export interface UsageEntry {
  server: string;
  tool: string;
  /** How often search_tools returned it to a model, on the page it saw. */
  searched: number;
  /** How often a model called it, through call_tool or by its exposed name. */
  called: number;
  /** How many of those calls the tool reported as failed. */
  failed: number;
  lastCalled?: Timestamp;
}

export interface UsageSnapshot {
  /** When counting began, or was last reset. */
  since: Timestamp;
  entries: UsageEntry[];
}

// ===== logs =====

export const LOG_MODULES = [
  'config',
  'upstream',
  'gateway',
  'api',
  'ws',
  'cli',
  'server',
] as const;
export type LogModule = (typeof LOG_MODULES)[number];

export interface LogEntry {
  time: Timestamp;
  level: LogLevel;
  message: string;
  module?: string;
  server?: string;
  attrs?: Record<string, string>;
}

export interface LogQuery {
  server?: string;
  module?: string;
  level?: LogLevel;
  since?: string;
  limit?: number;
}

export interface LogsResponse {
  entries: LogEntry[];
  /** Which servers have logs, for the filter to offer. */
  servers: string[];
}

// ===== health =====

export interface Health {
  status: string;
  version: string;
  startedAt: Timestamp;
  uptimeSeconds: number;
  connections?: number;
}

// ===== errors =====

export const ERROR_CODES = [
  'bad_request',
  'validation_failed',
  'not_found',
  'conflict',
  'forbidden',
  'too_many_requests',
  'unavailable',
  'internal',
] as const;
export type ErrorCode = (typeof ERROR_CODES)[number];

export interface FieldError {
  /** A dotted path into the configuration, such as `listen.port` or
   *  `mcpServers.files.command`, so a form can mark the right input. */
  field: string;
  message: string;
}

export interface Failure {
  code: ErrorCode;
  message: string;
  requestId?: string;
  fields?: FieldError[];
}

export interface Envelope {
  error: Failure;
}
