import { expect, test, type Page } from '@playwright/test'
import { alpha, beta, checkedAt, fleet, now, observed, watch } from './support'

// The Tasks page journey against a stubbed fleet: filter -> chip -> select -> panel -> detail route -> back
// (REQ:tasks-list, REQ:task-detail, REQ:detail-routes-share-the-panel), in the built application under the
// daemon's content security policy.

const pull = { id: 'pr-1', ...alpha, repository: 'repo-cli', worktree: 'wt-1', number: 7, url: 'https://github.com/specscore/specscore-cli/pull/7', state: 'open', mergeable: 'clean', checks_total: 4, checks_passed: 3, checks_failed: 0, checks_pending: 1, checks_green: false, checked_at: now }
const tasksFleet = {
  ...fleet,
  worktrees: [
    ...fleet.worktrees,
    { id: 'wt-4', ...alpha, repository: 'repo-web', task: 'add-search', branch: 'task/add-search', owner_state: 'idle', last_activity_at: observed, ahead: 0, has_upstream: true },
    { id: 'wt-5', ...beta, repository: 'repo-far', task: 'add-search', branch: 'task/add-search', owner_state: 'idle', last_activity_at: observed },
  ],
  pull_requests: [pull],
  agents: [{ id: 'run-7', ...alpha, kind: 'run', run_id: 'run-7', runtime: 'claude', model: 'opus', state: 'running', worktrees: ['wt-1'] }],
}

const listRows = (page: Page) => page.locator('[role=row][data-index]')

async function stubTasks(page: Page) {
  await page.route('**/api/v1/cockpit/fleet', (route) => route.fulfill({ json: tasksFleet, headers: { ETag: '"tasks"', 'Cache-Control': 'no-cache', ...checkedAt() } }))
  await page.route('**/api/v1/cockpit/session', (route) => route.fulfill({ json: { principal: 'anonymous-local', capabilities: ['fleet.read'], code_browser_url: 'https://codegrapher.dev/' } }))
  await page.route('**/api/v1/cockpit/machine-metrics?**', (route) => route.fulfill({ json: { machine: 'x', route: 'local', samples: [] } }))
}

test('filter, chip, select, panel, detail route and back keep each state, and the panel and the page say why the task is in its state', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await stubTasks(page)
  const expectClean = await watch(page)
  await page.goto('/cockpit/tasks')
  await expect(listRows(page)).toHaveCount(3)
  await expect(page.locator('.count')).toHaveText('3 of 3')

  const filter = page.getByRole('textbox', { name: 'Filter tasks' })
  await filter.fill('add')
  await expect(page).toHaveURL(/q=add/)
  await expect(listRows(page)).toHaveCount(1)
  await filter.fill('')
  await page.getByRole('button', { name: 'Has pull request', exact: true }).click()
  await expect(page).toHaveURL(/chips=pr/)
  await expect(listRows(page)).toHaveCount(1)
  await expect(listRows(page).first()).toContainText('add-search')
  await expect(listRows(page).first()).toContainText('#7')
  await expect(listRows(page).first()).toContainText('3/4')

  // The row selects on a click, and the panel names the state in words.
  await listRows(page).first().locator('[role=gridcell]:not(.open-cell)').last().click()
  await expect(page).toHaveURL(/sel=add-search/)
  const panel = page.getByRole('complementary')
  await expect(panel.getByRole('heading', { level: 2 })).toHaveText('add-search')
  await expect(panel.locator('.state')).toContainText('Not ready: ')
  await expect(panel.getByRole('region', { name: 'Worktrees' }).getByRole('listitem')).toHaveCount(3)
  await expect(panel.getByRole('region', { name: 'Pull requests' }).locator('app-pr-chip')).toHaveCount(1)
  await expect(panel.locator('details.raw')).not.toHaveAttribute('open', '')

  // The detail route is the same content.
  await page.getByRole('grid').focus()
  await page.keyboard.press('o')
  await expect(page).toHaveURL(/\/cockpit\/tasks\/detail\?task=add-search$/)
  const content = page.locator('app-task-panel')
  await expect(content.getByRole('heading', { level: 2 })).toHaveText('add-search')
  await expect(content.locator('.state')).toContainText('Not ready: ')
  await expect(content.locator('section').last()).toHaveAttribute('aria-label', 'Raw data')
  await expect(page.getByRole('link', { name: /Tasks/ }).first()).toBeVisible()

  // Back restores the list, the chip and the selection.
  await page.goBack()
  await expect(page).toHaveURL(/chips=pr/)
  await expect(page).toHaveURL(/sel=add-search/)
  await expect(page.getByRole('button', { name: 'Has pull request', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByRole('complementary').getByRole('heading', { level: 2 })).toHaveText('add-search')
  await expectClean()
})

test('a task that only another machine reports says so in the row and the page, and offers no land command', async ({ page }) => {
  await stubTasks(page)
  await page.goto('/cockpit/tasks')
  const row = listRows(page).filter({ hasText: 'far-task' })
  await expect(row).toContainText('via beta')
  await page.goto('/cockpit/tasks/detail?task=far-task')
  await expect(page.locator('.state .source')).toContainText('As reported by')
  await expect(page.locator('.state .source')).toContainText('beta')
  await expect(page.locator('app-action-slot')).toHaveCount(0)
  await expect(page.locator('app-copy-command-list')).not.toContainText('wb pr land')
})

test('the task name that needs encoding opens its page, and an unknown task says it is not there', async ({ page }) => {
  await stubTasks(page)
  await page.goto('/cockpit/tasks/detail?task=fix%2Fci%20100%25')
  await expect(page.getByText('This task is not in the fleet document')).toBeVisible()
  await page.goto('/cockpit/tasks/detail')
  await expect(page.getByText('No task was named')).toBeVisible()
})

test('the Tasks panel is a full-screen sheet on a phone, with no sideways scroll at 360 px', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 800 })
  await stubTasks(page)
  await page.goto('/cockpit/tasks')
  await listRows(page).first().locator('[role=gridcell]:not(.open-cell)').last().click()
  const sheet = page.getByRole('dialog')
  await expect(sheet).toBeVisible()
  expect(((await sheet.boundingBox()) as { width: number }).width).toBeGreaterThanOrEqual(359)
  await page.keyboard.press('Escape')
  await expect(sheet).toBeHidden()
  const widths = await page.evaluate(() => ({ page: document.documentElement.scrollWidth, window: window.innerWidth }))
  expect(widths.page).toBeLessThanOrEqual(widths.window)
})
