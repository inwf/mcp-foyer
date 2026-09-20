import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { setupServer } from 'msw/node';
import { http, HttpResponse } from 'msw';
import { renderWithProviders } from '@/test/harness';
import type { UsageSnapshot } from '@/api/types';
import { useToolLayoutStore } from '@/stores/tool-layout';
import { useToolCollapseStore } from '@/stores/tool-collapse';
import Tools from './Tools';

const api = setupServer();
beforeAll(() => api.listen({ onUnhandledRequest: 'error' }));
afterEach(() => api.resetHandlers());
afterAll(() => api.close());

// Two things this page got wrong, both of them omissions.
//
// The gateway serves its own tools to every client alongside the forwarded
// ones, and this page showed only the forwarded ones — so the tools a
// model uses to find its way around an installation were invisible here
// while being served perfectly well over MCP.
//
// And the forwarded ones were one flat list. Which server a tool came
// from was a word on its card, so "what does this server offer" meant
// reading every card, and "why is this server contributing nothing" had
// no answer on the page at all.

const FORWARDED = [
  {
    server: 'files',
    tool: 'read',
    exposed: 'files_read',
    description: 'read a file from disk',
  },
  {
    server: 'bing',
    tool: 'search',
    exposed: 'bing_search',
    description: 'search the web',
  },
];

const OWN = {
  tools: [
    { name: 'list_servers', description: 'list the configured servers' },
    { name: 'search_tools', description: 'search for tools across every server' },
    // What the gateway publishes includes the forwarded tools too, and
    // those must not be counted twice: systemTools is what says which are
    // its own.
    { name: 'files_read', description: 'read a file from disk' },
  ],
  total: 3,
  systemTools: ['list_servers', 'search_tools'],
};

/** A server as the API reports it: what was configured, and what came of
 *  connecting to it. */
function view(name: string, overrides: Record<string, unknown> = {}) {
  const { enabled = true, exposedCount = 0, ...status } = overrides;
  return {
    name,
    config: { transport: 'stdio', enabled, timeout: '30s', command: 'x' },
    exposedCount,
    status: {
      name,
      state: 'connected',
      toolCount: 1,
      resourceCount: 0,
      hasTools: true,
      hasResources: false,
      ...status,
    },
  };
}

const VIEWS = [view('files'), view('bing')];

/** Usage counts as the API reports them; empty unless a test is about
 *  them. */
const NO_USAGE: UsageSnapshot = { since: '2026-09-19T08:00:00Z', entries: [] };

function serving(
  forwarded = FORWARDED,
  own = OWN,
  views: unknown[] = VIEWS,
  usage: UsageSnapshot = NO_USAGE,
) {
  api.use(
    http.get('/api/tools', () =>
      HttpResponse.json({ tools: forwarded, total: forwarded.length }),
    ),
    http.get('/api/gateway/tools', () => HttpResponse.json(own)),
    http.get('/api/servers', () => HttpResponse.json({ servers: views })),
    http.get('/api/gateway/usage', () => HttpResponse.json(usage)),
  );
}

/** The panel whose heading is this name. Server names appear in the
 *  server filter's options as well, so a group has to be found by its
 *  heading rather than by the text alone. */
function group(name: string): HTMLElement {
  const heading = screen.getByRole('heading', { name });
  const panel = heading.closest('section');
  if (!panel) throw new Error(`the heading for ${name} is not inside a panel`);
  return panel;
}

describe("the gateway's own tools", () => {
  it('are shown', async () => {
    serving();
    renderWithProviders(<Tools />);

    expect(await screen.findByText('list_servers')).toBeInTheDocument();
    expect(screen.getByText('search_tools')).toBeInTheDocument();
  });

  it('are shown in a group of their own, not under a server', async () => {
    serving();
    renderWithProviders(<Tools />);

    await screen.findByText('list_servers');
    expect(within(group('系统工具')).getByText('list_servers')).toBeInTheDocument();
    expect(within(group('files')).queryByText('list_servers')).not.toBeInTheDocument();
  });

  // The gateway publishes its own tools and the forwarded ones through
  // the same endpoint. Taking all of them would list every forwarded tool
  // a second time, under the group that says they belong to no server.
  it('do not include the forwarded ones', async () => {
    serving();
    renderWithProviders(<Tools />);

    await screen.findByText('list_servers');
    expect(screen.getAllByText('files_read')).toHaveLength(1);
  });

  it('are called through the gateway rather than through a server', async () => {
    let path = '';
    serving();
    api.use(
      http.post('*/call', ({ request }) => {
        path = new URL(request.url).pathname;
        return HttpResponse.json({ isError: false, content: [] });
      }),
    );
    renderWithProviders(<Tools />);

    await screen.findByText('list_servers');
    const own = within(group('系统工具'));
    await userEvent.click(own.getAllByRole('button', { name: /调用/ })[0]!);
    await userEvent.click(screen.getByRole('button', { name: '执行' }));

    await waitFor(() => expect(path).toBe('/api/gateway/tools/list_servers/call'));
  });
});

