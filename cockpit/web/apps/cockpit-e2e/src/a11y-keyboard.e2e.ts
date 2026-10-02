import { expect, test, type Page } from '@playwright/test'
import { BUSY_ROUTES, stubBusy, watch } from './support'

// What axe cannot see (a11y.e2e.ts has what it can): one Tab stop per grid, a visible focus ring on every stop, the row
// keys, state that is never colour alone, the type sizes of tables, no sideways scroll at 360 px on any route, and
// layout shift under 0.01 from skeleton to data.

const LISTS = ['tasks', 'repositories', 'worktrees', 'agents', 'machines']
const grid = (page: Page) => page.getByRole('grid')

for (const list of LISTS) {
  test(`${list}: the grid is one Tab stop, takes the row keys and shows where focus is`, async ({ page }) => {
    await stubBusy(page)
    const expectClean = await watch(page)
    await page.goto(`/cockpit/${list}`)
    await expect(page.locator('[role=row][data-index]').first()).toBeVisible()
    // Inside the grid exactly one element is tabbable: the grid itself.
    expect(await grid(page).evaluate((element) => [element, ...element.querySelectorAll('a[href], button, input, select, textarea, [tabindex]')].filter((candidate) => (candidate as HTMLElement).tabIndex >= 0).length)).toBe(1)
    // Tabbing from the top reaches the grid once, and every stop on the way has a visible ring.
    await page.locator('body').click({ position: { x: 1, y: 1 } })
    let stopsInGrid = 0
    for (let press = 0; press < 40; press++) {
      await page.keyboard.press('Tab')
      // Past the last control the focus leaves the page for the browser's own: one round is enough.
      if (await page.evaluate(() => document.activeElement === document.body)) break
      const stop = await page.evaluate(() => {
        const element = document.activeElement as HTMLElement
        const ringOf = (target: HTMLElement) => {
          const look = getComputedStyle(target)
          return (look.outlineStyle !== 'none' && Number.parseFloat(look.outlineWidth) > 0) || look.boxShadow !== 'none'
        }
        // A text box shows its focus on the frame that holds it (:focus-within).
        const ring = ringOf(element) || (element.closest('.filter') !== null && ringOf(element.closest('.filter') as HTMLElement))
        return { inGrid: element.closest('[role=grid]') !== null || element.getAttribute('role') === 'grid', ring, name: `${element.tagName.toLowerCase()}.${element.className}` }
      })
      expect(stop.ring, `a visible ring on ${stop.name}`).toBe(true)
      if (stop.inGrid) stopsInGrid++
    }
    expect(stopsInGrid).toBe(1)
    // The row keys.
    await grid(page).focus()
    await page.keyboard.press('j')
    await expect(page.locator('.row.focused')).toHaveAttribute('aria-rowindex', '3')
    await page.keyboard.press('k')
    await page.keyboard.press('Enter')
    await expect(page.getByRole('complementary')).toBeVisible()
    await page.keyboard.press('Escape')
    await expect(page.getByRole('complementary')).toBeHidden()
    await page.keyboard.press('o')
    await expect(page).toHaveURL(new RegExp(`/cockpit/${list}/`.replace('tasks/', 'tasks/detail')))
    await expectClean()
  })
}

