import { existsSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, expect, it } from 'vitest'
import { NONCE_PLACEHOLDER, finishBuild } from './finish-build.mjs'

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
