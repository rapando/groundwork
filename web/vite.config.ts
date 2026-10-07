import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

// Build output is embedded into the Go binary (internal/server/dist).
export default defineConfig({
  plugins: [vue()],
  build: {
    outDir: '../internal/server/dist',
    emptyOutDir: true,
    // woff2 only: every browser groundwork supports can read it.
    assetsInlineLimit: 0,
  },
  server: {
    // `groundwork serve --no-open --port 7420` in another terminal; open the
    // printed URL once so the session cookie is set for the proxy origin.
    proxy: { '/api': 'http://127.0.0.1:7420', '/healthz': 'http://127.0.0.1:7420' },
  },
  test: { environment: 'jsdom', include: ['src/**/*.test.ts'] },
})
