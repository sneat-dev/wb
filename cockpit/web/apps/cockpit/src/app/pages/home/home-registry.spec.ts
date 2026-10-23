import { TestBed } from '@angular/core/testing'
import { FETCH } from '@cockpit/fleet-data'
import { registryAction } from '@cockpit/fleet-data/testing'
import { HomeRegistry, LAND_ACTION, PUSH_ACTION } from './home-registry'

const answers = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status })

function registryWith(fetcher: typeof fetch): HomeRegistry {
  TestBed.configureTestingModule({ providers: [{ provide: FETCH, useValue: fetcher }] })
  return TestBed.inject(HomeRegistry)
}

describe('HomeRegistry', () => {
  it('offers what the registry returned for a target, by action id', async () => {
    const calls: string[] = []
    const registry = registryWith(async (input) => {
      calls.push(String(input))
      return answers({ actions: [registryAction(PUSH_ACTION, 'Push'), registryAction('worktree.discard', 'Discard', { safety: 'destructive' })] })
    })
    expect(registry.offered('worktree:w1', PUSH_ACTION)).toBeUndefined()
    await registry.request(['worktree:w1'])
    expect(calls).toEqual(['/api/v1/cockpit/actions?target=worktree%3Aw1'])
    expect(registry.offered('worktree:w1', PUSH_ACTION)?.map((action) => action.title)).toEqual(['Push'])
    expect(registry.offered('worktree:w1', LAND_ACTION)).toBeUndefined()
    expect(registry.offered('worktree:other', PUSH_ACTION)).toBeUndefined()
  })

  it('reads nothing for no targets, and keeps the answers of earlier requests', async () => {
    const calls: string[] = []
    const registry = registryWith(async (input) => {
      calls.push(String(input))
      return answers({ actions: [registryAction(PUSH_ACTION, 'Push')] })
    })
    await registry.request([])
    expect(calls).toEqual([])
    await registry.request(['worktree:a'])
    await registry.request(['worktree:b'])
    expect(registry.offered('worktree:a', PUSH_ACTION)).toBeDefined()
    expect(registry.offered('worktree:b', PUSH_ACTION)).toBeDefined()
  })

  it('forgets the actions of a target that can no longer be read, whatever the failure', async () => {
    let mode: 'ok' | 'error' | 'odd' | 'throws' = 'ok'
    const registry = registryWith(async () => {
      if (mode === 'throws') throw new Error('offline')
      if (mode === 'error') return answers({}, 500)
      if (mode === 'odd') return answers({ actions: 'many' })
      return answers({ actions: [registryAction(PUSH_ACTION, 'Push')] })
    })
    for (const next of ['error', 'odd', 'throws'] as const) {
      mode = 'ok'
      await registry.request(['worktree:a'])
      expect(registry.offered('worktree:a', PUSH_ACTION), `${next} before`).toBeDefined()
      mode = next
      await registry.request(['worktree:a'])
      expect(registry.offered('worktree:a', PUSH_ACTION), next).toBeUndefined()
    }
  })

  it('stops asking once the route is absent', async () => {
    const calls: string[] = []
    const registry = registryWith(async (input) => {
      calls.push(String(input))
      return answers({}, 404)
    })
    await registry.request(['worktree:a', 'worktree:b'])
    expect(calls).toHaveLength(2)
    await registry.request(['worktree:c'])
    expect(calls).toHaveLength(2)
    expect(registry.offered('worktree:a', PUSH_ACTION)).toBeUndefined()
  })
})
