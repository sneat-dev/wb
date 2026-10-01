import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { REFUSED_PORTS, STATES, createPreviewHandler, documentFor, previewPort, shiftTimes, startPreview } from './preview-fixture.mjs'

describe('previewPort', () => {
  it('takes the port from the environment', () => {
    expect(previewPort({ COCKPIT_PREVIEW_PORT: '4347' })).toBe(4347)
  })

  it('wants one', () => {
    expect(() => previewPort({})).toThrow('COCKPIT_PREVIEW_PORT')
    expect(() => previewPort({ COCKPIT_PREVIEW_PORT: '' })).toThrow('COCKPIT_PREVIEW_PORT')
  })

  it('refuses what is not an unprivileged port', () => {
    for (const bad of ['abc', '80', '1023', '65536', '4347.5']) expect(() => previewPort({ COCKPIT_PREVIEW_PORT: bad }), bad).toThrow('from 1024 to 65535')
  })

  it('never serves on the daemon port or the end-to-end port', () => {
    expect(REFUSED_PORTS).toEqual([8766, 4300])
    for (const port of REFUSED_PORTS) expect(() => previewPort({ COCKPIT_PREVIEW_PORT: String(port) })).toThrow('refused')
  })
})

describe('shiftTimes', () => {
  it('moves times and dates, and leaves everything else', () => {
    const shifted = shiftTimes({ at: '2026-10-01T10:00:00Z', ms: '2026-10-01T10:00:00.250Z', day: '2026-10-01', list: ['2026-10-01T10:00:00Z', 5, null, true], name: 'acme/web' }, 2 * 86_400_000 + 3600_000)
    expect(shifted).toEqual({ at: '2026-10-03T11:00:00.000Z', ms: '2026-10-03T11:00:00.250Z', day: '2026-10-03', list: ['2026-10-03T11:00:00.000Z', 5, null, true], name: 'acme/web' })
  })
})

const data = {
  document: { schema_version: 2, snapshot_at: '2026-10-01T10:00:00Z', warming_up: false, repositories_total: 500, repositories_scanned: 500, repositories: Array.from({ length: 200 }, (_, i) => ({ id: `r${i}`, seen: '2026-10-01T09:00:00Z' })), worktrees: Array.from({ length: 100 }, (_, i) => ({ id: `w${i}` })) },
  metrics: new Map([['m1', { machine: 'm1', route: 'local', samples: [{ sampled_at: '2026-10-01T10:00:00Z' }] }]]),
  branches: new Map([['r1', [{ name: 'main', last_activity_at: '2026-10-01T09:00:00Z' }]]]),
}

describe('documentFor', () => {
  it('shows the other states of the daemon', () => {
    expect(STATES).toEqual(['ok', 'warming', 'daemon-older', 'page-older'])
    expect(documentFor(data.document, 'ok')).toBe(data.document)
    const warming = documentFor(data.document, 'warming')
    expect([warming.warming_up, warming.repositories_scanned, warming.repositories_total, warming.repositories.length, warming.worktrees.length]).toEqual([true, 120, 438, 120, 60])
    expect(documentFor(data.document, 'daemon-older').schema_version).toBe(1)
    expect(documentFor(data.document, 'page-older').schema_version).toBe(3)
  })
})

describe('the preview server', () => {
  let dist
  let server
  const NOW = Date.parse('2026-10-02T12:00:00Z')

  beforeEach(() => {
    dist = mkdtempSync(join(tmpdir(), 'preview-'))
    writeFileSync(join(dist, 'index.html'), '<app-root ngCspNonce="__CSP_NONCE__"></app-root>')
  })

  afterEach(async () => {
    await new Promise((done) => (server ? server.close(done) : done()))
    server = undefined
    rmSync(dist, { recursive: true, force: true })
  })

  async function open(options = {}) {
    server = await startPreview({ distRoot: dist, data, clock: () => NOW, ...options }, 0)
    const { address, port } = server.address()
    expect(address).toBe('127.0.0.1')
    return (path, init) => fetch(`http://127.0.0.1:${port}${path}`, init)
  }

  it('serves the application under /cockpit/ and the API from the fixtures, with the snapshot just taken', async () => {
    const get = await open()
    const page = await get('/cockpit/')
    expect(page.status).toBe(200)
    expect(await page.text()).toContain('ngCspNonce=')
    expect(page.headers.get('content-security-policy')).toContain("script-src 'self'")

    const fleet = await (await get('/api/v1/cockpit/fleet')).json()
    expect(fleet.snapshot_at).toBe(new Date(NOW - 8000).toISOString())
    expect(fleet.repositories[0].seen).toBe(new Date(NOW - 8000 - 3600_000).toISOString())
    expect((await (await get('/api/v1/cockpit/session')).json()).principal).toBe('anonymous-local')
    expect((await (await get('/api/v1/cockpit/machine-metrics?machine=m1')).json()).samples[0].sampled_at).toBe(new Date(NOW - 8000).toISOString())
    expect((await (await get('/api/v1/cockpit/branches?repository=r1')).json()).branches[0].name).toBe('main')
  })

  it('says a machine reports no metrics, and a repository has no branches', async () => {
    const get = await open()
    expect(await (await get('/api/v1/cockpit/machine-metrics?machine=nobody')).json()).toEqual({ machine: 'nobody', route: 'none', samples: [], reason: 'not_reported' })
    expect(await (await get('/api/v1/cockpit/machine-metrics')).json()).toMatchObject({ route: 'none' })
    expect(await (await get('/api/v1/cockpit/branches?repository=none')).json()).toEqual({ branches: [] })
    expect(await (await get('/api/v1/cockpit/branches')).json()).toEqual({ branches: [] })
  })

  it('serves an owner session and the other states when asked', async () => {
    const get = await open({ session: 'owner', state: 'warming' })
    expect((await (await get('/api/v1/cockpit/session')).json()).principal).toBe('owner')
    expect((await (await get('/api/v1/cockpit/fleet')).json()).warming_up).toBe(true)
  })

  it('falls back to an anonymous session for a session it does not know', async () => {
    const get = await open({ session: 'nobody' })
    expect((await (await get('/api/v1/cockpit/session')).json()).principal).toBe('anonymous-local')
  })

  it('reads the clock when none is given, and answers only what the application reads', async () => {
    server = await startPreview({ distRoot: dist, data, snapshotAgeMs: 0 }, 0)
    const { port } = server.address()
    const fleet = await (await fetch(`http://127.0.0.1:${port}/api/v1/cockpit/fleet`)).json()
    expect(Math.abs(Date.parse(fleet.snapshot_at) - Date.now())).toBeLessThan(5000)
    expect((await fetch(`http://127.0.0.1:${port}/api/v1/cockpit/actions`)).status).toBe(404)
    expect((await fetch(`http://127.0.0.1:${port}/api/v1/cockpit/fleet`, { method: 'POST' })).status).toBe(405)
    expect((await fetch(`http://127.0.0.1:${port}/elsewhere`)).status).toBe(404)
  })

  it('reports a port that is taken', async () => {
    server = await startPreview({ distRoot: dist, data }, 0)
    const { port } = server.address()
    await expect(startPreview({ distRoot: dist, data }, port)).rejects.toThrow()
  })

  it('creates a handler without listening', () => {
    expect(typeof createPreviewHandler({ distRoot: dist, data })).toBe('function')
  })
})
