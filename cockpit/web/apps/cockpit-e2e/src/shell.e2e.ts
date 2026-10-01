import { expect, test, type Response } from '@playwright/test'
import { fleet, stub, watch } from './support'
import { otherConsoleErrors, unexplainedViolations, type Violation } from './violations'

test('the built shell loads under /cockpit/ with no console errors and no CSP violations', async ({ page }) => {
  const consoleErrors: string[] = []
  page.on('console', (message) => {
    if (message.type() === 'error') consoleErrors.push(message.text())
  })
  page.on('pageerror', (error) => consoleErrors.push(`page error: ${error.message}`))
  // The shell reads the daemon's API; this static server has none, so stub it.
  await page.route('**/api/v1/cockpit/fleet', (route) => route.fulfill({ json: { schema_version: 2, warming_up: false, repositories_total: 0, repositories_scanned: 0, diagnostics: 0, machines: [], repositories: [], worktrees: [], pull_requests: [], agents: [] } }))
  await page.route('**/api/v1/cockpit/session', (route) => route.fulfill({ json: { principal: 'anonymous-local', capabilities: [], code_browser_url: 'https://codegrapher.dev/' } }))
  await page.addInitScript(() => {
    const store = window as unknown as { __violations: unknown[] }
    store.__violations = []
    document.addEventListener('securitypolicyviolation', (event) => {
      // The licence banner lives in a closed shadow root, so the event is
      // retargeted to its host, which is in the composed path.
      const inLicenseBanner = event
        .composedPath()
        .some((node) => (node as Element).id === 'p-license-host')
      store.__violations.push({
        directive: event.violatedDirective,
        blockedURI: event.blockedURI,
        inLicenseBanner,
      })
    })
  })

  const response = await page.goto('/cockpit/')
  const headers = await (response as Response).allHeaders()
  const policy = headers['content-security-policy']
  expect(policy).toContain("script-src 'self';")
  expect(policy).not.toContain('unsafe-')

  await expect(page.getByRole('link', { name: 'WB Cockpit' })).toBeVisible()
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Home')
  expect(await page.locator('style').count()).toBeGreaterThan(0)
  await page.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))))
  const violations = await page.evaluate(() => (window as unknown as { __violations: unknown[] }).__violations)
  expect(unexplainedViolations(violations as Violation[])).toEqual([])
  expect(otherConsoleErrors(consoleErrors)).toEqual([])
})

test('the shell follows the browser colour scheme', async ({ page }) => {
  await page.route('**/api/v1/cockpit/**', (route) => route.fulfill({ status: 401, json: { error: 'stubbed' } }))
  await page.emulateMedia({ colorScheme: 'dark' })
  await page.goto('/cockpit/')
  const dark = await page.evaluate(() => getComputedStyle(document.body).backgroundColor)
  await page.emulateMedia({ colorScheme: 'light' })
  const light = await page.evaluate(() => getComputedStyle(document.body).backgroundColor)
  expect(dark).not.toBe(light)
})