describe('one group per server', () => {
  it('puts each tool under the server it came from', async () => {
    serving();
    renderWithProviders(<Tools />);

    await screen.findByText('files_read');
    expect(within(group('files')).getByText('files_read')).toBeInTheDocument();
    expect(within(group('bing')).getByText('bing_search')).toBeInTheDocument();
    expect(within(group('files')).queryByText('bing_search')).not.toBeInTheDocument();
  });

  it('follows the order of the server list', async () => {
    serving();
    renderWithProviders(<Tools />);

    await screen.findByText('files_read');
    const headings = screen.getAllByRole('heading').map((node) => node.textContent);
    expect(headings.indexOf('files')).toBeLessThan(headings.indexOf('bing'));
  });

  // The pair, not the count. The gateway offers nothing it has not been
  // asked to, so a bare number reads as everything the server has.
  it('reports how much of each server is exposed', async () => {
    serving(FORWARDED, OWN, [
      view('files', { exposedCount: 1, toolCount: 9 }),
      view('bing', { exposedCount: 0, toolCount: 4 }),
    ]);
    renderWithProviders(<Tools />);

    await screen.findByText('files_read');
    expect(within(group('files')).getByText('已暴露 1 / 9')).toBeInTheDocument();
    expect(within(group('bing')).getByText('已暴露 0 / 4')).toBeInTheDocument();
  });

  it("reports each server's state on its group", async () => {
    serving(FORWARDED, OWN, [view('files'), view('bing', { state: 'failed', error: 'boom' })]);
    renderWithProviders(<Tools />);

    await screen.findByText('files_read');
    expect(within(group('bing')).getByText('连接失败')).toBeInTheDocument();
  });
});

// A server contributing nothing is the case a flat list could not
// express: it simply was not there, which looks the same as never having
// been configured.
describe('a server with no tools on offer', () => {
  it('still has a group', async () => {
    serving([], OWN, [view('files', { toolCount: 0 })]);
    renderWithProviders(<Tools />);

    await screen.findByRole('heading', { name: 'files' });
    expect(within(group('files')).getByText('没有工具')).toBeInTheDocument();
  });

  it('says the server is disabled when it is', async () => {
    serving([], OWN, [view('files', { enabled: false, state: 'disconnected', toolCount: 0 })]);
    renderWithProviders(<Tools />);

    await screen.findByRole('heading', { name: 'files' });
    expect(within(group('files')).getByText(/启用之后它的工具/)).toBeInTheDocument();
  });

  it('says the connection failed when it did', async () => {
    serving([], OWN, [view('files', { state: 'failed', error: 'boom', toolCount: 0 })]);
    renderWithProviders(<Tools />);

    await screen.findByRole('heading', { name: 'files' });
    expect(within(group('files')).getByText(/拿不到工具列表/)).toBeInTheDocument();
  });

  it('says the server declares no tools when it does not', async () => {
    serving([], OWN, [view('files', { hasTools: false, toolCount: 0 })]);
    renderWithProviders(<Tools />);

    await screen.findByRole('heading', { name: 'files' });
    expect(within(group('files')).getByText(/本来就不提供工具/)).toBeInTheDocument();
  });

  // The state most servers are in is not an empty group at all: the page
  // lists every tool a server offers, exposed or not, because it is where
  // exposure is decided. A group only comes up empty when the server truly
  // has nothing.
  it('lists unexposed tools rather than leaving the group empty', async () => {
    serving(
      [
        { server: 'files', tool: 'read', exposed: 'files_read', description: 'read a file' },
        // No exposed name: not in the gateway's tools/list.
        { server: 'files', tool: 'write', exposed: '', description: 'put a file somewhere' },
      ],
      OWN,
      [view('files', { exposedCount: 1, toolCount: 2 })],
    );
    renderWithProviders(<Tools />);

    await screen.findByRole('heading', { name: 'files' });
    const files = within(group('files'));
    expect(files.getByText('files_read')).toBeInTheDocument();
    // The unexposed one is shown under its own name, since it has no other.
    expect(files.getByText('write')).toBeInTheDocument();
    expect(files.queryByText('没有工具')).not.toBeInTheDocument();
  });
});

