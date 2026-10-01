import { TestBed } from '@angular/core/testing'
import { FETCH, FleetClient, FleetFormatError, FleetRequestError, FleetRead, ReadmeRequestError, isFleetDocument } from './fleet-client'
import { fleetDocument } from './test-data'

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
    expect(read).toEqual({ kind: 'changed', document: fleetDocument(), etag: '"v1"' })
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

  it('rejects a 200 that is not a version 1 fleet document', async () => {
    for (const body of [null, 'text', {}, { ...fleetDocument(), schema_version: 2 }, { ...fleetDocument(), agents: null }]) {
      const failure = await clientWith(async () => respond(200, body)).readFleet().catch((error: unknown) => error)
      expect(failure).toBeInstanceOf(FleetFormatError)
    }
    expect(isFleetDocument(fleetDocument())).toBe(true)
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
