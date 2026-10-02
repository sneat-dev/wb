import { expect, test, type Page } from '@playwright/test'
import { fleet, now, stub, watch } from './support'

// The five pages against a stubbed fleet document and session, in the built
// application served under the daemon's content security policy. Each test
// ends by checking that no policy violation and no console error occurred.

// The shared list (the Worktrees page) has no table: its rows are ARIA rows of a virtual grid.
const listRows = (page: Page) => page.locator('[role=row][data-index]')

test('Home, Repositories and the machine chip work, in the built application', async ({ page }) => {
  await stub(page)
  const expectClean = await watch(page)

  await page.goto('/cockpit/dashboard')
  // /dashboard is an alias for Home, which has no visible page heading of its own: its sections have theirs.
  await expect(page).toHaveURL(/\/cockpit\/$/)
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Home')
  await expect(page.getByRole('heading', { level: 2, name: 'Needs you' })).toBeVisible()
  await expect(page.getByRole('heading', { level: 2, name: 'In flight' })).toBeVisible()

  // Repositories is the shared list: one row per repository identity, each machine a chip with its age.
  await page.getByRole('navigation', { name: 'Pages' }).getByRole('link', { name: 'Repositories' }).click()
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Repositories')
  await expect(listRows(page)).toHaveCount(3)
  await expect(listRows(page).first()).toContainText('specscore/specscore-cli')
  await expect(listRows(page).filter({ hasText: 'acme/far' }).locator('a.machine')).toHaveText([/beta\s*1\d m/])
  await expect(listRows(page).filter({ hasText: 'acme/web' }).locator('a.machine')).toHaveText(['alpha'])
  await page.getByRole('button', { name: 'beta', exact: true }).click()
  await expect(page).toHaveURL(/[?&]machine=mach-beta/)
  await expect(listRows(page)).toHaveCount(1)
  await expect(listRows(page).first()).toContainText('acme/far')

  // Machines is the shared list too, and has its own journey (machines.e2e.ts).
  await expectClean()
})

test('a repository\'s worktree count opens exactly the worktrees it counted, in dark mode', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'dark' })
  await stub(page)
  const expectClean = await watch(page)
  await page.goto('/cockpit/repositories')

  const row = listRows(page).filter({ hasText: 'specscore-cli' })
  await row.getByRole('link', { name: '2', exact: true }).first().click()
  await expect(page).toHaveURL(/\/cockpit\/worktrees\?q=repo:%22specscore%2Fspecscore-cli%22$/)
  await expect(listRows(page)).toHaveCount(2)
  await expect(listRows(page).nth(0)).toContainText('add-search')
  await expect(listRows(page).nth(1)).toContainText('fix-index')

  // The dark theme: the page and the table are dark, not the light default.
  const luminance = (css: string) => {
    const [r, g, b] = css.match(/\d+(\.\d+)?/g)!.slice(0, 3).map(Number)
    return (0.2126 * r + 0.7152 * g + 0.0722 * b) / 255
  }
  expect(luminance(await page.evaluate(() => getComputedStyle(document.body).backgroundColor))).toBeLessThan(0.25)
  expect(luminance(await page.locator('.viewport').evaluate((list) => getComputedStyle(list).backgroundColor))).toBeLessThan(0.35)
  await expectClean()
})

test('each repository has icon buttons for the code browser, from the default base and from a configured one, and for its host', async ({ page }) => {
  for (const [base, expected] of [
    ['https://codegrapher.dev/', 'https://codegrapher.dev/github.com/specscore/specscore-cli'],
    ['https://code.example.test/', 'https://code.example.test/github.com/specscore/specscore-cli'],
  ]) {
    await stub(page, base)
    const expectClean = await watch(page)
    await page.goto('/cockpit/repositories')
    const row = listRows(page).filter({ hasText: 'specscore-cli' })
    const link = row.getByRole('link', { name: 'Browse the code of specscore/specscore-cli' })
    await expect(link).toHaveAttribute('href', expected)
    await expect(link).toHaveAttribute('target', '_blank')
    await expect(link).toHaveAttribute('rel', 'noopener noreferrer')
    await expect(link).toHaveAttribute('title', 'Browse the code')
    // There is no text "Code" link any more.
    await expect(page.getByRole('link', { name: 'Code', exact: true })).toHaveCount(0)
    // A repository whose origin names no forge host has no link.
    await expect(listRows(page).filter({ hasText: 'acme/far' }).locator('a.icon')).toHaveCount(0)
    await expectClean()
    await page.unrouteAll()
  }
})

