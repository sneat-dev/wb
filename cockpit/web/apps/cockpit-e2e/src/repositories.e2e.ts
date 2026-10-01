import { expect, test, type Page } from '@playwright/test'
import { watch } from './support'

// The Repositories page, its panel and its detail route, in the built application against a stubbed
// daemon: one row per repository across machines, the one-click sort presets, the machine chips, the
// panel's lazily read branches and the older repository address.

const hour = 60 * 60 * 1000
const now = new Date().toISOString()
const ago = (milliseconds: number) => new Date(Date.now() - milliseconds).toISOString()
const alpha = { machine: 'alpha', machine_id: 'mach-alpha', route: 'local', observed_at: now }
const beta = { machine: 'beta', machine_id: 'mach-beta', route: 'cached', observed_at: ago(3 * hour) }
const gamma = { machine: 'gamma', machine_id: 'mach-gamma', route: 'cached', observed_at: ago(49 * hour) }

const fleet = {
  schema_version: 2,
  snapshot_at: now,
  warming_up: false,
  repositories_total: 4,
  repositories_scanned: 4,
  diagnostics: 0,
  code_index_provider: 'codegrapher',
  machines: [
    { id: 'mach-alpha', ...alpha, wb_version: '1.0.0', repository_count: 3, worktree_count: 2 },
    { id: 'mach-beta', ...beta, repository_count: 1, worktree_count: 1 },
    { id: 'mach-gamma', ...gamma, repository_count: 1, worktree_count: 0 },
  ],
  repositories: [
    { id: 'go-a', ...alpha, host: 'github.com', name: 'sneat-co/sneat-go', default_branch: 'main', worktree_count: 2, local_branch_count: 5, remote_branch_count: 12, active_agent_count: 0, last_activity_at: ago(2 * hour), remote_url_web: 'https://github.com/sneat-co/sneat-go', code_index: [{ indexer: 'codegrapher', state: 'fresh' }] },
    { id: 'go-b', ...beta, host: 'github.com', name: 'sneat-co/sneat-go', worktree_count: 1, local_branch_count: 3, remote_branch_count: 4 },
    { id: 'go-c', ...gamma, host: 'github.com', name: 'sneat-co/sneat-go', worktree_count: 0 },
    { id: 'wb-a', ...alpha, host: 'github.com', name: 'sneat-dev/wb', default_branch: 'main', worktree_count: 1, local_branch_count: 40, remote_branch_count: 2, active_agent_count: 0, last_activity_at: ago(30 * hour), remote_url_web: 'https://github.com/sneat-dev/wb' },
    { id: 'lone', ...alpha, name: 'acme/lone', default_branch: 'main', worktree_count: 0, local_branch_count: 1, remote_branch_count: 0, active_agent_count: 0, last_activity_at: ago(300 * hour), error: 'scan timed out' },
  ],
  worktrees: [
    { id: 'w1', ...alpha, repository: 'go-a', task: 'fix-ci', branch: 'agent/fix-ci', owner_state: 'active', last_activity_at: ago(2 * hour) },
    { id: 'w2', ...alpha, repository: 'go-a', task: 'add-search', branch: 'add-search', owner_state: 'idle', last_activity_at: ago(20 * hour) },
    { id: 'w3', ...beta, repository: 'go-b', task: 'far-task', branch: 'far-task' },
    { id: 'w4', ...alpha, repository: 'wb-a', task: 'cockpit', branch: 'cockpit', owner_state: 'idle', last_activity_at: ago(30 * hour) },
  ],
  pull_requests: [],
  agents: [],
}

const requests: string[] = []

async function stub(page: Page, options: { codeBrowser?: string; branchesDelayMs?: number } = {}) {
  requests.length = 0
  await page.route('**/api/v1/cockpit/fleet', (route) => route.fulfill({ json: fleet, headers: { ETag: '"repositories"', 'Cache-Control': 'no-cache' } }))
  await page.route('**/api/v1/cockpit/session', (route) => route.fulfill({ json: { principal: 'anonymous-local', capabilities: ['fleet.read'], ...(options.codeBrowser ? { code_browser_url: options.codeBrowser } : {}) } }))
  await page.route('**/api/v1/cockpit/machine-metrics?**', (route) => route.fulfill({ json: { machine: 'x', route: 'local', samples: [] } }))
  await page.route('**/api/v1/cockpit/branches?**', async (route) => {
    const id = new URL(route.request().url()).searchParams.get('repository') as string
    requests.push(id)
    if (options.branchesDelayMs) await new Promise((done) => setTimeout(done, options.branchesDelayMs))
    const cached = id !== 'go-a'
    await route.fulfill({
      json: cached
        ? { branches: [], reason: 'This checkout is cached from another machine; its branches are listed on that machine.' }
        : { branches: [{ id: 'b1', ...alpha, repository: id, name: 'main', scope: 'local', upstream: 'origin/main' }, { id: 'b2', ...alpha, repository: id, name: 'agent/fix-ci', scope: 'local', ahead: 2, task: 'fix-ci' }, { id: 'b3', ...alpha, repository: id, name: 'origin/main', scope: 'remote' }] },
    })
  })
}