for (const scheme of ['light', 'dark'] as const) {
  test.describe(`${scheme}: state is never colour alone`, () => {
    test.use({ colorScheme: scheme })

    for (const route of BUSY_ROUTES) {
      test(`${route.name}: every badge has a glyph and a word, and the tables are 13 px with tabular numerals`, async ({ page }) => {
        await stubBusy(page)
        await page.goto(`/cockpit/${route.path}`)
        await page.locator('app-overlays').waitFor({ state: 'attached' })
        await page.waitForTimeout(300)
        const report = await page.evaluate(() => {
          const bare: string[] = []
          for (const badge of document.querySelectorAll('app-state-badge')) {
            const word = badge.querySelector('.text')?.textContent?.trim() ?? ''
            if (word === '' || badge.querySelector('svg path') === null) bare.push(badge.outerHTML.slice(0, 120))
          }
          // The marks and chips that carry state in a colour also carry its word.
          for (const mark of document.querySelectorAll('.mark, .home-chip, .chip')) if ((mark.textContent ?? '').trim() === '') bare.push(mark.outerHTML.slice(0, 120))
          const cells = [...document.querySelectorAll<HTMLElement>('[role=row][data-index] [role=gridcell]')]
          const sizes = [...new Set(cells.map((cell) => getComputedStyle(cell).fontSize))]
          const numbers = [...document.querySelectorAll<HTMLElement>('[role=row][data-index] .tnum')].filter((element) => !getComputedStyle(element).fontVariantNumeric.includes('tabular-nums')).length
          const badges = [...document.querySelectorAll<HTMLElement>('.badge, app-state-badge .text')].map((element) => getComputedStyle(element).fontSize)
          return { bare, sizes, numbers, badgeSizes: [...new Set(badges)] }
        })
        expect(report.bare).toEqual([])
        expect(report.numbers).toBe(0)
        if (report.sizes.length > 0) expect(report.sizes).toEqual(['13px'])
        for (const size of report.badgeSizes) expect(['12px', '13px']).toContain(size)
      })
    }
  })
}

for (const width of [360, 390]) {
  test(`${width} px: no route scrolls sideways, with its panel open too`, async ({ page }) => {
    await page.setViewportSize({ width, height: 800 })
    await stubBusy(page)
    for (const route of BUSY_ROUTES) {
      await page.goto(`/cockpit/${route.path}`)
      await expect(page.getByRole('heading', { level: 1 })).not.toHaveText('')
      await page.locator('app-overlays').waitFor({ state: 'attached' })
      await page.waitForTimeout(300)
      const widths = await page.evaluate(() => ({ page: document.documentElement.scrollWidth, window: window.innerWidth }))
      expect(widths.page, route.name).toBeLessThanOrEqual(widths.window)
    }
    for (const list of LISTS) {
      await page.goto(`/cockpit/${list}`)
      await page.locator('[role=row][data-index]').first().locator('[role=gridcell]:not(.open-cell)').last().click()
      await expect(page.getByRole('dialog')).toBeVisible()
      const widths = await page.evaluate(() => ({ page: document.documentElement.scrollWidth, window: window.innerWidth }))
      expect(widths.page, `${list} panel`).toBeLessThanOrEqual(widths.window)
    }
  })
}

// cockpit-views#ac:no-layout-shift-on-arrival, on every route: the skeleton stands where the data will.
for (const route of BUSY_ROUTES) {
  test(`${route.name}: the layout shift from skeleton to data stays under 0.01`, async ({ page }) => {
    await page.addInitScript(() => {
      const store = window as unknown as { __cls: number }
      store.__cls = 0
      new PerformanceObserver((list) => {
        for (const entry of list.getEntries() as unknown as { value: number; hadRecentInput: boolean }[]) if (!entry.hadRecentInput) store.__cls += entry.value
      }).observe({ type: 'layout-shift', buffered: true })
    })
    await stubBusy(page)
    // The fleet arrives late, so the skeleton is what the first paint shows.
    await page.route('**/api/v1/cockpit/fleet', async (fulfil) => {
      await new Promise((done) => setTimeout(done, 500))
      await fulfil.fallback()
    })
    await page.goto(`/cockpit/${route.path}`)
    await expect(page.getByRole('heading', { level: 1 })).not.toHaveText('')
    await page.locator('app-overlays').waitFor({ state: 'attached' })
    await page.waitForTimeout(1500)
    expect(await page.evaluate(() => (window as unknown as { __cls: number }).__cls)).toBeLessThan(0.01)
  })
}