// cockpit#ac:code-index-freshness-appears, the table half: three checkouts, one
// whose latest receipt is at HEAD, one three commits behind and one with none.
test('the Worktrees page shows fresh, stale with its count, and never', async ({ page }) => {
  const indexed = {
    ...fleet,
    worktrees: [
      { ...fleet.worktrees[0], id: 'wt-a', task: 'at-head', code_index: [{ indexer: 'codegrapher', state: 'fresh', receipt_at: now }] },
      { ...fleet.worktrees[0], id: 'wt-b', task: 'behind', code_index: [{ indexer: 'codegrapher', state: 'stale', behind: 3, receipt_at: now }] },
      { ...fleet.worktrees[0], id: 'wt-c', task: 'unindexed', code_index: [{ indexer: 'codegrapher', state: 'never' }] },
      { ...fleet.worktrees[2], id: 'wt-d', task: 'elsewhere' },
    ],
  }
  await stub(page)
  await page.route('**/api/v1/cockpit/fleet', (route) => route.fulfill({ json: indexed, headers: { ETag: '"indexed"', 'Cache-Control': 'no-cache' } }))
  const expectClean = await watch(page)
  await page.goto('/cockpit/worktrees')
  await expect(page.getByRole('columnheader', { name: 'Code index' })).toBeVisible()
  const indexCell = (task: string) => listRows(page).filter({ hasText: task }).locator('app-code-index-cell')
  await expect(indexCell('at-head')).toContainText('fresh')
  await expect(indexCell('behind')).toContainText('stale, 3 behind')
  await expect(indexCell('unindexed')).toContainText('never')
  // A checkout whose freshness is not known shows a dash, not a guess.
  await expect(indexCell('elsewhere')).toContainText('—')
  // The receipt's age is visible text, not only a tooltip.
  await expect(indexCell('at-head')).toContainText('just now')
  await expectClean()
})

test('the Repositories page shows the code-index freshness of each repository', async ({ page }) => {
  await stub(page)
  const expectClean = await watch(page)
  await page.goto('/cockpit/repositories')
  await expect(page.getByRole('columnheader', { name: 'Code index' })).toBeVisible()
  await expect(listRows(page).filter({ hasText: 'specscore-cli' }).locator('app-state-badge')).toContainText('stale')
  // A repository on another machine carries no freshness: a dash.
  await expect(listRows(page).filter({ hasText: 'acme/far' }).getByText('—', { exact: true })).not.toHaveCount(0)
  await expectClean()
})

test('no page scrolls sideways at 360 px', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 800 })
  await stub(page)
  const expectClean = await watch(page)
  for (const path of ['', 'repositories', 'worktrees', 'agents', 'machines']) {
    await page.goto(`/cockpit/${path}`)
    // Home has no table: its first section is its landmark.
    const first = path === '' ? page.getByRole('heading', { level: 2, name: 'Needs you' }) : listRows(page).first()
    await expect(first).toBeVisible()
    const widths = await page.evaluate(() => ({ page: document.documentElement.scrollWidth, window: window.innerWidth }))
    expect(widths.page).toBeLessThanOrEqual(widths.window)
  }
  await expectClean()
})

test('the fleet shows while the first scan is still running', async ({ page }) => {
  await page.route('**/api/v1/cockpit/fleet', (route) =>
    route.fulfill({ json: { ...fleet, warming_up: true, repositories_total: 40, repositories_scanned: 3, diagnostics: 2 } }),
  )
  await page.route('**/api/v1/cockpit/session', (route) => route.fulfill({ status: 401, json: { error: 'no' } }))
  await page.goto('/cockpit/repositories')
  await expect(page.getByTestId('freshness-chip').filter({ hasText: 'scanned 3 of 40' })).toBeVisible()
  await expect(page.getByText('2 pull requests could not be matched')).toBeVisible()
  await expect(listRows(page)).toHaveCount(3)
  await expect(page.getByRole('link', { name: 'Code' })).toHaveCount(0)
})
