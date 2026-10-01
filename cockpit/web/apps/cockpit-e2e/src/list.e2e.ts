import { performanceFixture } from '@cockpit/fleet-data/testing'
import { expect, test, type Page } from '@playwright/test'
import { watch } from './support'

// The shared list and its side panel, on the Worktrees page, against the 600-worktree fixture
// of the performance budgets (REQ:bounded-row-elements, REQ:fast-filtering, REQ:look-layout).

const { document: fleet } = performanceFixture()
const first = fleet.worktrees[0]

const listRows = (page: Page) => page.locator('[role=row][data-index]')
const grid = (page: Page) => page.getByRole('grid')

async function stubFixture(page: Page, delayMs = 0) {
  await page.route('**/api/v1/cockpit/fleet', async (route) => {
    if (delayMs > 0) await new Promise((done) => setTimeout(done, delayMs))
    await route.fulfill({ json: fleet, headers: { ETag: '"perf"', 'Cache-Control': 'no-cache' } })
  })
  await page.route('**/api/v1/cockpit/session', (route) => route.fulfill({ json: { principal: 'anonymous-local', capabilities: ['fleet.read'], code_browser_url: 'https://codegrapher.dev/' } }))
  await page.route('**/api/v1/cockpit/machine-metrics?**', (route) => route.fulfill({ json: { machine: 'x', route: 'local', samples: [] } }))
}

test('at most 60 rows are in the DOM at 1080 px with 600 worktrees, however far the list is scrolled, under a header that stays', async ({ page }) => {
  await page.setViewportSize({ width: 1920, height: 1080 })
  await stubFixture(page)
  const expectClean = await watch(page)
  await page.goto('/cockpit/worktrees')
  await expect(listRows(page).first()).toBeVisible()
  expect(fleet.worktrees).toHaveLength(600)
  const count = await listRows(page).count()
  expect(count).toBeGreaterThan(20)
  expect(count).toBeLessThanOrEqual(60)
  await expect(grid(page)).toHaveAttribute('aria-rowcount', '601')
  await expect(page.locator('.count')).toHaveText('600 of 600')

  const before = await listRows(page).first().getAttribute('aria-rowindex')
  for (const top of [9000, 4000, 'end'] as const) {
    await page.locator('.viewport').evaluate((list, to) => (list.scrollTop = to === 'end' ? list.scrollHeight : to), top)
    await expect(listRows(page).first()).not.toHaveAttribute('aria-rowindex', before as string)
    expect(await listRows(page).count()).toBeLessThanOrEqual(60)
  }
  // At the very end the last row is there and fills the viewport to its bottom.
  await expect(listRows(page).last()).toHaveAttribute('aria-rowindex', '601')
  const head = (await page.locator('.head').boundingBox())!
  const list = (await page.locator('.viewport').boundingBox())!
  expect(Math.abs(head.y - list.y)).toBeLessThan(4)
  await expectClean()
})

test('on a cold load the rows fill the viewport as soon as the data arrives, with no scroll', async ({ page }) => {
  await page.setViewportSize({ width: 1920, height: 1080 })
  await stubFixture(page, 600)
  await page.goto('/cockpit/worktrees')
  await expect(listRows(page).first()).toBeVisible()
  const viewport = (await page.locator('.viewport').boundingBox())!
  const last = (await listRows(page).last().boundingBox())!
  expect(last.y + last.height).toBeGreaterThanOrEqual(viewport.y + viewport.height - 2)
})

test('filtering shows its result within one animation frame, and the address follows', async ({ page }) => {
  await stubFixture(page)
  await page.goto('/cockpit/worktrees')
  await expect(listRows(page).first()).toBeVisible()
  const shown = await page.evaluate(async () => {
    const input = document.querySelector('input') as HTMLInputElement
    input.value = 'zzzqq'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await new Promise((done) => requestAnimationFrame(() => done(null)))
    return (document.querySelector('.count') as HTMLElement).textContent
  })
  expect(shown).toBe('0 of 600')
  await expect(page).toHaveURL(/[?&]q=zzzqq/)
  await expect(page.getByText('No worktrees match the filter “zzzqq”.')).toBeVisible()
  await page.getByRole('button', { name: 'Clear filters' }).click()
  await expect(page.locator('.count')).toHaveText('600 of 600')
  await expect(page).not.toHaveURL(/q=/)
})

