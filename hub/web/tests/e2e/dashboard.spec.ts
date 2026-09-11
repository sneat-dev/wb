import { expect, test } from '@playwright/test';

test('shows only real snapshot states and marks retained metrics stale after refresh failure', async ({ page }) => {
  let requests = 0;
  const requestedURLs: string[] = [];
  await page.route('https://wb-github-app.sneat.dev/**', async (route) => {
    requestedURLs.push(route.request().url());
    requests += 1;
    const headers = {
      'access-control-allow-credentials': 'true',
      'access-control-allow-origin': 'http://127.0.0.1:4321',
      'content-type': 'application/json',
    };
    if (requests === 1) {
      await route.fulfill({
        status: 200,
        headers,
        body: JSON.stringify({
          generated_at: '2026-09-06T06:00:00Z',
          summary: { repositories: 1, open_pulls: 2, merged_pulls: 3, open_issues: 4, releases: 5 },
        }),
      });
      return;
    }
    await route.fulfill({ status: 503, headers, body: JSON.stringify({ error: 'control_plane_not_configured' }) });
  });

  await page.goto('/bench/dashboard/?state=empty&range=7d');
  await expect(page.locator('[data-dashboard-state="error"]')).toBeVisible();
  await expect(page.locator('[data-dashboard-state="empty"]')).toBeHidden();
  await expect(page.locator('[data-dashboard-range="7d"]')).toHaveAttribute('aria-current', 'page');

  await page.locator('[data-dashboard-refresh-button]').click();
  const metrics = page.locator('[data-dashboard-snapshot-metrics]');
  await expect(metrics).toHaveAttribute('data-state', 'current');
  await expect(metrics.locator('.metric-card')).toHaveCount(5);
  await expect(page.locator('[data-dashboard-snapshot-time]')).toContainText('2026-09-06T06:00:00Z');
  await expect(page.locator('[data-dashboard-status-label]')).toHaveText('Authorized snapshot');

  await page.locator('[data-dashboard-refresh-button]').click();
  await expect(metrics).toHaveAttribute('data-state', 'stale');
  await expect(page.locator('[data-dashboard-snapshot-time]')).toContainText('Showing the last authorized snapshot from 2026-09-06T06:00:00Z');
  await expect(page.locator('[data-dashboard-refresh-state]')).toHaveText('Snapshot stale');
  await expect(page.locator('[data-dashboard-state="error"]')).toBeVisible();
  expect(requestedURLs).toEqual([
    'https://wb-github-app.sneat.dev/v0/workbench/dashboard',
    'https://wb-github-app.sneat.dev/v0/workbench/dashboard',
  ]);
});

test('filters the authorized cross-machine worktree inventory and preserves filters in the URL', async ({ page }) => {
  await page.route('https://wb-github-app.sneat.dev/**', async (route) => {
    await route.fulfill({
      status: 200,
      headers: {
        'access-control-allow-credentials': 'true',
        'access-control-allow-origin': 'http://127.0.0.1:4321',
        'content-type': 'application/json',
      },
      body: JSON.stringify({
        generated_at: '2026-09-06T16:00:00Z',
        summary: { repositories: 2, open_pulls: 1, merged_pulls: 0, open_issues: 0, releases: 0 },
        fleet: {
          machines: [
            {
              name: 'laptop', state: 'online', last_seen_at: '2026-09-06T15:59:00Z', worktrees: [
                { task: 'dashboard', repository: 'sneat-co/workbench-web', branch: 'feat/dashboard', status: 'active', last_activity_at: '2026-09-06T15:58:00Z' },
                { task: 'skills-sync', stream: 'cli-rollout', repository: 'strongo/cli-helpers', branch: 'stream/cli-rollout', status: 'ready', last_activity_at: '2026-09-06T15:55:00Z' },
              ],
            },
            {
              name: 'vm', state: 'offline', last_seen_at: '2026-09-05T10:00:00Z', worktrees: [
                {
                  task: 'daemon-recovery', repository: 'sneat-dev/wb', branch: 'fix/daemon-recovery', status: 'blocked',
                  last_activity_at: '2026-09-05T09:50:00Z', needs_attention: true, attention_reason: 'CI failed',
                  pull_request: { number: 431, url: 'https://github.com/sneat-dev/wb/pull/431', state: 'open' },
                },
              ],
            },
          ],
        },
      }),
    });
  });

  await page.goto('/bench/dashboard/?range=30d&machine=vm&attention=1');
  await page.locator('[data-dashboard-refresh-button]').click();

  const visibleWorktrees = page.locator('[data-worktree-table]:visible [data-worktree-rows] tr, [data-worktree-cards]:visible .worktree-card');
  await expect(visibleWorktrees).toHaveCount(1);
  await expect(visibleWorktrees.first()).toContainText('sneat-dev/wb');
  await expect(visibleWorktrees.first()).toContainText('vm · offline');
  await expect(visibleWorktrees.first().getByRole('link', { name: 'Pull request 431, open' })).toHaveAttribute('href', 'https://github.com/sneat-dev/wb/pull/431');
  await expect(page.locator('[data-worktree-count]')).toHaveText('1 of 3 worktrees');
  await expect(page).toHaveURL(/machine=vm/);
  await expect(page).toHaveURL(/attention=1/);

  await page.locator('[data-worktree-filter="machine"]').selectOption('');
  await page.locator('[data-worktree-filter="attention"]').uncheck();
  await page.locator('[data-worktree-filter="task"]').fill('cli');
  await expect(visibleWorktrees).toHaveCount(1);
  await expect(visibleWorktrees.first()).toContainText('strongo/cli-helpers');
  await expect(page).toHaveURL(/task=cli/);
  await expect(page).not.toHaveURL(/machine=vm/);

  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.locator('[data-worktree-table]')).toBeHidden();
  await expect(page.locator('[data-worktree-cards] .worktree-card')).toHaveCount(1);
  await expect(page.locator('[data-worktree-cards] .worktree-card')).toContainText('strongo/cli-helpers');
});
