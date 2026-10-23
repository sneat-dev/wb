import { TestBed } from '@angular/core/testing'
import {
  FETCH,
  FleetClient,
  FleetFormatError,
  FleetRequestError,
  FleetRead,
  FleetSchemaError,
  RELOAD_MESSAGE,
  UPDATE_WB_MESSAGE,
  cleanOptionalFields,
  cleanThroughput,
  digestOf,
  dropBadEntries,
  hasFleetShape,
  isFleetDocument,
} from './fleet-client'
import { agent, fleetDocument, machine, pullRequest, worktree } from './test-data'

function respond(status: number, body: unknown, etag?: string): Response {
  return new Response(status === 304 ? null : JSON.stringify(body), {
    status,
    headers: etag ? { ETag: etag } : {},
  })
}

function clientWith(fetcher: typeof fetch): FleetClient {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [{ provide: FETCH, useValue: fetcher }] })
  return TestBed.inject(FleetClient)
}

describe('FleetClient', () => {
  it('reads the document and its ETag, sending no validator the first time', async () => {
    const fetcher = vi.fn(async () => respond(200, fleetDocument(), '"v1"'))
    const read = await clientWith(fetcher).readFleet()
    expect(read).toEqual({ kind: 'changed', document: fleetDocument(), dropped: 0, etag: '"v1"', digest: digestOf(JSON.stringify(fleetDocument())) })
    expect(fetcher).toHaveBeenCalledWith('/api/v1/cockpit/fleet', {
      headers: { Accept: 'application/json' },
      credentials: 'same-origin',
      signal: expect.any(AbortSignal),
    })
  })

  it('tolerates a response with no ETag', async () => {
    const read = (await clientWith(async () => respond(200, fleetDocument())).readFleet()) as Extract<FleetRead, { kind: 'changed' }>
    expect(read.etag).toBe('')
  })

  // The header is how a client learns the freshness: it is sent on a 304 too, and an older daemon sends none.
  it('reads X-Wb-Cockpit-Checked-At on a 200 and on a 304, and reports none for a daemon that sends none or one that is not a time', async () => {
    const stamp = '2026-10-01T10:00:00Z'
    const withHeader = (status: number, value: string) => new Response(status === 304 ? null : JSON.stringify(fleetDocument()), { status, headers: { ETag: '"v"', 'X-Wb-Cockpit-Checked-At': value } })
    const read200 = await clientWith(vi.fn(async () => withHeader(200, stamp))).readFleet()
    expect(read200).toMatchObject({ kind: 'changed', checkedAt: Date.parse(stamp) })
    expect(await clientWith(vi.fn(async () => withHeader(304, stamp))).readFleet('"v"')).toEqual({ kind: 'unchanged', checkedAt: Date.parse(stamp) })
    expect(await clientWith(vi.fn(async () => withHeader(304, 'not a time'))).readFleet('"v"')).toEqual({ kind: 'unchanged' })
    expect(await clientWith(vi.fn(async () => respond(200, fleetDocument(), '"v"'))).readFleet()).not.toHaveProperty('checkedAt')
  })

  it('revalidates with If-None-Match and reports a 304 as unchanged', async () => {
    const fetcher = vi.fn(async () => respond(304, null))
    expect(await clientWith(fetcher).readFleet('"v1"')).toEqual({ kind: 'unchanged' })
    expect(fetcher).toHaveBeenCalledWith('/api/v1/cockpit/fleet', {
      headers: { Accept: 'application/json', 'If-None-Match': '"v1"' },
      credentials: 'same-origin',
      signal: expect.any(AbortSignal),
    })
  })

  it('says the daemon refused the request on 401 and 403', async () => {
    for (const status of [401, 403]) {
      const failure = await clientWith(async () => respond(status, {})).readFleet().catch((error: unknown) => error)
      expect(failure).toBeInstanceOf(FleetRequestError)
      expect((failure as FleetRequestError).status).toBe(status)
      expect((failure as FleetRequestError).message).toBe(`The daemon refused the request (status ${status}).`)
    }
  })

  it('rejects a 200 that is not a fleet document of the page schema', async () => {
    for (const body of [null, 'text', {}, { ...fleetDocument(), agents: null }, { ...fleetDocument(), machines: undefined }]) {
      const failure = await clientWith(async () => respond(200, body)).readFleet().catch((error: unknown) => error)
      expect(failure).toBeInstanceOf(FleetFormatError)
    }
    const notJson = await clientWith(async () => new Response('<html>', { status: 200 })).readFleet().catch((error: unknown) => error)
    expect(notJson).toBeInstanceOf(FleetFormatError)
    expect(isFleetDocument(fleetDocument())).toBe(true)
    expect(isFleetDocument({ ...fleetDocument(), schema_version: 1 })).toBe(false)
    expect(isFleetDocument({ ...fleetDocument(), schema_version: 3 }, 3)).toBe(true)
    expect(hasFleetShape(null)).toBe(false)
  })

  // cockpit-views#ac:client-accepts-only-schema-2
  it('shows "update wb on this machine" for an older daemon, "reload" for an older page, and reads a matching one', async () => {
    const daemonOlder = await clientWith(async () => respond(200, { ...fleetDocument(), schema_version: 1 })).readFleet().catch((error: unknown) => error)
    expect(daemonOlder).toBeInstanceOf(FleetSchemaError)
    expect((daemonOlder as FleetSchemaError).message).toBe(UPDATE_WB_MESSAGE)
    expect(UPDATE_WB_MESSAGE).toBe('update wb on this machine')
    expect((daemonOlder as FleetSchemaError).mismatch).toBe('daemon-older')
    expect([(daemonOlder as FleetSchemaError).daemonVersion, (daemonOlder as FleetSchemaError).pageVersion]).toEqual([1, 2])
    // The page expects 2 and the daemon answers 3: the page is the older side.
    const pageOlder = await clientWith(async () => respond(200, { ...fleetDocument(), schema_version: 3 })).readFleet().catch((error: unknown) => error)
    expect((pageOlder as FleetSchemaError).message).toBe(RELOAD_MESSAGE)
    expect(RELOAD_MESSAGE).toBe('reload')
    expect((pageOlder as FleetSchemaError).mismatch).toBe('page-older')
    const match = await clientWith(async () => respond(200, fleetDocument())).readFleet(undefined, 2)
    expect(match.kind).toBe('changed')
    expect(fleetDocument().schema_version).toBe(2)
    // A page built for 3 against a daemon answering 2 is the older daemon.
    const expectsThree = await clientWith(async () => respond(200, fleetDocument())).readFleet(undefined, 3).catch((error: unknown) => error)
    expect((expectsThree as FleetSchemaError).mismatch).toBe('daemon-older')
  })

  it('drops an entry that lacks a required field instead of throwing, and counts it, keeping unknown enum values', async () => {
    const good = fleetDocument()
    const bad = {
      ...good,
      machines: [...good.machines, { id: 'm' }, null, 'x'],
      repositories: [...good.repositories, { ...good.repositories[0], name: 7 }],
      worktrees: [...good.worktrees, { ...good.worktrees[0], branch: undefined }],
      pull_requests: [{ ...pullRequest('p', 'r1', undefined), number: 'x' }, { ...pullRequest('p2', 'r1', undefined), state: 'futuristic', mergeable: 'newvalue' }],
      agents: [...good.agents, { ...good.agents[0], state: 5 }, { ...good.agents[0], id: 'a9', kind: 'future-kind' }],
    }
    const read = (await clientWith(async () => respond(200, bad)).readFleet()) as Extract<FleetRead, { kind: 'changed' }>
    expect(read.dropped).toBe(7)
    expect(read.document.machines).toEqual(good.machines)
    expect(read.document.pull_requests.map((p) => p.id)).toEqual(['p2'])
    expect(read.document.agents.map((a) => a.id)).toEqual([...good.agents.map((a) => a.id), 'a9'])
    expect(read.document.worktrees).toEqual(good.worktrees)
  })

  it('keeps the same document object when nothing is dropped, and rejects a document with no document-level facts', async () => {
    const document = fleetDocument()
    expect(dropBadEntries(document)).toEqual({ document, dropped: 0 })
    expect(dropBadEntries(document).document).toBe(document)
    for (const broken of [{ warming_up: 'no' }, { repositories_total: 'x' }, { repositories_scanned: undefined }, { diagnostics: null }]) {
      await expect(clientWith(async () => respond(200, { ...document, ...broken })).readFleet()).rejects.toBeInstanceOf(FleetFormatError)
    }
  })

  it('gives an identical body an identical digest and a different one another', () => {
    expect(digestOf('abc')).toBe(digestOf('abc'))
    expect(digestOf('abc')).not.toBe(digestOf('abd'))
    expect(digestOf('')).toBe('0:811c9dc5')
  })

  it('gives every request a timeout', async () => {
    const fetcher = vi.fn(async () => respond(200, { principal: 'x', capabilities: [], code_browser_url: '' }))
    await clientWith(fetcher).readSession()
    const init = fetcher.mock.calls[0] as unknown as [string, RequestInit]
    expect(init[1].signal).toBeInstanceOf(AbortSignal)
  })

  it('reports any other status', async () => {
    const failure = await clientWith(async () => respond(500, {})).readFleet().catch((error: unknown) => error)
    expect((failure as FleetRequestError).message).toContain('500')
  })

  it('reads the session', async () => {
    const session = { principal: 'anonymous-local', capabilities: ['fleet.read'], code_browser_url: 'https://c.test/' }
    const fetcher = vi.fn(async () => respond(200, session))
    expect(await clientWith(fetcher).readSession()).toEqual(session)
    expect(fetcher).toHaveBeenCalledWith('/api/v1/cockpit/session', expect.objectContaining({ credentials: 'same-origin' }))
  })

  // cockpit#ac:session-key-reaches-the-page-in-the-fragment
  it('asks the session with a session key it is given, and names none otherwise', async () => {
    const fetcher = vi.fn(async () => respond(200, { principal: 'owner', capabilities: [], code_browser_url: '' }))
    const client = clientWith(fetcher)
    await client.readSession('offered-key')
    await client.readSession()
    const headers = (fetcher.mock.calls as unknown as [string, RequestInit][]).map(([, init]) => init.headers)
    expect(headers).toEqual([{ Accept: 'application/json', 'X-Wb-Cockpit-Session-Key': 'offered-key' }, { Accept: 'application/json' }])
  })

  it('fails the session read on an error status', async () => {
    await expect(clientWith(async () => respond(401, {})).readSession()).rejects.toBeInstanceOf(FleetRequestError)
  })

  it('uses the global fetch by default', async () => {
    const global = vi.spyOn(globalThis, 'fetch').mockResolvedValue(respond(200, fleetDocument(), '"g"'))
    TestBed.configureTestingModule({})
    expect(await TestBed.inject(FleetClient).readFleet()).toMatchObject({ etag: '"g"' })
    global.mockRestore()
  })
})


