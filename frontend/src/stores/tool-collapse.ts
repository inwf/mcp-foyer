import { create } from 'zustand';
import { persist } from 'zustand/middleware';

/**
 * Which of the tools page's groups are folded shut.
 *
 * One panel per server was the right answer to "what does this server
 * offer" (it replaced a flat list where the server was a word on a card),
 * but it left nothing to do about five servers of forty tools each except
 * scroll past the ones that are not the question. The list layout made
 * each group shorter; it did not offer the step before reading, which is
 * putting away what you are not reading.
 *
 * Kept in the browser rather than in the gateway's configuration, for the
 * same reason as the theme and the layout: it belongs to whoever is
 * looking at the screen, not to the installation.
 */

/** Named for the project so that it cannot collide with anything else
 *  served from the same origin. */
const STORAGE_KEY = 'mcp-foyer.tools.collapsed';

/** The key the gateway's own group is remembered under. Not a legal server
 *  name — the empty string cannot be one, and neither can anything with a
 *  space in it — so it cannot be confused with a server's. */
export const SYSTEM_GROUP = '';

interface CollapseState {
  /** Collapsed group keys: server names, or SYSTEM_GROUP. Remembered by
   *  name rather than by position, because a server's position shifts as
   *  others are added and removed while its name is what was recognised. */
  collapsed: string[];
  isCollapsed: (key: string) => boolean;
  toggle: (key: string) => void;
}

export const useToolCollapseStore = create<CollapseState>()(
  persist(
    (set, get) => ({
      // Everything open to begin with. A page that hid its contents on
      // first sight would be answering a question nobody had asked yet.
      collapsed: [],

      isCollapsed: (key) => get().collapsed.includes(key),

      toggle: (key) =>
        set((state) => ({
          collapsed: state.collapsed.includes(key)
            ? state.collapsed.filter((each) => each !== key)
            : [...state.collapsed, key],
        })),
    }),
    { name: STORAGE_KEY },
  ),
);
