// Runs after the Angular build. The build empties dist/, which removes the
// tracked dist/.gitkeep placeholder that lets go:embed compile in a clean
// clone, so put it back. It fails when the build emitted no entry document
// (cockpit/web/embed.go treats dist/index.html as the "built" marker, so
// without it wb would embed a placeholder and serve the "not built" page) or
// when that document lost the __CSP_NONCE__ placeholder, which embed.go
// replaces with the per-response style nonce.
//
// It then checks the initial JavaScript budget (cockpit-views#req:initial-script-size):
// the scripts the entry document loads at once, with the chunks they import
// statically, counted over JavaScript files only (styles, fonts and the lazy
// chunks of the routes are not part of it), must not exceed INITIAL_SCRIPT_BUDGET
// raw bytes. The build fails when they do.
//
// It then compresses every text asset once, beside the original as `<name>.gz`
// (cockpit-views#req:compressed-responses): the daemon serves that file when a
// client accepts gzip, so no asset is compressed per request. The entry
// document is not compressed here, because the daemon substitutes a nonce into
// it per response and compresses that response itself.
import { existsSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { basename, extname, join, relative } from 'node:path'
import { gzipSync } from 'node:zlib'

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

// 350 kB raw, in the kilobytes the Angular build report uses (1,000 bytes).
export const INITIAL_SCRIPT_BUDGET = 350_000

// The page whose code the budget measures: the default route, the first thing the operator sees.
export const FIRST_PAGE_ENTRY = 'pages/home/home-page.ts'

const ENTRY_SCRIPTS = /<script\b[^>]*\bsrc="([^"]+)"/g
const PRELOADS = /<link\b[^>]*\brel="modulepreload"[^>]*\bhref="([^"]+)"/g
// A static import or re-export of a sibling chunk, in minified output; a dynamic
// `import("./x.js")` has a parenthesis and is not matched.
const STATIC_IMPORT = /(?:\bfrom|\bimport)\s*["']\.\/([^"']+)["']/g

const PLAIN_NAME = /^[A-Za-z0-9._-]+$/

// The JavaScript the entry document loads at once: its scripts and preloads and
// every chunk those import statically, as file names directly under dist. It fails
// closed: a script that is named but missing, or not a plain file name directly
// under dist, is a problem, so a build that moved or lost a file cannot pass.
export function initialScripts(dist) {
  const html = readFileSync(join(dist, 'index.html'), 'utf8')
  const problems = []
  const pending = [...html.matchAll(ENTRY_SCRIPTS), ...html.matchAll(PRELOADS)].map((match) => match[1])
  const found = new Set()
  while (pending.length > 0) {
    const name = pending.pop()
    if (found.has(name)) continue
    if (!PLAIN_NAME.test(name) || name === '.' || name === '..' || !existsSync(join(dist, name))) {
      problems.push(`${name} is named by the build but is not a file directly under dist`)
      continue
    }
    found.add(name)
    for (const match of readFileSync(join(dist, name), 'utf8').matchAll(STATIC_IMPORT)) pending.push(match[1])
  }
  if (found.size === 0) problems.push('dist/index.html loads no script')
  return { files: [...found].sort(), problems }
}

// Closes `names` under the static imports of the build's metafile (dist/stats.json).
function staticClosure(outputs, names) {
  const found = new Set()
  const pending = [...names]
  while (pending.length > 0) {
    const name = pending.pop()
    if (found.has(name) || outputs[name] === undefined) continue
    found.add(name)
    for (const edge of outputs[name].imports ?? []) if (edge.kind === 'import-statement') pending.push(edge.path)
  }
  return found
}

// The scripts needed to render the page whose source file ends with `entry`:
// the initial scripts, the page's own chunk with what it imports statically, and
// the lazy chunks that load it (the route's own, such as the PrimeNG theme of a
// legacy page). Needs the metafile the production build writes; without it, or
// when no output is that page, it fails closed.
export function firstPageScripts(dist, initial, entry) {
  const statsPath = join(dist, 'stats.json')
  if (!existsSync(statsPath)) return { files: [], problems: ['dist/stats.json is missing: build with "statsJson": true'] }
  const { outputs } = JSON.parse(readFileSync(statsPath, 'utf8'))
  const target = Object.keys(outputs).find((name) => outputs[name].entryPoint?.endsWith(entry))
  if (target === undefined) return { files: [], problems: [`no build output is ${entry}`] }
  const loaded = staticClosure(outputs, [target])
  for (let grew = true; grew; ) {
    grew = false
    for (const [name, output] of Object.entries(outputs)) {
      if (loaded.has(name) || initial.includes(name)) continue
      if ((output.imports ?? []).some((edge) => edge.kind === 'dynamic-import' && loaded.has(edge.path))) {
        for (const member of staticClosure(outputs, [name])) loaded.add(member)
        grew = true
      }
    }
  }
  const files = [...new Set([...initial, ...[...loaded].filter((name) => name.endsWith('.js'))])].sort()
  return { files, problems: [] }
}

const kilobytes = (bytes) => `${(bytes / 1000).toFixed(2)} kB`
const sizeOf = (dist, names) => names.map((name) => ({ name, bytes: statSync(join(dist, name)).size }))
const total = (files) => files.reduce((sum, file) => sum + file.bytes, 0)

// The two numbers: the initial static graph, and the first page's. The budget is on the second.
export function scriptBudget(dist, entry = FIRST_PAGE_ENTRY) {
  const initial = initialScripts(dist)
  const problems = [...initial.problems]
  const initialFiles = sizeOf(dist, initial.files)
  let firstFiles = []
  if (problems.length === 0) {
    const first = firstPageScripts(dist, initial.files, entry)
    problems.push(...first.problems)
    firstFiles = sizeOf(dist, first.files)
  }
  return { problems, initial: { files: initialFiles, total: total(initialFiles) }, firstPage: { files: firstFiles, total: total(firstFiles) }, budget: INITIAL_SCRIPT_BUDGET }
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

export function finishBuild(dist, log, report = () => {}) {
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
  const size = scriptBudget(dist)
  if (size.problems.length > 0) {
    log(`cockpit/web cannot check the JavaScript budget: ${size.problems.join('; ')}`)
    return 1
  }
  const detail = size.firstPage.files.map((file) => `${file.name} ${kilobytes(file.bytes)}`).join(', ')
  report(`initial static JavaScript ${kilobytes(size.initial.total)}; first page (Home) ${kilobytes(size.firstPage.total)} of ${kilobytes(size.budget)} (${detail})`)
  if (size.firstPage.total > size.budget) {
    log(`cockpit/web first-page JavaScript is ${kilobytes(size.firstPage.total)}, over the budget of ${kilobytes(size.budget)} (${detail}); load page code lazily and keep PrimeNG out of the first page`)
    return 1
  }
  rmSync(join(dist, 'stats.json'), { force: true })
  precompress(dist)
  writeFileSync(join(dist, '.gitkeep'), '')
  return 0
}
