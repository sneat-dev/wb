import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, expect, it } from 'vitest'
import { gunzipSync } from 'node:zlib'
import { readFileSync } from 'node:fs'
import { NONCE_PLACEHOLDER, finishBuild, precompress } from './finish-build.mjs'

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
