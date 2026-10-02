import { expect, test } from '@playwright/test'
import { stub, watch } from './support'

// The control surface against stubbed responses: the gallery of the preview
// build (dist-preview, served beside the production build), and the sign-in card
// of the production shell.
const gallery = `http://127.0.0.1:${Number(process.env['COCKPIT_E2E_PREVIEW_PORT'] || Number(process.env['COCKPIT_E2E_PORT'] || '4300') + 1)}/cockpit/gallery`
// With a handler bound, which no page of the application has yet: the registry's own buttons.
const handled = `${gallery}?handler=1`

test('the gallery draws every chart on a canvas under the strict policy, with a text alternative each', async ({ page }) => {
  await stub(page)
  const expectClean = await watch(page)
  const response = await page.goto(gallery)
  expect((await (response as NonNullable<typeof response>).allHeaders())['content-security-policy']).not.toContain('unsafe-')
  await expect(page.locator('app-chart canvas')).toHaveCount(6)
  await expect.poll(() => page.locator('app-chart canvas').evaluateAll((canvases) => canvases.every((canvas) => canvas.getBoundingClientRect().height > 100 && (canvas as HTMLCanvasElement).width > 0))).toBe(true)
  await expect(page.locator('app-chart .data table')).toHaveCount(6)
  expect(await page.locator('app-chart script, app-chart style, app-chart [style*="url"]').count()).toBe(0)
  await expectClean()
})

test('the gallery redraws its charts in the other colour scheme and honours reduced motion', async ({ page }) => {
  await stub(page)
  await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
  await page.goto(gallery)
  await expect(page.locator('app-chart canvas')).toHaveCount(6)
  const pixels = () => page.locator('app-chart canvas').evaluateAll((canvases) => canvases.map((canvas) => (canvas as HTMLCanvasElement).toDataURL()).join('|'))
  await expect.poll(async () => (await pixels()).length).toBeGreaterThan(1000)
  const light = await pixels()
  await page.emulateMedia({ colorScheme: 'dark', reducedMotion: 'reduce' })
  // Every canvas is drawn again with the dark theme's colours, not left as it was.
  await expect.poll(async () => (await pixels()) !== light).toBe(true)
  const dark = await page.locator('app-chart canvas').evaluateAll((canvases) => canvases.map((canvas) => (canvas as HTMLCanvasElement).toDataURL()))
  const lightEach = light.split('|')
  expect(dark.every((url, index) => url !== lightEach[index])).toBe(true)
})

/** Whether a request is for the page's own script or style: same origin, under the app's path, a .js or .css file. */
function isOwnCode(url: string, page: string): boolean {
  const asked = new URL(url)
  return asked.origin === new URL(page).origin && /^\/cockpit\/[^/]+\.(js|css)$/.test(asked.pathname)
}

test('a copy button puts exactly the library command on the clipboard and says so; an action slot only emits', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: new URL(gallery).origin })
  await stub(page)
  const requests: string[] = []
  await page.goto(handled)
  await expect(page.locator('app-copy-command-list').first()).toBeVisible()
  // The gallery's charts load Chart.js (two chunks) once they have rendered, which can be after the command list is
  // visible. They are the last thing the page fetches by itself, and a chart is on its canvas only once they are in,
  // so wait for that before listening: what is counted below is what the click causes, not what the page was still loading.
  await expect.poll(() => page.locator('app-chart canvas').evaluateAll((canvases) => canvases.length === 6 && canvases.every((canvas) => (canvas as HTMLCanvasElement).toDataURL().length > 1000))).toBe(true)
  // What is counted is what a click asks of the daemon. The page's own code is not that: a chunk the gallery was
  // still loading (a chart's, on a slow runner) is static, same-origin and carries no command, so it is left out.
  page.on('request', (request) => {
    if (!isOwnCode(request.url(), handled)) requests.push(request.url())
  })
  const first = page.locator('app-copy-command-list li').first()
  const command = (await first.locator('code').innerText()).trim()
  await first.getByRole('button', { name: /^Copy wb worktree list/ }).click()
  await expect(first.getByRole('button', { name: /^Copy wb worktree list/ })).toContainText('Copied')
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(command)
  expect(command).toBe("wb worktree list 'fix-ci'")
  await page.getByRole('button', { name: 'Open pull request', exact: true }).first().click()
  await expect(page.getByText('Emitted pr.create for worktree:wt-1; nothing ran.')).toBeVisible()
  expect(requests).toEqual([])
  expect(page.url()).toBe(handled)
})

test('disabled actions keep their reason, and the absent registry leaves no box', async ({ page }) => {
  await stub(page)
  await page.goto(handled)
  const disabled = page.getByRole('button', { name: 'Push branch' }).first()
  await expect(disabled).toHaveAttribute('aria-disabled', 'true')
  await expect(disabled).toHaveAccessibleDescription('Nothing to push')
  // The reason is also in words on hover and focus, and read once (no title).
  await expect(disabled).not.toHaveAttribute('title', /./)
  await disabled.hover()
  await expect(page.getByText('Push branch: Nothing to push')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Open pull request', exact: true }).nth(1)).toHaveAccessibleDescription('Needs an owner session')
  await page.getByRole('button', { name: 'More actions' }).first().click()
  await expect(page.getByRole('menuitem', { name: /Delete branch/ }).first()).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('menuitem', { name: /Delete branch/ }).first()).toBeHidden()
  const absent = page.locator('.slot-row').nth(2)
  expect(await absent.locator('app-action-slot').evaluate((slot) => getComputedStyle(slot).display)).toBe('contents')
  expect(await absent.locator('app-action-slot').evaluate((slot) => slot.children.length)).toBe(0)
})

test('the gallery has no horizontal scroll at 360 wide', async ({ page }) => {
  await stub(page)
  await page.setViewportSize({ width: 360, height: 800 })
  await page.goto(gallery)
  await expect(page.locator('app-chart canvas')).toHaveCount(6)
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
})

test('the anonymous session chip offers "Sign in as owner: run wb cockpit" with a copy button, in one place', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  await stub(page)
  const expectClean = await watch(page)
  await page.goto('/cockpit/')
  const chip = page.getByRole('button', { name: /anonymous/ })
  await chip.click()
  const card = page.getByRole('dialog', { name: 'Sign in as owner' })
  await expect(card).toContainText('Sign in as owner: run wb cockpit')
  await card.getByRole('button', { name: /^Copy wb cockpit/ }).click()
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('wb cockpit')
  await page.keyboard.press('Escape')
  await expect(card).toBeHidden()
  await expect(chip).toBeFocused()
  await expectClean()
})

test('the production build has no /gallery route: the address falls back to Home', async ({ page }) => {
  await stub(page)
  await page.goto('/cockpit/gallery')
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Home')
  await expect(page.locator('app-gallery')).toHaveCount(0)
  await expect(page).toHaveURL(/\/cockpit\/$/)
})
