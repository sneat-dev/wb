import { defineConfig, devices } from '@playwright/test';

export default defineConfig({
  testDir: './tests/e2e',
  webServer: {
    command: 'pnpm build:fast && pnpm astro preview --host 127.0.0.1',
    // This build has no index route (the marketing site owns `/bench/`; only
    // dashboard routes live here), so probe a route this project actually
    // serves. ASTRO_PREVIEW_BACKGROUND disables Astro's auto-detected-AI-agent
    // preview backgrounding, which would otherwise detach the preview server
    // from this process before Playwright can observe it staying up.
    url: 'http://127.0.0.1:4321/bench/dashboard/',
    reuseExistingServer: !process.env.CI,
    env: { ASTRO_PREVIEW_BACKGROUND: '0' },
  },
  use: { baseURL: 'http://127.0.0.1:4321' },
  projects: [
    { name: 'desktop', use: { ...devices['Desktop Chrome'], permissions: ['clipboard-read', 'clipboard-write'] } },
    { name: 'mobile', use: { ...devices['iPhone 13'] } },
  ],
});
