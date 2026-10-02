// Runs after the Angular build. The build empties dist/, which removes the
// tracked dist/.gitkeep placeholder that lets go:embed compile in a clean
// clone, so put it back. It fails when the build emitted no entry document
// (cockpit/web/embed.go treats dist/index.html as the "built" marker, so
// without it wb would embed a placeholder and serve the "not built" page) or
// when that document lost the __CSP_NONCE__ placeholder, which embed.go
// replaces with the per-response style nonce.
//
// It then checks the first-page JavaScript budget of every route
// (cockpit-views#req:initial-script-size, tools/lib/route-budgets.mjs): the scripts
// the entry document loads at once with the chunks they import statically, plus the
// route's own page, counted raw over JavaScript files only, at most 350 kB for Home and
// 500 kB for every other route. It prints the table of them and fails the build
// when a route is over its budget.
//
// It then compresses every text asset once, beside the original as `<name>.gz`
// (cockpit-views#req:compressed-responses): the daemon serves that file when a
// client accepts gzip, so no asset is compressed per request. The entry
// document is not compressed here, because the daemon substitutes a nonce into
// it per response and compresses that response itself.
import { existsSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { extname, join, relative } from 'node:path'
import { gzipSync } from 'node:zlib'
import { budgetTable, overBudget, routeBudgets } from './route-budgets.mjs'

export { firstPageScripts, initialScripts } from './script-graph.mjs'
export { FIRST_PAGE_ENTRY, HOME_BUDGET, ROUTE_BUDGET } from './route-budgets.mjs'

export const NONCE_PLACEHOLDER = '__CSP_NONCE__'

// The extensions worth compressing: text. Fonts and images are already
// compressed, and a source map is never served to a page.
export const COMPRESSIBLE = new Set(['.js', '.mjs', '.css', '.json', '.svg'])

// Writes `<file>.gz` beside every compressible asset under dist, except the
// entry document, and returns how many it wrote. A file the compressor does not
// make smaller is left alone: the daemon then serves the original.
export function precompress(dist) {
  let written = 0
  for (const entry of readdirSync(dist, { recursive: true, withFileTypes: true })) {
    const path = join(entry.parentPath, entry.name)
    if (!entry.isFile() || !COMPRESSIBLE.has(extname(entry.name)) || relative(dist, path) === 'index.html') {
      continue
    }
    const original = readFileSync(path)
    const compressed = gzipSync(original, { level: 9 })
    if (compressed.length < original.length) {
      writeFileSync(`${path}.gz`, compressed)
      written++
    }
  }
  return written
}

// What only the gallery of the preview build contains: its component's selector and the marker
// its fixtures carry (apps/cockpit/src/app/gallery/gallery-data.ts). The production build must
// hold neither, so the gallery is provably not in the binary.
export const GALLERY_MARKERS = ['app-gallery', 'cockpit-gallery-fixture']

// The files directly under dist that hold a gallery marker.
export function galleryLeaks(dist) {
  return readdirSync(dist, { withFileTypes: true })
    .filter((entry) => entry.isFile() && entry.name.endsWith('.js'))
    .map((entry) => entry.name)
    .filter((name) => {
      const text = readFileSync(join(dist, name), 'utf8')
      return GALLERY_MARKERS.some((marker) => text.includes(marker))
    })
}

/**
 * `routesSource` is the text of apps/cockpit/src/app/app.routes.ts, whose lazy page routes are the routes measured.
 */
export function finishBuild(dist, log, report, routesSource) {
  const index = join(dist, 'index.html')
  if (!existsSync(index)) {
    log('cockpit/web build emitted no dist/index.html')
    return 1
  }
  if (!readFileSync(index, 'utf8').includes(NONCE_PLACEHOLDER)) {
    log(`cockpit/web dist/index.html does not contain the ${NONCE_PLACEHOLDER} placeholder, so the daemon cannot issue a style nonce`)
    return 1
  }
  const leaks = galleryLeaks(dist)
  if (leaks.length > 0) {
    log(`cockpit/web the gallery of the preview build is in the production build (${leaks.join(', ')}): it must stay behind gallery/gallery-routes.ts`)
    return 1
  }
  const size = routeBudgets(dist, routesSource)
  if (size.problems.length > 0) {
    log(`cockpit/web cannot check the JavaScript budgets: ${size.problems.join('; ')}`)
    return 1
  }
  for (const line of budgetTable(size)) report(line)
  const over = overBudget(size)
  if (over.length > 0) {
    for (const line of over) log(`cockpit/web ${line}`)
    return 1
  }
  rmSync(join(dist, 'stats.json'), { force: true })
  precompress(dist)
  writeFileSync(join(dist, '.gitkeep'), '')
  return 0
}