// This page is where exposure is decided, so every tool needs a switch on
// it — including, and especially, the ones that are not exposed. Showing
// only the exposed ones left a fresh installation with an empty page and
// nothing to click.
describe('deciding what to expose', () => {
  const MIXED = [
    { server: 'files', tool: 'read', exposed: 'files_read', description: 'read a file' },
    { server: 'files', tool: 'write', exposed: '', description: 'put a file somewhere' },
  ];

  function withFiles(exposedTools: string[] | undefined) {
    const base = view('files', { exposedCount: exposedTools?.length ?? 0, toolCount: 2 });
    return [{ ...base, config: { ...base.config, exposedTools } }];
  }

  it('offers a switch for every tool, exposed or not', async () => {
    serving(MIXED, OWN, withFiles(['read']));
    renderWithProviders(<Tools />);

    await screen.findByRole('heading', { name: 'files' });
    const switches = within(group('files')).getAllByRole('switch');
    expect(switches).toHaveLength(2);
    expect(switches[0]).toBeChecked();
    expect(switches[1]).not.toBeChecked();
  });

  // The gateway's own tools are not a server's, and there is nothing to
  // decide about them: they are always on offer.
  it('offers no switch for the gateway’s own tools', async () => {
    serving(MIXED, OWN, withFiles(['read']));
    renderWithProviders(<Tools />);

    await screen.findByText('list_servers');
    expect(within(group('系统工具')).queryAllByRole('switch')).toHaveLength(0);
  });

  it('writes the whole allow list when a tool is switched on', async () => {
    let body: unknown = null;
    serving(MIXED, OWN, withFiles(['read']));
    api.use(
      http.put('/api/servers/files', async ({ request }) => {
        body = await request.json();
        return HttpResponse.json({});
      }),
    );
    renderWithProviders(<Tools />);

    await screen.findByRole('heading', { name: 'files' });
    await userEvent.click(within(group('files')).getAllByRole('switch')[1]!);

    await waitFor(() => expect(body).not.toBeNull());
    // Under a key, and the whole list rather than a delta. Getting either
    // wrong fails silently — the write succeeds and changes nothing.
    expect(body).toMatchObject({ server: { exposedTools: ['read', 'write'] } });
  });

  it('writes the list without the tool when one is switched off', async () => {
    let body: unknown = null;
    serving(MIXED, OWN, withFiles(['read']));
    api.use(
      http.put('/api/servers/files', async ({ request }) => {
        body = await request.json();
        return HttpResponse.json({});
      }),
    );
    renderWithProviders(<Tools />);

    await screen.findByRole('heading', { name: 'files' });
    await userEvent.click(within(group('files')).getAllByRole('switch')[0]!);

    await waitFor(() => expect(body).not.toBeNull());
    expect(body).toMatchObject({ server: { exposedTools: [] } });
  });

  // The state a fresh installation is in: nothing exposed anywhere, which
  // is exactly when the page has to be usable.
  it('works from a server that exposes nothing at all', async () => {
    let body: unknown = null;
    serving(
      [
        { server: 'files', tool: 'read', exposed: '', description: 'read a file' },
        { server: 'files', tool: 'write', exposed: '', description: 'put a file somewhere' },
      ],
      OWN,
      withFiles(undefined),
    );
    api.use(
      http.put('/api/servers/files', async ({ request }) => {
        body = await request.json();
        return HttpResponse.json({});
      }),
    );
    renderWithProviders(<Tools />);

    await screen.findByRole('heading', { name: 'files' });
    const switches = within(group('files')).getAllByRole('switch');
    expect(switches).toHaveLength(2);
    expect(switches[0]).not.toBeChecked();

    await userEvent.click(switches[0]!);
    await waitFor(() => expect(body).not.toBeNull());
    expect(body).toMatchObject({ server: { exposedTools: ['read'] } });
  });

  it('says so when the write fails', async () => {
    serving(MIXED, OWN, withFiles(['read']));
    api.use(
      http.put('/api/servers/files', () =>
        HttpResponse.json(
          { error: { code: 'invalid', message: 'the server is not valid' } },
          { status: 422 },
        ),
      ),
    );
    renderWithProviders(<Tools />);

    await screen.findByRole('heading', { name: 'files' });
    await userEvent.click(within(group('files')).getAllByRole('switch')[0]!);

    expect(await screen.findByText(/the server is not valid/)).toBeInTheDocument();
  });
});

