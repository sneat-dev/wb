// CLI entry: COCKPIT_SHOTS_DIR=<dir> node tools/shots.mjs, run from cockpit/web
// after `pnpm build`. Serves the build with the fixtures (tools/lib/preview-fixture.mjs,
// on COCKPIT_PREVIEW_PORT when set, else a free loopback port) and writes a
// screenshot of every route, in light and dark at 1440x900 and 390x844, and of
// the list and its side panel, the palette, the shortcut sheet and the warming-up and schema-mismatch states,
// and, when dist-preview/ exists (pnpm build:preview), of the control-surface gallery.
import { existsSync, mkdirSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { chromium } from '@playwright/test'
import { loadFixture } from './fixtures.mjs'
import { previewPort, startPreview } from './lib/preview-fixture.mjs'
import { GALLERY_DIST, galleryPlan, homeShotPlan, shotPlan } from './lib/shot-plan.mjs'

const directory = process.env.COCKPIT_SHOTS_DIR
if (!directory) throw new Error('shots: set COCKPIT_SHOTS_DIR to the directory the screenshots go to')
mkdirSync(directory, { recursive: true })

const data = await loadFixture({ home: true })
// The gallery is a route of the preview build only; without that build it is skipped.
const galleryBuilt = existsSync(join(process.cwd(), GALLERY_DIST, 'index.html'))
if (!galleryBuilt) console.log(`shots: no ${GALLERY_DIST}/ (run pnpm build:preview), so the gallery is not photographed`)
// COCKPIT_SHOTS_FILTER=<text> keeps only the shots whose file name contains the text.
const plan = [...shotPlan(data.document), ...homeShotPlan(), ...(galleryBuilt ? galleryPlan() : [])].filter((shot) => shot.file.includes(process.env.COCKPIT_SHOTS_FILTER ?? ''))
const port = process.env.COCKPIT_PREVIEW_PORT ? previewPort(process.env) : 0
const browser = await chromium.launch()
const servers = new Map()

// A server per (build, daemon state, session, registry, fleet): what a shot needs around it.
async function baseOf({ state, dist = 'dist', session, registry, home }) {
  const key = [dist, state, session, registry, home].join('/')
  if (!servers.has(key)) {
    // The port from the environment serves the first server; the others take a free one.
    const served = home === undefined ? data : { ...data, document: data.home[home].document, metrics: data.home[home].metrics, branches: data.home[home].branches }
    servers.set(key, await startPreview({ distRoot: join(process.cwd(), dist), data: served, state, session, registry }, servers.size === 0 ? port : 0))
  }
  return `http://127.0.0.1:${servers.get(key).address().port}/cockpit`
}

// One step of a list shot (tools/lib/shot-plan.mjs): what an operator does to the list.
async function runStep(page, step) {
  const colon = step.indexOf(':')
  const kind = colon < 0 ? step : step.slice(0, colon)
  const argument = colon < 0 ? '' : step.slice(colon + 1)
  if (kind === 'filter') await page.getByRole('textbox', { name: /^Filter/ }).fill(argument)
  else if (kind === 'chip') await page.getByRole('button', { name: argument, exact: true }).click()
  // The last cell but the open button is the time: a click there selects the row.
  else if (kind === 'row') await page.locator('[role=row][data-index]').nth(Number(argument)).locator('[role=gridcell]:not(.open-cell)').last().click()
  else if (kind === 'raw') await page.getByText('Raw data', { exact: true }).click()
  await page.waitForTimeout(200)
}

for (const shot of plan) {
  const context = await browser.newContext({ viewport: { width: shot.viewport.width, height: shot.viewport.height }, colorScheme: shot.scheme, reducedMotion: 'reduce' })
  const page = await context.newPage()
  await page.goto(`${await baseOf(shot)}${shot.url}`)
  await page.locator('app-overlays').waitFor({ state: 'attached' })
  await page.locator('h1').waitFor({ state: 'attached' })
  if (shot.ready) await page.locator(shot.ready).first().waitFor({ state: 'attached' })
  await page.waitForTimeout(shot.ready ? 1200 : 400)
  for (const step of shot.steps ?? []) await runStep(page, step)
  for (const key of shot.keys ?? []) {
    if (key.startsWith('type:')) await page.getByRole('combobox').fill(key.slice('type:'.length))
    else await page.keyboard.press(key)
  }
  if (shot.click) await page.locator(shot.click).click()
  // The charts draw when their section nears the viewport, and a canvas is cleared when the page is resized for a
  // full-page photograph: so the viewport is made as tall as the page, the charts are waited for there, and the photograph is plain.
  if (shot.scrollEnd) {
    const height = await page.evaluate(() => document.documentElement.scrollHeight)
    await page.setViewportSize({ width: shot.viewport.width, height })
    // Until the charts have been drawn, or four seconds: a fleet without throughput never has any.
    await page.waitForFunction(() => document.querySelector('.home-lazy-slot') === null, undefined, { timeout: 4000 }).catch(() => undefined)
    await page.waitForTimeout(800)
  }
  await page.waitForTimeout(150)
  await page.screenshot({ path: resolve(directory, shot.file), fullPage: shot.fullPage === true && !shot.scrollEnd })
  await context.close()
  console.log(shot.file)
}

await browser.close()
for (const server of servers.values()) server.close()
console.log(`${plan.length} screenshots in ${resolve(directory)}`)
