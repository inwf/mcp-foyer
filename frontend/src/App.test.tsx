import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { setupServer } from 'msw/node';
import { http, HttpResponse } from 'msw';
import { renderWithProviders } from '@/test/harness';
import { routes } from '@/routes';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';

/*
 * That the application mounts and shows its pages.
 *
 * Every other test here renders one component. This renders what the
 * browser renders — the shell, the router, the lazily-loaded page — so
 * that a failure to start is caught by the suite rather than by opening
 * the page and finding it blank.
 */

const api = setupServer(
  http.get('/api/gateway/status', () =>
    HttpResponse.json({
      servers: { configured: 2, connected: 1, failed: 0 },
      tools: 7,
      sessions: 0,
      sessionMode: 'stateful',
    }),
  ),
  http.get('/api/servers', () =>
    HttpResponse.json({
      servers: [
        {
          name: 'files',
          config: { transport: 'stdio', enabled: true, timeout: '1m0s', command: 'echo' },
          status: {
            name: 'files',
            state: 'connected',
            toolCount: 7,
            resourceCount: 0,
            hasTools: true,
            hasResources: false,
            hasPrompts: false,
            hasLogging: false,
          },
        },
      ],
    }),
  ),
  http.get('/api/health', () =>
    HttpResponse.json({
      status: 'ok',
      version: 'test',
      startedAt: '2026-08-27T10:00:00Z',
      uptimeSeconds: 3600,
      connections: 1,
    }),
  ),
);

beforeAll(() => api.listen({ onUnhandledRequest: 'bypass' }));
afterEach(() => api.resetHandlers());
afterAll(() => api.close());

/** Lazily-loaded pages are compiled on first use here, which is slower
 *  than testing-library's one-second default allows for. The wait is
 *  stated rather than left implicit so a real hang is still a failure. */
const LAZY = { timeout: 4_000 };

function visit(path: string) {
  renderWithProviders(
    <RouterProvider router={createMemoryRouter(routes, { initialEntries: [path] })} />,
  );
}

describe('the application', () => {
  it('renders the shell with every section reachable', async () => {
    visit('/');

    // The rail is the navigation; if it is missing there is no way
    // anywhere.
    for (const section of ['总览', '服务器', '工具', '资源', '日志', '设置']) {
      expect(await screen.findByLabelText(section)).toBeInTheDocument();
    }
  });

  it('shows the overview with live figures', async () => {
    visit('/');

    await waitFor(() => expect(screen.getByText('已暴露工具')).toBeInTheDocument(), LAZY);
    // The server inventory also contains tool counts; assert the gateway
    // total in its metrics, where clients' exposed tools are reported.
    expect(within(screen.getByLabelText('运行统计')).getByText('7')).toBeInTheDocument();
  });

  it('shows the servers page with its add button', async () => {
    visit('/servers');

    // There is one in the toolbar; the empty-state offers a second, so
    // this asserts at least one rather than exactly one.
    await waitFor(
      () =>
        expect(screen.getAllByRole('button', { name: '添加服务器' }).length).toBeGreaterThan(0),
      LAZY,
    );
    expect(await screen.findByText('files')).toBeInTheDocument();
  });

  it('copies the client endpoint using the address visible to the browser', async () => {
    const user = userEvent.setup();
    visit('/');

    await user.click(await screen.findByRole('button', { name: '复制地址' }));
    // The listener's internal address would be wrong behind a reverse proxy.
    expect(await navigator.clipboard.readText()).toBe(`${window.location.origin}/mcp`);
    expect(screen.getByRole('textbox', { name: 'MCP 接入地址' })).toHaveValue(
      `${window.location.origin}/mcp`,
    );
  });

  it('reports an unknown path rather than rendering nothing', async () => {
    visit('/nowhere');

    expect(await screen.findByText('找不到该页面')).toBeInTheDocument();
  });
});
