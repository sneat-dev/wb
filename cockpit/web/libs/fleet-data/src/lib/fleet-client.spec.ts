import { TestBed } from '@angular/core/testing'
import {
  FETCH,
  FleetClient,
  FleetFormatError,
  FleetRequestError,
  FleetRead,
  FleetSchemaError,
  RELOAD_MESSAGE,
  ReadmeRequestError,
  UPDATE_WB_MESSAGE,
  digestOf,
  dropBadEntries,
  hasFleetShape,
  isFleetDocument,
} from './fleet-client'
import { fleetDocument, pullRequest } from './test-data'

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

  it('reads the branches of one repository from the lazy route, naming it in the query', async () => {
    const fetcher = vi.fn(async () => respond(200, { branches: [], reason: 'cached repository' }))
    expect(await clientWith(fetcher).readBranches('repo/a b')).toEqual({ branches: [], reason: 'cached repository' })
    expect(fetcher).toHaveBeenCalledWith('/api/v1/cockpit/branches?repository=repo%2Fa%20b', {
      headers: { Accept: 'application/json' },
      credentials: 'same-origin',
      signal: expect.any(AbortSignal),
    })
  })

  it('fails a branches read on an error status or a body with no list', async () => {
    await expect(clientWith(async () => respond(404, {})).readBranches('x')).rejects.toBeInstanceOf(FleetRequestError)
    await expect(clientWith(async () => respond(200, {})).readBranches('x')).rejects.toBeInstanceOf(FleetFormatError)
    await expect(clientWith(async () => respond(200, null)).readBranches('x')).rejects.toBeInstanceOf(FleetFormatError)
    await expect(clientWith(async () => new Response('nope', { status: 200 })).readBranches('x')).rejects.toBeInstanceOf(FleetFormatError)
  })

  it('reads a machine\'s metrics, naming its id in the query, in every route', async () => {
    const body = { machine: 'mach-vm', route: 'none', samples: [], reason: 'no source' }
    const fetcher = vi.fn(async () => respond(200, body))
    expect(await clientWith(fetcher).readMachineMetrics('mach vm')).toEqual(body)
    expect(fetcher).toHaveBeenCalledWith('/api/v1/cockpit/machine-metrics?machine=mach%20vm', {
      headers: { Accept: 'application/json' },
      credentials: 'same-origin',
      signal: expect.any(AbortSignal),
    })
  })

  it('fails a metrics read on an error status or a body with no samples', async () => {
    await expect(clientWith(async () => respond(404, {})).readMachineMetrics('x')).rejects.toBeInstanceOf(FleetRequestError)
    await expect(clientWith(async () => respond(200, { machine: 'x' })).readMachineMetrics('x')).rejects.toBeInstanceOf(FleetFormatError)
    await expect(clientWith(async () => respond(200, null)).readMachineMetrics('x')).rejects.toBeInstanceOf(FleetFormatError)
    await expect(clientWith(async () => new Response('nope', { status: 200 })).readMachineMetrics('x')).rejects.toBeInstanceOf(FleetFormatError)
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

  it('fails the session read on an error status', async () => {
    await expect(clientWith(async () => respond(401, {})).readSession()).rejects.toBeInstanceOf(FleetRequestError)
  })

  it('uses the global fetch by default', async () => {
    const global = vi.spyOn(globalThis, 'fetch').mockResolvedValue(respond(200, fleetDocument(), '"g"'))
    TestBed.configureTestingModule({})
    expect(await TestBed.inject(FleetClient).readFleet()).toMatchObject({ etag: '"g"' })
    global.mockRestore()
  })

  // cockpit#ac:readme-needs-owner
  it('reads a README as text from the owner route, naming the repository in the query', async () => {
    const fetcher = vi.fn(async () => new Response('# Title\n', { status: 200, headers: { 'Content-Type': 'text/markdown' } }))
    expect(await clientWith(fetcher).readReadme('repo/a b')).toBe('# Title\n')
    expect(fetcher).toHaveBeenCalledWith('/api/v1/cockpit/readme?repository=repo%2Fa%20b', {
      headers: { Accept: 'text/markdown' },
      credentials: 'same-origin',
      signal: expect.any(AbortSignal),
    })
  })

  it('fails a README read with the status and the daemon short code, or none when it sent none', async () => {
    const failure = async (response: Response) => (await clientWith(async () => response).readReadme('r').catch((error: unknown) => error)) as ReadmeRequestError
    const refused = await failure(new Response(JSON.stringify({ error: 'readme_not_found' }), { status: 404 }))
    expect(refused).toBeInstanceOf(ReadmeRequestError)
    expect([refused.status, refused.code]).toEqual([404, 'readme_not_found'])
    expect(refused.message).toContain('404')
    for (const body of ['not json', '"text"', 'null', JSON.stringify({ error: 5 }), JSON.stringify({})]) {
      const other = await failure(new Response(body, { status: 500 }))
      expect([other.status, other.code]).toEqual([500, ''])
    }
  })
})
