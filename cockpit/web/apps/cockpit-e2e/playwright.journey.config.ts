import { defineConfig, devices } from '@playwright/test'

// The whole journey on the real chain: a wb binary built here, its own daemon
// on a free port, a temporary projects root. Needs Go and the production web
// build (pnpm test:journey runs both). No web server is started here; the test
// starts, and always stops, the daemon itself.
export default defineConfig({
  testDir: './src/journey',
  testMatch: '**/*.e2e.ts',
  outputDir: '../../test-results/journey',
  reporter: process.env['CI'] ? [['github'], ['html', { outputFolder: '../../playwright-report', open: 'never' }]] : 'list',
  timeout: 300_000,
  workers: 1,
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
