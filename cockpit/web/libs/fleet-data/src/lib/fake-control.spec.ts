import { isTerminal } from './control.types'
import { ACTIONS_PATH, FakeOperations, OPERATIONS_PATH, fakeControlFetch, registryAction } from './fake-control'

const next = vi.fn(async () => new Response('next', { status: 200 }))

async function ask(fetcher: typeof fetch, input: string | URL | Request): Promise<Response> {
  return fetcher(input, {})
}

describe('the fake registry', () => {
  it('answers each target with its actions, an empty list for another, and 404 when the route is absent', async () => {
    const push = registryAction('branch.push', 'Push', { permitted: false, reason: 'owner session needed' })
    const fetcher = fakeControlFetch({ registry: { 'worktree:wt-1': [push] } }, next)
    const listed = await ask(fetcher, `${ACTIONS_PATH}?target=worktree:wt-1`)
    expect(await listed.json()).toEqual({ actions: [push] })
    expect(push).toMatchObject({ id: 'branch.push', title: 'Push', applicable: true, safety: 'safe', capability: 'branch.push' })
    expect(await (await ask(fetcher, `${ACTIONS_PATH}?target=worktree:other`)).json()).toEqual({ actions: [] })
    expect(await (await ask(fetcher, ACTIONS_PATH)).json()).toEqual({ actions: [] })
    expect((await ask(fakeControlFetch({}, next), `${ACTIONS_PATH}?target=worktree:wt-1`)).status).toBe(404)
  })

  it('accepts a URL or a Request as the input', async () => {
    const fetcher = fakeControlFetch({ registry: { 'repository:r1': [registryAction('a', 'A')] } }, next)
    expect((await ask(fetcher, new URL('http://cockpit.test/api/v1/cockpit/actions?target=repository:r1'))).status).toBe(200)
    expect((await ask(fetcher, new Request('http://cockpit.test/api/v1/cockpit/actions?target=repository:r1'))).status).toBe(200)
  })
})

describe('the fake operations route', () => {
  it('follows an operation from queued through running to a terminal state, once', async () => {
    const operations = new FakeOperations()
    const fetcher = fakeControlFetch({ operations }, next)
    const started = operations.start('pr.land', 'pull_request:p1')
    expect(started).toEqual({ id: 'op-1', action: 'pr.land', target: 'pull_request:p1', state: 'queued' })
    expect(await (await ask(fetcher, `${OPERATIONS_PATH}/op-1`)).json()).toMatchObject({ state: 'queued' })
    operations.advance('op-1', 'running')
    expect(operations.get('op-1')?.state).toBe('running')
    operations.advance('op-1', 'succeeded', ['landed'])
    operations.advance('op-1', 'failed')
    expect(await (await ask(fetcher, `${OPERATIONS_PATH}/op-1`)).json()).toMatchObject({ state: 'succeeded', summary: ['landed'] })
    // An unknown operation is a 404, and advancing it does nothing.
    operations.advance('op-9', 'running')
    expect((await ask(fetcher, `${OPERATIONS_PATH}/op-9`)).status).toBe(404)
    expect(operations.get('op-9')).toBeUndefined()
    expect((await ask(fakeControlFetch({}, next), `${OPERATIONS_PATH}/op-1`)).status).toBe(404)
  })

  it('knows which states are terminal', () => {
    expect(isTerminal('queued')).toBe(false)
    expect(isTerminal('running')).toBe(false)
    for (const state of ['succeeded', 'failed', 'cancelled'] as const) expect(isTerminal(state)).toBe(true)
  })
})

describe('other requests', () => {
  it('pass through to the next fetch', async () => {
    const response = await ask(fakeControlFetch({}, next), '/api/v1/cockpit/fleet')
    expect(await response.text()).toBe('next')
    expect(next).toHaveBeenCalledWith('/api/v1/cockpit/fleet', {})
  })
})
