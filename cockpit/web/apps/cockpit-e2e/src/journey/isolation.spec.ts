import { createServer } from 'node:net'
import { describe, expect, it, vi } from 'vitest'
import { assertInside, assertSafePort, DEFAULT_DAEMON_PORT, freePort, isolatedEnv, portInUse, portInUseSync, recordedLivePid } from './isolation'

describe('assertSafePort', () => {
  it('refuses the default daemon port and unusable ports', () => {
    expect(() => assertSafePort(DEFAULT_DAEMON_PORT)).toThrow('default daemon port')
    expect(() => assertSafePort(0)).toThrow('usable')
    expect(() => assertSafePort(70000)).toThrow('usable')
    expect(() => assertSafePort(1.5)).toThrow('usable')
    expect(() => assertSafePort(43000)).not.toThrow()
  })
})

describe('assertInside', () => {
  it('accepts only paths strictly under the root', () => {
    expect(() => assertInside('/tmp/j/projects/.wb/x.sock', '/tmp/j')).not.toThrow()
    expect(() => assertInside('/tmp/j', '/tmp/j')).toThrow('not inside')
    expect(() => assertInside('/tmp/other/x', '/tmp/j')).toThrow('not inside')
    expect(() => assertInside('/tmp/j/../x', '/tmp/j')).toThrow('not inside')
  })
})

describe('isolatedEnv', () => {
  it('drops everything it does not name and points every location under the temporary directory', () => {
    const env = isolatedEnv('/tmp/j', { PATH: '/bin', LANG: 'C', WB_HOME: '/real', WB_PROJECTS_ROOT: '/real', HOME: '/Users/real', XDG_CONFIG_HOME: '/real', GH_TOKEN: 'secret', EMPTY: '' })
    expect(env['PATH']).toBe('/bin')
    expect(env['LANG']).toBe('C')
    expect(env['WB_HOME']).toBeUndefined()
    expect(env['GH_TOKEN']).toBeUndefined()
    for (const name of ['HOME', 'XDG_CONFIG_HOME', 'XDG_STATE_HOME', 'XDG_CACHE_HOME', 'XDG_DATA_HOME', 'TMPDIR', 'WB_PROJECTS_ROOT']) {
      expect(env[name]).toMatch(/^\/tmp\/j\//)
    }
    expect(Object.keys(env).filter((name) => name.startsWith('WB_'))).toEqual(['WB_PROJECTS_ROOT'])
  })

  it('tolerates a missing PATH', () => {
    expect(isolatedEnv('/tmp/j', {})['PATH']).toBeUndefined()
  })
})

describe('freePort and portInUse', () => {
  it('returns a free port that is not the default and reports a busy one', async () => {
    const port = await freePort()
    expect(port).not.toBe(DEFAULT_DAEMON_PORT)
    expect(await portInUse(port)).toBe(false)
    const holder = createServer()
    await new Promise<void>((done) => holder.listen(port, '127.0.0.1', done))
    expect(await portInUse(port)).toBe(true)
    await new Promise((done) => holder.close(done))
  })

  it('never settles on the default port and gives up after its attempts', async () => {
    const real = createServer
    const addresses = [DEFAULT_DAEMON_PORT, 43111]
    vi.resetModules()
    vi.doMock('node:net', () => ({
      createServer: () => {
        const server = real()
        const port = addresses.shift() ?? DEFAULT_DAEMON_PORT
        server.address = (() => ({ port })) as never
        return server
      },
    }))
    const mocked = await import('./isolation')
    expect(await mocked.freePort()).toBe(43111)
    await expect(mocked.freePort(1)).rejects.toThrow('no free port')
    vi.doUnmock('node:net')
  })

  it('gives up when it is allowed no attempts', async () => {
    await expect(freePort.call(null, 0)).rejects.toThrow('no free port')
  })
})

describe('portInUseSync and recordedLivePid', () => {
  it('sees a busy port and a free one', async () => {
    const port = await freePort()
    expect(portInUseSync(port)).toBe(false)
    const holder = createServer()
    await new Promise<void>((done) => holder.listen(port, '127.0.0.1', done))
    expect(portInUseSync(port)).toBe(true)
    await new Promise((done) => holder.close(done))
  })

  it('offers a pid only for a recorded, not stopped daemon whose port is still busy', () => {
    expect(recordedLivePid('{"pid":4242,"status":"ready"}', true)).toBe(4242)
    expect(recordedLivePid('{"pid":4242,"status":"starting"}', true)).toBe(4242)
    expect(recordedLivePid('{"pid":4242,"status":"ready"}', false)).toBeUndefined()
    expect(recordedLivePid('{"pid":4242,"status":"stopped"}', true)).toBeUndefined()
    expect(recordedLivePid('{"pid":1,"status":"ready"}', true)).toBeUndefined()
    expect(recordedLivePid('{"pid":"4242","status":"ready"}', true)).toBeUndefined()
    expect(recordedLivePid('{"pid":4242}', true)).toBeUndefined()
    expect(recordedLivePid('not json', true)).toBeUndefined()
  })
})
