import { expect, test, type Page } from '@playwright/test'
import { alpha, beta, fleet, now, watch } from './support'

// The Machines page journey against a stubbed fleet: filter -> chip -> select -> panel -> detail route -> back
// (REQ:machines-list, REQ:machine-detail, REQ:detail-routes-share-the-panel), with the machine-metrics route stubbed
// per machine, in the built application under the daemon's content security policy.

const MINUTE = 60_000
const ago = (milliseconds: number) => new Date(Date.now() - milliseconds).toISOString()
const gamma = { machine: 'gamma', machine_id: 'mach-gamma', route: 'cached', observed_at: ago(30 * 60 * MINUTE) }
const machinesFleet = {
  ...fleet,
  machines: [
    { id: 'mach-alpha', ...alpha, wb_version: '1.2.0', os: 'darwin', arch: 'arm64', cpu_count: 10, boot_time: ago(3 * 24 * 60 * MINUTE), repository_count: 2, worktree_count: 2 },
    { id: 'mach-beta', ...beta, wb_version: '1.0.0', transport: 'ssh', repository_count: 1, worktree_count: 1 },
    { id: 'mach-gamma', ...gamma, remote_error: 'daemon_not_running', repository_count: 0, worktree_count: 0 },
  ],
}

const sample = (minutesAgo: number, cpu: number) => ({
  cpu_percent: cpu,
  load1: cpu / 20,
  memory_used_bytes: 8 * 2 ** 30,
  memory_total_bytes: 16 * 2 ** 30,
  disk_free_bytes: 200 * 2 ** 30,
  disk_total_bytes: 500 * 2 ** 30,
  sampled_at: ago(minutesAgo * MINUTE),
})
const answers: Record<string, unknown> = {
  'mach-alpha': { machine: 'mach-alpha', route: 'local', samples: Array.from({ length: 360 }, (_, index) => sample((359 - index) / 6, 20 + (index % 40))) },
  'mach-beta': { machine: 'mach-beta', route: 'cached', samples: [sample(30, 35)] },
  'mach-gamma': { machine: 'mach-gamma', route: 'none', samples: [], reason: 'not_reported' },
}

const listRows = (page: Page) => page.locator('[role=row][data-index]')

async function stubMachines(page: Page) {
  await page.route('**/api/v1/cockpit/fleet', (route) => route.fulfill({ json: { ...machinesFleet, snapshot_at: now }, headers: { ETag: '"machines"', 'Cache-Control': 'no-cache' } }))
  await page.route('**/api/v1/cockpit/session', (route) => route.fulfill({ json: { principal: 'anonymous-local', capabilities: ['fleet.read'], code_browser_url: 'https://codegrapher.dev/' } }))
  await page.route('**/api/v1/cockpit/machine-metrics?**', (route) => route.fulfill({ json: answers[new URL(route.request().url()).searchParams.get('machine') as string] }))
}

