import { expect, test } from '@playwright/test';

const authorizedStatus = {
  generated_at: '2026-09-06T18:00:00Z',
  connection: { state: 'connected', app_name: 'Workbench', account: 'alex', connect_url: 'https://github.com/settings/installations/123' },
  installations: [
    { id: 'installed-1', account: 'sneat-dev', account_type: 'organization', state: 'installed', repository_selection: 'all', repositories: 4, manage_url: 'https://github.com/settings/installations/123' },
    { id: 'suspended-1', account: 'acme', state: 'suspended' },
  ],
  machines: [
    { id: 'laptop', name: 'Alex laptop', state: 'online', last_seen_at: '2026-09-06T17:59:00Z' },
    { id: 'vm', name: 'Build VM', state: 'offline' },
  ],
  delivery: {
    last_received: { delivery_id: 'delivery-8', event: 'push', occurred_at: '2026-09-06T17:58:00Z' },
    last_acknowledged: { delivery_id: 'delivery-7', event: 'push', occurred_at: '2026-09-06T17:57:00Z' },
  },
  pending_refreshes: [{ id: 'refresh-1', repository: 'sneat-dev/wb', event: 'push', queued_at: '2026-09-06T17:58:01Z', installation_id: 'installed-1' }],
  errors: [{ code: 'installation_suspended', message: 'Restore this installation.', installation_id: 'suspended-1' }],
};

test('shows authorized delivery status and persists installation filters in the URL', async ({ page }) => {
  let enrolledName = '';
  await page.route('https://wb-github-app.sneat.dev/v0/workbench/github/status', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify(authorizedStatus),
  }));
  await page.route('https://wb-github-app.sneat.dev/v0/workbench/machines/enroll', async (route) => {
    enrolledName = (await route.request().postDataJSON()).name;
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        machine: { id: 'machine-new', name: enrolledName },
        identity: { id: 'viewer-1', display_name: 'Alex' },
        token: 'one-time-opaque-token',
        enrolled_at: '2026-09-06T18:04:00Z',
      }),
    });
  });

  await page.goto('/bench/dashboard/github/?state=pending');
  await expect(page.locator('[data-github-indicator-label]')).toHaveText('GitHub connected');
  await expect(page.locator('[data-github-machine-count]')).toHaveText('2');
  await expect(page.locator('[data-github-last-received]')).toContainText('push');
  await expect(page.locator('[data-github-installations] .installation-row')).toHaveCount(1);
  await expect(page.locator('[data-github-installations]')).toContainText('sneat-dev');
  await expect(page.locator('[data-github-pending] tr')).toHaveCount(1);

  await page.locator('[data-github-filter="error"]').click();
  await expect(page).toHaveURL(/state=error/);
  await expect(page.locator('[data-github-installations] .installation-row')).toHaveCount(1);
  await expect(page.locator('[data-github-installations]')).toContainText('acme');

  await page.locator('#machine-name').fill('studio-mac');
  await page.locator('[data-machine-enrollment] button[type="submit"]').click();
  await expect(page.locator('[data-machine-enrollment-result]')).toBeVisible();
  await expect(page.locator('[data-machine-enrollment-token]')).toHaveText('one-time-opaque-token');
  expect(enrolledName).toBe('studio-mac');
  expect(await page.evaluate(() => ({ local: localStorage.length, session: sessionStorage.length }))).toEqual({ local: 0, session: 0 });
  await page.locator('[data-machine-token-clear]').click();
  await expect(page.locator('[data-machine-enrollment-result]')).toBeHidden();
  await expect(page.locator('[data-machine-enrollment-token]')).toBeEmpty();
});

test('fails closed when the viewer is not authorized', async ({ page }) => {
  await page.route('https://wb-github-app.sneat.dev/v0/workbench/github/status', (route) => route.fulfill({
    status: 401,
    contentType: 'application/json',
    body: JSON.stringify({ error: 'viewer_unavailable' }),
  }));

  await page.goto('/bench/dashboard/github/');
  await expect(page.locator('[data-github-state="error"]')).toBeVisible();
  await expect(page.locator('[data-github-error-message]')).toContainText('authorized account');
  await expect(page.locator('[data-github-content]')).toBeHidden();
});
