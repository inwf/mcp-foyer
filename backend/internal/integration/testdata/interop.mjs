import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { resolve } from 'node:path';

// Resolve the real SDK from a disposable pnpm installation, keeping it
// out of the frontend's production dependencies.
const require = createRequire(resolve(process.argv[2], 'package.json'));
const { Client } = require('@modelcontextprotocol/sdk/client/index.js');
const { StreamableHTTPClientTransport } = require('@modelcontextprotocol/sdk/client/streamableHttp.js');

const client = new Client({ name: 'mcp-foyer-interop', version: '1.0' });
try {
  await client.connect(new StreamableHTTPClientTransport(new URL(process.argv[3])));
  const listed = await client.listTools();
  const systemNames = ['call_tool', 'get_tool_details', 'list_servers', 'search_tools'];
  assert.deepEqual(listed.tools.map(tool => tool.name).sort(),
    [...systemNames, 'http_echo', 'stdio_echo'].sort());

  const call = async (name, args) => {
    const result = await client.callTool({ name, arguments: args });
    assert.ok(!result.isError, JSON.stringify(result.content));
    assert.ok(result.structuredContent, `${name} returned no structured content`);
    return result.structuredContent;
  };

  const own = await call('search_tools', { server: 'mcp-foyer', includeSchema: true });
  assert.deepEqual(own.hits.map(hit => hit.tool).sort(), systemNames);
  for (const hit of own.hits) {
    assert.deepEqual(hit.inputSchema, listed.tools.find(tool => tool.name === hit.tool).inputSchema);
  }

  for (const server of ['stdio', 'http']) {
    const summary = await call('search_tools', { server, query: 'metadata' });
    assert.equal(summary.hits.length, 2);
    assert.ok(summary.hits.every(hit => !Object.hasOwn(hit, 'inputSchema')));

    let cursor;
    const names = [];
    for (let i = 0; i < 2; i++) {
      const page = await call('search_tools', {
        server, query: 'metadata', includeSchema: true, limit: 1,
        ...(cursor ? { cursor } : {}),
      });
      assert.equal(page.hits.length, 1);
      const hit = page.hits[0];
      names.push(hit.tool);
      assert.equal(hit.server, server);
      assert.equal(hit.exposed, '');
      const tagsType = hit.inputSchema.properties.tags.type;
      // Go slices may also accept null; preserve the upstream schema.
      assert.ok(tagsType === 'array' || (Array.isArray(tagsType) && tagsType.includes('array')));
      assert.equal(hit.annotations.readOnlyHint, true);
      const details = await call('get_tool_details', { server, tool: hit.tool });
      assert.equal(details.tool, hit.tool);
      assert.ok(!Object.hasOwn(details, 'name'));
      assert.deepEqual(details.inputSchema, hit.inputSchema);
      const result = await call('call_tool', {
        server, tool: hit.tool, args: { tags: ['客户', 'project'] },
      });
      assert.deepEqual(result.tags, ['客户', 'project']);
      cursor = page.nextCursor;
    }
    assert.deepEqual(names, ['call_tool', 'search_tools']);
    assert.equal(cursor, undefined);
  }

  const invalid = await client.callTool({ name: 'search_tools', arguments: {} });
  assert.equal(invalid.isError, true);
  console.log('Validated tools/list, self-discovery, paged schemas, details and business tags through stdio + HTTP.');
  console.log(`tools/list payload: ${Buffer.byteLength(JSON.stringify(listed))} bytes`);
} finally {
  await client.close();
}
