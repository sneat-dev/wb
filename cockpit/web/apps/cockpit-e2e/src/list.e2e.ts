import { performanceFixture } from '@cockpit/fleet-data/testing'
import { expect, test, type Page } from '@playwright/test'
import { watch } from './support'

// The shared list and its side panel, on the Worktrees page, against the 600-worktree fixture
// of the performance budgets (REQ:bounded-row-elements, REQ:fast-filtering, REQ:look-layout).

const { document: fleet } = performanceFixture()
const first = fleet.worktrees[0]

const listRows = (page: Page) => page.locator('[role=row][aria-rowindex]')
const grid = (page: Page) => page.getByRole('grid')

async function stubFixture(page: Page, delayMs = 0) {
  await page.route('**/api/v1/cockpit/fleet', async (route) => {
    if (delayMs > 0) await new Promise((done) => setTimeout(done, delayMs))
    await route.fulfill({ json: fleet, headers: { ETag: '"perf"', 'Cache-Control': 'no-cache' } })
  })
  await page.route('**/api/v1/cockpit/session', (route) => route.fulfill({ json: { principal: 'anonymous-local', capabilities: ['fleet.read'], code_browser_url: 'https://codegrapher.dev/' } }))
  await page.route('**/api/v1/cockpit/machine-metrics?**', (route) => route.fulfill({ json: { machine: 'x', route: 'local', samples: [] } }))
}

test('at most 60 rows are in the DOM at 1080 px with 600 worktrees, under a header that stays while the rows move', async ({ page }) => {
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
  await page.locator('.viewport').evaluate((list) => (list.scrollTop = 9000))
  await expect(listRows(page).first()).not.toHaveAttribute('aria-rowindex', before as string)
  expect(await listRows(page).count()).toBeLessThanOrEqual(60)
  const head = (await page.locator('.head').boundingBox())!
  const list = (await page.locator('.viewport').boundingBox())!
  expect(Math.abs(head.y - list.y)).toBeLessThan(4)
  await expectClean()
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
  const second = (await page.locator('.row.focused .task').textContent())!
  await expect(page).toHaveURL(/[?&]sel=/)
  const panel = page.getByRole('complementary')
  await expect(panel).toBeVisible()
  await expect(panel.getByRole('heading', { level: 2 })).toHaveText(second)
  // The list is still there beside it, with the selected row marked.
  await expect(listRows(page).first()).toBeVisible()
  await expect(page.locator('.row.selected')).toHaveCount(1)
  await page.keyboard.press('Escape')
  await expect(panel).toBeHidden()
  await expect(page).not.toHaveURL(/sel=/)
  await expect(grid(page)).toBeFocused()
  await expectClean()
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
  const raw = panel.locator('details')
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
  await listRows(page).first().locator('[role=gridcell]').last().click()
  const sheet = page.getByRole('dialog')
  await expect(sheet).toBeVisible()
  const box = (await sheet.boundingBox())!
  expect(box.width).toBeGreaterThanOrEqual(389)
  expect(box.height).toBeGreaterThanOrEqual(843)
  await expect(sheet).toBeFocused()
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
  await expect(content.locator('details')).not.toHaveAttribute('open', '')
})
