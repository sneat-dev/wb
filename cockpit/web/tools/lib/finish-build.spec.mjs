import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, expect, it } from 'vitest'
import { gunzipSync } from 'node:zlib'
import { readFileSync } from 'node:fs'
import { INITIAL_SCRIPT_BUDGET, NONCE_PLACEHOLDER, finishBuild, initialScriptSize, initialScripts, precompress } from './finish-build.mjs'

let dist

beforeEach(() => {
  dist = mkdtempSync(join(tmpdir(), 'finish-build-'))
})

afterEach(() => {
  rmSync(dist, { recursive: true, force: true })
})

it('restores the placeholder file and succeeds for a good build', () => {
  writeFileSync(join(dist, 'index.html'), `<app-root ngCspNonce="${NONCE_PLACEHOLDER}">`)
  const lines = []
  expect(finishBuild(dist, (line) => lines.push(line))).toBe(0)
  expect(existsSync(join(dist, '.gitkeep'))).toBe(true)
  expect(lines).toEqual([])
})

it('fails naming the entry document when the build emitted none', () => {
  mkdirSync(dist, { recursive: true })
  const lines = []
  expect(finishBuild(dist, (line) => lines.push(line))).toBe(1)
  expect(lines.join('')).toContain('dist/index.html')
})

it('fails naming the placeholder when the entry document lost it', () => {
  writeFileSync(join(dist, 'index.html'), '<app-root></app-root>')
  const lines = []
  expect(finishBuild(dist, (line) => lines.push(line))).toBe(1)
  expect(lines.join('')).toContain('__CSP_NONCE__')
  expect(existsSync(join(dist, '.gitkeep'))).toBe(false)
})

it('compresses every text asset beside the original and leaves the rest', () => {
  const script = 'export const answer = 42\n'.repeat(50)
  mkdirSync(join(dist, 'media'), { recursive: true })
  writeFileSync(join(dist, 'index.html'), `<app-root ngCspNonce="${NONCE_PLACEHOLDER}">`.repeat(40))
  writeFileSync(join(dist, 'main-ABCDEF12.js'), script)
  writeFileSync(join(dist, 'media', 'logo.svg'), '<svg>'.repeat(200))
  writeFileSync(join(dist, 'font.woff2'), 'binary'.repeat(200))
  writeFileSync(join(dist, 'main-ABCDEF12.js.map'), script)
  mkdirSync(join(dist, 'folder.js'))
  expect(finishBuild(dist, () => {})).toBe(0)
  expect(gunzipSync(readFileSync(join(dist, 'main-ABCDEF12.js.gz'))).toString()).toBe(script)
  expect(existsSync(join(dist, 'media', 'logo.svg.gz'))).toBe(true)
  for (const skipped of ['index.html.gz', 'font.woff2.gz', 'main-ABCDEF12.js.map.gz', 'folder.js.gz']) {
    expect(existsSync(join(dist, skipped))).toBe(false)
  }
})

it('writes no compressed file that is not smaller than its original', () => {
  writeFileSync(join(dist, 'tiny.js'), 'x')
  expect(precompress(dist)).toBe(0)
  expect(existsSync(join(dist, 'tiny.js.gz'))).toBe(false)
})

it('counts the files it compressed', () => {
  writeFileSync(join(dist, 'a.css'), 'a{color:red}'.repeat(40))
  writeFileSync(join(dist, 'b.json'), JSON.stringify({ k: 'v'.repeat(300) }))
  expect(precompress(dist)).toBe(2)
})

// cockpit-views#ac:initial-script-fits-the-budget
function writeApplication({ mainBytes = 1000, chunkBytes = 2000, lazyBytes = 900_000 } = {}) {
  writeFileSync(
    join(dist, 'index.html'),
    `<link rel="stylesheet" href="styles-ABC.css"><app-root ngCspNonce="${NONCE_PLACEHOLDER}"></app-root><link rel="modulepreload" href="chunk-SHARED.js"><script src="main-ABC.js" type="module" nonce="${NONCE_PLACEHOLDER}"></script>`,
  )
  // main imports a shared chunk statically and a lazy one dynamically; the shared one re-exports a third.
  writeFileSync(join(dist, 'main-ABC.js'), `import{a as b}from"./chunk-SHARED.js";import"./chunk-SIDE.js";const lazy=()=>import("./chunk-LAZY.js");${'x'.repeat(mainBytes)}`)
  writeFileSync(join(dist, 'chunk-SHARED.js'), `export{c}from"./chunk-DEEP.js";${'y'.repeat(chunkBytes)}`)
  writeFileSync(join(dist, 'chunk-SIDE.js'), 'export{}')
  writeFileSync(join(dist, 'chunk-DEEP.js'), 'export const c=1')
  writeFileSync(join(dist, 'chunk-LAZY.js'), 'z'.repeat(lazyBytes))
  writeFileSync(join(dist, 'styles-ABC.css'), 'a{color:red}'.repeat(100_000))
}

it('counts the scripts the entry document loads, the preloads and what they import statically, and no lazy chunk or style', () => {
  writeApplication()
  expect(initialScripts(dist)).toEqual(['chunk-DEEP.js', 'chunk-SHARED.js', 'chunk-SIDE.js', 'main-ABC.js'])
  const size = initialScriptSize(dist)
  expect(size.files.map((file) => file.name)).toEqual(['chunk-DEEP.js', 'chunk-SHARED.js', 'chunk-SIDE.js', 'main-ABC.js'])
  expect(size.total).toBe(size.files.reduce((sum, file) => sum + file.bytes, 0))
  expect(size.total).toBeLessThan(5000)
  expect(size.budget).toBe(INITIAL_SCRIPT_BUDGET)
  expect(INITIAL_SCRIPT_BUDGET).toBe(350_000)
})

it('ignores a script the document names but the build did not emit', () => {
  writeFileSync(join(dist, 'index.html'), `<app-root ngCspNonce="${NONCE_PLACEHOLDER}"></app-root><script src="main-GONE.js" type="module"></script>`)
  expect(initialScripts(dist)).toEqual([])
})

it('reports the size and succeeds within the budget', () => {
  writeApplication()
  const lines = []
  const failures = []
  expect(finishBuild(dist, (line) => failures.push(line), (line) => lines.push(line))).toBe(0)
  expect(failures).toEqual([])
  expect(lines).toHaveLength(1)
  expect(lines[0]).toMatch(/^initial JavaScript \d+\.\d\d kB of 350\.00 kB \(chunk-DEEP\.js .*main-ABC\.js/)
  expect(existsSync(join(dist, 'main-ABC.js.gz'))).toBe(true)
})

it('fails the build, naming the files, when the initial JavaScript is over the budget', () => {
  writeApplication({ mainBytes: INITIAL_SCRIPT_BUDGET })
  const failures = []
  expect(finishBuild(dist, (line) => failures.push(line))).toBe(1)
  expect(failures.join('')).toContain('over the budget of 350.00 kB')
  expect(failures.join('')).toContain('main-ABC.js')
  expect(existsSync(join(dist, '.gitkeep'))).toBe(false)
})

it('passes at exactly the budget', () => {
  writeApplication({ mainBytes: 0, chunkBytes: 0 })
  const base = initialScriptSize(dist).total
  writeApplication({ mainBytes: INITIAL_SCRIPT_BUDGET - base, chunkBytes: 0 })
  expect(initialScriptSize(dist).total).toBe(INITIAL_SCRIPT_BUDGET)
  expect(finishBuild(dist, () => {})).toBe(0)
})
