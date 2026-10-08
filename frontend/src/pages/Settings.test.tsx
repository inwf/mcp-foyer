import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { setupServer } from 'msw/node';
import { http, HttpResponse } from 'msw';
import { renderWithProviders } from '@/test/harness';
import Settings from './Settings';

const api = setupServer();
beforeAll(() => api.listen({ onUnhandledRequest: 'error' }));
afterEach(() => api.resetHandlers());
afterAll(() => api.close());

/*
 * The settings form edits part of a document and sends back the whole
 * thing, which is the arrangement that loses settings: anything the form
 * does not carry through is replaced by whatever the backend defaults to.
 *
 * That is not hypothetical. Until the logging and startup sections were
 * spread, they were rebuilt field by field — so a switch the form had
 * never heard of was dropped by the act of saving something else, and
 * nothing said so. What is pinned here is that saving preserves what the
 * form does not show.
 */

const CONFIG = {
  version: 1,
  listen: { host: '127.0.0.1', port: 7788 },
  logging: {
    level: 'info',
    format: 'console',
    maxAge: '168h',
    maxSizeMB: 50,
    mcpWireDebug: false,
    apiDebug: false,
    gatewayDebug: true,
    showTraceContext: false,
  },
  security: {
    allowedNetworks: ['127.0.0.1/32'],
    maxConnections: 100,
    maxConcurrentRequests: 50,
    connectionTimeout: '30s',
    idleConnectionTimeout: '5m',
  },
  gateway: {
    defaultSessionMode: 'stateful',
    // Rules with something in them, not `{}`. An empty fixture would let
    // the two "does not lose settings" tests below pass on a form that
    // dropped these entirely — there would be nothing to drop.
    sessionModeRules: { stateful: ['claude'], stateless: ['curl'] },
    sessionTimeout: '30m',
    notifyDebounce: '300ms',
    keepAlive: '30s',
    keepAliveFailureThreshold: 3,
  },
  startup: { connectDelay: '0s', maxRetries: 3, retryBackoff: '2s' },
  // The shape the backend sends: `enabled`, `timeout` and
  // `minSimilarity` are always there, the rest appear only once they have
  // a value.
  embedding: { enabled: false, timeout: '15s', minSimilarity: 0.35 },
  mcpServers: {},
};

// saved captures the body of the write, which is the thing under test:
// what the form sends is what the file becomes.
function editing(config: object = CONFIG) {
  const saved: { body?: { config: Record<string, unknown> } } = {};

  api.use(
    http.get('/api/config', () =>
      HttpResponse.json({ config, path: '/data/config.yaml' }),
    ),
    http.put('/api/config', async ({ request }) => {
      saved.body = (await request.json()) as { config: Record<string, unknown> };
      return HttpResponse.json({ config, changes: [] });
    }),
    // Every render of this page asks for the index status. A test that
    // cares about the answer replaces this after calling editing().
    http.get('/api/embedding/status', () =>
      HttpResponse.json({ enabled: false, indexed: 0, total: 0, building: false }),
    ),
  );

  renderWithProviders(<Settings />);
  return saved;
}

async function save() {
  const button = await screen.findByRole('button', { name: /保存/ });
  await userEvent.click(button);
}

