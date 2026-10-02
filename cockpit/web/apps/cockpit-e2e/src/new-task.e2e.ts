import { expect, test, type Page } from '@playwright/test'
import { alpha, beta, checkedAt, fleet, now, watch } from './support'

// The New task form against a stubbed fleet (REQ:new-task-form, REQ:copy-the-command), in the built application under
// the daemon's content security policy: the repository picker, the commands it produces, the address that holds the
// form, and the load verdict beside the machine. Nothing the form does sends anything to the daemon.

const newTaskFleet = {
  ...fleet,
  repositories: [
    { id: 'repo-go', ...alpha, host: 'github.com', name: 'sneat-co/sneat-go', default_branch: 'main', worktree_count: 0 },
    { id: 'repo-bots', ...alpha, host: 'github.com', name: 'sneat-co/bots-go', default_branch: 'main', worktree_count: 0 },
    { id: 'repo-web', ...alpha, host: 'github.com', name: 'acme/web', default_branch: 'trunk', worktree_count: 0 },
    { id: 'repo-odd', ...beta, name: 'acme/odd name', worktree_count: 0 },
  ],
  worktrees: [{ id: 'wt-1', ...alpha, repository: 'repo-go', task: 'add-search', branch: 'task/add-search', owner_state: 'active', last_activity_at: now }],
  agents: [{ id: 'run-7', ...alpha, kind: 'run', run_id: 'run-7', runtime: 'claude', model: 'sonnet-5-5', state: 'running', repository: 'repo-go' }],
}

const sample = (cpu: number) => ({ cpu_percent: cpu, load1: 1, memory_used_bytes: 4 * 2 ** 30, memory_total_bytes: 16 * 2 ** 30, disk_free_bytes: 2 ** 40, disk_total_bytes: 2 ** 41, sampled_at: new Date().toISOString() })

async function stubForm(page: Page, cpu = 20) {
  const requests: string[] = []
  page.on('request', (request) => requests.push(`${request.method()} ${new URL(request.url()).pathname}`))
  await page.route('**/api/v1/cockpit/fleet', (route) => route.fulfill({ json: { ...newTaskFleet, snapshot_at: now }, headers: { ETag: '"new-task"', 'Cache-Control': 'no-cache', ...checkedAt() } }))
  await page.route('**/api/v1/cockpit/session', (route) => route.fulfill({ json: { principal: 'anonymous-local', capabilities: ['fleet.read'], code_browser_url: 'https://codegrapher.dev/' } }))
  await page.route('**/api/v1/cockpit/machine-metrics?**', (route) => route.fulfill({ json: { machine: 'mach-alpha', route: 'local', samples: [sample(cpu)] } }))
  return requests
}

const commandRows = (page: Page) => page.locator('app-copy-command-list li')

// cockpit-views#ac:new-task-form-produces-commands
test('typing a pattern, choosing both repositories and filling the form gives the commands to copy, and sends nothing', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  const requests = await stubForm(page)
  const expectClean = await watch(page)
  await page.goto('/cockpit/')
  // The top bar's button opens the form.
  await page.getByRole('link', { name: 'New task' }).click()
  await expect(page).toHaveURL(/\/cockpit\/tasks\/new$/)
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('New task')
  await expect(commandRows(page).first()).toContainText('name the task')

  const picker = page.getByRole('combobox', { name: 'Repository' })
  await picker.fill('sneat-*/*-go')
  // Only names that can be picked are offered: the one with a space in it is not.
  await expect(page.getByRole('option')).toHaveText(['sneat-co/sneat-go', 'sneat-co/bots-go'])
  await page.getByRole('button', { name: 'Add all 2 matches' }).click()
  await expect(page.locator('.chip-name')).toHaveText(['sneat-co/sneat-go', 'sneat-co/bots-go'])

  await page.getByLabel('Task name').fill('fix-ci')
  await page.getByLabel('Brief', { exact: true }).fill('Fix the flaky CI.')
  await page.getByLabel(/Base branch/).fill('main')
  // With a brief the commands are the dispatches alone (dispatch creates the worktree itself): no creation before them, and no model asked for.
  await expect(commandRows(page)).toHaveCount(2)
  await expect(commandRows(page).nth(0).locator('code')).toHaveText("wb agent dispatch --repo='sneat-co/sneat-go' --task='Fix the flaky CI.' --profile=<<<edit:profile>>> --new-worktree='fix-ci' --base='main'")
  await expect(commandRows(page).nth(1).locator('code')).toHaveText("wb agent dispatch --repo='sneat-co/bots-go' --task='Fix the flaky CI.' --profile=<<<edit:profile>>> --new-worktree='fix-ci' --base='main'")
  for (const row of await commandRows(page).all()) {
    await expect(row).toContainText('edit before running')
    await expect(row).toContainText('run here')
  }
  await commandRows(page).nth(0).getByRole('button', { name: /^Copy template/ }).click()
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe("wb agent dispatch --repo='sneat-co/sneat-go' --task='Fix the flaky CI.' --profile=<<<edit:profile>>> --new-worktree='fix-ci' --base='main'")
  // The profile fills the one placeholder; the command then has nothing left to edit.
  await page.getByLabel(/Agent profile/).fill('cheap-coder')
  await expect(commandRows(page).nth(0).locator('code')).toHaveText("wb agent dispatch --repo='sneat-co/sneat-go' --task='Fix the flaky CI.' --profile='cheap-coder' --new-worktree='fix-ci' --base='main'")
  await commandRows(page).nth(0).getByRole('button', { name: /^Copy wb agent dispatch/ }).click()
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe("wb agent dispatch --repo='sneat-co/sneat-go' --task='Fix the flaky CI.' --profile='cheap-coder' --new-worktree='fix-ci' --base='main'")
  // Without a brief there is nothing to dispatch: the creation command alone, and now the model is required.
  await page.getByLabel('Brief', { exact: true }).fill('')
  await expect(commandRows(page)).toHaveCount(1)
  await expect(commandRows(page).first()).toContainText('a model is required')
  await page.getByLabel('Model').fill('opus')
  await expect(commandRows(page)).toHaveCount(1)
  await expect(commandRows(page).first().locator('code')).toHaveText("wb worktree create 'fix-ci' 'sneat-co/sneat-go' 'sneat-co/bots-go' --model='opus' --original-prompt-file=<<<edit:file>>> --base='main'")

  // Nothing was run or sent: the form made no request but the reads of the page itself.
  expect(requests.filter((request) => !request.startsWith('GET ')).length).toBe(0)
  expect(requests.filter((request) => request.includes('/api/v1/cockpit/') && !/fleet|session|machine-metrics/.test(request))).toEqual([])
  await expectClean()
})