describe('narrowing the list', () => {
  // Picking a server asks about that server. The gateway's own tools are
  // on no server, so they are not an answer to it.
  it("leaves out the other servers and the gateway's own tools", async () => {
    serving();
    renderWithProviders(<Tools />);

    await screen.findByText('list_servers');
    await userEvent.click(screen.getByRole('combobox'));
    await userEvent.click(await screen.findByTitle('files'));

    await waitFor(() => expect(screen.queryByText('list_servers')).not.toBeInTheDocument());
    expect(screen.getByText('files_read')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'bing' })).not.toBeInTheDocument();
  });

  // Every configured server is on offer in the filter, including one with
  // no tools — asking about it is how its group's explanation is reached.
  it('offers a server with no tools in the filter', async () => {
    serving([], OWN, [view('quiet', { toolCount: 0 })]);
    renderWithProviders(<Tools />);

    await screen.findByRole('heading', { name: 'quiet' });
    await userEvent.click(screen.getByRole('combobox'));
    expect(await screen.findByTitle('quiet')).toBeInTheDocument();
  });

  // The gateway ranks the forwarded tools; these are filtered here. A
  // search that reached only one group would answer half the question
  // without saying so.
  it("applies the search to the gateway's own tools too", async () => {
    serving();
    renderWithProviders(<Tools />);

    await screen.findByText('list_servers');
    await userEvent.type(screen.getByRole('searchbox'), 'servers');

    await waitFor(() => expect(screen.queryByText('search_tools')).not.toBeInTheDocument());
    expect(screen.getByText('list_servers')).toBeInTheDocument();
  });

  // Under a search a group with nothing in it is not part of the answer,
  // where without one it is reporting the state of its server.
  it('drops the groups a search did not reach', async () => {
    serving();
    renderWithProviders(<Tools />);

    await screen.findByRole('heading', { name: 'bing' });
    api.use(
      http.get('/api/tools', () => HttpResponse.json({ tools: [FORWARDED[0]], total: 1 })),
    );
    await userEvent.type(screen.getByRole('searchbox'), 'disk');

    await waitFor(() =>
      expect(screen.queryByRole('heading', { name: 'bing' })).not.toBeInTheDocument(),
    );
    expect(screen.getByRole('heading', { name: 'files' })).toBeInTheDocument();
  });

  it('says so when nothing matched at all', async () => {
    serving();
    renderWithProviders(<Tools />);

    await screen.findByRole('heading', { name: 'files' });
    api.use(
      http.get('/api/tools', () => HttpResponse.json({ tools: [], total: 0 })),
      http.get('/api/gateway/tools', () =>
        HttpResponse.json({ tools: [], total: 0, systemTools: [] }),
      ),
    );
    await userEvent.type(screen.getByRole('searchbox'), 'nothing like this');

    expect(await screen.findByText('没有匹配的工具')).toBeInTheDocument();
  });
});

describe('no servers configured', () => {
  it('says so rather than reporting that nothing matched', async () => {
    serving([], OWN, []);
    renderWithProviders(<Tools />);

    expect(await screen.findByText('没有可用的工具')).toBeInTheDocument();
    // The gateway's own tools are still on offer, and still shown.
    expect(screen.getByText('list_servers')).toBeInTheDocument();
  });
});