describe('optional fields of schema 2 (REQ:field-tables)', () => {
  const block = { window_days: 30, per_day: [{ date: '2026-10-01', finished: 2, dropped: 1, landed: 1 }], slowest: [{ task: 't', duration_seconds: 60, landed_at: '2026-10-01T00:00:00Z' }], median_seconds: 60, p90_seconds: 90, capped: true }

  it('keeps a document whose optional fields are valid, and the document object when it has no throughput', () => {
    const document = fleetDocument({
      machines: [{ ...machine('alpha'), export_dropped: 2, remote_error: 'something_new', publish_error: 'collect_failed', agents_truncated: true }],
      pull_requests: [pullRequest('p', 'r1', undefined, { mergeable: 'has_hooks' })],
      agents: [agent('a', 'r1', 'live', { activity: 'done', task: 't', worktrees: ['w1'], started_at: '2026-10-01T00:00:00Z', finished_at: '2026-10-01T01:00:00Z', exit_code: 0 })],
      agents_truncated: true,
      pull_requests_throttled: false,
    })
    expect(cleanOptionalFields(document)).toBe(document)
    expect(cleanThroughput(block)).toEqual(block)
  })

  it('removes a field of the wrong type or an enum value outside its set, and keeps the entry', () => {
    const document = fleetDocument({
      machines: [{ ...machine('alpha'), export_dropped: 'many' as never, remote_error: 5 as never, publish_error: 'a free text' as never, agents_truncated: 'yes' as never }],
      pull_requests: [pullRequest('p', 'r1', undefined, { mergeable: 'wobbly' as never }), pullRequest('q', 'r1', undefined, { mergeable: 'dirty' })],
      agents: [agent('a', 'r1', 'live', { activity: 'sleeping' as never, task: 4 as never, worktrees: [1] as never, started_at: 3 as never, finished_at: false as never, exit_code: 'x' as never })],
      agents_truncated: 'yes' as never,
      pull_requests_throttled: 1 as never,
    })
    const cleaned = cleanOptionalFields(document)
    expect(cleaned.machines[0]).not.toHaveProperty('export_dropped')
    expect(cleaned.machines[0]).not.toHaveProperty('remote_error')
    expect(cleaned.machines[0]).not.toHaveProperty('publish_error')
    expect(cleaned.machines[0]).not.toHaveProperty('agents_truncated')
    expect(cleaned.pull_requests.map((p) => p.mergeable)).toEqual([undefined, 'dirty'])
    expect(cleaned.agents[0]).toEqual(agent('a', 'r1', 'live'))
    expect(cleaned).not.toHaveProperty('agents_truncated')
    expect(cleaned).not.toHaveProperty('pull_requests_throttled')
    expect(cleaned.pull_requests[1]).toBe(document.pull_requests[1])
  })

  // cockpit-views#ac:field-tables
  it('reads a worktree or pull request field outside its closed set as "not reported", so an owner state it does not know never puts a task at risk', async () => {
    const odd = {
      worktrees: [{ ...worktree('w1', 'r1', 'alpha'), task: 'fix-ci', owner_state: 'zombie', lifecycle: 'in_progress', ahead: 'two', behind: null, upstream_gone: 'no', has_upstream: 0, last_activity_at: 7 }],
      pull_requests: [pullRequest('p1', 'r1', 'w1', { state: 'futuristic' as never, checks_total: 'x' as never, checks_passed: null as never, checks_failed: 'y' as never, checks_pending: {} as never, checks_skipped: [] as never, checks_green: 'yes' as never, checked_at: 5 as never, failed_check: 9 as never })],
    }
    const read = (await clientWith(async () => respond(200, { ...fleetDocument(), ...odd })).readFleet()) as Extract<FleetRead, { kind: 'changed' }>
    expect(read.dropped).toBe(0)
    const [cleaned] = read.document.worktrees
    for (const field of ['owner_state', 'lifecycle', 'ahead', 'behind', 'upstream_gone', 'has_upstream', 'last_activity_at']) expect(cleaned, field).not.toHaveProperty(field)
    expect(cleaned).toMatchObject({ id: 'w1', task: 'fix-ci' })
    const [pr] = read.document.pull_requests
    for (const field of ['state', 'checks_total', 'checks_passed', 'checks_failed', 'checks_pending', 'checks_skipped', 'checks_green', 'checked_at', 'failed_check']) expect(pr, field).not.toHaveProperty(field)
    // And what the page knows is kept.
    const known = fleetDocument({ worktrees: [{ ...worktree('w1', 'r1', 'alpha'), owner_state: 'orphaned', lifecycle: 'review', ahead: 2, behind: 0, upstream_gone: false, has_upstream: true }], pull_requests: [pullRequest('p1', 'r1', 'w1', { state: 'draft' })] })
    expect(cleanOptionalFields(known)).toBe(known)
  })

  it('accepts a well-formed throughput block through the client, and drops a malformed one', async () => {
    const read = async (throughput: unknown) => ((await clientWith(async () => respond(200, { ...fleetDocument(), throughput })).readFleet()) as Extract<FleetRead, { kind: 'changed' }>).document.throughput
    expect(await read(block)).toEqual(block)
    for (const bad of [5, null, { window_days: 'x', per_day: [], slowest: [] }, { window_days: 30, per_day: {}, slowest: [] }, { window_days: 30, per_day: [], slowest: 'none' }]) {
      expect(await read(bad)).toBeUndefined()
    }
  })

  it('leaves out the days and slowest entries that are malformed, and optional numbers of the wrong type', () => {
    const cleaned = cleanThroughput({
      window_days: 30,
      per_day: [{ date: '2026-10-01', finished: 1, dropped: 0, landed: 'x' }, { date: '2026-09-30', landed: 3 }, 'day', null, { date: '2026-09-29', finished: 0, dropped: 2 }],
      slowest: [{ task: 'a', duration_seconds: 'long', landed_at: 'x' }, { task: 'b', duration_seconds: 5, landed_at: '2026-10-01T00:00:00Z' }, 7, null],
      median_seconds: 'x',
      p90_seconds: null,
      capped: 'true',
    })
    expect(cleaned).toEqual({
      window_days: 30,
      per_day: [{ date: '2026-10-01', finished: 1, dropped: 0 }, { date: '2026-09-29', finished: 0, dropped: 2 }],
      slowest: [{ task: 'b', duration_seconds: 5, landed_at: '2026-10-01T00:00:00Z' }],
    })
  })
})