describe('the settings form', () => {
  it('shows what the configuration says', async () => {
    editing();

    expect(await screen.findByDisplayValue('127.0.0.1')).toBeInTheDocument();
    expect(screen.getByDisplayValue('7788')).toBeInTheDocument();
  });

  // The two debug switches are per-layer on purpose, and a switch nobody
  // can find is a switch nobody uses.
  it('offers the gateway and trace-context switches in their current state', async () => {
    editing();

    const gateway = await screen.findByRole('switch', { name: /记录 gateway 模块细节/ });
    const trace = screen.getByRole('switch', { name: /显示追踪标识/ });

    expect(gateway).toBeChecked();
    expect(trace).not.toBeChecked();
  });

  it('sends a switch that was turned on', async () => {
    const saved = editing();

    await userEvent.click(await screen.findByRole('switch', { name: /显示追踪标识/ }));
    await save();

    await waitFor(() => expect(saved.body).toBeDefined());
    expect(saved.body?.config).toMatchObject({
      logging: { showTraceContext: true, gatewayDebug: true },
    });
  });

  // A listening port only takes effect on the next start, and the page
  // says so. Scanning response changes for a listen.* field is how it
  // knows — which is the one place a malformed wire shape would surface
  // as a missing message rather than a crash.
  it('says a listen change needs a restart', async () => {
    editing();
    api.use(
      http.put('/api/config', () =>
        // Matches the shape the backend actually sends, with the tags
        // that make it lowercase.
        HttpResponse.json({
          config: CONFIG,
          changes: [{ field: 'listen.port', old: '7788', new: '9000' }],
        }),
      ),
    );

    await userEvent.click(await screen.findByRole('button', { name: /保存/ }));
    expect(await screen.findByText(/需要重启 mcp-foyer 才会生效/)).toBeInTheDocument();
  });

  // The guard for the whole arrangement: a field the form does not
  // render has to survive a save that changed something else.
  it('keeps a logging setting the form does not show', async () => {
    const saved = editing({
      ...CONFIG,
      logging: { ...CONFIG.logging, somethingNewer: 'kept' },
    });

    await userEvent.click(await screen.findByRole('switch', { name: /记录 API 细节/ }));
    await save();

    await waitFor(() => expect(saved.body).toBeDefined());
    expect(saved.body?.config).toMatchObject({
      logging: { somethingNewer: 'kept', apiDebug: true },
    });
  });

  it('keeps a startup setting the form does not show', async () => {
    const saved = editing({
      ...CONFIG,
      startup: { ...CONFIG.startup, readyEverything: 'kept' },
    });

    await userEvent.click(await screen.findByRole('switch', { name: /记录 API 细节/ }));
    await save();

    await waitFor(() => expect(saved.body).toBeDefined());
    expect(saved.body?.config).toMatchObject({ startup: { readyEverything: 'kept' } });
  });

  it('keeps a gateway setting the form does not show', async () => {
    const saved = editing({
      ...CONFIG,
      gateway: { ...CONFIG.gateway, somethingNewer: 'kept' },
    });

    await userEvent.click(await screen.findByRole('switch', { name: /记录 API 细节/ }));
    await save();

    await waitFor(() => expect(saved.body).toBeDefined());
    expect(saved.body?.config).toMatchObject({ gateway: { somethingNewer: 'kept' } });
  });

  it('keeps an embedding setting the form does not show', async () => {
    const saved = editing({
      ...CONFIG,
      embedding: { enabled: false, timeout: '15s', somethingNewer: 'kept' },
    });

    await userEvent.click(await screen.findByRole('switch', { name: /记录 API 细节/ }));
    await save();

    await waitFor(() => expect(saved.body).toBeDefined());
    expect(saved.body?.config).toMatchObject({
      embedding: { timeout: '15s', somethingNewer: 'kept' },
    });
  });

  it('shows the embedding settings and sends them back', async () => {
    const saved = editing({
      ...CONFIG,
      embedding: {
        enabled: true,
        url: 'https://api.example.com/v1',
        model: 'bge-m3',
        timeout: '15s',
      },
    });

    expect(await screen.findByDisplayValue('https://api.example.com/v1')).toBeInTheDocument();
    expect(screen.getByDisplayValue('bge-m3')).toBeInTheDocument();
    expect(screen.getByRole('switch', { name: /使用嵌入模型/ })).toBeChecked();

    await save();

    await waitFor(() => expect(saved.body).toBeDefined());
    expect(saved.body?.config).toMatchObject({
      embedding: {
        enabled: true,
        url: 'https://api.example.com/v1',
        model: 'bge-m3',
        timeout: '15s',
      },
    });
  });

  it('turns the embedding model on', async () => {
    const saved = editing();

    await userEvent.click(await screen.findByRole('switch', { name: /使用嵌入模型/ }));
    await save();

    await waitFor(() => expect(saved.body).toBeDefined());
    expect(saved.body?.config).toMatchObject({ embedding: { enabled: true } });
  });

  // A stored key reaches the form as its placeholder and goes back the
  // same way. That spelling means "keep it", so the form must not turn
  // it into a new key by touching it.
  it('sends a stored key back as the placeholder', async () => {
    const saved = editing({
      ...CONFIG,
      embedding: {
        enabled: true,
        url: 'https://api.example.com/v1',
        model: 'bge-m3',
        apiKey: '[redacted]',
        timeout: '15s',
      },
    });

    expect(await screen.findByDisplayValue('[redacted]')).toBeInTheDocument();
    await save();

    await waitFor(() => expect(saved.body).toBeDefined());
    expect(saved.body?.config).toMatchObject({ embedding: { apiKey: '[redacted]' } });
  });
});