// The endpoint reports the number of tools it returned, not the number it
// had, so a list cut off at the limit is indistinguishable from a complete
// one — and every group count on the page is then a lower bound.
describe('a list that hit the limit', () => {
  const many = Array.from({ length: 200 }, (_, i) => ({
    server: 'files',
    tool: `t${i}`,
    exposed: `files_t${i}`,
    description: `tool number ${i}`,
  }));

  it('says the list was cut off', async () => {
    serving(many);
    renderWithProviders(<Tools />);

    expect(await screen.findByText(/只列出了前 200 个工具/)).toBeInTheDocument();
  });

  // Alphabetical order by exposed name means the servers late in the
  // alphabet are the ones cut, so "this server exposes nothing" is
  // exactly the wrong conclusion to state confidently.
  it('does not claim an empty group has nothing to offer', async () => {
    serving(many, OWN, [view('files', { toolCount: 200 }), view('zzz', { toolCount: 7 })]);
    renderWithProviders(<Tools />);

    await screen.findByRole('heading', { name: 'zzz' });
    const empty = within(group('zzz'));
    expect(empty.getByText(/可能只是没被列出来/)).toBeInTheDocument();
    expect(empty.queryByText(/都没有勾选对外开放/)).not.toBeInTheDocument();
  });
});

/*
 * The two layouts.
 *
 * Cards read well for a dozen tools with descriptions worth reading; they
 * become a wall to scroll when a server offers forty. Both therefore
 * exist, and what matters is that they are two views of one page rather
 * than two pages: the same tools, the same actions on each. A test that
 * only asserted the toggle exists would pass on a list layout that had
 * quietly lost its switches.
 */
describe('the layout toggle', () => {
  // The choice is persisted, so it survives from one test into the next
  // unless it is put back.
  afterEach(() => {
    useToolLayoutStore.setState({ layout: 'cards' });
    localStorage.clear();
  });

  // The label rather than the radio inside it: antd's segmented control
  // leaves the input with pointer-events: none, as the label is what a
  // person clicks.
  async function switchToList() {
    await userEvent.click(screen.getByText('列表'));
  }

  it('shows the same tools in either layout', async () => {
    serving();
    renderWithProviders(<Tools />);

    await screen.findByText('files_read');
    await userEvent.click(screen.getByText('卡片'));
    expect(within(group('files')).getByText('files_read')).toBeInTheDocument();
    await switchToList();

    // Still there, still under its own server.
    expect(within(group('files')).getByText('files_read')).toBeInTheDocument();
    expect(within(group('bing')).getByText('bing_search')).toBeInTheDocument();
    // And the gateway's own, which belong to no server.
    expect(within(group('系统工具')).getByText('list_servers')).toBeInTheDocument();
  });

  it('can call a tool from the list layout', async () => {
    serving();
    renderWithProviders(<Tools />);

    await screen.findByText('files_read');
    await switchToList();

    const row = screen.getByText('files_read').closest('div');
    if (!row) throw new Error('the row is not there');
    await userEvent.click(within(row).getByRole('button', { name: /调用/ }));

    // The call dialog names the tool it is about to call.
    expect(await screen.findByRole('dialog')).toHaveTextContent('read');
  });

  it('can expose a tool from the list layout', async () => {
    let sent: unknown = null;
    serving([{ server: 'files', tool: 'read', exposed: '', description: 'read a file' }]);
    api.use(
      http.put('/api/servers/files', async ({ request }) => {
        sent = await request.json();
        return HttpResponse.json(view('files'));
      }),
    );
    renderWithProviders(<Tools />);

    await screen.findByText('read');
    await switchToList();
    await userEvent.click(within(group('files')).getByRole('switch'));

    // The whole allow list, through the ordinary server-update endpoint —
    // the same request the card layout makes.
    await waitFor(() => expect(sent).not.toBeNull());
    expect(sent).toMatchObject({ server: { exposedTools: ['read'] } });
  });

  it('remembers the choice', async () => {
    serving();
    renderWithProviders(<Tools />);

    await screen.findByText('files_read');
    await switchToList();

    expect(useToolLayoutStore.getState().layout).toBe('list');
    // In the browser's storage, not the gateway's configuration: the
    // choice belongs to whoever is looking at the screen.
    expect(localStorage.getItem('mcphub.tools.layout')).toContain('list');
  });
});

/*
 * Folding a group away.
 *
 * One panel per server answered "what does this server offer"; it did not
 * answer "put away the four servers that are not my question". Five
 * servers of forty tools each is a page you scroll through rather than
 * read, and the list layout only made each group shorter.
 *
 * What the tests below have to distinguish is a handle that is present
 * from a handle that works: asserting the arrow exists would pass on a
 * panel that never folds anything.
 */