test('rows keep their place from the placeholders to the data: the layout shift stays under 0.01', async ({ page }) => {
  await page.addInitScript(() => {
    const store = window as unknown as { __cls: number }
    store.__cls = 0
    new PerformanceObserver((list) => {
      for (const entry of list.getEntries() as unknown as { value: number; hadRecentInput: boolean }[]) if (!entry.hadRecentInput) store.__cls += entry.value
    }).observe({ type: 'layout-shift', buffered: true })
  })
  await stubFixture(page, 700)
  await page.goto('/cockpit/worktrees')
  await expect(listRows(page).first()).toBeVisible()
  await page.waitForTimeout(500)
  expect(await page.evaluate(() => (window as unknown as { __cls: number }).__cls)).toBeLessThan(0.01)
})

test('j, k, Enter and Esc move through the rows and open and close the panel, and the address holds the selection', async ({ page }) => {
  await stubFixture(page)
  const expectClean = await watch(page)
  await page.goto('/cockpit/worktrees')
  await expect(listRows(page).first()).toBeVisible()
  await grid(page).focus()
  await page.keyboard.press('j')
  await page.keyboard.press('j')
  await page.keyboard.press('k')
  await expect(page.locator('.row.focused')).toHaveAttribute('aria-rowindex', '3')
  await page.keyboard.press('Enter')
  const second = (await page.locator('.row.focused a.name').textContent())!
  await expect(page).toHaveURL(/[?&]sel=/)
  const panel = page.getByRole('complementary')
  await expect(panel).toBeVisible()
  await expect(panel.getByRole('heading', { level: 2 })).toHaveText(second)
  // Beside the list the focus stays in the list, which keeps browsing: the panel follows.
  await expect(grid(page)).toBeFocused()
  await page.keyboard.press('j')
  await expect(panel.getByRole('heading', { level: 2 })).not.toHaveText(second)
  await page.keyboard.press('Enter')
  await expect(panel).toBeFocused()
  await grid(page).focus()
  await expect(page.locator('.row.selected')).toHaveCount(1)
  await page.keyboard.press('Escape')
  await expect(panel).toBeHidden()
  await expect(page).not.toHaveURL(/sel=/)
  await expect(grid(page)).toBeFocused()
  await expect(page.locator('.row.focused')).toHaveAttribute('aria-rowindex', '4')
  await expectClean()
})

test('the back button restores each earlier state: filter, chip, sort and selection', async ({ page }) => {
  await stubFixture(page)
  await page.goto('/cockpit/worktrees')
  await expect(listRows(page).first()).toBeVisible()
  const filter = page.getByRole('textbox', { name: 'Filter worktrees' })
  await filter.fill('fix')
  await expect(page).toHaveURL(/q=fix/)
  await page.getByRole('button', { name: 'Unpushed', exact: true }).click()
  await expect(page).toHaveURL(/chips=unpushed/)
  const unfiltered = (await page.locator('.count').textContent())!
  await expect(page.locator('.count')).not.toHaveText(unfiltered)
  const withChip = await page.locator('.count').textContent()
  await page.getByRole('button', { name: 'Machine', exact: true }).click()
  await expect(page).toHaveURL(/sort=machine/)
  await listRows(page).first().locator('[role=gridcell]:not(.open-cell)').last().click()
  await expect(page).toHaveURL(/sel=/)
  await page.goBack()
  await expect(page).not.toHaveURL(/sel=/)
  await expect(page.getByRole('complementary')).toBeHidden()
  await page.goBack()
  await expect(page).not.toHaveURL(/sort=/)
  await expect(page.locator('.count')).toHaveText(withChip!)
  await page.goBack()
  await expect(page).not.toHaveURL(/chips=/)
  await expect(page.getByRole('button', { name: 'Unpushed', exact: true })).toHaveAttribute('aria-pressed', 'false')
  await expect(filter).toHaveValue('fix')
  await page.goForward()
  await expect(page).toHaveURL(/chips=unpushed/)
  await expect(page.getByRole('button', { name: 'Unpushed', exact: true })).toHaveAttribute('aria-pressed', 'true')
})

