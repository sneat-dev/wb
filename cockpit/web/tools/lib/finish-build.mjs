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
import { existsSync, readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs'
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

const ENTRY_SCRIPTS = /<script\b[^>]*\bsrc="([^"]+\.m?js)"/g
const PRELOADS = /<link\b[^>]*\brel="modulepreload"[^>]*\bhref="([^"]+\.m?js)"/g
// A static import or re-export of a sibling chunk, in minified output; a dynamic
// `import("./x.js")` has a parenthesis and is not matched.
const STATIC_IMPORT = /(?:\bfrom|\bimport)\s*["']\.\/([^"']+\.m?js)["']/g

// The JavaScript files the entry document loads at once: its scripts and
// preloads, and every chunk those import statically, as paths relative to dist.
export function initialScripts(dist) {
  const html = readFileSync(join(dist, 'index.html'), 'utf8')
  const pending = [...html.matchAll(ENTRY_SCRIPTS), ...html.matchAll(PRELOADS)].map((match) => basename(match[1]))
  const found = new Set()
  while (pending.length > 0) {
    const name = pending.pop()
    if (found.has(name) || !existsSync(join(dist, name))) continue
    found.add(name)
    for (const match of readFileSync(join(dist, name), 'utf8').matchAll(STATIC_IMPORT)) pending.push(basename(match[1]))
  }
  return [...found].sort()
}

const kilobytes = (bytes) => `${(bytes / 1000).toFixed(2)} kB`

// Checks the initial JavaScript against the budget. Returns the files with
// their sizes, the total, and the budget.
export function initialScriptSize(dist) {
  const files = initialScripts(dist).map((name) => ({ name, bytes: statSync(join(dist, name)).size }))
  return { files, total: files.reduce((sum, file) => sum + file.bytes, 0), budget: INITIAL_SCRIPT_BUDGET }
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
  const size = initialScriptSize(dist)
  const detail = size.files.map((file) => `${file.name} ${kilobytes(file.bytes)}`).join(', ')
  report(`initial JavaScript ${kilobytes(size.total)} of ${kilobytes(size.budget)} (${detail})`)
  if (size.total > size.budget) {
    log(`cockpit/web initial JavaScript is ${kilobytes(size.total)}, over the budget of ${kilobytes(size.budget)} (${detail}); load the page code lazily and import PrimeNG components per component`)
    return 1
  }
  precompress(dist)
  writeFileSync(join(dist, '.gitkeep'), '')
  return 0
}