const listRows = (page: Page) => page.locator('[role=row][data-index]')
const expectNames = (page: Page, expected: string[]) => expect(listRows(page).locator('app-repo-name')).toHaveText(expected)

// cockpit-views#ac:repositories-merge-across-machines, repositories-quick-filters, repository-detail-loads-branches-lazily
test('filter, chip, select, panel with lazily read branches, detail page, back', async ({ page }) => {
  await stub(page, { codeBrowser: 'https://codegrapher.dev/', branchesDelayMs: 600 })
  const expectClean = await watch(page)
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto('/cockpit/repositories')
  await expect(listRows(page)).toHaveCount(3)
  // One row for the repository on three machines, with a chip for each.
  const go = listRows(page).filter({ hasText: 'sneat-go' })
  await expect(go.locator('a.machine')).toHaveText(['alpha', /beta\s*3 h/, /gamma\s*2 d · stale/])
  await expect(page.locator('.count')).toHaveText('3 of 3')

  // A wildcard filter, then a chip.
  await page.getByRole('textbox', { name: 'Filter repositories' }).fill('sneat-*/*')
  await expect(listRows(page)).toHaveCount(2)
  await expect(page).toHaveURL(/q=sneat-/)
  await page.getByRole('button', { name: 'Has worktrees' }).click()
  await expect(page).toHaveURL(/chips=worktrees/)
  await expect(listRows(page)).toHaveCount(2)
  await page.getByRole('textbox', { name: 'Filter repositories' }).fill('sneat-co/*')
  await expect(listRows(page)).toHaveCount(1)

  // Selecting a row opens the panel: nothing is asked of the branches route until the panel is there, and then only for the first machine.
  expect(requests).toEqual([])
  await listRows(page).first().locator('[role=gridcell]').nth(2).click()
  await expect(page).toHaveURL(/sel=go-a/)
  const panel = page.getByRole('complementary')
  await expect(panel.getByRole('heading', { level: 2 })).toHaveText('sneat-co/sneat-go')
  await expect(panel.locator('app-repository-machine-section')).toHaveCount(3)
  await expect(panel.locator('.skeleton')).toHaveCount(3)
  await expect(panel.locator('.branch-name').first()).toHaveText('main')
  await expect(panel.locator('.skeleton')).toHaveCount(0)
  expect(requests).toEqual(['go-a'])
  // The second machine is closed until opened; opening it asks for its own checkout and shows the daemon's reason.
  const beta = panel.locator('app-repository-machine-section').nth(1)
  await expect(beta.locator('details')).not.toHaveAttribute('open', '')
  await beta.locator('summary').click()
  await expect(beta.getByText('its branches are listed on that machine')).toBeVisible()
  expect(requests).toEqual(['go-a', 'go-b'])
  await expect(panel.getByText('Copy template')).toBeVisible()

  // The detail page is the same content.
  await page.getByRole('link', { name: 'Open sneat-co/sneat-go', exact: true }).click()
  await expect(page).toHaveURL(/\/cockpit\/repositories\/github\.com\/sneat-co\/sneat-go$/)
  const content = page.locator('app-repository-panel')
  await expect(content.getByRole('heading', { level: 2 })).toHaveText('sneat-co/sneat-go')
  await expect(content.locator('app-repository-machine-section')).toHaveCount(3)
  await expect(content.locator('.branch-name').first()).toHaveText('main')
  await expect(content.locator('section').last()).toHaveAttribute('aria-label', 'Raw data')
  await page.goBack()
  await expect(page).toHaveURL(/q=sneat-co/)
  await expect(page.getByRole('textbox', { name: 'Filter repositories' })).toHaveValue('sneat-co/*')
  await expectClean()
})

// cockpit-views#ac:repository-detail-loads-branches-lazily: the older repository id still opens the page
test('the older repository id and the address by name both open the merged repository', async ({ page }) => {
  await stub(page)
  const expectClean = await watch(page)
  for (const address of ['/cockpit/repositories/go-b', '/cockpit/repositories/github.com/SNEAT-co/sneat-go', '/cockpit/repositories/-/acme/lone']) {
    await page.goto(address)
    await expect(page.locator('app-repository-panel h2')).toHaveText(address.endsWith('lone') ? 'acme/lone' : 'sneat-co/sneat-go')
  }
  await expect(page.locator('app-repository-panel')).toContainText('scan timed out')
  await page.goto('/cockpit/repositories/github.com/nobody/nothing')
  await expect(page.getByText('This repository is not in the fleet document')).toBeVisible()
  await expectClean()
})

