import { expect, it } from 'vitest';
import { withToolDisabled } from './tool-state';
import type { MCPServer } from '@/api/types';

const config: MCPServer = {
  transport: 'stdio', enabled: true, timeout: '30s', command: 'node',
  exposedTools: ['read', 'write'], disabledTools: ['remove'],
  env: { TOKEN: '[redacted]' }, readyPatterns: ['ready'],
};

it('disables a tool and removes its exposure in one write without changing other settings', () => {
  const next = withToolDisabled(config, 'write', true);
  expect(next).toEqual({ ...config, exposedTools: ['read'], disabledTools: ['remove', 'write'] });
  expect(config.exposedTools).toEqual(['read', 'write']);
  expect(config.disabledTools).toEqual(['remove']);
  expect(withToolDisabled(next, 'write', true)).toEqual(next);
});

it('enables a tool without exposing it or enabling other disabled tools', () => {
  const next = withToolDisabled(withToolDisabled(config, 'write', true), 'write', false);
  expect(next.exposedTools).toEqual(['read']);
  expect(next.disabledTools).toEqual(['remove']);
});

it('can disable a tool when neither list has been configured', () => {
  const next = withToolDisabled({ transport: 'stdio', enabled: true, timeout: '30s' }, 'write', true);
  expect(next.disabledTools).toEqual(['write']);
  expect(next.exposedTools).toEqual([]);
});
