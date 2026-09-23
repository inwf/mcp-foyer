import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithProviders } from '@/test/harness';
import { applyMode, resolveMode, storedChoice, useThemeStore } from '@/stores/theme';
import { antdThemeFor } from '@/theme/antd';
import { ThemeSwitch } from './ThemeSwitch';

// Three choices, two outcomes. Light and dark are answers; "system" is a
// deferral, and it has to be resolved before anything can be painted.

/** Installs a matchMedia that answers the dark query as told, and lets a
 *  test fire a change the way a machine switching at dusk would. */
function systemPrefers(dark: boolean) {
  const listeners = new Set<() => void>();
  let matches = dark;

  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: query.includes('dark') ? matches : !matches,
    media: query,
    addEventListener: (_: string, fn: () => void) => listeners.add(fn),
    removeEventListener: (_: string, fn: () => void) => listeners.delete(fn),
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
    onchange: null,
  }));

  return {
    switchTo(next: boolean) {
      matches = next;
      act(() => listeners.forEach((fn) => fn()));
    },
  };
}

beforeEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute('data-theme');
  document.documentElement.style.colorScheme = '';
  useThemeStore.setState({ choice: 'system', mode: 'dark' });
});

afterEach(() => vi.unstubAllGlobals());

describe('resolving the choice', () => {
  it('takes light and dark at face value', () => {
    systemPrefers(true);
    expect(resolveMode('light')).toBe('light');
    expect(resolveMode('dark')).toBe('dark');
  });

  it('asks the system when told to follow it', () => {
    systemPrefers(false);
    expect(resolveMode('system')).toBe('light');

    systemPrefers(true);
    expect(resolveMode('system')).toBe('dark');
  });

  // An environment that cannot be asked gets this application's own
  // default, which is the same answer as a user who has said nothing.
  it('falls back to light where the question cannot be asked', () => {
    vi.stubGlobal('matchMedia', undefined);
    expect(resolveMode('system')).toBe('light');
  });
});

// Setting only the attribute leaves a light page with dark scrollbars:
// the browser's own furniture reads color-scheme, not the attribute.
describe('applying it to the document', () => {
  it('sets both the attribute and the colour scheme', () => {
    applyMode('light');
    expect(document.documentElement.dataset['theme']).toBe('light');
    expect(document.documentElement.style.colorScheme).toBe('light');

    applyMode('dark');
    expect(document.documentElement.dataset['theme']).toBe('dark');
    expect(document.documentElement.style.colorScheme).toBe('dark');
  });
});

describe('what is remembered', () => {
  it('reads a stored choice', () => {
    for (const choice of ['light', 'dark', 'system'] as const) {
      localStorage.setItem('mcp-foyer.theme', JSON.stringify({ state: { choice } }));
      expect(storedChoice()).toBe(choice);
    }
  });

  // The stored value is whatever happens to be under that key, which is
  // not necessarily ours and not necessarily intact. None of that is
  // worth failing a page load over.
  it('opens the light workbench when no valid preference is stored', () => {
    localStorage.setItem('mcp-foyer.theme', 'not json at all');
    expect(storedChoice()).toBe('light');

    localStorage.setItem('mcp-foyer.theme', JSON.stringify({ state: { choice: 'chartreuse' } }));
    expect(storedChoice()).toBe('light');

    localStorage.removeItem('mcp-foyer.theme');
    expect(storedChoice()).toBe('light');
  });

  // Storing the resolved mode as well would leave a stale answer
  // surviving a reboot into a differently configured system.
  it('stores the choice and not the resolved mode', async () => {
    systemPrefers(true);
    useThemeStore.getState().setChoice('light');

    const raw = localStorage.getItem('mcp-foyer.theme') ?? '';
    expect(raw).toContain('light');
    expect(JSON.parse(raw)).toEqual({ state: { choice: 'light' }, version: 0 });
  });
});

describe('the switch', () => {
  it('offers all three choices', () => {
    systemPrefers(true);
    renderWithProviders(<ThemeSwitch />);

    expect(screen.getByLabelText('浅色')).toBeInTheDocument();
    expect(screen.getByLabelText('深色')).toBeInTheDocument();
    expect(screen.getByLabelText('跟随系统')).toBeInTheDocument();
  });

  it('changes the theme when one is picked', async () => {
    systemPrefers(true);
    renderWithProviders(<ThemeSwitch />);

    await userEvent.click(screen.getByLabelText('浅色'));
    expect(useThemeStore.getState().mode).toBe('light');
  });
});

// Someone whose machine switches at dusk wants this to switch with it —
// but only while that is what they asked for.
describe('following the system', () => {
  function Probe() {
    return <div data-testid="mode">{useThemeStore((state) => state.mode)}</div>;
  }

  it('follows a change while the choice defers to it', async () => {
    const system = systemPrefers(true);
    const { useTheme } = await import('@/hooks/use-theme');

    function Harness() {
      useTheme();
      return <Probe />;
    }
    render(<Harness />);

    expect(screen.getByTestId('mode')).toHaveTextContent('dark');
    system.switchTo(false);
    expect(screen.getByTestId('mode')).toHaveTextContent('light');
  });

  it('ignores a change once a theme has been picked', async () => {
    const system = systemPrefers(true);
    const { useTheme } = await import('@/hooks/use-theme');

    function Harness() {
      useTheme();
      return <Probe />;
    }
    render(<Harness />);

    act(() => useThemeStore.getState().setChoice('dark'));
    system.switchTo(false);
    expect(screen.getByTestId('mode')).toHaveTextContent('dark');
  });
});

// Two palettes and one configuration built from them: a component themed
// in one and forgotten in the other is only visible to someone who
// switches, which is exactly the fault this shape is meant to prevent.
describe('the two antd configurations', () => {
  it('theme every component in both', () => {
    const dark = antdThemeFor('dark').components ?? {};
    const light = antdThemeFor('light').components ?? {};

    expect(Object.keys(light).sort()).toEqual(Object.keys(dark).sort());
  });

  it('set every token in both', () => {
    const dark = antdThemeFor('dark').token ?? {};
    const light = antdThemeFor('light').token ?? {};

    expect(Object.keys(light).sort()).toEqual(Object.keys(dark).sort());
  });

  it('differ where they must', () => {
    expect(antdThemeFor('light').token?.colorPrimary).not.toBe(
      antdThemeFor('dark').token?.colorPrimary,
    );
    expect(antdThemeFor('light').algorithm).not.toBe(antdThemeFor('dark').algorithm);
  });
});