describe('folding a group away', () => {
  afterEach(() => {
    useToolCollapseStore.setState({ collapsed: [] });
    localStorage.clear();
  });

  /** The fold handle of the group with this heading. The heading is inside
   *  the button, so the button is what carries the expanded state. */
  function handle(name: string): HTMLElement {
    const button = screen.getByRole('heading', { name }).closest('button');
    if (!button) throw new Error(`the heading for ${name} is not a fold handle`);
    return button;
  }

  it('hides a group’s tools when its heading is clicked', async () => {
    serving();
    renderWithProviders(<Tools />);

    await screen.findByText('files_read');
    await userEvent.click(handle('files'));

    expect(screen.queryByText('files_read')).not.toBeInTheDocument();
    // Only that group. The point of folding one is to read another.
    expect(screen.getByText('bing_search')).toBeInTheDocument();
  });

  it('brings them back when it is clicked again', async () => {
    serving();
    renderWithProviders(<Tools />);

    await screen.findByText('files_read');
    await userEvent.click(handle('files'));
    await userEvent.click(handle('files'));

    expect(screen.getByText('files_read')).toBeInTheDocument();
  });

  // The heading stays, and so does everything on it. Folded, the count and
  // the state badge are the only things the group is still saying.
  it('keeps the heading, the count and the state on screen', async () => {
    serving(FORWARDED, OWN, [
      view('files', { exposedCount: 1, toolCount: 9, state: 'failed', error: 'boom' }),
      view('bing'),
    ]);
    renderWithProviders(<Tools />);

    await screen.findByText('files_read');
    await userEvent.click(handle('files'));

    const files = within(group('files'));
    expect(files.getByText('已暴露 1 / 9')).toBeInTheDocument();
    expect(files.getByText('连接失败')).toBeInTheDocument();
  });

  it('says whether a group is open, for a reader who cannot see the arrow', async () => {
    serving();
    renderWithProviders(<Tools />);

    await screen.findByText('files_read');
    expect(handle('files')).toHaveAttribute('aria-expanded', 'true');

    await userEvent.click(handle('files'));
    expect(handle('files')).toHaveAttribute('aria-expanded', 'false');
  });

  it('remembers by server name, in the browser', async () => {
    serving();
    renderWithProviders(<Tools />);

    await screen.findByText('files_read');
    await userEvent.click(handle('files'));

    // The name, not the position: a server's position shifts as others
    // are added and removed, while its name is what was recognised.
    expect(useToolCollapseStore.getState().collapsed).toEqual(['files']);
    expect(localStorage.getItem('mcphub.tools.collapsed')).toContain('files');
  });

  // Searching is asking to see something. A group holding a match that
  // stayed folded would be a search that found the tool and hid it.
  it('opens a folded group that a search reached', async () => {
    serving();
    renderWithProviders(<Tools />);

    await screen.findByText('files_read');
    await userEvent.click(handle('files'));
    expect(screen.queryByText('files_read')).not.toBeInTheDocument();

    await userEvent.type(screen.getByRole('searchbox'), 'disk');

    expect(await screen.findByText('files_read')).toBeInTheDocument();
  });

  // Ignored while searching, not forgotten by it — otherwise every search
  // would silently undo whatever had been put away.
  it('folds it again when the search is cleared', async () => {
    serving();
    renderWithProviders(<Tools />);

    await screen.findByText('files_read');
    await userEvent.click(handle('files'));

    const box = screen.getByRole('searchbox');
    await userEvent.type(box, 'disk');
    await screen.findByText('files_read');
    await userEvent.clear(box);

    await waitFor(() => expect(screen.queryByText('files_read')).not.toBeInTheDocument());
  });

  // An empty group's body is the sentence saying why it is empty. Folding
  // it away would leave a heading with no answer under it.
  it('gives no handle to a group with nothing in it', async () => {
    serving([], OWN, [view('quiet', { toolCount: 0 })]);
    renderWithProviders(<Tools />);

    await screen.findByRole('heading', { name: 'quiet' });
    expect(screen.getByRole('heading', { name: 'quiet' }).closest('button')).toBeNull();
    expect(within(group('quiet')).getByText('没有工具')).toBeInTheDocument();
  });

  // Under progressive disclosure these are the only tools a client is
  // shown without asking, so nothing should put them away for you.
  it('leaves the gateway’s own group open to begin with, but foldable', async () => {
    serving();
    renderWithProviders(<Tools />);

    await screen.findByText('list_servers');
    expect(handle('系统工具')).toHaveAttribute('aria-expanded', 'true');

    await userEvent.click(handle('系统工具'));
    expect(screen.queryByText('list_servers')).not.toBeInTheDocument();
  });

  // The two layouts are two views of one page, so a fold has to mean the
  // same thing in both.
  it('folds in the list layout too', async () => {
    serving();
    renderWithProviders(<Tools />);

    await screen.findByText('files_read');
    await userEvent.click(screen.getByText('列表'));
    await userEvent.click(handle('files'));

    expect(screen.queryByText('files_read')).not.toBeInTheDocument();

    useToolLayoutStore.setState({ layout: 'cards' });
  });
});

