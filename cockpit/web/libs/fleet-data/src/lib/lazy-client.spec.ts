import { FleetFormatError, FleetRequestError, ReadmeRequestError } from './fleet-client'
import { readBranches, readMachineMetrics, readReadme } from './lazy-client'

function respond(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status })
}

describe('the lazy reads', () => {
  it('reads the branches of one repository from the lazy route, naming it in the query', async () => {
    const fetcher = vi.fn(async () => respond(200, { branches: [], reason: 'cached repository' }))
    expect(await readBranches(fetcher, 'repo/a b')).toEqual({ branches: [], reason: 'cached repository' })
    expect(fetcher).toHaveBeenCalledWith('/api/v1/cockpit/branches?repository=repo%2Fa%20b', {
      headers: { Accept: 'application/json' },
      credentials: 'same-origin',
      signal: expect.any(AbortSignal),
    })
  })

  it('fails a branches read on an error status or a body with no list', async () => {
    await expect(readBranches(async () => respond(404, {}), 'x')).rejects.toBeInstanceOf(FleetRequestError)
    await expect(readBranches(async () => respond(200, {}), 'x')).rejects.toBeInstanceOf(FleetFormatError)
    await expect(readBranches(async () => respond(200, null), 'x')).rejects.toBeInstanceOf(FleetFormatError)
    await expect(readBranches(async () => new Response('nope', { status: 200 }), 'x')).rejects.toBeInstanceOf(FleetFormatError)
  })

  it('reads a machine\'s metrics, naming its id in the query, in every route', async () => {
    const body = { machine: 'mach-vm', route: 'none', samples: [], reason: 'no source' }
    const fetcher = vi.fn(async () => respond(200, body))
    expect(await readMachineMetrics(fetcher, 'mach vm')).toEqual(body)
    expect(fetcher).toHaveBeenCalledWith('/api/v1/cockpit/machine-metrics?machine=mach%20vm', {
      headers: { Accept: 'application/json' },
      credentials: 'same-origin',
      signal: expect.any(AbortSignal),
    })
  })

  // cockpit-views#ac:metrics-poll-only-while-visible
  it('cancels a metrics read when the caller\'s signal aborts, and still times out without one', async () => {
    const seen: AbortSignal[] = []
    const fetcher = vi.fn((_url: unknown, init?: RequestInit) => {
      seen.push(init?.signal as AbortSignal)
      return new Promise<Response>((_resolve, reject) => {
        if (init?.signal?.aborted) reject(init.signal.reason)
        else init?.signal?.addEventListener('abort', () => reject(init.signal?.reason))
      })
    })
    const controller = new AbortController()
    const read = readMachineMetrics(fetcher as unknown as typeof fetch, 'm1', controller.signal)
    await vi.waitFor(() => expect(seen).toHaveLength(1))
    expect(seen[0].aborted).toBe(false)
    controller.abort(new DOMException('stopped', 'AbortError'))
    await expect(read).rejects.toMatchObject({ name: 'AbortError' })
    expect(seen[0].aborted).toBe(true)
    // Already aborted before the call: nothing is sent to a live connection.
    const already = AbortSignal.abort()
    await expect(readMachineMetrics(fetcher as unknown as typeof fetch, 'm1', already)).rejects.toBeDefined()
    // Without a signal the request still carries its own timeout signal.
    void readMachineMetrics(fetcher as unknown as typeof fetch, 'm1')
    await vi.waitFor(() => expect(seen).toHaveLength(3))
    expect(seen[2]).toBeInstanceOf(AbortSignal)
    expect(seen[2].aborted).toBe(false)
  })

  it('fails a metrics read on an error status or a body with no samples', async () => {
    await expect(readMachineMetrics(async () => respond(404, {}), 'x')).rejects.toBeInstanceOf(FleetRequestError)
    await expect(readMachineMetrics(async () => respond(200, { machine: 'x' }), 'x')).rejects.toBeInstanceOf(FleetFormatError)
    await expect(readMachineMetrics(async () => respond(200, null), 'x')).rejects.toBeInstanceOf(FleetFormatError)
    await expect(readMachineMetrics(async () => new Response('nope', { status: 200 }), 'x')).rejects.toBeInstanceOf(FleetFormatError)
  })


  // cockpit#ac:readme-needs-owner
  it('reads a README as text from the owner route, naming the repository in the query', async () => {
    const fetcher = vi.fn(async () => new Response('# Title\n', { status: 200, headers: { 'Content-Type': 'text/markdown' } }))
    expect(await readReadme(fetcher, 'repo/a b')).toBe('# Title\n')
    expect(fetcher).toHaveBeenCalledWith('/api/v1/cockpit/readme?repository=repo%2Fa%20b', {
      headers: { Accept: 'text/markdown' },
      credentials: 'same-origin',
      signal: expect.any(AbortSignal),
    })
  })

  it('fails a README read with the status and the daemon short code, or none when it sent none', async () => {
    const failure = async (response: Response) => (await readReadme(async () => response, 'r').catch((error: unknown) => error)) as ReadmeRequestError
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