/*
 * Which clients get a stateful session.
 *
 * The three levels have been in the gateway all along — a request header
 * outranks a User-Agent rule, which outranks the default mode — and the
 * type carried the rules through this page untouched. What was missing was
 * anywhere to type one: the only way to add a keyword was the raw YAML
 * tab, and the priority order was written down nowhere a person editing
 * this would see it.
 */
describe('the session mode rules', () => {
  /** The list editor under this label. Both lists are the same control, so
   *  a query by role alone cannot tell them apart — and the two mode names
   *  appear in the segmented control and the explanation as well, so the
   *  label has to be matched whole. */
  function list(label: string): HTMLElement {
    const field = screen.getByText(label).closest('.ant-form-item');
    if (!field) throw new Error(`no form field is labelled ${label}`);
    return field as HTMLElement;
  }

  const STATEFUL = '用 stateful 的客户端关键词';
  const STATELESS = '用 stateless 的客户端关键词';

  it('shows the keywords the configuration already has', async () => {
    editing();

    await screen.findByDisplayValue('127.0.0.1');
    expect(within(list(STATEFUL)).getByDisplayValue('claude')).toBeInTheDocument();
    expect(within(list(STATELESS)).getByDisplayValue('curl')).toBeInTheDocument();
  });

  it('sends a keyword that was added', async () => {
    const saved = editing();

    await screen.findByDisplayValue('127.0.0.1');
    await userEvent.click(within(list(STATELESS)).getByRole('button', { name: /添加/ }));
    // The new row is the empty one; the existing keyword is in the first.
    const rows = within(list(STATELESS)).getAllByRole('textbox');
    await userEvent.type(rows[rows.length - 1]!, 'python-httpx');
    await save();

    await waitFor(() => expect(saved.body).toBeDefined());
    expect(saved.body?.config).toMatchObject({
      gateway: { sessionModeRules: { stateful: ['claude'], stateless: ['curl', 'python-httpx'] } },
    });
  });

  it('sends a list emptied of its last keyword', async () => {
    const saved = editing();

    await screen.findByDisplayValue('127.0.0.1');
    await userEvent.click(within(list(STATEFUL)).getByRole('button', { name: /移除/ }));
    await save();

    await waitFor(() => expect(saved.body).toBeDefined());
    // Empty rather than gone: for these two the empty list and the absent
    // one mean the same thing, so nothing is lost by sending it — and a
    // key that vanished would look like a save that failed halfway.
    expect(saved.body?.config).toMatchObject({
      gateway: { sessionModeRules: { stateful: [], stateless: ['curl'] } },
    });
  });

  // A rule that is quietly overridden by a request header is the thing
  // someone would come to this page to work out. Saying the order beats
  // leaving them to find it in the gateway's source.
  it('says what outranks what', async () => {
    editing();

    await screen.findByDisplayValue('127.0.0.1');
    const explanation = screen.getByText(/x-mcp-session-mode/);
    expect(explanation).toHaveTextContent('请求头');
    expect(explanation).toHaveTextContent('默认模式');
  });
});

/*
 * The embedding probe.
 *
 * It asks the server, not the browser: a stored key is one this page
 * never sees in clear, and a service this page could reach from a
 * browser is not necessarily one the backend can reach from where it
 * runs.
 */
