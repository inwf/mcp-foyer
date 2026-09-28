import { describe, expect, it } from 'vitest';
import { serverFrom, type ServerFormValues } from './ServerForm';
import type { MCPServer } from '@/api/types';

const values: ServerFormValues = {
  name: 'files', transport: 'stdio', enabled: true, description: '',
  timeout: '30s', command: 'node', args: [], env: {}, url: '',
  headers: {}, proxy: '', exposedTools: [],
};
const original: MCPServer = {
  transport: 'stdio', enabled: true, timeout: '30s', command: 'node',
  disabledTools: ['write'], readyPatterns: ['ready'], readyTimeout: '5s',
  exposedTools: ['read'], description: 'old description',
};

describe('server form configuration', () => {
  it('preserves disabled tools and readiness when saving ordinary fields', () => {
    const result = serverFrom(values, original);
    expect(result.disabledTools).toEqual(['write']);
    expect(result.readyPatterns).toEqual(['ready']);
    expect(result.readyTimeout).toBe('5s');
    expect(result.exposedTools).toBeUndefined();
    expect(result.description).toBeUndefined();
    expect(original.exposedTools).toEqual(['read']);
  });
  it('keeps disabling but removes process readiness when switching to HTTP', () => {
    const result = serverFrom({ ...values, transport: 'streamable-http', url: 'https://example.com/mcp' }, original);
    expect(result.disabledTools).toEqual(['write']);
    expect(result.readyPatterns).toBeUndefined();
    expect(result.readyTimeout).toBeUndefined();
    expect(result.command).toBeUndefined();
    expect(result.url).toBe('https://example.com/mcp');
  });
  it('preserves the latest JSON configuration through the form', () => {
    expect(serverFrom(values, { ...original, disabledTools: ['read'] }).disabledTools).toEqual(['read']);
    expect(serverFrom(values, { transport: 'stdio', enabled: true, timeout: '30s' }).disabledTools).toBeUndefined();
  });
});