/*
 * What the models have done with each tool.
 *
 * The page exists so that an operator can decide what to expose, and the
 * counts are the evidence: what gets reached for, what gets found but
 * never used, what was never found at all. A number that could be mistaken
 * for a measurement of nothing — a zero — is avoided in favour of saying
 * "never".
 */
describe('usage counts', () => {
  const USAGE: UsageSnapshot = {
    since: '2026-09-19T08:00:00Z',
    entries: [
      {
        server: 'bing',
        tool: 'search',
        searched: 7,
        called: 3,
        failed: 1,
        lastCalled: '2026-09-19T09:30:00Z',
      },
    ],
  };

  afterEach(() => {
    useToolLayoutStore.setState({ layout: 'cards' });
    localStorage.clear();
  });

  it('shows how often a tool was called and found', async () => {
    serving(FORWARDED, OWN, VIEWS, USAGE);
    renderWithProviders(<Tools />);

    await screen.findByText('bing_search');
    expect(within(group('bing')).getByText(/调用 3 · 搜到 7/)).toBeInTheDocument();
    expect(within(group('bing')).getByText(/失败 1/)).toBeInTheDocument();
  });

  it('says a tool was never used rather than showing zeros', async () => {
    serving(FORWARDED, OWN, VIEWS, USAGE);
    renderWithProviders(<Tools />);

    await screen.findByText('files_read');
    expect(within(group('files')).getByText('未被使用')).toBeInTheDocument();
  });

  // The gateway's own tools are in every client's list and are not
  // counted, so a "never used" on them would be a false statement.
  it("does not count the gateway's own tools", async () => {
    serving(FORWARDED, OWN, VIEWS, USAGE);
    renderWithProviders(<Tools />);

    await screen.findByText('list_servers');
    expect(within(group('系统工具')).queryByText('未被使用')).not.toBeInTheDocument();
  });

  it('can order the tools by use', async () => {
    serving(
      [
        { server: 'files', tool: 'read', exposed: 'files_read', description: 'read a file' },
        { server: 'files', tool: 'write', exposed: 'files_write', description: 'write a file' },
      ],
      OWN,
      VIEWS,
      {
        since: '2026-09-19T08:00:00Z',
        entries: [{ server: 'files', tool: 'write', searched: 0, called: 5, failed: 0 }],
      },
    );
    renderWithProviders(<Tools />);

    await screen.findByText('files_read');
    await userEvent.click(screen.getByText('列表'));
    const names = () =>
      within(group('files'))
        .getAllByRole('listitem')
        .map((row) => row.textContent ?? '');

    // By name to begin with, as the gateway ordered them.
    expect(names()[0]).toContain('files_read');

    await userEvent.click(screen.getByText('按使用'));
    expect(names()[0]).toContain('files_write');
  });

  it('can be reset', async () => {
    let reset = false;
    serving(FORWARDED, OWN, VIEWS, USAGE);
    api.use(
      http.delete('/api/gateway/usage', () => {
        reset = true;
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderWithProviders(<Tools />);

    await screen.findByText('bing_search');
    await userEvent.click(screen.getByRole('button', { name: '清零' }));
    await userEvent.click(await screen.findByRole('button', { name: '确定' }));

    await waitFor(() => expect(reset).toBe(true));
  });

  // The counts are a convenience. A page that cannot get them is still
  // the tools page, and must not turn into an error page.
  it('still shows the tools when the counts cannot be loaded', async () => {
    serving();
    api.use(http.get('/api/gateway/usage', () => HttpResponse.error()));
    renderWithProviders(<Tools />);

    expect(await screen.findByText('files_read')).toBeInTheDocument();
  });
});
