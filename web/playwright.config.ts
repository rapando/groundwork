import { defineConfig } from '@playwright/test'

// Uses the machine's installed Chrome (no browser download). Set GW_BIN to a
// built groundwork binary; `make e2e` does that.
export default defineConfig({
  testDir: './e2e',
  // generous: every test boots groundwork and real tools, and a busy machine
  // (Spotlight indexing the temp repos, say) can make a first load take seconds
  timeout: 60_000,
  expect: { timeout: 15_000 },
  workers: 1,
  use: { channel: 'chrome', viewport: { width: 1440, height: 1100 } },
})
