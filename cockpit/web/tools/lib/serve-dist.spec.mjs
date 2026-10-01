import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import { createHandler, policy } from './serve-dist.mjs'

let dist
let server
let base

beforeAll(async () => {
  dist = mkdtempSync(join(tmpdir(), 'serve-dist-'))
  mkdirSync(join(dist, 'sub'))
  const files = {
    'index.html': '<app-root ngCspNonce="__CSP_NONCE__"></app-root>',
    'main.js': 'export {}',
    'styles.css': ':root{}',
    'data.json': '{}',
    'main.js.map': '{}',
    'logo.svg': '<svg/>',
    'a.woff2': 'f',
    'a.woff': 'f',
    'a.png': 'p',
    'favicon.ico': 'i',
    'blob.bin': 'b',
    '.gitkeep': '',
  }
  for (const [name, text] of Object.entries(files)) writeFileSync(join(dist, name), text)
  server = createServer(createHandler(dist))
  await new Promise((done) => server.listen(0, '127.0.0.1', done))
  base = `http://127.0.0.1:${server.address().port}`
})

afterAll(async () => {
  await new Promise((done) => server.close(done))
  rmSync(dist, { recursive: true, force: true })
})

const get = (path, init) => fetch(base + path, init)

describe('serve-dist', () => {
  it('serves each known content type', async () => {
    const want = {
      'main.js': 'text/javascript; charset=utf-8',
      'styles.css': 'text/css; charset=utf-8',
      'data.json': 'application/json',
      'main.js.map': 'application/json',
      'logo.svg': 'image/svg+xml',
      'a.woff2': 'font/woff2',
      'a.woff': 'font/woff',
      'a.png': 'image/png',
      'favicon.ico': 'image/vnd.microsoft.icon',
      'blob.bin': 'application/octet-stream',
      'index.html': 'text/html; charset=utf-8',
    }
    for (const [name, type] of Object.entries(want)) {
      const response = await get(`/cockpit/${name}`)
      expect(response.status, name).toBe(200)
      expect(response.headers.get('content-type'), name).toBe(type)
    }
  })

  it('answers only GET and HEAD', async () => {
    const post = await get('/cockpit/', { method: 'POST' })
    expect(post.status).toBe(405)
    expect(post.headers.get('allow')).toBe('GET, HEAD')
    expect(post.headers.get('content-security-policy')).toContain("script-src 'self'")
    const head = await get('/cockpit/', { method: 'HEAD' })
    expect(head.status).toBe(200)
    expect(await head.text()).toBe('')
  })

  it('is 404 for a missing path with a known extension and for .gitkeep', async () => {
    for (const path of ['/cockpit/missing.js', '/cockpit/sub/none.png', '/cockpit/.gitkeep', '/cockpit/sub/.gitkeep']) {
      expect((await get(path)).status, path).toBe(404)
    }
  })

  it('falls back to the entry document for any other missing path', async () => {
    for (const path of ['/cockpit/', '/cockpit/fleet/alpha', '/cockpit/repo/v1.2', '/cockpit/a.b/c', '/cockpit/sub', '/cockpit/x.txt']) {
      const response = await get(path)
      expect(response.status, path).toBe(200)
      expect(response.headers.get('content-type'), path).toBe('text/html; charset=utf-8')
      expect(response.headers.get('cache-control'), path).toBe('no-cache')
      expect(await response.text(), path).toContain('<app-root')
    }
    expect((await get('/cockpit/main.js')).headers.get('cache-control')).toBeNull()
  })

  it('does not leave the dist directory', async () => {
    expect((await get('/cockpit/..%2f..%2fetc%2fpasswd.js')).status).toBe(404)
    const escaped = await get('/cockpit/..%2f..%2fetc%2fpasswd')
    expect(await escaped.text()).toContain('<app-root')
  })

  it('is 404 outside /cockpit/', async () => {
    expect((await get('/other')).status).toBe(404)
  })

  it('issues a fresh nonce per response, in the policy and the entry document', async () => {
    const seen = new Set()
    for (let i = 0; i < 3; i++) {
      const response = await get('/cockpit/')
      const nonce = /'nonce-([0-9a-f]+)'/.exec(response.headers.get('content-security-policy'))[1]
      expect(response.headers.get('content-security-policy')).toBe(policy(nonce))
      expect(await response.text()).toBe(`<app-root ngCspNonce="${nonce}"></app-root>`)
      expect(seen.has(nonce)).toBe(false)
      seen.add(nonce)
    }
  })
})