test('o opens the focused row\'s page and c copies its name', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await stubFixture(page)
  await page.goto('/cockpit/worktrees')
  await expect(listRows(page).first()).toBeVisible()
  await grid(page).focus()
  const name = (await page.locator('.row.focused a.name').textContent())!
  await page.keyboard.press('c')
  await expect(page.locator('section > [role=status]')).toHaveText(`Copied ${name}`)
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(name)
  await page.keyboard.press('o')
  await expect(page).toHaveURL(/\/cockpit\/worktrees\/wt-/)
})

test('/ focuses the filter, Esc clears it and then closes the panel, and nothing fires while typing or composing', async ({ page }) => {
  await stubFixture(page)
  await page.goto(`/cockpit/worktrees?sel=${encodeURIComponent(first.id)}`)
  const panel = page.getByRole('complementary')
  await expect(panel).toBeVisible()
  const filter = page.getByRole('textbox', { name: 'Filter worktrees' })
  await page.locator('body').click({ position: { x: 5, y: 5 } })
  await page.keyboard.press('/')
  await expect(filter).toBeFocused()
  // Typing letters that are shortcuts elsewhere (g then a, ? and /) goes into the box.
  await page.keyboard.type('ga?/')
  await expect(filter).toHaveValue('ga?/')
  await expect(page).toHaveURL(/\/cockpit\/worktrees/)
  await expect(page.getByRole('dialog', { name: /shortcuts/i })).toHaveCount(0)
  // Esc clears the text and leaves the panel; the next Esc, in the empty box, closes the panel.
  await page.keyboard.press('Escape')
  await expect(filter).toHaveValue('')
  await expect(panel).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(panel).toBeHidden()
  // A key during an IME composition is not a shortcut.
  await page.locator('body').click({ position: { x: 5, y: 5 } })
  await page.evaluate(() => document.dispatchEvent(new KeyboardEvent('keydown', { key: '/', isComposing: true, bubbles: true })))
  await expect(filter).not.toBeFocused()
})

test('the panel shows the summary, the related entities and the commands, with Raw data collapsed until opened, and copies the full value', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await stubFixture(page)
  await page.goto(`/cockpit/worktrees?sel=${encodeURIComponent(first.id)}`)
  const panel = page.getByRole('complementary')
  await expect(panel.getByRole('heading', { level: 2 })).toHaveText(first.task)
  await expect(panel.getByText('Copy command', { exact: true })).toBeVisible()
  const raw = panel.locator('details.raw')
  await expect(raw).not.toHaveAttribute('open', '')
  await expect(panel.locator('pre')).toHaveCount(0)
  await raw.locator('summary').click()
  await expect(panel.locator('pre')).toContainText(`"id": "${first.id}"`)
  await panel.getByRole('button', { name: 'Copy branch' }).click()
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(first.branch)
})

test('the panel is a full-screen sheet on a phone, and Esc closes it', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await stubFixture(page)
  await page.goto('/cockpit/worktrees')
  await listRows(page).first().locator('[role=gridcell]:not(.open-cell)').last().click()
  const sheet = page.getByRole('dialog')
  await expect(sheet).toBeVisible()
  const box = (await sheet.boundingBox())!
  expect(box.width).toBeGreaterThanOrEqual(389)
  expect(box.height).toBeGreaterThanOrEqual(843)
  await expect(sheet).toBeFocused()
  await expect(page.locator('section.list')).toHaveAttribute('inert', '')
  await page.keyboard.press('Escape')
  await expect(sheet).toBeHidden()
  const widths = await page.evaluate(() => ({ page: document.documentElement.scrollWidth, window: window.innerWidth }))
  expect(widths.page).toBeLessThanOrEqual(widths.window)
})

test('the detail route renders the same content as the panel, ending with Raw data', async ({ page }) => {
  await stubFixture(page)
  await page.goto(`/cockpit/worktrees/${encodeURIComponent(first.id)}`)
  const content = page.locator('app-worktree-panel')
  await expect(content.getByRole('heading', { level: 2 })).toHaveText(first.task)
  await expect(content.locator('section').last()).toHaveAttribute('aria-label', 'Raw data')
  await expect(content.locator('details.raw')).not.toHaveAttribute('open', '')
})
