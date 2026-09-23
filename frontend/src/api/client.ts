import type { Envelope, ErrorCode, FieldError } from './types';

/** How long a request may take before it is given up on. Generous: a
 *  tool call runs on someone else's machine, and the backend applies
 *  its own per-server timeout, which is the one that should decide. */
const DEFAULT_TIMEOUT_MS = 60_000;

const REQUEST_ID_HEADER = 'X-Request-Id';

/**
 * A request that did not succeed, in one shape.
 *
 * Three things can go wrong and a caller usually wants to treat them
 * differently, so all three arrive as this and are told apart by their
 * fields rather than by their type:
 *
 *   - the server refused the request, and said why (`status` >= 400,
 *     `code` and often `fields` from the error envelope);
 *   - the server was never reached (`status` 0);
 *   - the server answered with something that is not an envelope
 *     (`status` >= 400, `code` guessed from the status).
 *
 * The last is worth keeping distinct from the first. It means a proxy
 * or a dev server answered instead of the gateway, and its body is
 * usually HTML — reporting "unexpected token <" would send someone
 * looking in the wrong place.
 */
export class ApiError extends Error {
  readonly code: ErrorCode;
  readonly status: number;
  readonly fields: FieldError[];
  readonly requestId: string | undefined;

  constructor(init: {
    code: ErrorCode;
    message: string;
    status: number;
    fields?: FieldError[];
    requestId?: string | undefined;
  }) {
    super(init.message);
    this.name = 'ApiError';
    this.code = init.code;
    this.status = init.status;
    this.fields = init.fields ?? [];
    this.requestId = init.requestId;
  }

  /** True when the request never reached the gateway. Worth separating
   *  in the UI: retrying is reasonable, and nothing the user typed is
   *  at fault. */
  get isOffline(): boolean {
    return this.status === 0;
  }

  /** True when the gateway rejected the contents of the request. The
   *  settings and server forms use this to mark individual inputs. */
  get isValidation(): boolean {
    return this.code === 'validation_failed' || this.fields.length > 0;
  }
}

export interface RequestOptions {
  /** Sent as JSON. */
  body?: unknown;
  /** Appended to the path; entries with a nullish value are dropped. */
  query?: Record<string, string | number | boolean | undefined | null>;
  signal?: AbortSignal | undefined;
  timeoutMs?: number;
}

function buildURL(path: string, query: RequestOptions['query']): string {
  if (!query) return path;

  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null || value === '') continue;
    params.append(key, String(value));
  }
  const encoded = params.toString();
  return encoded ? `${path}?${encoded}` : path;
}

/** Reads the error envelope, falling back to something truthful when
 *  the body is not one. */
async function failureFrom(response: Response): Promise<ApiError> {
  const requestId = response.headers.get(REQUEST_ID_HEADER) ?? undefined;

  let body: unknown;
  try {
    body = await response.json();
  } catch {
    // Not JSON at all. Something other than the gateway answered.
    return new ApiError({
      code: codeForStatus(response.status),
      message: `服务器返回了无法解析的响应（HTTP ${response.status}）`,
      status: response.status,
      requestId,
    });
  }

  const envelope = body as Partial<Envelope>;
  if (!envelope?.error?.message) {
    return new ApiError({
      code: codeForStatus(response.status),
      message: `请求失败（HTTP ${response.status}）`,
      status: response.status,
      requestId,
    });
  }

  return new ApiError({
    code: envelope.error.code ?? codeForStatus(response.status),
    message: envelope.error.message,
    status: response.status,
    fields: envelope.error.fields ?? [],
    // The envelope's own id is preferred: it is the one written in the
    // server's log next to what actually happened.
    requestId: envelope.error.requestId ?? requestId,
  });
}

function codeForStatus(status: number): ErrorCode {
  switch (status) {
    case 400:
      return 'bad_request';
    case 403:
      return 'forbidden';
    case 404:
      return 'not_found';
    case 409:
      return 'conflict';
    case 422:
      return 'validation_failed';
    case 429:
      return 'too_many_requests';
    case 503:
      return 'unavailable';
    default:
      return 'internal';
  }
}

export async function request<T>(
  method: string,
  path: string,
  options: RequestOptions = {},
): Promise<T> {
  const { body, query, signal, timeoutMs = DEFAULT_TIMEOUT_MS } = options;

  // The caller's cancellation and the timeout both have to be able to
  // abort this, and a request must not outlive the component that asked
  // for it.
  const timeout = AbortSignal.timeout(timeoutMs);
  const abort = signal ? AbortSignal.any([signal, timeout]) : timeout;

  const headers: Record<string, string> = { Accept: 'application/json' };
  if (body !== undefined) headers['Content-Type'] = 'application/json';

  let response: Response;
  try {
    response = await fetch(buildURL(path, query), {
      method,
      headers,
      signal: abort,
      ...(body !== undefined ? { body: JSON.stringify(body) } : {}),
    });
  } catch (cause) {
    // A cancellation the caller asked for is not a failure to report.
    if (signal?.aborted) throw cause;

    const timedOut = timeout.aborted;
    throw new ApiError({
      code: 'unavailable',
      message: timedOut
        ? `请求超时（超过 ${Math.round(timeoutMs / 1000)} 秒）`
        : '无法连接到 mcp-foyer，它可能没有在运行',
      status: 0,
    });
  }

  if (!response.ok) throw await failureFrom(response);

  // 204, and any other answer with nothing in it.
  if (response.status === 204 || response.headers.get('Content-Length') === '0') {
    return undefined as T;
  }
  return (await response.json()) as T;
}

export const api = {
  get: <T>(path: string, options?: RequestOptions) => request<T>('GET', path, options),
  post: <T>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>('POST', path, { ...options, body }),
  put: <T>(path: string, body?: unknown, options?: RequestOptions) =>
    request<T>('PUT', path, { ...options, body }),
  delete: <T>(path: string, options?: RequestOptions) => request<T>('DELETE', path, options),
};
