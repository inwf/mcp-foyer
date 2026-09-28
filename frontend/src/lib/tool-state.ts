import type { MCPServer } from '@/api/types';

/** Both lists are saved together so a disabled tool cannot remain exposed. */
export function withToolDisabled(config: MCPServer, tool: string, disabled: boolean): MCPServer {
  const current = config.disabledTools ?? [];
  return {
    ...config,
    disabledTools: disabled
      ? [...new Set([...current, tool])]
      : current.filter((name) => name !== tool),
    exposedTools: (config.exposedTools ?? []).filter((name) => name !== tool),
  };
}
