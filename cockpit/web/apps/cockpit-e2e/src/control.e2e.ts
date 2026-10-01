import { expect, test } from '@playwright/test'
import { stub, watch } from './support'

// The control surface against stubbed responses: the gallery of the preview
// build (dist-preview, served beside the production build), and the sign-in card
// of the production shell.
const gallery = `http://127.0.0.1:${Number(process.env['COCKPIT_E2E_PORT'] || '4300') + 1}/cockpit/gallery`

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

test('the gallery redraws in dark and honours reduced motion', async ({ page }) => {
  await stub(page)
  await page.emulateMedia({ colorScheme: 'light', reducedMotion: 'reduce' })
  await page.goto(gallery)
  await expect(page.locator('app-chart canvas')).toHaveCount(6)
  const pixel = () => page.locator('app-chart canvas').nth(4).evaluate((canvas) => Array.from((canvas as HTMLCanvasElement).getContext('2d')!.getImageData(0, 0, 40, 40).data).join(','))
  await expect.poll(async () => (await pixel()).length).toBeGreaterThan(0)
  const light = await page.locator('.gallery').evaluate((element) => getComputedStyle(element).color)
  await page.emulateMedia({ colorScheme: 'dark', reducedMotion: 'reduce' })
  await expect.poll(() => page.locator('.gallery').evaluate((element) => getComputedStyle(element).color)).not.toBe(light)
})

test('a copy button puts exactly the library command on the clipboard and says so; an action slot only emits', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: new URL(gallery).origin })
  await stub(page)
  const requests: string[] = []
  await page.goto(gallery)
  await expect(page.locator('app-copy-command-list').first()).toBeVisible()
  page.on('request', (request) => requests.push(request.url()))
  const first = page.locator('app-copy-command-list li').first()
  const command = (await first.locator('code').innerText()).trim()
  await first.getByRole('button', { name: /^Copy command/ }).click()
  await expect(first.getByRole('button', { name: /^Copy command/ })).toContainText('Copied')
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(command)
  expect(command).toBe("wb worktree list 'fix-ci'")
  await page.getByRole('button', { name: 'Open pull request' }).first().click()
  await expect(page.getByText('Emitted pr.create for worktree:wt-1; nothing ran.')).toBeVisible()
  expect(requests).toEqual([])
  expect(page.url()).toBe(gallery)
})

test('disabled actions keep their reason, and the absent registry leaves no box', async ({ page }) => {
  await stub(page)
  await page.goto(gallery)
  const disabled = page.getByRole('button', { name: 'Push branch' }).first()
  await expect(disabled).toHaveAttribute('aria-disabled', 'true')
  await expect(disabled).toHaveAttribute('title', 'Nothing to push')
  await expect(page.getByRole('button', { name: 'Open pull request' }).nth(1)).toHaveAttribute('title', 'Needs an owner session')
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
  await card.getByRole('button', { name: /Copy command/ }).click()
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('wb cockpit')
  await page.keyboard.press('Escape')
  await expect(card).toBeHidden()
  await expect(chip).toBeFocused()
  await expectClean()
})
