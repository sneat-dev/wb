import { defineConfig, devices } from '@playwright/test'

const port = Number(process.env['COCKPIT_E2E_PORT'] || '4300')

// The production build, served under /cockpit/ with the daemon's policy.
export default defineConfig({
  testDir: './src',
  testMatch: '**/*.e2e.ts',
  outputDir: '../../test-results',
  reporter: process.env['CI'] ? 'github' : 'list',
  use: { baseURL: `http://127.0.0.1:${port}` },
  webServer: {
    command: `PORT=${port} node tools/serve-dist.mjs`,
    cwd: '../..',
    url: `http://127.0.0.1:${port}/cockpit/`,
    reuseExistingServer: !process.env['CI'],
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
