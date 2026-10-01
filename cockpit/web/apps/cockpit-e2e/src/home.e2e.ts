import { expect, test, type Page } from '@playwright/test'
import { stub, watch } from './support'

// Home against a stubbed fleet and session, in the built application under the daemon's policy: its sections
// from the model, the actions of its rows, the lazy chunk of the rest and the charts that load only on scroll,
// the phone layout and what it costs the layout. Each test ends by checking that no policy violation and no
// console error occurred.

const now = Date.now()
const ago = (minutes: number) => new Date(now - minutes * 60_000).toISOString()
const today = (offset: number) => new Date(now - offset * 86_400_000).toISOString().slice(0, 10)

const local = { machine: 'alpha', machine_id: 'mach-alpha', route: 'local', observed_at: ago(0) }
const homeFleet = {
  schema_version: 2,
  snapshot_at: ago(0),
  warming_up: false,
  repositories_total: 2,
  repositories_scanned: 2,
  diagnostics: 0,
  machines: [{ id: 'mach-alpha', ...local, wb_version: '1.0.0', repository_count: 2, worktree_count: 3 }],
  repositories: [
    { id: 'repo-web', ...local, host: 'github.com', name: 'acme/web', default_branch: 'main', worktree_count: 2 },
    { id: 'repo-cli', ...local, host: 'github.com', name: 'acme/cli', default_branch: 'main', worktree_count: 1 },
  ],
  worktrees: [
    { id: 'wt-risk', ...local, repository: 'repo-web', task: 'refactor-cache', branch: 'task/refactor-cache', owner_state: 'orphaned', last_activity_at: ago(30), ahead: 2, has_upstream: true },
    { id: 'wt-fail', ...local, repository: 'repo-web', task: 'fix-ci-race', branch: 'task/fix-ci-race', owner_state: 'idle', last_activity_at: ago(40), ahead: 0, has_upstream: true },
    { id: 'wt-ready', ...local, repository: 'repo-cli', task: 'improve-docs', branch: 'task/improve-docs', owner_state: 'idle', last_activity_at: ago(20), ahead: 0, has_upstream: true },
  ],
  pull_requests: [
    { id: 'pr-fail', ...local, repository: 'repo-web', worktree: 'wt-fail', number: 131, url: 'https://github.com/acme/web/pull/131', state: 'open', mergeable: 'clean', checks_total: 5, checks_passed: 3, checks_failed: 1, checks_pending: 0, checks_green: false, failed_check: 'build-linux', checked_at: ago(3) },
    { id: 'pr-ready', ...local, repository: 'repo-cli', worktree: 'wt-ready', number: 7, url: 'https://github.com/acme/cli/pull/7', state: 'open', mergeable: 'clean', checks_total: 5, checks_passed: 5, checks_failed: 0, checks_pending: 0, checks_green: true, checked_at: ago(4) },
  ],
  agents: [{ id: 'run-1', ...local, kind: 'run', run_id: 'run-1', runtime: 'claude', model: 'opus', state: 'running', activity: 'working', started_at: ago(35), task: 'improve-docs', worktrees: ['wt-ready'] }],
  throughput: {
    window_days: 30,
    per_day: [0, 1, 2, 3, 4].map((offset) => ({ date: today(offset), finished: offset, dropped: 1 })),
    slowest: [{ task: 'migrate-auth', duration_seconds: 15 * 3600, landed_at: ago(2000) }],
    median_seconds: 900,
    p90_seconds: 54_000,
  },
}

const ANONYMOUS = { principal: 'anonymous-local', capabilities: ['fleet.read'], code_browser_url: 'https://codegrapher.dev/' }
const OWNER = { principal: 'owner', capabilities: ['fleet.read', 'repo.content.read', 'branch.push', 'pr.land'], code_browser_url: 'https://codegrapher.dev/' }

async function serve(page: Page, session = ANONYMOUS) {
  await stub(page)
  await page.route('**/api/v1/cockpit/fleet', (route) => route.fulfill({ json: homeFleet, headers: { ETag: '"home"', 'Cache-Control': 'no-cache' } }))
  await page.route('**/api/v1/cockpit/session', (route) => route.fulfill({ json: session }))
}