describe('the embedding probe', () => {
  const STORED = {
    ...CONFIG,
    embedding: {
      enabled: true,
      url: 'https://api.example.com/v1',
      model: 'bge-m3',
      timeout: '15s',
    },
  };

  it('sends what the form holds and shows what came back', async () => {
    const probed: { body?: { embedding: Record<string, unknown> } } = {};
    api.use(
      http.post('/api/embedding/test', async ({ request }) => {
        probed.body = (await request.json()) as { embedding: Record<string, unknown> };
        return HttpResponse.json({ ok: true, latencyMs: 42, dimension: 1024 });
      }),
    );
    editing(STORED);

    await screen.findByDisplayValue('https://api.example.com/v1');
    await userEvent.click(screen.getByRole('button', { name: '测试' }));

    await waitFor(() => expect(probed.body).toBeDefined());
    expect(probed.body?.embedding).toMatchObject({
      url: 'https://api.example.com/v1',
      model: 'bge-m3',
    });
    expect(await screen.findByText(/1024 维/)).toBeInTheDocument();
  });

  it('shows the reason a probe failed', async () => {
    api.use(
      http.post('/api/embedding/test', () =>
        HttpResponse.json({ ok: false, error: 'connection refused' }),
      ),
    );
    editing(STORED);

    await screen.findByDisplayValue('https://api.example.com/v1');
    await userEvent.click(screen.getByRole('button', { name: '测试' }));

    expect(await screen.findByText(/connection refused/)).toBeInTheDocument();
  });
});

/*
 * How far the index has got.
 *
 * The page polls this while a build runs. What matters here is that the
 * count is shown when there is one, that a failure carries its reason,
 * and that the line stays out of the way while the section is off.
 */
describe('the embedding index status', () => {
  const STORED = {
    ...CONFIG,
    embedding: {
      enabled: true,
      url: 'https://api.example.com/v1',
      model: 'bge-m3',
      timeout: '15s',
    },
  };

  it('shows how far the build has got', async () => {
    editing(STORED);
    api.use(
      http.get('/api/embedding/status', () =>
        HttpResponse.json({ enabled: true, indexed: 3, total: 10, building: true }),
      ),
    );

    expect(await screen.findByText('已建索引 3/10')).toBeInTheDocument();
    expect(screen.getByText('构建中')).toBeInTheDocument();
  });

  it('shows a failure with its reason', async () => {
    editing(STORED);
    api.use(
      http.get('/api/embedding/status', () =>
        HttpResponse.json({
          enabled: true,
          indexed: 0,
          total: 10,
          building: false,
          lastError: 'connection refused',
        }),
      ),
    );

    expect(await screen.findByText(/最近一次失败：connection refused/)).toBeInTheDocument();
  });

  it('says nothing while the section is off', async () => {
    editing();

    await screen.findByDisplayValue('127.0.0.1');
    expect(screen.queryByText(/已建索引/)).not.toBeInTheDocument();
  });
});

/*
 * The preview.
 *
 * One query ranked both ways, which is how the similarity floor gets
 * chosen: the page cannot pick a value for the model, so it shows what
 * the current one does to this directory.
 */
describe('the embedding preview', () => {
  const STORED = {
    ...CONFIG,
    embedding: {
      enabled: true,
      url: 'https://api.example.com/v1',
      model: 'bge-m3',
      timeout: '15s',
      minSimilarity: 0.35,
    },
  };

  it('asks for one query and shows both rankings', async () => {
    const asked: { body?: { query?: string } } = {};
    api.use(
      http.post('/api/embedding/preview', async ({ request }) => {
        asked.body = (await request.json()) as { query?: string };
        return HttpResponse.json({
          enabled: true,
          model: 'bge-m3',
          literal: [{ server: 'issues', tool: 'list_issues', matched: 2, score: 1200 }],
          fused: [
            { server: 'issues', tool: 'list_issues', matched: 2, score: 32000 },
            { server: 'jira', tool: 'list_tickets', matched: 0, score: 16667 },
          ],
        });
      }),
    );
    editing(STORED);

    const box = await screen.findByPlaceholderText(/输入一条查询/);
    await userEvent.type(box, 'bug tracking');
    await userEvent.click(screen.getByRole('button', { name: '预览' }));

    await waitFor(() => expect(asked.body).toBeDefined());
    expect(asked.body?.query).toBe('bug tracking');
    // The tool only the meaning found sits in the fused column.
    expect(await screen.findByText('jira/list_tickets')).toBeInTheDocument();
  });
});
