import { defineConfig, devices } from '@playwright/test'

const port = Number(process.env['COCKPIT_E2E_PORT'] || '4300')
// The preview build (dist-preview, with the control-surface gallery) is served beside the production one.
export const previewPort = port + 1

// The production build, served under /cockpit/ with the daemon's policy.
export default defineConfig({
  testDir: './src',
  testMatch: '**/*.e2e.ts',
  // The journey needs Go and runs under its own config (playwright.journey.config.ts).
  testIgnore: '**/journey/**',
  outputDir: '../../test-results',
  reporter: process.env['CI'] ? 'github' : 'list',
  use: { baseURL: `http://127.0.0.1:${port}` },
  webServer: [
    {
      command: `PORT=${port} node tools/serve-dist.mjs`,
      cwd: '../..',
      url: `http://127.0.0.1:${port}/cockpit/`,
      reuseExistingServer: !process.env['CI'],
    },
    {
      command: `PORT=${previewPort} DIST_DIR=dist-preview node tools/serve-dist.mjs`,
      cwd: '../..',
      url: `http://127.0.0.1:${previewPort}/cockpit/`,
      reuseExistingServer: !process.env['CI'],
    },
  ],
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
