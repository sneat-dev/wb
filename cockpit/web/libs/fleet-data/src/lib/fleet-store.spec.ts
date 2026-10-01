import { TestBed } from '@angular/core/testing'
import { FleetClient, FleetRead } from './fleet-client'
import { FleetStore, POLL_INTERVALS } from './fleet-store'
import { Session } from './fleet.types'
import { fleetDocument } from './test-data'

const session: Session = { principal: 'anonymous-local', capabilities: [], code_browser_url: 'https://c.test/' }

function storeWith(client: Partial<FleetClient>): FleetStore {
  TestBed.configureTestingModule({
    providers: [
      { provide: FleetClient, useValue: client },
      { provide: POLL_INTERVALS, useValue: { warmingUp: 10, steady: 100 } },
    ],
  })
  return TestBed.inject(FleetStore)
}

const changed = (warming: boolean, etag: string): FleetRead => ({
  kind: 'changed',
  etag,
  document: fleetDocument({ warming_up: warming, repositories_scanned: 1, repositories_total: 4 }),
})

describe('FleetStore', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('loads the document and session, and exposes what the pages need', async () => {
    const store = storeWith({ readFleet: async () => changed(false, '"a"'), readSession: async () => session })
    store.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(store.loaded()).toBe(true)
    expect(store.warmingUp()).toBe(false)
    expect(store.progress()).toEqual({ scanned: 1, total: 4 })
    expect(store.codeBrowserUrl()).toBe('https://c.test/')
    expect(store.machineOptions().map((o) => o.id)).toEqual(['mach-alpha', 'mach-beta'])
    expect(store.repositoryById().get('r2')?.machine).toBe('beta')
    expect(store.repositoryName('r1')).toBe('github.com/acme/r1')
    expect(store.repositoryName('r-gone')).toBe('r-gone')
    expect(store.repositoryName(undefined)).toBe('—')
    expect(store.error()).toBeNull()
    expect(store.sessionStatus()).toBe('ready')
    expect(store.codeIndexProvider()).toBeUndefined()
    expect(store.canReadContent()).toBe(false)
    store.stop()
  })

  it('exposes the configured provider and whether the session may read content', async () => {
    const owner: Session = { principal: 'owner', capabilities: ['fleet.read', 'repo.content.read'], code_browser_url: '' }
    const withProvider: FleetRead = { kind: 'changed', etag: '"p"', document: fleetDocument({ code_index_provider: 'codegrapher' }) }
    const store = storeWith({ readFleet: async () => withProvider, readSession: async () => owner })
    store.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(store.codeIndexProvider()).toBe('codegrapher')
    expect(store.canReadContent()).toBe(true)
    store.stop()
  })

  it('polls fast while warming up, slowly after, and revalidates with the last ETag', async () => {
    const readFleet = vi
      .fn<(etag?: string) => Promise<FleetRead>>()
      .mockResolvedValueOnce(changed(true, '"a"'))
      .mockResolvedValueOnce(changed(false, '"b"'))
      .mockResolvedValueOnce({ kind: 'unchanged' })
    const store = storeWith({ readFleet, readSession: async () => session })
    store.start()
    store.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(readFleet).toHaveBeenCalledTimes(1)
    expect(store.warmingUp()).toBe(true)
    await vi.advanceTimersByTimeAsync(9)
    expect(readFleet).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(readFleet).toHaveBeenCalledTimes(2)
    expect(readFleet).toHaveBeenLastCalledWith('"a"')
    expect(store.warmingUp()).toBe(false)
    await vi.advanceTimersByTimeAsync(99)
    expect(readFleet).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(1)
    expect(readFleet).toHaveBeenCalledTimes(3)
    expect(readFleet).toHaveBeenLastCalledWith('"b"')
    expect(store.document().warming_up).toBe(false)
    store.stop()
  })

  it('keeps the last document and shows the error when a read fails, then recovers', async () => {
    const readFleet = vi
      .fn<(etag?: string) => Promise<FleetRead>>()
      .mockResolvedValueOnce(changed(false, '"a"'))
      .mockRejectedValueOnce(new Error('down'))
      .mockRejectedValueOnce('odd')
      .mockResolvedValueOnce({ kind: 'unchanged' })
    const store = storeWith({ readFleet, readSession: async () => session })
    store.start()
    await vi.advanceTimersByTimeAsync(0)
    await vi.advanceTimersByTimeAsync(100)
    expect(store.error()).toBe('down')
    expect(store.document().machines).toHaveLength(2)
    await vi.advanceTimersByTimeAsync(20)
    expect(store.error()).toBe('odd')
    await vi.advanceTimersByTimeAsync(40)
    expect(store.error()).toBeNull()
    store.stop()
  })

  it('has no code browser base when the session cannot be read', async () => {
    const store = storeWith({ readFleet: async () => ({ kind: 'unchanged' }), readSession: async () => Promise.reject(new Error('401')) })
    store.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(store.session()).toBeNull()
    expect(store.sessionStatus()).toBe('failed')
    expect(store.codeBrowserUrl()).toBeUndefined()
    store.stop()
  })

  it('stops polling, also when stopped during a read, and can start again', async () => {
    let release: (read: FleetRead) => void = () => undefined
    const readFleet = vi
      .fn<(etag?: string) => Promise<FleetRead>>()
      .mockImplementationOnce(() => new Promise<FleetRead>((resolve) => (release = resolve)))
      .mockResolvedValue({ kind: 'unchanged' })
    const store = storeWith({ readFleet, readSession: async () => session })
    store.start()
    store.stop()
    release(changed(false, '"a"'))
    await vi.advanceTimersByTimeAsync(500)
    expect(readFleet).toHaveBeenCalledTimes(1)
    store.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(readFleet).toHaveBeenCalledTimes(2)
    store.stop()
    await vi.advanceTimersByTimeAsync(500)
    expect(readFleet).toHaveBeenCalledTimes(2)
  })

  it('backs off after failures, doubling up to the slow interval, and resets on success', async () => {
    const readFleet = vi.fn<(etag?: string) => Promise<FleetRead>>().mockRejectedValue(new Error('down'))
    const store = storeWith({ readFleet, readSession: async () => session })
    store.start()
    await vi.advanceTimersByTimeAsync(0)
    const callsAfter = async (ms: number) => {
      await vi.advanceTimersByTimeAsync(ms)
      return readFleet.mock.calls.length
    }
    expect(readFleet).toHaveBeenCalledTimes(1)
    expect(await callsAfter(19)).toBe(1)
    expect(await callsAfter(1)).toBe(2)
    expect(await callsAfter(40)).toBe(3)
    expect(await callsAfter(80)).toBe(4)
    expect(await callsAfter(99)).toBe(4)
    expect(await callsAfter(1)).toBe(5)
    readFleet.mockResolvedValue(changed(false, '"x"'))
    expect(await callsAfter(100)).toBe(6)
    expect(await callsAfter(100)).toBe(7)
    store.stop()
  })

  it('retries the session read on every poll until it succeeds, one read at a time', async () => {
    let release: (value: Session) => void = () => undefined
    const readSession = vi
      .fn<() => Promise<Session>>()
      .mockRejectedValueOnce(new Error('401'))
      .mockImplementationOnce(() => new Promise<Session>((resolve) => (release = resolve)))
    const store = storeWith({ readFleet: async () => ({ kind: 'unchanged' }), readSession })
    store.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(readSession).toHaveBeenCalledTimes(1)
    expect(store.sessionStatus()).toBe('failed')
    await vi.advanceTimersByTimeAsync(100)
    expect(readSession).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(100)
    expect(readSession).toHaveBeenCalledTimes(2)
    release(session)
    await vi.advanceTimersByTimeAsync(100)
    expect(store.sessionStatus()).toBe('ready')
    expect(store.codeBrowserUrl()).toBe('https://c.test/')
    expect(readSession).toHaveBeenCalledTimes(2)
    store.stop()
  })

  it('has real default intervals', () => {
    TestBed.configureTestingModule({})
    expect(TestBed.inject(POLL_INTERVALS)).toEqual({ warmingUp: 2000, steady: 15000 })
  })
})