// The shell against a stubbed fleet: the top bar, the palette, the shortcuts and the sheet.
test('the top bar shows the tabs, the signals, the freshness and the session, and names the page', async ({ page }) => {
  await stub(page)
  const expectClean = await watch(page)
  await page.goto('/cockpit/')
  const tabs = page.getByRole('navigation', { name: 'Pages' })
  await expect(tabs.getByRole('link')).toHaveText([/^Home/, /^Tasks/, /^Repositories/, /^Worktrees/, /^Agents\s*1/, /^Machines/])
  await expect(tabs.getByRole('link', { name: /^Home/ })).toHaveAttribute('aria-current', 'page')
  await expect(page.getByRole('link', { name: 'New task' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Search' })).toBeVisible()
  await expect(page.getByTestId('freshness-chip').filter({ hasText: /updated \d+ s ago/ })).toBeVisible()
  await expect(page.getByText('anonymous', { exact: true })).toBeVisible()
  await expect(page).toHaveTitle('Home')
  // No heading on screen repeats the tab.
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Home')
  expect(await page.getByRole('heading', { level: 1 }).evaluate((h) => h.getBoundingClientRect().width)).toBeLessThanOrEqual(1)

  await tabs.getByRole('link', { name: /^Worktrees/ }).click()
  await expect(page).toHaveTitle('Worktrees')
  await expect(tabs.getByRole('link', { name: /^Worktrees/ })).toHaveAttribute('aria-current', 'page')
  await expect(tabs.getByRole('link', { name: /^Home/ })).not.toHaveAttribute('aria-current', 'page')
  await expectClean()
})

// Between a wide window and a phone the bar gives up room in a fixed order (search to its icon, then "New task" to its
// icon, ...), and the tabs never run under a control: no two of its parts overlap and the page does not scroll sideways.
test('the top bar fits between 1440 and 769 px wide: no part overlaps another and nothing scrolls sideways', async ({ page }) => {
  await stub(page)
  await page.goto('/cockpit/')
  await expect(page.getByTestId('freshness-chip').filter({ hasText: /updated \d+ s ago/ })).toBeVisible()
  // Left to right: the brand, the tab strip (its last tab is the one that was clipped), the search entry, "New task", the chip and the session.
  const parts = ['.brand', '.tabs', '.palette-trigger', '.new-task', 'app-freshness-chip .chip', '.session']
  const boxOf = async (selector: string) => (await page.locator(`app-top-bar ${selector}`).boundingBox()) as { x: number; width: number }
  for (const width of [1440, 1330, 1200, 1100, 1024, 900, 800, 769]) {
    await page.setViewportSize({ width, height: 800 })
    const boxes = await Promise.all(parts.map(boxOf))
    for (let index = 1; index < parts.length; index++) {
      expect(boxes[index].x, `${parts[index]} starts after ${parts[index - 1]} ends, at ${width}`).toBeGreaterThanOrEqual(boxes[index - 1].x + boxes[index - 1].width)
    }
    const lastTab = await boxOf('.tab >> nth=-1')
    expect(lastTab.x + lastTab.width, `the last tab is inside the strip, at ${width}`).toBeLessThanOrEqual(boxes[1].x + boxes[1].width + 0.5)
    expect(boxes[5].x + boxes[5].width, `the session chip is inside the window, at ${width}`).toBeLessThanOrEqual(width)
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth), `no sideways scroll at ${width}`).toBe(true)
  }
})

test('the palette opens with Control+K, groups what matches and opens the highlighted result with Enter', async ({ page }) => {
  await stub(page)
  const expectClean = await watch(page)
  await page.goto('/cockpit/')
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Home')
  // The overlays are fetched once the shell has rendered; wait for the entry to answer.
  await expect(page.locator('app-overlays')).toBeAttached()
  // Home's own lazy sections load right after the first page; their last heading says they are in.
  await expect(page.getByRole('heading', { level: 2, name: 'Cleanup' })).toBeVisible()
  const requestsBefore: string[] = []
  page.on('request', (request) => requestsBefore.push(request.url()))
  await page.keyboard.press('Control+k')
  const dialog = page.getByRole('dialog', { name: 'Search' })
  await expect(dialog).toBeVisible()
  await expect(dialog.getByRole('combobox')).toBeFocused()
  // Opening it needed no request (Home's machine strip polls the metrics every 10 seconds, whatever the palette does).
  expect(requestsBefore.filter((url) => !url.includes('/machine-metrics'))).toEqual([])

  await dialog.getByRole('combobox').fill('cli')
  await expect(dialog.getByRole('group').first()).toBeVisible()
  const options = dialog.getByRole('option')
  await expect(options.first()).toHaveAttribute('aria-selected', 'true')
  await page.keyboard.press('ArrowDown')
  await expect(options.nth(1)).toHaveAttribute('aria-selected', 'true')
  await page.keyboard.press('Escape')
  await expect(dialog).toBeHidden()

  await page.keyboard.press('/')
  await expect(dialog).toBeVisible()
  await dialog.getByRole('combobox').fill('far')
  await expect(options.first()).toContainText('far-task')
  await page.keyboard.press('Enter')
  await expect(dialog).toBeHidden()
  await expect(page).toHaveURL(/\/cockpit\/tasks\/detail\?task=far-task$/)

  await page.keyboard.press('Control+k')
  await dialog.getByRole('combobox').fill('acme/far')
  await dialog.getByRole('group', { name: 'Repositories' }).getByRole('option').first().click()
  await expect(page).toHaveURL(/\/cockpit\/repositories\/-\/acme\/far$/)
  await expectClean()
})

test('g then a letter switches tabs, keys typed into an input stay text, ? shows the sheet and Esc closes it', async ({ page }) => {
  await stub(page)
  const expectClean = await watch(page)
  await page.goto('/cockpit/')
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Home')
  await page.keyboard.press('g')
  await page.keyboard.press('w')
  await expect(page).toHaveURL(/\/cockpit\/worktrees$/)
  await page.keyboard.press('g')
  await page.keyboard.press('m')
  await expect(page).toHaveURL(/\/cockpit\/machines$/)
  await page.keyboard.press('g')
  await page.keyboard.press('h')
  await expect(page).toHaveURL(/\/cockpit\/$/)

  // In an input the keys are text. (The palette's input stands for any input here; the page filter box
  // and the side panel half of cockpit-views#ac:shortcuts-navigate-and-respect-typing is Task 12's.)
  await page.keyboard.press('/')
  const search = page.getByRole('dialog', { name: 'Search' }).getByRole('combobox')
  await search.pressSequentially('g w')
  await expect(search).toHaveValue('g w')
  await expect(page).toHaveURL(/\/cockpit\/$/)
  await page.keyboard.press('Escape')

  await page.keyboard.press('?')
  const sheet = page.getByRole('dialog', { name: 'Keyboard shortcuts' })
  await expect(sheet).toBeVisible()
  await expect(sheet.getByText('Machines')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(sheet).toBeHidden()
  await expectClean()
})

test('the schema-mismatch state replaces the data: update wb when the daemon is older, reload when the page is', async ({ page }) => {
  await stub(page)
  await page.route('**/api/v1/cockpit/fleet', (route) => route.fulfill({ json: { schema_version: 1, warming_up: false, repositories_total: 0, repositories_scanned: 0, diagnostics: 0, machines: [], repositories: [], worktrees: [], pull_requests: [], agents: [] } }))
  await page.goto('/cockpit/')
  const alert = page.getByRole('alert')
  await expect(alert).toContainText('update wb on this machine')
  await expect(page.locator('tbody tr')).toHaveCount(0)
  await page.unroute('**/api/v1/cockpit/fleet')
  await page.route('**/api/v1/cockpit/fleet', (route) => route.fulfill({ json: { schema_version: 3, warming_up: false, repositories_total: 0, repositories_scanned: 0, diagnostics: 0, machines: [], repositories: [], worktrees: [], pull_requests: [], agents: [] } }))
  await page.reload()
  await expect(page.getByRole('alert')).toContainText('reload')
  await expect(page.getByRole('button', { name: 'Reload the page' })).toBeVisible()
})

// cockpit-views#ac:warming-up-shows-progress: the chip, the skeleton rows, and no layout shift when the complete document arrives.
test('while the daemon warms up the chip counts the scan and skeleton rows wait, and the complete document shifts nothing', async ({ page }) => {
  await stub(page)
  let reads = 0
  await page.route('**/api/v1/cockpit/fleet', (route) => {
    reads++
    const warming = reads <= 2
    return route.fulfill({
      json: warming ? { ...fleet, warming_up: true, repositories_total: 438, repositories_scanned: 120, repositories: fleet.repositories.slice(0, 1), worktrees: fleet.worktrees.slice(0, 1) } : fleet,
      headers: { 'Cache-Control': 'no-cache' },
    })
  })
  await page.addInitScript(() => {
    const shifts = window as unknown as { __cls: number }
    shifts.__cls = 0
    new PerformanceObserver((list) => {
      for (const entry of list.getEntries() as unknown as { value: number; hadRecentInput: boolean }[]) if (!entry.hadRecentInput) shifts.__cls += entry.value
    }).observe({ type: 'layout-shift', buffered: true })
  })
  await page.goto('/cockpit/')
  const chip = page.getByTestId('freshness-chip')
  await expect(chip).toContainText('scanned 120 of 438')
  // Home has its own skeleton row for each of its first three sections that has nothing yet (here "Needs you" and "Ready to land": the fleet has an agent already); the shell's rows below the page are not shown on Home, since sections arriving above them would push them.
  await expect(page.locator('.home app-skeleton-rows .skeleton-row')).toHaveCount(2)
  expect(await page.locator('.home app-skeleton-rows .skeleton-row').first().evaluate((row) => row.getBoundingClientRect().height)).toBe(32)
  await expect(page.locator('app-skeleton-rows.warming')).toBeHidden()

  await expect(chip).toContainText(/updated \d+ s ago/, { timeout: 15_000 })
  await expect(page.locator('app-skeleton-rows')).toHaveCount(0)
  await expect(page.getByRole('heading', { level: 2, name: 'In flight' })).toBeVisible()
  expect(await page.evaluate(() => (window as unknown as { __cls: number }).__cls)).toBeLessThan(0.01)
})

// cockpit-views#ac:initial-script-fits-the-budget, the loading half: a page's code is a chunk of its own, fetched only when its route is opened.
test('a page loads its own script chunk only when it is opened', async ({ page }) => {
  await stub(page)
  const scripts = new Set<string>()
  page.on('request', (request) => {
    if (request.url().endsWith('.js')) scripts.add(request.url())
  })
  await page.goto('/cockpit/')
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Home')
  await expect(page.locator('app-overlays')).toBeAttached()
  const home = new Set(scripts)
  expect(home.size).toBeGreaterThan(3)

  await page.goto('/cockpit/worktrees/wt-1')
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Worktree')
  const detailOnly = [...scripts].filter((url) => !home.has(url))
  expect(detailOnly.length).toBeGreaterThan(0)
})

// cockpit-views#req:responsive-to-360: nothing makes a page scroll sideways at the narrowest width, overlays included.
test('no route scrolls sideways at 360 px, and the palette and the sheet fit', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 800 })
  await stub(page)
  for (const path of ['', 'tasks', 'tasks/new', 'tasks/detail?task=add-search', 'repositories/-/acme/far', 'agents/ag-1', 'machines/mach-alpha', 'worktrees/wt-1', 'repositories/repo-cli']) {
    await page.goto(`/cockpit/${path}`)
    await expect(page.getByRole('heading', { level: 1 })).not.toHaveText('')
    await page.locator('app-overlays').waitFor({ state: 'attached' })
    const widths = await page.evaluate(() => ({ page: document.documentElement.scrollWidth, window: window.innerWidth }))
    expect(widths.page, path).toBeLessThanOrEqual(widths.window)
  }
  await page.keyboard.press('Control+k')
  const box = await page.getByRole('dialog', { name: 'Search' }).boundingBox()
  expect(box!.x).toBeGreaterThanOrEqual(0)
  expect(box!.x + box!.width).toBeLessThanOrEqual(360)
  await page.keyboard.press('Escape')
  await page.keyboard.press('?')
  const sheet = await page.getByRole('dialog', { name: 'Keyboard shortcuts' }).boundingBox()
  expect(sheet!.x + sheet!.width).toBeLessThanOrEqual(360)
})
