/// <reference types="vitest/config" />
import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';
import { fileURLToPath, URL } from 'node:url';

// The gateway's own default. The dev server proxies to it so that the
// UI runs against a real backend rather than a stand-in — the event
// stream in particular is not worth faking.
const BACKEND = process.env['MCP_FOYER_BACKEND'] ?? 'http://127.0.0.1:7788';

export default defineConfig({
  plugins: [react()],

  resolve: {
    alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) },
  },

  server: {
    port: 5173,
    // A silent fallback to the next free port would break the
    // gateway's websocket origin check, and the only symptom would be
    // the UI reporting itself disconnected with no reason given.
    // Failing to start is the clearer outcome.
    strictPort: true,
    proxy: {
      '/api': { target: BACKEND, changeOrigin: false },
      // changeOrigin stays off so that the backend sees this page's
      // origin and its websocket origin check is actually exercised in
      // development, rather than passing only because the proxy
      // disguised it.
      '/ws': { target: BACKEND, ws: true, changeOrigin: false },
      '/mcp': { target: BACKEND, changeOrigin: false },
    },
  },

  build: {
    // Read by the Go binary's embed directive at build time.
    outDir: 'dist',
    emptyOutDir: true,
    // The whole app is served from a single binary on a local machine,
    // so there is no cache-warming or CDN to play to: fewer, larger
    // files load faster here than many small ones.
    chunkSizeWarningLimit: 900,
  },

  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    include: ['src/**/*.test.{ts,tsx}'],
    restoreMocks: true,
  },
});