// cockpit-views#ac:repositories-sort-presets
test('Recent, Most worktrees and Most branches switch the order in one click and survive a reload', async ({ page }) => {
  await stub(page)
  const expectClean = await watch(page)
  await page.goto('/cockpit/repositories')
  const group = page.getByRole('group', { name: 'Show' })
  await expect(group.getByRole('radio', { name: 'Recent' })).toBeChecked()
  await expectNames(page, ['sneat-co/sneat-go', 'sneat-dev/wb', 'acme/lone'])

  await group.getByText('Most branches').click()
  await expect(page).toHaveURL(/sort=branches&dir=desc/)
  await expectNames(page, ['sneat-dev/wb', 'sneat-co/sneat-go', 'acme/lone'])

  await group.getByText('Most worktrees').click()
  await expect(page).toHaveURL(/sort=worktrees&dir=desc/)
  await expectNames(page, ['sneat-co/sneat-go', 'sneat-dev/wb', 'acme/lone'])

  await page.reload()
  await expect(group.getByRole('radio', { name: 'Most worktrees' })).toBeChecked()
  await expectNames(page, ['sneat-co/sneat-go', 'sneat-dev/wb', 'acme/lone'])

  // The radio group is a keyboard control, and a header click is reflected in it.
  await group.getByRole('radio', { name: 'Most worktrees' }).focus()
  await page.keyboard.press('ArrowRight')
  await expect(page).toHaveURL(/sort=branches&dir=desc/)
  await page.getByRole('button', { name: 'Repository', exact: true }).click()
  await expect(page).toHaveURL(/sort=repository/)
  await expect(group.getByRole('radio', { checked: true })).toHaveCount(0)
  await page.goBack()
  await expect(group.getByRole('radio', { name: 'Most branches' })).toBeChecked()
  await expectClean()
})

// cockpit-views#ac:repository-actions-follow-configuration
test('the browse-code button needs a code browser, and the host button an address', async ({ page }) => {
  await stub(page)
  await page.goto('/cockpit/repositories')
  await expect(listRows(page)).toHaveCount(3)
  await expect(page.getByRole('link', { name: /Browse the code/ })).toHaveCount(0)
  const host = page.getByRole('link', { name: 'Open sneat-co/sneat-go on github.com' })
  await expect(host).toHaveAttribute('href', 'https://github.com/sneat-co/sneat-go')
  await expect(host).toHaveAttribute('rel', 'noopener noreferrer')
  await expect(host).toHaveAttribute('title', 'Open on github.com')
  await expect(listRows(page).filter({ hasText: 'acme/lone' }).locator('a.icon')).toHaveCount(0)
  await page.unrouteAll()
  await stub(page, { codeBrowser: 'https://code.example.test/' })
  await page.goto('/cockpit/repositories')
  await expect(page.getByRole('link', { name: 'Browse the code of sneat-dev/wb' })).toHaveAttribute('href', 'https://code.example.test/github.com/sneat-dev/wb')
})

test('a machine chip opens the panel at that machine\'s checkout', async ({ page }) => {
  await stub(page)
  const expectClean = await watch(page)
  await page.goto('/cockpit/repositories')
  await listRows(page).first().locator('a.machine').nth(2).click()
  await expect(page).toHaveURL(/sel=go-a#machine-mach-gamma$/)
  const section = page.getByRole('complementary').locator('app-repository-machine-section').nth(2)
  await expect(section.locator('details')).toHaveAttribute('open', '')
  await expect(section).toBeInViewport()
  await expectClean()
})

test('at 360 px the list does not scroll sideways, and the panel is a sheet that Esc closes', async ({ page }) => {
  await stub(page)
  const expectClean = await watch(page)
  await page.setViewportSize({ width: 360, height: 800 })
  await page.goto('/cockpit/repositories')
  await expect(listRows(page).first()).toBeVisible()
  const fits = () => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)
  expect(await fits()).toBe(true)
  await listRows(page).first().locator('[role=gridcell]').first().click()
  const sheet = page.getByRole('dialog')
  await expect(sheet).toBeVisible()
  expect(await fits()).toBe(true)
  await page.keyboard.press('Escape')
  await expect(sheet).toBeHidden()
  await page.goto('/cockpit/repositories/github.com/sneat-co/sneat-go')
  await expect(page.locator('app-repository-panel h2')).toBeVisible()
  expect(await fits()).toBe(true)
  await expectClean()
})
