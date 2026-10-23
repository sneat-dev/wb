import AxeBuilder from '@axe-core/playwright'
import { expect, test, type Page } from '@playwright/test'
import { BUSY_ROUTES, stubBusy, watch } from './support'

// An automated accessibility check (axe) of every route, in both themes, on the busy fleet that shows every state of
// the lists, and of the states a page has beside its route: a list with its panel open, the palette, the shortcut
// sheet, the owner card and the phone widths. WCAG 2 A and AA rules, contrast included, in the built application.
// What axe cannot see (focus order, one Tab stop per grid, state beside colour) is asserted in a11y-keyboard.e2e.ts.

const SCHEMES = ['light', 'dark'] as const
const TAGS = ['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa', 'wcag22aa', 'best-practice']

async function settled(page: Page, path: string): Promise<void> {
  await page.goto(`/cockpit/${path}`)
  await expect(page.getByRole('heading', { level: 1 })).not.toHaveText('')
  await page.locator('app-overlays').waitFor({ state: 'attached' })
  if (path === '') {
    await expect(page.getByRole('heading', { level: 2, name: 'Cleanup' })).toBeVisible()
    // The charts are a lazy chunk requested right after the first paint; wait for them so axe sees the canvases and their tables.
    await expect(page.locator('app-chart canvas').first()).toBeVisible()
  }
  if (path.startsWith('machines/mach-')) await expect(page.locator('app-machine-panel .source')).not.toContainText('Reading')
  await page.waitForTimeout(300)
}

async function violations(page: Page): Promise<string[]> {
  const results = await new AxeBuilder({ page }).withTags(TAGS).analyze()
  return results.violations.map((violation) => `${violation.id} (${violation.impact}): ${violation.help} — ${violation.nodes.slice(0, 4).map((node) => node.target.join(' ')).join(' | ')}`)
}

for (const scheme of SCHEMES) {
  test.describe(`${scheme} theme`, () => {
    test.use({ colorScheme: scheme })

    for (const route of BUSY_ROUTES) {
      test(`${route.name} has no axe violations`, async ({ page }) => {
        await stubBusy(page)
        const expectClean = await watch(page)
        await settled(page, route.path)
        expect(await violations(page)).toEqual([])
        await expectClean()
      })
    }

    for (const list of ['tasks', 'repositories', 'worktrees', 'agents', 'machines']) {
      test(`${list} with its panel open has no axe violations`, async ({ page }) => {
        await stubBusy(page)
        await settled(page, list)
        await page.locator('[role=row][data-index]').first().locator('[role=gridcell]:not(.open-cell)').first().click()
        await expect(page.getByRole('complementary')).toBeVisible()
        expect(await violations(page)).toEqual([])
      })
    }

    test('the palette, the shortcut sheet and the owner card have no axe violations', async ({ page }) => {
      await stubBusy(page)
      await settled(page, '')
      await page.keyboard.press('Control+k')
      await page.getByRole('dialog', { name: 'Search' }).getByRole('combobox').fill('re')
      await expect(page.getByRole('dialog', { name: 'Search' }).getByRole('option').first()).toBeVisible()
      expect(await violations(page)).toEqual([])
      await page.keyboard.press('Escape')
      await page.keyboard.press('?')
      await expect(page.getByRole('dialog', { name: 'Keyboard shortcuts' })).toBeVisible()
      expect(await violations(page)).toEqual([])
      await page.keyboard.press('Escape')
      await page.getByText('anonymous', { exact: true }).click()
      await expect(page.getByRole('dialog', { name: 'Sign in as owner' })).toBeVisible()
      expect(await violations(page)).toEqual([])
    })

    for (const width of [390, 360]) {
      test(`Home and Worktrees at ${width} px wide have no axe violations`, async ({ page }) => {
        await page.setViewportSize({ width, height: 800 })
        await stubBusy(page)
        await page.goto('/cockpit/')
        await expect(page.getByRole('heading', { level: 2, name: 'Needs you' })).toBeVisible()
        await page.getByRole('button', { name: 'More' }).click()
        await expect(page.getByRole('heading', { level: 2, name: 'Cleanup' })).toBeAttached()
        // Back to the top: what has scrolled under the sticky bar is obscured, which is the bar's job, not a target's size.
        await page.evaluate(() => window.scrollTo(0, 0))
        expect(await violations(page)).toEqual([])
        await settled(page, 'worktrees')
        await page.locator('[role=row][data-index]').first().locator('[role=gridcell]:not(.open-cell)').last().click()
        await expect(page.getByRole('dialog')).toBeVisible()
        expect(await violations(page)).toEqual([])
      })
    }
  })
}
