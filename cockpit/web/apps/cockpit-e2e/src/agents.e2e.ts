import { expect, test, type Page } from '@playwright/test'
import { alpha, beta, checkedAt, fleet, observed, watch } from './support'

// The Agents page journey against a stubbed fleet: filter -> chip -> select -> panel -> detail route -> back
// (REQ:agents-list, REQ:agent-detail, REQ:detail-routes-share-the-panel), in the built application under the
// daemon's content security policy.

const hoursAgo = (hours: number) => new Date(Date.now() - hours * 3_600_000).toISOString()
const agentsFleet = {
  ...fleet,
  machines: [...fleet.machines, { id: 'mach-gamma', machine: 'gamma', machine_id: 'mach-gamma', route: 'cached', observed_at: observed, repository_count: 0, worktree_count: 0 }],
  agents: [
    { id: 'run-7', ...alpha, kind: 'run', run_id: 'run-7', runtime: 'claude', model: 'sonnet-5-5', state: 'running', activity: 'working', repository: 'repo-cli', worktrees: ['wt-1'], started_at: hoursAgo(2) },
    { id: 'ag-1', ...alpha, kind: 'session', session_id: 'wbs-3da4ea95-1111-4222-8333-444455556666', runtime: 'claude', model: 'opus', state: 'live', started_at: hoursAgo(3) },
    { id: 'ag-2', ...beta, kind: 'run', run_id: 'run-9', runtime: 'codex', state: 'failed', repository: 'repo-far', started_at: hoursAgo(9), finished_at: hoursAgo(8), exit_code: 1 },
  ],
}

const listRows = (page: Page) => page.locator('[role=row][data-index]')

async function stubAgents(page: Page) {
  await page.route('**/api/v1/cockpit/fleet', (route) => route.fulfill({ json: agentsFleet, headers: { ETag: '"agents"', 'Cache-Control': 'no-cache', ...checkedAt() } }))
  await page.route('**/api/v1/cockpit/session', (route) => route.fulfill({ json: { principal: 'anonymous-local', capabilities: ['fleet.read'], code_browser_url: 'https://codegrapher.dev/' } }))
  await page.route('**/api/v1/cockpit/machine-metrics?**', (route) => route.fulfill({ json: { machine: 'x', route: 'local', samples: [] } }))
}

test('filter, chip, select, panel, detail route and back keep each state, and no row shows an identifier', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await stubAgents(page)
  const expectClean = await watch(page)
  await page.goto('/cockpit/agents')
  await expect(listRows(page)).toHaveCount(3)
  await expect(page.locator('.count')).toHaveText('3 of 3')
  await expect(listRows(page).first()).toContainText('claude · sonnet-5-5')
  await expect(listRows(page).first()).toContainText('working')
  await expect(listRows(page).nth(1)).toContainText('session, started 3 h ago')
  await expect(listRows(page).nth(1)).toContainText('state not reported')
  await expect(page.locator('[role=grid]')).not.toContainText('wbs-3da4ea95')
  await expect(page.locator('.note')).toContainText('gamma reports none')

  const filter = page.getByRole('textbox', { name: 'Filter agents' })
  await filter.fill('codex')
  await expect(page).toHaveURL(/q=codex/)
  await expect(listRows(page)).toHaveCount(1)
  await filter.fill('')
  await page.getByRole('button', { name: 'claude', exact: true }).click()
  await expect(page).toHaveURL(/chips=runtime-claude/)
  await expect(listRows(page)).toHaveCount(2)

  // The row selects on a click; the panel has the id with its copy button and says a session cannot be controlled.
  await listRows(page).nth(1).locator('[role=gridcell]:not(.open-cell)').first().click()
  await expect(page).toHaveURL(/sel=ag-1/)
  const panel = page.getByRole('complementary')
  await expect(panel.getByRole('heading', { level: 2 })).toHaveText('claude · opus')
  await expect(panel.locator('.control')).toHaveText('This session was not started by wb; it cannot be stopped or messaged from here.')
  await expect(panel).toContainText('wbs-3da4ea95-1111-4222-8333-444455556666')
  await panel.getByRole('button', { name: 'Copy session id' }).click()
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('wbs-3da4ea95-1111-4222-8333-444455556666')
  await expect(panel.locator('details.raw')).not.toHaveAttribute('open', '')

  // The detail route is the same content.
  await page.getByRole('grid').focus()
  await page.keyboard.press('o')
  await expect(page).toHaveURL(/\/cockpit\/agents\/ag-1$/)
  const content = page.locator('app-agent-panel')
  await expect(content.getByRole('heading', { level: 2 })).toHaveText('claude · opus')
  await expect(content.locator('.control')).toBeVisible()
  await expect(content.locator('section').last()).toHaveAttribute('aria-label', 'Raw data')
  await expect(page.getByRole('link', { name: /Agents/ }).first()).toBeVisible()

  // Back restores the list, the chip and the selection.
  await page.goBack()
  await expect(page).toHaveURL(/chips=runtime-claude/)
  await expect(page).toHaveURL(/sel=ag-1/)
  await expect(page.getByRole('button', { name: 'claude', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByRole('complementary').getByRole('heading', { level: 2 })).toHaveText('claude · opus')
  await expectClean()
})

test('a dispatched run offers status, logs and stop; an agent of another machine says where it is and offers nothing', async ({ page }) => {
  await stubAgents(page)
  await page.goto('/cockpit/agents/run-7')
  await expect(page.locator('.why')).toContainText('Running for 2 h on alpha')
  await expect(page.locator('app-copy-command-list')).toContainText('wb agent stop')
  await expect(page.locator('[aria-label="Worktrees"] li')).toHaveCount(1)
  await page.goto('/cockpit/agents/ag-2')
  await expect(page.locator('.why')).toContainText('Finished 8 h ago, exit code 1')
  await expect(page.locator('.control')).toContainText('reported by beta')
  await expect(page.locator('app-copy-command-list li')).toHaveCount(0)
  await page.goto('/cockpit/agents/nope')
  await expect(page.getByText('This agent is not in the fleet document')).toBeVisible()
})

test('the Agents panel is a full-screen sheet on a phone, with no sideways scroll at 360 px', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 800 })
  await stubAgents(page)
  await page.goto('/cockpit/agents')
  await listRows(page).first().locator('[role=gridcell]:not(.open-cell)').first().click()
  const sheet = page.getByRole('dialog')
  await expect(sheet).toBeVisible()
  expect(((await sheet.boundingBox()) as { width: number }).width).toBeGreaterThanOrEqual(359)
  await page.keyboard.press('Escape')
  await expect(sheet).toBeHidden()
  const widths = await page.evaluate(() => ({ page: document.documentElement.scrollWidth, window: window.innerWidth }))
  expect(widths.page).toBeLessThanOrEqual(widths.window)
})
