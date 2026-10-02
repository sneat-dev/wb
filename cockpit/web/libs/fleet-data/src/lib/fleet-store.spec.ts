import { buildRepositories } from './model-repositories'
import { buildWorktreePanel } from './entity-views'
import { buildCleanup } from './home-details'
import { TestBed } from '@angular/core/testing'
import { DOCUMENT } from '@angular/common'
import { FleetClient, FleetRead, FleetRequestError, FleetSchemaError } from './fleet-client'
import { EXPECTED_SCHEMA, FleetStore, MODEL_OPTIONS, POLL_INTERVALS } from './fleet-store'
import { Session } from './fleet.types'
import { fleetDocument, worktree } from './test-data'

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
  digest: etag,
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
    const remoteRead: FleetRead = { ...changed(false, '"a"'), document: fleetDocument({ worktrees: [{ ...worktree('w3', 'r2', 'beta'), route: 'cached' }] }) }
    const owner: Session = { principal: 'owner', capabilities: ['fleet.read', 'repo.content.read'], code_browser_url: '' }
    const withProvider: FleetRead = { kind: 'changed', etag: '"p"', digest: 'p', document: fleetDocument({ code_index_provider: 'codegrapher' }) }
    const store = storeWith({ readFleet: async () => withProvider, readSession: async () => owner })
    store.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(store.codeIndexProvider()).toBe('codegrapher')
    expect(store.canReadContent()).toBe(true)
    store.stop()
  })

  it('polls fast while warming up, slowly after, and revalidates with the last ETag', async () => {
    const readFleet = vi
      .fn<(etag?: string, expected?: number) => Promise<FleetRead>>()
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
    expect(readFleet).toHaveBeenLastCalledWith('"a"', 2)
    expect(store.warmingUp()).toBe(false)
    await vi.advanceTimersByTimeAsync(99)
    expect(readFleet).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(1)
    expect(readFleet).toHaveBeenCalledTimes(3)
    expect(readFleet).toHaveBeenLastCalledWith('"b"', 2)
    expect(store.document().warming_up).toBe(false)
    store.stop()
  })

  // cockpit-views#ac:polling-follows-visibility
  describe('while the page is hidden', () => {
    function hideable() {
      let visibility: DocumentVisibilityState = 'visible'
      const listeners = new Set<() => void>()
      const doc = {
        get visibilityState() {
          return visibility
        },
        addEventListener: (_: string, listener: () => void) => listeners.add(listener),
        removeEventListener: (_: string, listener: () => void) => listeners.delete(listener),
      }
      return { doc, listeners, set: (next: DocumentVisibilityState) => { visibility = next; for (const listener of [...listeners]) listener() } }
    }

    function storeAndPage(client: Partial<FleetClient>) {
      const page = hideable()
      TestBed.configureTestingModule({
        providers: [
          { provide: FleetClient, useValue: client },
          { provide: DOCUMENT, useValue: page.doc },
          { provide: POLL_INTERVALS, useValue: { warmingUp: 10, steady: 100 } },
        ],
      })
      return { store: TestBed.inject(FleetStore), page }
    }

    it('reads nothing, reads at once when the page is visible again, and reads the session again then', async () => {
      const readFleet = vi.fn<(etag?: string, expected?: number) => Promise<FleetRead>>().mockResolvedValueOnce(changed(false, '"a"')).mockResolvedValue({ kind: 'unchanged' })
      const readSession = vi.fn<() => Promise<Session>>().mockResolvedValue(session)
      const { store, page } = storeAndPage({ readFleet, readSession })
      store.start()
      await vi.advanceTimersByTimeAsync(0)
      expect(readFleet).toHaveBeenCalledTimes(1)
      expect(readSession).toHaveBeenCalledTimes(1)
      page.set('hidden')
      // Many intervals pass and nothing is read.
      await vi.advanceTimersByTimeAsync(1000)
      expect(readFleet).toHaveBeenCalledTimes(1)
      expect(vi.getTimerCount()).toBe(0)
      page.set('visible')
      await vi.advanceTimersByTimeAsync(0)
      expect(readFleet).toHaveBeenCalledTimes(2)
      expect(readSession).toHaveBeenCalledTimes(2)
      // And it carries on at its pace.
      await vi.advanceTimersByTimeAsync(100)
      expect(readFleet).toHaveBeenCalledTimes(3)
      // A new owner session read on return replaces the old one.
      readSession.mockResolvedValue({ ...session, principal: 'owner' })
      page.set('hidden')
      page.set('visible')
      await vi.advanceTimersByTimeAsync(0)
      expect(store.session()?.principal).toBe('owner')
      store.stop()
      expect(page.listeners.size).toBe(0)
    })

    it('does not start a second read when the page returns while one is out, and ends the loop for a read that finishes hidden', async () => {
      let finish: (read: FleetRead) => void = () => undefined
      const readFleet = vi.fn<(etag?: string, expected?: number) => Promise<FleetRead>>(() => new Promise((resolve) => (finish = resolve)))
      const { store, page } = storeAndPage({ readFleet, readSession: async () => session })
      store.start()
      await vi.advanceTimersByTimeAsync(0)
      page.set('hidden')
      page.set('visible')
      expect(readFleet).toHaveBeenCalledTimes(1)
      finish({ kind: 'unchanged' })
      await vi.advanceTimersByTimeAsync(0)
      // Hidden again before the read answers: the answer schedules nothing.
      readFleet.mockImplementation(() => new Promise((resolve) => (finish = resolve)))
      await vi.advanceTimersByTimeAsync(100)
      expect(readFleet).toHaveBeenCalledTimes(2)
      page.set('hidden')
      finish({ kind: 'unchanged' })
      await vi.advanceTimersByTimeAsync(1000)
      expect(readFleet).toHaveBeenCalledTimes(2)
      // Visibility changes of a stopped store are ignored.
      store.stop()
      page.set('visible')
      expect(readFleet).toHaveBeenCalledTimes(2)
    })

    it('reads the session again when a read is answered 401, and keeps the old one when that read fails', async () => {
      const readFleet = vi.fn<(etag?: string, expected?: number) => Promise<FleetRead>>().mockResolvedValueOnce(changed(false, '"a"')).mockRejectedValue(new FleetRequestError(401))
      const readSession = vi.fn<() => Promise<Session>>().mockResolvedValue({ ...session, principal: 'owner' })
      const { store } = storeAndPage({ readFleet, readSession })
      store.start()
      await vi.advanceTimersByTimeAsync(0)
      // The session is read once, with the first poll; the owner session expires meanwhile.
      expect(readSession).toHaveBeenCalledTimes(1)
      expect(store.session()?.principal).toBe('owner')
      readSession.mockResolvedValue({ ...session, principal: 'anonymous-local' })
      // The next read is answered 401: the session is read again, and is no longer the owner's.
      await vi.advanceTimersByTimeAsync(100)
      expect(readSession).toHaveBeenCalledTimes(2)
      expect(store.session()?.principal).toBe('anonymous-local')
      readSession.mockRejectedValue(new Error('offline'))
      await vi.advanceTimersByTimeAsync(100)
      expect(store.session()?.principal).toBe('anonymous-local')
      expect(store.sessionStatus()).toBe('ready')
      // Other statuses do not.
      store.stop()
    })
  })

  it('keeps the last document and shows the error when a read fails, then recovers', async () => {
    const readFleet = vi
      .fn<(etag?: string, expected?: number) => Promise<FleetRead>>()
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
      .fn<(etag?: string, expected?: number) => Promise<FleetRead>>()
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
    const readFleet = vi.fn<(etag?: string, expected?: number) => Promise<FleetRead>>().mockRejectedValue(new Error('down'))
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

  // cockpit-views#ac:client-accepts-only-schema-2
  it('renders no data for another schema version and says what to do, then recovers', async () => {
    const readFleet = vi
      .fn<(etag?: string, expected?: number) => Promise<FleetRead>>()
      .mockResolvedValueOnce(changed(false, '"a"'))
      .mockRejectedValueOnce(new FleetSchemaError('daemon-older', 1, 2))
      .mockResolvedValueOnce(changed(false, '"a"'))
    const store = storeWith({ readFleet, readSession: async () => session })
    store.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(store.schemaMismatch()).toBeNull()
    expect(store.document().machines).toHaveLength(2)
    await vi.advanceTimersByTimeAsync(100)
    expect(store.schemaMismatch()).toBe('daemon-older')
    expect(store.error()).toBe('update wb on this machine')
    expect(store.document().machines).toEqual([])
    expect(store.warmingUp()).toBe(false)
    await vi.advanceTimersByTimeAsync(300)
    // The validator is dropped with the data, so the next read is a full one.
    expect(readFleet).toHaveBeenNthCalledWith(3, undefined, 2)
    expect(store.schemaMismatch()).toBeNull()
    expect(store.document().machines).toHaveLength(2)
    store.stop()
  })

  it('reads the expected schema version from its token', async () => {
    const readFleet = vi.fn(async (): Promise<FleetRead> => ({ kind: 'unchanged' }))
    TestBed.configureTestingModule({
      providers: [
        { provide: FleetClient, useValue: { readFleet, readSession: async () => session } },
        { provide: EXPECTED_SCHEMA, useValue: 3 },
      ],
    })
    const store = TestBed.inject(FleetStore)
    store.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(readFleet).toHaveBeenCalledWith(undefined, 3)
    store.stop()
  })

  // cockpit-views#ac:unchanged-snapshot-does-nothing, cockpit-views#ac:derived-collections-computed-once
  it('derives each collection once per document: a 304, an identical body and a clock tick recompute nothing', async () => {
    vi.setSystemTime(Date.parse('2026-10-01T10:00:05Z'))
    const derived: string[] = []
    const same = changed(false, '"a"')
    const readFleet = vi
      .fn<(etag?: string, expected?: number) => Promise<FleetRead>>()
      .mockResolvedValueOnce(same)
      .mockResolvedValueOnce({ kind: 'unchanged' })
      // The same body again, under a new ETag and as a freshly parsed object.
      .mockResolvedValueOnce({ ...same, etag: '"b"', document: { ...same.document } })
      .mockResolvedValueOnce({ ...changed(false, '"c"'), digest: 'different' })
    TestBed.configureTestingModule({
      providers: [
        { provide: FleetClient, useValue: { readFleet, readSession: async () => session } },
        { provide: POLL_INTERVALS, useValue: { warmingUp: 10, steady: 100 } },
        { provide: MODEL_OPTIONS, useValue: { now: () => Date.parse('2026-10-01T10:00:00Z'), onDerive: (name: string) => derived.push(name) } },
      ],
    })
    const store = TestBed.inject(FleetStore)
    store.start()
    await vi.advanceTimersByTimeAsync(0)
    const first = store.model()
    const render = (): void => {
      void [buildRepositories(store.model()), store.model().tasks, store.model().needsYou, store.model().readyToLand, buildCleanup(store.model())]
    }
    render()
    render()
    const once = [...derived]
    expect(once.filter((name) => name === 'tasks')).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(100)
    expect(readFleet).toHaveBeenCalledTimes(2)
    store.now.set(store.now() + 5000)
    render()
    expect(store.model()).toBe(first)
    // The store's clock and the model's agree to the bucket: the model is made from the store's clock.
    expect(Math.floor(store.model().now / 60_000)).toBe(Math.floor(store.now() / 60_000))
    await vi.advanceTimersByTimeAsync(100)
    expect(readFleet).toHaveBeenCalledTimes(3)
    expect(store.model()).toBe(first)
    expect(derived).toEqual(once)
    // A different document is derived once more.
    await vi.advanceTimersByTimeAsync(100)
    expect(store.model()).not.toBe(first)
    render()
    expect(derived.filter((name) => name === 'tasks')).toHaveLength(2)
    expect(derived.filter((name) => name === 'repositories')).toHaveLength(2)
    store.stop()
  })

  it('exposes how many entries were dropped for the diagnostic line, and resets it for another schema', async () => {
    const readFleet = vi
      .fn<(etag?: string, expected?: number) => Promise<FleetRead>>()
      .mockResolvedValueOnce({ ...changed(false, '"a"'), dropped: 3 })
      .mockResolvedValueOnce({ ...changed(false, '"b"') })
      .mockRejectedValueOnce(new FleetSchemaError('page-older', 3, 2))
    const store = storeWith({ readFleet, readSession: async () => session })
    store.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(store.droppedEntries()).toBe(3)
    await vi.advanceTimersByTimeAsync(100)
    expect(store.droppedEntries()).toBe(0)
    await vi.advanceTimersByTimeAsync(100)
    expect(store.schemaMismatch()).toBe('page-older')
    expect(store.droppedEntries()).toBe(0)
    store.stop()
  })

  it('threads the owner-only machine routes of the session into the model, and none for an anonymous reader', async () => {
    const remoteRead: FleetRead = { ...changed(false, '"a"'), document: fleetDocument({ worktrees: [{ ...worktree('w3', 'r2', 'beta'), route: 'cached' }] }) }
    const owner: Session = { principal: 'owner', capabilities: [], code_browser_url: '', machine_routes: [{ machine_id: 'mach-beta', ssh: { host: 'beta.example', user: 'alex', wb_path: 'wb' } }] }
    const store = storeWith({ readFleet: async () => remoteRead, readSession: async () => owner })
    store.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(store.model().machineRoutes).toBe(owner.machine_routes)
    expect(buildWorktreePanel(store.model(), 'w3')?.commands[0].command).toMatchObject({ text: expect.stringContaining('ssh alex@beta.example') })
    store.stop()
    TestBed.resetTestingModule()
    const anonymous = storeWith({ readFleet: async () => remoteRead, readSession: async () => session })
    anonymous.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(anonymous.model().machineRoutes).toBeUndefined()
    expect(buildWorktreePanel(anonymous.model(), 'w3')?.commands[0].command).toMatchObject({ label: 'run on beta' })
    anonymous.stop()
  })

  it('has real default intervals', () => {
    TestBed.configureTestingModule({})
    expect(TestBed.inject(POLL_INTERVALS)).toEqual({ warmingUp: 2000, steady: 15000 })
  })
})