// cockpit-views#ac:machines-table-title-and-links, machines-filter-and-stale-chip, machine-detail-metrics-charts
test('filter, chip, select, panel, detail route and back keep each state, with the first header as the title', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await stubMachines(page)
  const expectClean = await watch(page)
  await page.goto('/cockpit/machines')
  await expect(listRows(page)).toHaveCount(3)
  await expect(page.locator('.count')).toHaveText('3 of 3')

  // The first header is the title, in the section-title size; no heading of the page's own repeats it.
  const title = page.getByRole('columnheader', { name: 'Machines' })
  await expect(title).toBeVisible()
  expect(Number.parseFloat(await title.evaluate((element) => getComputedStyle(element).fontSize))).toBeGreaterThanOrEqual(16)
  await expect(page.getByRole('heading', { level: 2 })).toHaveCount(0)

  // Rows: this machine first, the cached ones with their age and stale mark, the older version marked, CPU and memory only where reported.
  await expect(listRows(page).nth(0)).toContainText('alpha')
  await expect(listRows(page).nth(0)).toContainText('local')
  await expect(listRows(page).nth(0).locator('a.name')).toHaveAttribute('href', '/cockpit/machines/mach-alpha')
  await expect(listRows(page).filter({ hasText: 'beta' })).toContainText(/cached, 1\d min ago/)
  await expect(listRows(page).filter({ hasText: 'beta' })).toContainText('1.0.0 older')
  await expect(listRows(page).filter({ hasText: 'gamma' })).toContainText('stale')
  await expect(listRows(page).filter({ hasText: 'gamma' })).toContainText('warning: its daemon is not running')
  await expect(listRows(page).filter({ hasText: 'alpha' }).locator('.meter')).toHaveCount(2)
  await expect(listRows(page).filter({ hasText: 'gamma' }).locator('.meter')).toHaveCount(0)
  await expect(listRows(page).filter({ hasText: 'gamma' })).toContainText('load unknown')

  const filter = page.getByRole('textbox', { name: 'Filter machines' })
  await filter.fill('amm')
  await expect(page).toHaveURL(/q=amm/)
  await expect(listRows(page)).toHaveCount(1)
  await filter.fill('')
  await page.getByRole('button', { name: 'Stale', exact: true }).click()
  await expect(page).toHaveURL(/chips=stale/)
  await expect(listRows(page)).toHaveCount(1)
  await page.getByRole('button', { name: 'Stale', exact: true }).click()
  await page.getByRole('button', { name: 'Older WB' }).click()
  await expect(page).toHaveURL(/chips=outdated/)
  await expect(listRows(page)).toHaveCount(1)
  await expect(listRows(page).first()).toContainText('beta')
  await page.getByRole('button', { name: 'Older WB' }).click()
  await expect(listRows(page)).toHaveCount(3)

  // The row selects on a click; the panel has the summary, the source of the metrics and, once they scroll near, the charts.
  await listRows(page).nth(0).locator('[role=gridcell]:not(.open-cell)').first().click()
  await expect(page).toHaveURL(/sel=mach-alpha/)
  const panel = page.getByRole('complementary')
  await expect(panel.getByRole('heading', { level: 2 })).toHaveText('alpha')
  await expect(panel).toContainText('darwin')
  await expect(panel).toContainText('3 d')
  await expect(panel.locator('.source')).toContainText('local: this machine')
  await expect(panel.locator('details.raw')).not.toHaveAttribute('open', '')

  // The detail route is the same content, with the four charts.
  await page.getByRole('grid').focus()
  await page.keyboard.press('o')
  await expect(page).toHaveURL(/\/cockpit\/machines\/mach-alpha$/)
  const content = page.locator('app-machine-panel')
  await expect(content.getByRole('heading', { level: 2 })).toHaveText('alpha')
  await expect(content.locator('app-machine-charts canvas')).toHaveCount(4)
  await expect(content.locator('app-machine-charts .title')).toHaveText(['CPU', 'Load (1 minute)', 'Memory used', 'Disk free'])
  await expect(content.locator('section').last()).toHaveAttribute('aria-label', 'Raw data')
  await expect(content.locator('[aria-label="Counts"] a').first()).toHaveAttribute('href', '/cockpit/repositories?machine=mach-alpha')
  await expect(page.getByRole('link', { name: /Machines/ }).first()).toBeVisible()

  // Back restores the list and the selection.
  await page.goBack()
  await expect(page).toHaveURL(/sel=mach-alpha/)
  await expect(page.getByRole('complementary').getByRole('heading', { level: 2 })).toHaveText('alpha')
  await expectClean()
})

// cockpit-views#ac:machine-without-metrics-says-so, remote-error-is-visible
test('a machine without metrics says so, a cached sample has no history chart, and the fix is a command to copy', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await stubMachines(page)
  const expectClean = await watch(page)
  await page.goto('/cockpit/machines/mach-gamma')
  await expect(page.locator('.source')).toHaveText('Metrics are not reported for this machine.')
  await expect(page.locator('app-machine-charts, canvas')).toHaveCount(0)
  await expect(page.locator('app-machine-panel')).toContainText('gamma: its daemon is not running')
  const commands = page.locator('app-copy-command-list')
  await expect(commands).toContainText('wb daemon start')
  await expect(commands).toContainText('run on gamma')
  await commands.getByRole('button', { name: /Copy command: Fix the remote read/ }).click()
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('wb daemon start')

  await page.goto('/cockpit/machines/mach-beta')
  await expect(page.locator('.source')).toContainText('cached: the latest sample of its published snapshot, 30 min ago')
  await expect(page.locator('app-machine-panel')).toContainText('35%')
  await expect(page.locator('app-machine-charts, canvas')).toHaveCount(0)
  await expectClean()
})

test('the Machines panel is a full-screen sheet on a phone, with no sideways scroll at 360 px', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 800 })
  await stubMachines(page)
  await page.goto('/cockpit/machines')
  await expect(listRows(page).first()).toBeVisible()
  let widths = await page.evaluate(() => ({ page: document.documentElement.scrollWidth, window: window.innerWidth }))
  expect(widths.page).toBeLessThanOrEqual(widths.window)
  await listRows(page).first().locator('[role=gridcell]:not(.open-cell)').first().click()
  const sheet = page.getByRole('dialog')
  await expect(sheet).toBeVisible()
  expect(((await sheet.boundingBox()) as { width: number }).width).toBeGreaterThanOrEqual(359)
  await page.keyboard.press('Escape')
  await expect(sheet).toBeHidden()
  await page.goto('/cockpit/machines/mach-alpha')
  await expect(page.locator('app-machine-charts canvas')).toHaveCount(4)
  widths = await page.evaluate(() => ({ page: document.documentElement.scrollWidth, window: window.innerWidth }))
  expect(widths.page).toBeLessThanOrEqual(widths.window)
})