async function measureLayoutShift(page: Page) {
  await page.addInitScript(() => {
    const shifts = window as unknown as { __cls: number }
    shifts.__cls = 0
    new PerformanceObserver((list) => {
      for (const entry of list.getEntries() as unknown as { value: number; hadRecentInput: boolean }[]) if (!entry.hadRecentInput) shifts.__cls += entry.value
    }).observe({ type: 'layout-shift', buffered: true })
  })
}

// cockpit-views#ac:needs-you-pr-checks-failed, #ac:needs-you-work-at-risk, #ac:ready-to-land-groups-by-task
test('Home lists what needs the operator with one action each, and what is ready to land with a command to copy', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await serve(page)
  const expectClean = await watch(page)
  await measureLayoutShift(page)
  await page.goto('/cockpit/')

  const needs = page.getByRole('region', { name: /^Needs you/ })
  await expect(needs.locator('.home-row')).toHaveCount(2)
  await expect(needs.locator('.home-row').nth(0)).toContainText('refactor-cache')
  await expect(needs.locator('.home-row').nth(0)).toContainText('2 unpushed commits')
  const failure = needs.getByRole('link', { name: /^Open failure/ })
  await expect(failure).toHaveAttribute('href', 'https://github.com/acme/web/pull/131')
  await expect(failure).toHaveAttribute('rel', 'noopener noreferrer')
  await expect(failure).toHaveAttribute('target', '_blank')
  await expect(needs.locator('.home-row').nth(1)).toContainText('build-linux failed on acme/web#131')

  // Work at risk opens its task first; with no registry its secondary action, a lazy chunk, is a quiet "Copy template" icon button.
  await expect(needs.locator('.home-row').nth(0).getByRole('link', { name: 'Open task refactor-cache' })).toHaveAttribute('href', /\/tasks\?sel=refactor-cache$/)
  const template = needs.locator('.home-row').nth(0).getByRole('button', { name: /^Copy command template/ })
  await expect(template).toHaveText('Copy template')
  await expect(template.locator('.visually-hidden')).toHaveText('Copy template')
  await template.click()
  await expect(template).toHaveText('Copied')
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe("wb pr create 'refactor-cache' --commit-all --message=<<<edit:message>>>")

  const ready = page.getByRole('region', { name: /^Ready to land/ })
  await expect(ready.locator('.home-row')).toHaveCount(1)
  await expect(ready.locator('.home-row')).toContainText('improve-docs')
  await expect(ready.locator('.home-row')).toContainText('5/5 checks')
  const land = ready.getByRole('button', { name: /^Copy command: wb pr land/ })
  await land.click()
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe("wb pr land 'acme/cli#7'")

  const flight = page.getByRole('region', { name: /^In flight/ })
  await expect(flight).toContainText('claude opus')
  await expect(flight.getByRole('button', { name: 'Copy command: wb agent stop' })).toBeVisible()
  await expect(flight.getByLabel('Machines').locator('.home-tile')).toHaveCount(1)
  expect(await page.evaluate(() => (window as unknown as { __cls: number }).__cls)).toBeLessThan(0.01)
  await expectClean()
})

// cockpit-views#ac:home-charts-from-throughput, #ac:csp-and-canvas-only: Chart.js is fetched only when its section nears the viewport.
test('the charts are canvases that are drawn, and fetched, only when they scroll near the viewport', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 300 })
  await serve(page)
  const expectClean = await watch(page)
  const scripts = new Set<string>()
  page.on('request', (request) => {
    if (request.url().endsWith('.js')) scripts.add(request.url())
  })
  await page.goto('/cockpit/')
  await expect(page.getByRole('region', { name: /^Fleet health|^Cleanup/ }).first()).toBeAttached()
  await expect(page.getByRole('heading', { level: 2, name: 'Throughput' })).toBeAttached()
  // The charts' own slot stands where they will be, with nothing fetched for them yet.
  await expect(page.locator('.home-lazy-slot')).toBeAttached()
  await expect(page.locator('app-home-charts')).toHaveCount(0)
  expect(await page.locator('canvas').count()).toBe(0)
  const before = scripts.size

  await page.getByRole('heading', { level: 2, name: 'Throughput' }).scrollIntoViewIfNeeded()
  await expect(page.locator('app-home-charts canvas')).toHaveCount(2)
  expect(scripts.size).toBeGreaterThan(before)
  await expect(page.locator('app-home-charts .data table')).toHaveCount(2)
  await expect(page.locator('app-home-charts')).toContainText('median 15 min · p90 15 h')
  // A chart does not link: pressing on its canvas goes nowhere.
  const url = page.url()
  await page.locator('app-home-charts canvas').first().click({ position: { x: 60, y: 60 } })
  expect(page.url()).toBe(url)
  await expectClean()
})

