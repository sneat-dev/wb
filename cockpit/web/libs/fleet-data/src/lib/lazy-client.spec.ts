import { FleetFormatError, FleetRequestError, ReadmeRequestError } from './fleet-client'
import { readAgentSessionStats, readBranches, readMachineMetrics, readReadme } from './lazy-client'

function respond(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status })
}

describe('the lazy reads', () => {
  it('reads owner-only session and repository totals from the local daemon', async () => {
    const bucket = { session_id: 'thread-one', session_name: 'builder', repository: 'acme/app', runs: 2,
      completed: 1, failed: 1, pending: 0, duration_ms: 1500, usage_runs: 1,
      input_tokens: 120, output_tokens: 30, cost_runs: 1, estimated_usd: 0.25 }
    const report = { total: { ...bucket, session_id: '', repository: '' }, by_session_repository: [bucket] }
    const fetcher = vi.fn(async () => respond(200, report))
    expect(await readAgentSessionStats(fetcher)).toEqual(report)
    expect(fetcher).toHaveBeenCalledWith('/api/v1/cockpit/agent-session-stats', {
      headers: { Accept: 'application/json' }, credentials: 'same-origin', signal: expect.any(AbortSignal),
    })
    await expect(readAgentSessionStats(async () => respond(401, {}))).rejects.toBeInstanceOf(FleetRequestError)
    await expect(readAgentSessionStats(async () => respond(200, { total: bucket, by_session_repository: [{ ...bucket, runs: 'two' }] }))).rejects.toThrow('Invalid session usage report')
  })

  it('rejects malformed or incomplete session usage without rendering its fields', async () => {
    const bucket = { session_id: '', repository: 'acme/app', runs: 1, completed: 1, failed: 0, pending: 0,
      duration_ms: 1000, usage_runs: 1, input_tokens: 2, output_tokens: 3, cost_runs: 0, estimated_usd: 0 }
    const valid = { total: bucket, by_session_repository: [bucket] }
    for (const body of [null, 'text', 3, { ...valid, total: null }, { ...valid, total: 'bad' },
      { ...valid, total: { ...bucket, session_id: 7 } }, { ...valid, total: { ...bucket, repository: null } },
      { ...valid, total: { ...bucket, session_name: 5 } }, { ...valid, total: { ...bucket, session_name: 'named', runs: -1 } },
      { ...valid, total: { ...bucket, runs: 'one' } }, { ...valid, total: { ...bucket, runs: Number.POSITIVE_INFINITY } },
      { ...valid, by_session_repository: null }, { ...valid, by_session_repository: [null] },
    ]) {
      await expect(readAgentSessionStats(async () => respond(200, body))).rejects.toThrow('Invalid session usage report')
    }
    await expect(readAgentSessionStats(async () => new Response('not-json', { status: 200 }))).rejects.toThrow('Invalid session usage report')
    expect(await readAgentSessionStats(async () => respond(200, valid))).toEqual(valid)
  })

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
    await expect(readMachineMetrics(fetcher as unknown as typeof fetch, 'm1', already)).rejects.toMatchObject({ name: 'AbortError' })
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
