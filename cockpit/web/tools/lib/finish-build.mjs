// Runs after the Angular build. The build empties dist/, which removes the
// tracked dist/.gitkeep placeholder that lets go:embed compile in a clean
// clone, so put it back. It fails when the build emitted no entry document
// (cockpit/web/embed.go treats dist/index.html as the "built" marker, so
// without it wb would embed a placeholder and serve the "not built" page) or
// when that document lost the __CSP_NONCE__ placeholder, which embed.go
// replaces with the per-response style nonce.
//
// It then compresses every text asset once, beside the original as `<name>.gz`
// (cockpit-views#req:compressed-responses): the daemon serves that file when a
// client accepts gzip, so no asset is compressed per request. The entry
// document is not compressed here, because the daemon substitutes a nonce into
// it per response and compresses that response itself.
import { existsSync, readFileSync, readdirSync, writeFileSync } from 'node:fs'
import { extname, join, relative } from 'node:path'
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

export function finishBuild(dist, log) {
  const index = join(dist, 'index.html')
  if (!existsSync(index)) {
    log('cockpit/web build emitted no dist/index.html')
    return 1
  }
  if (!readFileSync(index, 'utf8').includes(NONCE_PLACEHOLDER)) {
    log(`cockpit/web dist/index.html does not contain the ${NONCE_PLACEHOLDER} placeholder, so the daemon cannot issue a style nonce`)
    return 1
  }
  precompress(dist)
  writeFileSync(join(dist, '.gitkeep'), '')
  return 0
}
