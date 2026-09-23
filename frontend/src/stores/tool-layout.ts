import { create } from 'zustand';
import { persist } from 'zustand/middleware';

/**
 * How the tools page lays its tools out.
 *
 * Cards read well when there are a dozen of them and the descriptions are
 * worth reading. They stop working when a server offers forty: the page
 * becomes a wall to scroll, and the question turns from "what does this do"
 * into "where is the one I want". A row per tool answers that one, so both
 * exist and the choice is the reader's.
 *
 * Kept in the browser rather than in the gateway's configuration, for the
 * same reason as the theme: it belongs to whoever is looking at the screen,
 * not to the installation. Two people sharing one gateway should not be
 * overwriting each other's preference.
 */

export type ToolLayout = 'cards' | 'list';

/** Named for the project so that it cannot collide with anything else
 *  served from the same origin. */
const STORAGE_KEY = 'mcp-foyer.tools.layout';

interface LayoutState {
  layout: ToolLayout;
  setLayout: (layout: ToolLayout) => void;
}

export const useToolLayoutStore = create<LayoutState>()(
  persist(
    (set) => ({
      layout: 'list',
      setLayout: (layout) => set({ layout }),
    }),
    { name: STORAGE_KEY },
  ),
);
