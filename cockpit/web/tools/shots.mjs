// CLI entry: COCKPIT_SHOTS_DIR=<dir> node tools/shots.mjs, run from cockpit/web
// after `pnpm build`. Serves the build with the fixtures (tools/lib/preview-fixture.mjs,
// on COCKPIT_PREVIEW_PORT when set, else a free loopback port) and writes a
// screenshot of every route, in light and dark at 1440x900 and 390x844, and of
// the palette, the shortcut sheet and the warming-up and schema-mismatch states.
import { mkdirSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { chromium } from '@playwright/test'
import { loadFixture } from './fixtures.mjs'
import { previewPort, startPreview } from './lib/preview-fixture.mjs'
import { shotPlan } from './lib/shot-plan.mjs'

const directory = process.env.COCKPIT_SHOTS_DIR
if (!directory) throw new Error('shots: set COCKPIT_SHOTS_DIR to the directory the screenshots go to')
mkdirSync(directory, { recursive: true })

const data = await loadFixture()
const plan = shotPlan(data.document)
const port = process.env.COCKPIT_PREVIEW_PORT ? previewPort(process.env) : 0
const browser = await chromium.launch()
const servers = new Map()

async function baseOf(state) {
  if (!servers.has(state)) {
    // The port from the environment serves the first state; the others take a free one.
    servers.set(state, await startPreview({ distRoot: join(process.cwd(), 'dist'), data, state }, servers.size === 0 ? port : 0))
  }
  return `http://127.0.0.1:${servers.get(state).address().port}/cockpit`
}

for (const shot of plan) {
  const context = await browser.newContext({ viewport: { width: shot.viewport.width, height: shot.viewport.height }, colorScheme: shot.scheme, reducedMotion: 'reduce' })
  const page = await context.newPage()
  await page.goto(`${await baseOf(shot.state)}${shot.url}`)
  await page.locator('app-overlays').waitFor({ state: 'attached' })
  await page.locator('h1').waitFor({ state: 'attached' })
  await page.waitForTimeout(400)
  for (const key of shot.keys ?? []) {
    if (key.startsWith('type:')) await page.getByRole('combobox').fill(key.slice('type:'.length))
    else await page.keyboard.press(key)
  }
  await page.waitForTimeout(150)
  await page.screenshot({ path: resolve(directory, shot.file) })
  await context.close()
  console.log(shot.file)
}

await browser.close()
for (const server of servers.values()) server.close()
console.log(`${plan.length} screenshots in ${resolve(directory)}`)