test('an owner whose daemon has an action registry gets the registry\'s actions in slots; an anonymous reader is never asked', async ({ page }) => {
  const asked: string[] = []
  await serve(page, OWNER)
  await page.route('**/api/v1/cockpit/actions?**', (route) => {
    const target = new URL(route.request().url()).searchParams.get('target') ?? ''
    asked.push(target)
    const id = target.startsWith('pull_request:') ? 'pr.land' : 'branch.push'
    return route.fulfill({
      json: { actions: [{ id, title: id === 'pr.land' ? 'Land' : 'Push', target_types: [target.split(':')[0]], applicable: true, parameters: [], capability: id, safety: 'guarded', permitted: true }] },
    })
  })
  const expectClean = await watch(page)
  await page.goto('/cockpit/')
  await expect(page.getByRole('region', { name: /^Needs you/ }).getByRole('button', { name: 'Push' })).toBeVisible()
  await expect(page.getByRole('region', { name: /^Ready to land/ }).getByRole('button', { name: 'Land' })).toBeVisible()
  await expect(page.getByRole('region', { name: /^Needs you/ }).getByRole('button', { name: /^Copy command template/ })).toHaveCount(0)
  expect(asked.sort()).toEqual(['pull_request:pr-ready', 'worktree:wt-risk'])
  await expectClean()

  const anonymous = await page.context().newPage()
  const requests: string[] = []
  anonymous.on('request', (request) => requests.push(request.url()))
  await serve(anonymous)
  await anonymous.goto('/cockpit/')
  await expect(anonymous.getByRole('region', { name: /^Ready to land/ })).toContainText('improve-docs')
  expect(requests.filter((url) => url.includes('/api/v1/cockpit/actions'))).toEqual([])
})

// cockpit-views#ac:home-phone-layout
test('at 360 px Home shows its first three sections as cards and the rest behind "More", and never scrolls sideways', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 800 })
  await serve(page)
  const expectClean = await watch(page)
  await page.goto('/cockpit/')
  await expect(page.getByRole('region', { name: /^Ready to land/ })).toBeVisible()
  await expect(page.getByRole('region', { name: /^In flight/ })).toBeVisible()
  const more = page.getByRole('button', { name: 'More' })
  await expect(more).toHaveAttribute('aria-expanded', 'false')
  await expect(page.getByRole('heading', { level: 2, name: 'Resume' })).toHaveCount(0)
  await expect(page.getByRole('heading', { level: 2, name: 'Throughput' })).toHaveCount(0)
  const sideways = () => page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
  expect(await sideways()).toBeLessThanOrEqual(0)
  // A row is its own card: bordered, rounded and apart from the next.
  const row = page.getByRole('region', { name: /^Needs you/ }).locator('.home-row').first()
  expect(await row.evaluate((element) => getComputedStyle(element).borderTopLeftRadius)).not.toBe('0px')

  await more.click()
  await expect(more).toHaveAttribute('aria-expanded', 'true')
  await expect(page.getByRole('heading', { level: 2, name: 'Resume' })).toBeVisible()
  await expect(page.getByRole('heading', { level: 2, name: 'Cleanup' })).toBeVisible()
  expect(await sideways()).toBeLessThanOrEqual(0)
  await expectClean()
})

test('a healthy fleet shows a short, reassuring page: one calm line for each empty section and no Fleet health', async ({ page }) => {
  await stub(page)
  await page.route('**/api/v1/cockpit/fleet', (route) =>
    route.fulfill({ json: { ...homeFleet, worktrees: [], pull_requests: [], agents: [], throughput: undefined }, headers: { ETag: '"calm"', 'Cache-Control': 'no-cache' } }),
  )
  const expectClean = await watch(page)
  await page.goto('/cockpit/')
  await expect(page.getByText('Nothing needs you.')).toBeVisible()
  await expect(page.getByText('Nothing is ready to land.')).toBeVisible()
  await expect(page.getByText('No agents running.')).toBeVisible()
  await expect(page.getByRole('heading', { level: 2, name: /^Fleet health/ })).toHaveCount(0)
  await expect(page.getByText(/^No charts: the daemon reports no throughput/)).toBeVisible()
  await expectClean()
})
