import { QueryClient } from '@tanstack/react-query';
import { ApiError } from './client';

/**
 * The query keys, in one place.
 *
 * Written as a hierarchy so that invalidating a prefix invalidates
 * everything below it: `keys.servers.all` covers every server list and
 * every individual server, which is what an event saying "a server
 * changed" needs to reach without naming each query.
 */
export const keys = {
  health: ['health'] as const,

  config: {
    all: ['config'] as const,
    current: () => [...keys.config.all, 'current'] as const,
  },

  servers: {
    all: ['servers'] as const,
    list: () => [...keys.servers.all, 'list'] as const,
    one: (name: string) => [...keys.servers.all, 'one', name] as const,
    tools: (name: string) => [...keys.servers.all, 'one', name, 'tools'] as const,
    resources: (name: string) => [...keys.servers.all, 'one', name, 'resources'] as const,
  },

  tools: {
    all: ['tools'] as const,
    aggregated: (search: string, all = false) =>
      [...keys.tools.all, 'aggregated', search, all] as const,
  },

  resources: {
    all: ['resources'] as const,
    aggregated: () => [...keys.resources.all, 'aggregated'] as const,
  },

  gateway: {
    all: ['gateway'] as const,
    status: () => [...keys.gateway.all, 'status'] as const,
    sessions: () => [...keys.gateway.all, 'sessions'] as const,
    tools: () => [...keys.gateway.all, 'tools'] as const,
    usage: () => [...keys.gateway.all, 'usage'] as const,
  },

  logs: {
    all: ['logs'] as const,
    query: (filter: unknown) => [...keys.logs.all, 'query', filter] as const,
  },
};

/**
 * Retrying is for a gateway that was not reachable, not for a request it
 * understood and refused. Repeating a rejected configuration produces
 * the same rejection three times, delays the error the user is waiting
 * for, and writes three identical failures into the log.
 */
function shouldRetry(failureCount: number, error: unknown): boolean {
  if (error instanceof ApiError && !error.isOffline) return false;
  return failureCount < 2;
}

export function createQueryClient(): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: shouldRetry,
        retryDelay: (attempt) => Math.min(1000 * 2 ** attempt, 8000),

        // The event stream is what keeps this view fresh, so polling is
        // not the mechanism here and a generous window costs nothing.
        // Data still refetches when a window is focused again, which
        // covers the case of the stream having missed something while
        // the tab was in the background.
        staleTime: 30_000,
        refetchOnWindowFocus: true,
        refetchOnReconnect: true,
      },
      mutations: {
        retry: false,
      },
    },
  });
}