test('the address holds the form but not the brief, a reload restores it, and back undoes a step', async ({ page }) => {
  await stubForm(page)
  await page.goto('/cockpit/tasks/new')
  const picker = page.getByRole('combobox', { name: 'Repository' })
  await picker.fill('acme')
  await page.getByRole('option', { name: 'acme/web' }).click()
  await page.getByLabel('Task name').fill('try-it')
  await page.getByLabel('Model').fill('sonnet')
  await page.getByLabel('Brief', { exact: true }).fill('A private note that stays here.')
  await expect(page).toHaveURL(/repo=acme%2Fweb/)
  await expect(page).toHaveURL(/task=try-it/)
  await expect(page).toHaveURL(/model=sonnet/)
  expect(page.url()).not.toContain('private')
  // The default branch of what is chosen is the base's placeholder.
  await expect(page.getByLabel(/Base branch/)).toHaveAttribute('placeholder', 'trunk')

  await page.reload()
  await expect(page.locator('.chip-name')).toHaveText(['acme/web'])
  await expect(page.getByLabel('Task name')).toHaveValue('try-it')
  await expect(page.getByLabel('Brief', { exact: true })).toHaveValue('')
  // The brief is not in the address, so after a reload there is none and the form gives the creation command.
  await expect(commandRows(page).first().locator('code')).toContainText("wb worktree create 'try-it' 'acme/web' --model='sonnet'")

  // Back: choosing the repository was a step, so back leaves it chosen no more.
  await page.getByRole('button', { name: 'Remove acme/web' }).click()
  await expect(page.locator('.chosen .chip')).toHaveCount(0)
  await page.goBack()
  await expect(page.locator('.chip-name')).toHaveText(['acme/web'])
})

test('the picker works from the keyboard, and a task that exists is said so while typing', async ({ page }) => {
  await stubForm(page)
  await page.goto('/cockpit/tasks/new')
  const picker = page.getByRole('combobox', { name: 'Repository' })
  await picker.focus()
  await expect(picker).toHaveAttribute('aria-expanded', 'true')
  await page.keyboard.press('ArrowDown')
  await page.keyboard.press('Enter')
  await expect(page.locator('.chip-name')).toHaveCount(1)
  await page.keyboard.press('Escape')
  await page.getByLabel('Task name').fill('add-search')
  await expect(page.locator('#new-task-name-hint')).toContainText('A task of this name exists, with 1 worktree')
  await page.getByLabel('Task name').fill('bad name')
  await expect(page.getByRole('alert')).toContainText('letters, digits, dots, underscores and hyphens')
})

test('the load verdict sits beside the machine: busy for a loaded one, free for an idle one', async ({ page }) => {
  await stubForm(page, 95)
  await page.goto('/cockpit/tasks/new')
  await expect(page.locator('.machine')).toContainText('This machine (alpha)')
  await expect(page.locator('.machine app-state-badge')).toContainText('busy')
  const free = await page.context().newPage()
  await stubForm(free, 10)
  await free.goto('/cockpit/tasks/new')
  await expect(free.locator('.machine app-state-badge')).toContainText('free')
})

test('the form fits 360 px without sideways scroll, with the commands filled in', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 800 })
  await stubForm(page)
  await page.goto('/cockpit/tasks/new?repo=sneat-co%2Fsneat-go&repo=sneat-co%2Fbots-go&task=fix-ci&base=main&model=opus')
  await expect(commandRows(page)).toHaveCount(1)
  await page.getByLabel('Brief', { exact: true }).fill('Fix the flaky CI.')
  await expect(commandRows(page)).toHaveCount(2)
  const widths = await page.evaluate(() => ({ page: document.documentElement.scrollWidth, window: window.innerWidth }))
  expect(widths.page).toBeLessThanOrEqual(widths.window)
})
