import { expect, test, type Page } from '@playwright/test'
import { fleet, now, stub, watch } from './support'

// The five pages against a stubbed fleet document and session, in the built
// application served under the daemon's content security policy. Each test
// ends by checking that no policy violation and no console error occurred,
// beyond the PrimeUI licence banner this build is known to show.

const rows = (page: Page) => page.locator('tbody tr')
// The shared list (the Worktrees page) has no table: its rows are ARIA rows of a virtual grid.
const listRows = (page: Page) => page.locator('[role=row][aria-rowindex]')

test('every page lists its collection, cached rows show route and age, and the machine filter works', async ({ page }) => {
  await stub(page)
  const expectClean = await watch(page)

  await page.goto('/cockpit/dashboard')
  // /dashboard is an alias for Home, which has no visible heading of its own.
  await expect(page).toHaveURL(/\/cockpit\/$/)
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Home')
  await expect(page.locator('.tile')).toHaveCount(5)
  await expect(rows(page).filter({ hasText: 'beta' }).first()).toContainText(/cached, 1\d min ago/)
  await expect(page.locator('tbody').nth(1).locator('tr')).toHaveCount(3)

  const lists = [
    { link: 'Repositories', rows: 3, first: 'github.com/specscore/specscore-cli' },
    { link: 'Agents', rows: 2, first: 'claude session sess-1' },
    { link: 'Machines', rows: 2, first: 'alpha' },
  ]
  for (const list of lists) {
    await page.getByRole('navigation', { name: 'Pages' }).getByRole('link', { name: list.link }).click()
    await expect(page.getByRole('heading', { level: 1 })).toHaveText(list.link)
    await expect(rows(page)).toHaveCount(list.rows)
    await expect(rows(page).first()).toContainText(list.first)
    // Every cached row shows its route and age; a local row says local.
    const cached = rows(page).filter({ hasText: 'beta' })
    await expect(cached).not.toHaveCount(0)
    for (const row of await cached.all()) await expect(row).toContainText(/cached, 1\d min ago/)
    for (const row of await rows(page).filter({ hasText: 'alpha' }).all()) await expect(row).toContainText('local')

    // The machine filter leaves only that machine's rows.
    await page.locator('#filter-machine').click()
    await page.getByRole('option', { name: 'beta' }).click()
    await expect(page).toHaveURL(/[?&]machine=mach-beta/)
    await expect(rows(page)).toHaveCount(1)
    await expect(rows(page).first()).toContainText('beta')
    await expect(rows(page).first()).toContainText('cached')
  }
  await expectClean()
})

test('a count shows its entities on hover or focus and opens exactly them, in dark mode', async ({ page }) => {
  await page.emulateMedia({ colorScheme: 'dark' })
  await stub(page)
  const expectClean = await watch(page)
  await page.goto('/cockpit/repositories')

  const row = rows(page).filter({ hasText: 'specscore-cli' })
  const count = row.locator('app-count').first()
  await count.locator('a').hover()
  const card = count.locator('.count-card')
  await expect(card).toBeVisible()
  await expect(card.locator('li')).toHaveText(['add-search (task/add-search)', 'fix-index (task/fix-index)'])
  await expect(card.locator('button, a, input, select')).toHaveCount(0)

  // The keyboard reaches the same card, and Escape dismisses it.
  await page.mouse.move(0, 0)
  await expect(card).toBeHidden()
  await count.locator('a').focus()
  await expect(card).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(card).toBeHidden()

  await count.locator('a').click()
  await expect(page).toHaveURL(/\/cockpit\/worktrees\?repository=repo-cli$/)
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

test('each repository links to the code browser, from the default base and from a configured one', async ({ page }) => {
  for (const [base, expected] of [
    ['https://codegrapher.dev/', 'https://codegrapher.dev/github.com/specscore/specscore-cli'],
    ['https://code.example.test/', 'https://code.example.test/github.com/specscore/specscore-cli'],
  ]) {
    await stub(page, base)
    const expectClean = await watch(page)
    await page.goto('/cockpit/repositories')
    const link = rows(page).filter({ hasText: 'specscore-cli' }).getByRole('link', { name: 'Code' })
    await expect(link).toHaveAttribute('href', expected)
    await expect(link).toHaveAttribute('target', '_blank')
    // A repository whose origin names no forge host has no link.
    await expect(rows(page).filter({ hasText: 'acme/far' }).getByRole('link', { name: 'Code' })).toHaveCount(0)
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
  const indexCell = (task: string) => listRows(page).filter({ hasText: task }).locator('app-code-index-label')
  await expect(indexCell('at-head')).toContainText('fresh')
  await expect(indexCell('behind')).toContainText('stale, 3 behind')
  await expect(indexCell('unindexed')).toHaveText('never')
  // A checkout whose freshness is not known shows a dash, not a guess.
  await expect(indexCell('elsewhere')).toHaveText('—')
  // The receipt's age is visible text, not only a tooltip.
  await expect(indexCell('at-head')).toContainText('just now')
  await expectClean()
})

test('the Repositories page shows the code-index freshness of each repository', async ({ page }) => {
  await stub(page)
  const expectClean = await watch(page)
  await page.goto('/cockpit/repositories')
  await expect(page.getByRole('columnheader', { name: 'Code index' })).toBeVisible()
  await expect(rows(page).filter({ hasText: 'specscore-cli' }).locator('app-code-index-label')).toHaveText('stale, 3 behind')
  // A repository on another machine carries no freshness: a dash.
  await expect(rows(page).filter({ hasText: 'acme/far' }).locator('app-code-index-label')).toHaveText('—')
  await expectClean()
})

test('no page scrolls sideways at 360 px, and the hover card stays on screen', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 800 })
  await stub(page)
  const expectClean = await watch(page)
  for (const path of ['', 'repositories', 'worktrees', 'agents', 'machines']) {
    await page.goto(`/cockpit/${path}`)
    await expect((path === 'worktrees' ? listRows(page) : rows(page)).first()).toBeVisible()
    const widths = await page.evaluate(() => ({ page: document.documentElement.scrollWidth, window: window.innerWidth }))
    expect(widths.page).toBeLessThanOrEqual(widths.window)
  }
  await page.goto('/cockpit/machines')
  await page.locator('app-count a').last().hover()
  const box = await page.locator('.count-card.open').boundingBox()
  expect(box!.x).toBeGreaterThanOrEqual(0)
  expect(box!.x + box!.width).toBeLessThanOrEqual(360)
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
  await expect(rows(page)).toHaveCount(3)
  await expect(page.getByRole('link', { name: 'Code' })).toHaveCount(0)
})
