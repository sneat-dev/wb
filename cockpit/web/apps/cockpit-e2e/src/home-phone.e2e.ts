import { expect, test } from '@playwright/test'
import { stubBusy, watch } from './support'

// cockpit-views#ac:home-phone-layout and usable-at-360-wide on Home: "Needs you" first, no chart above the fold,
// every control a 44 px target, nothing scrolls sideways, and the bar's chips do not overlap.

for (const width of [390, 360]) {
  test.describe(`${width} px wide`, () => {
    test.use({ viewport: { width, height: 800 } })

    test('Needs you comes first, no chart is above the fold, and every tap target is 44 px', async ({ page }) => {
      await stubBusy(page)
      const expectClean = await watch(page)
      await page.goto('/cockpit/')
      await expect(page.getByRole('heading', { level: 2, name: 'Needs you' })).toBeVisible()
      await expect(page.getByRole('button', { name: 'More' })).toBeVisible()
      // The first section is "Needs you", and the charts (behind More) are not in the document before it is opened.
      await expect(page.locator('h2.home-h').first()).toContainText('Needs you')
      await expect(page.locator('app-chart canvas')).toHaveCount(0)
      const widths = await page.evaluate(() => ({ page: document.documentElement.scrollWidth, window: window.innerWidth }))
      expect(widths.page).toBeLessThanOrEqual(widths.window)

      const measure = () => page.evaluate(() => {
        const found: string[] = []
        for (const element of document.querySelectorAll<HTMLElement>('a[href], button, [role=button], input, [role=combobox]')) {
          const box = element.getBoundingClientRect()
          const style = getComputedStyle(element)
          // Hidden or clipped elements are not targets; a skip link or the visually hidden are not either.
          if (box.width <= 1 || box.height <= 1 || style.visibility === 'hidden' || style.display === 'none' || element.closest('[aria-hidden=true]')) continue
          if (box.top > window.innerHeight * 3) continue
          // A small control holds its 44 px target in a positioned ::after of its own (no room taken): that counts.
          const after = getComputedStyle(element, '::after')
          const extra = after.position === 'absolute' ? { width: Number.parseFloat(after.width) || 0, height: Number.parseFloat(after.height) || 0 } : { width: 0, height: 0 }
          const wide = Math.max(box.width, extra.width)
          const high = Math.max(box.height, extra.height)
          if (element.classList.contains('skip-link')) continue
          if (high < 43.5 || wide < 43.5) found.push(`${element.tagName.toLowerCase()}.${element.className} "${(element.textContent ?? element.getAttribute('aria-label') ?? '').trim().slice(0, 30)}" ${Math.round(wide)}x${Math.round(high)}`)
        }
        return found
      })
      expect(await measure()).toEqual([])
      // Behind More: Resume, Cleanup, Fleet health and the charts are targets too.
      await page.getByRole('button', { name: 'More' }).click()
      await expect(page.getByRole('heading', { level: 2, name: 'Cleanup' })).toBeVisible()
      expect(await measure()).toEqual([])
      await expectClean()
    })

    test('the bar keeps its chips apart', async ({ page }) => {
      await stubBusy(page)
      await page.goto('/cockpit/')
      await expect(page.getByTestId('freshness-chip')).toBeVisible()
      const boxes = await page.evaluate(() => ['app-freshness-chip .chip', '.session'].map((selector) => document.querySelector(selector)?.getBoundingClientRect()).map((box) => ({ left: box?.left ?? 0, right: box?.right ?? 0 })))
      expect(boxes[0].right).toBeLessThanOrEqual(boxes[1].left)
      expect(boxes[1].right).toBeLessThanOrEqual(width)
    })
  })
}
