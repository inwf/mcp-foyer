import type { CSSProperties } from 'react';

/** Keep the last item in a large list from waiting seconds to appear. */
export function stagger(index: number): CSSProperties {
  return { animationDelay: `${Math.min(index, 5) * 40}ms` };
}
