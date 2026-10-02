import { TestBed } from '@angular/core/testing'
import { FleetDocument, FleetStore, hrefOf } from '@cockpit/fleet-data'
import { agent, fleetDocument, repository, worktree } from '@cockpit/fleet-data/testing'
import { NOW } from '../pages/test-harness'
import { PALETTE_KINDS, PaletteGroup, RESULTS_PER_KIND, resolveResult, searchPalette } from './palette-search'

function groupOf(groups: PaletteGroup[], kind: string): PaletteGroup {
  const found = groups.find((group) => group.kind === kind)
  if (found === undefined) throw new Error(`no ${kind} group in ${groups.map((group) => group.kind).join(', ')}`)
  return found
}

function model(document: FleetDocument) {
  const store = TestBed.inject(FleetStore)
  store.document.set(document)
  store.now.set(NOW)
  return store.model()
}

/** More than eight repositories whose name holds `go`, and one of each other kind that matches as well. */
function goFleet(): FleetDocument {
  const repositories = Array.from({ length: 10 }, (_, index) => repository(`g${index}`, 'alpha', { name: `acme/go-${index}`, worktree_count: 0, active_agent_count: 0 }))
  const worktrees = [{ ...worktree('w1', 'g0', 'alpha'), task: 'go-live', branch: 'task/go-live' }, { ...worktree('w2', 'g1', 'alpha'), task: 'other', branch: 'other-branch' }]
  return fleetDocument({
    repositories: [...repositories, repository('plain', 'alpha', { name: 'acme/plain' })],
    worktrees,
    agents: [agent('a1', 'g0', 'running', { runtime: 'golang', model: 'opus' }), agent('a2', undefined, 'idle', { session_id: undefined, run_id: 'r2', kind: 'run' })],
  })
}

describe('searchPalette', () => {
  // cockpit-views#ac:palette-groups-results
  it('groups results by kind with at most 8 each, and does not search lazily loaded branches', () => {
    const groups = searchPalette(model(goFleet()), 'go', NOW)
    expect(groups.map((group) => group.kind)).toEqual(['task', 'repository', 'worktree', 'branch', 'agent'])
    const repositories = groupOf(groups, 'repository')
    expect(repositories.results).toHaveLength(RESULTS_PER_KIND)
    expect(repositories.more).toBe(2)
    for (const group of groups) {
      expect(group.results.length).toBeLessThanOrEqual(RESULTS_PER_KIND)
      expect(group.title).toBe(PALETTE_KINDS.find((info) => info.kind === group.kind)?.title)
    }
    // A branch that exists only in a repository's lazy branch list is not in the document, so it is not found.
    expect(searchPalette(model(goFleet()), 'only-in-the-lazy-list', NOW)).toEqual([])
    expect(groups.find((group) => group.kind === 'task')?.results.map((result) => result.label)).toEqual(['go-live', 'other'])
    expect(groups.find((group) => group.kind === 'branch')?.results.map((result) => result.label)).toEqual(['task/go-live', 'other-branch'])
    expect(groups.find((group) => group.kind === 'agent')?.results.map((result) => result.label)).toEqual(['golang session s-a1'])
  })

  it('opens the page of each kind', () => {
    const groups = searchPalette(model(goFleet()), 'go', NOW)
    const first = (kind: string) => groupOf(groups, kind).results[0]
    expect(hrefOf(first('task').link)).toBe('/tasks/detail?task=go-live')
    expect(hrefOf(first('repository').link)).toMatch(/^\/repositories\/github\.com\/acme\/go-\d$/)
    expect(hrefOf(first('worktree').link)).toBe('/worktrees/w1')
    expect(hrefOf(first('branch').link)).toBe('/worktrees/w1')
    expect(hrefOf(first('agent').link)).toBe('/agents/a1')
  })

  it('describes each result', () => {
    const groups = searchPalette(model(goFleet()), 'go', NOW)
    expect(groups.find((group) => group.kind === 'worktree')?.results[0]).toMatchObject({ label: 'go-live', detail: 'task/go-live · acme/go-0' })
    expect(groups.find((group) => group.kind === 'branch')?.results[0]).toMatchObject({ detail: 'acme/go-0 · go-live' })
    expect(groups.find((group) => group.kind === 'repository')?.results[0].detail).toBe('alpha')
    expect(groups.find((group) => group.kind === 'task')?.results[0].detail).toContain('acme/go-0')
  })

  it('finds machines, and says which one is this machine', () => {
    const groups = searchPalette(model(fleetDocument()), 'a', NOW)
    const machines = groupOf(groups, 'machine')
    expect(machines.results.map((result) => [result.label, result.detail])).toEqual([
      ['alpha', 'live, this machine'],
      ['beta', 'cached'],
    ])
    expect(hrefOf(machines.results[0].link)).toBe('/machines/mach-alpha')
  })

  it('describes an agent with no task and no repository by its machine alone, and one with both by all three', () => {
    // The run is found by its id, which is not part of its label.
    const groups = searchPalette(model(goFleet()), 'R2', NOW)
    expect(groups.find((group) => group.kind === 'agent')?.results.map((result) => [result.label, result.detail])).toEqual([['run r2', 'alpha']])
    const detailed = searchPalette(model(goFleet()), 'golang', NOW)
    expect(detailed.find((group) => group.kind === 'agent')?.results[0].detail).toContain('acme/go-0')
    expect(searchPalette(model(goFleet()), 's-a1', NOW).find((group) => group.kind === 'agent')?.results).toHaveLength(1)
  })

  it('ranks a whole name before a leading match before a word start before the middle of a word, then in order', () => {
    const repositories = ['acme/xgo', 'acme/go-tools', 'acme/go', 'acme/mygo-x', 'acme/gopher', 'acme/the-go'].map((name, index) => repository(`r${index}`, 'alpha', { name, worktree_count: 0, active_agent_count: 0 }))
    const groups = searchPalette(model(fleetDocument({ repositories, worktrees: [], agents: [] })), 'go', NOW)
    expect(groups[0].results.map((result) => result.label)).toEqual(['acme/go', 'acme/go-tools', 'acme/gopher', 'acme/the-go', 'acme/xgo', 'acme/mygo-x'])
  })

  it('puts the more recently active of two equal matches first', () => {
    const doc = fleetDocument({
      worktrees: [
        { ...worktree('w1', 'r1', 'alpha'), task: 'same-1', last_activity_at: '2026-09-01T00:00:00Z' },
        { ...worktree('w2', 'r1', 'alpha'), task: 'same-2', last_activity_at: '2026-09-30T00:00:00Z' },
        { ...worktree('w3', 'r1', 'alpha'), task: 'same-3' },
      ],
    })
    expect(searchPalette(model(doc), 'same', NOW).find((group) => group.kind === 'task')?.results.map((result) => result.label)).toEqual(['same-2', 'same-1', 'same-3'])
  })

  // cockpit-views#ac:default-sorts
  it('ranks a task at risk for over 14 days last, never out of the results, and offers none for an empty query', () => {
    const days = (count: number): string => new Date(NOW - count * 86_400_000).toISOString()
    const risky = { owner_state: 'orphaned' as const, ahead: 1 }
    const doc = fleetDocument({
      worktrees: [
        { ...worktree('w1', 'r1', 'alpha'), task: 'risk-old', last_activity_at: days(20), ...risky },
        { ...worktree('w2', 'r1', 'alpha'), task: 'risk-new', last_activity_at: days(2), ...risky },
        { ...worktree('w3', 'r1', 'alpha'), task: 'risk-idle', last_activity_at: days(30), owner_state: 'idle' as const },
      ],
    })
    const tasks = (text: string) => searchPalette(model(doc), text, NOW).find((group) => group.kind === 'task')?.results.map((result) => result.label)
    // The old at-risk task comes after a task that is less recent and matches no better.
    expect(tasks('risk')).toEqual(['risk-new', 'risk-idle', 'risk-old'])
    // Finding by name always works, with or without the state term.
    expect(tasks('risk-old')).toEqual(['risk-old'])
    expect(tasks('state:at-risk')).toEqual(['risk-new', 'risk-old'])
    // An empty query suggests nothing from the fleet (the palette shows what was opened before).
    expect(searchPalette(model(doc), '', NOW)).toEqual([])
    expect(resolveResult(model(doc), 'task:risk-old')?.label).toBe('risk-old')
  })

  it('uses the matcher: a glob, an exclusion, a field and an unknown field as plain text', () => {
    const doc = goFleet()
    expect(searchPalette(model(doc), '*go-*', NOW).find((group) => group.kind === 'repository')?.results).toHaveLength(8)
    const excluded = groupOf(searchPalette(model(doc), 'go -go-1', NOW), 'repository')
    expect(excluded.results.map((result) => result.label)).not.toContain('acme/go-1')
    expect(excluded.results.map((result) => result.label)).not.toContain('acme/go-10')
    expect(searchPalette(model(doc), 'repo:plain', NOW).map((group) => group.kind)).toEqual(['repository'])
    expect(searchPalette(model(doc), 'task:go-live', NOW).map((group) => group.kind)).toEqual(['task', 'worktree', 'branch'])
    expect(searchPalette(model(doc), 'nothing:go', NOW)).toEqual([])
  })

  it('shows nothing for no text, only blanks, or no match', () => {
    const doc = model(goFleet())
    expect(searchPalette(doc, '', NOW)).toEqual([])
    expect(searchPalette(doc, '   ', NOW)).toEqual([])
    expect(searchPalette(doc, 'zzzz', NOW)).toEqual([])
  })

  it('lists a branch once however many worktrees hold it, and skips a worktree with no branch', () => {
    const doc = fleetDocument({
      worktrees: [
        { ...worktree('w1', 'r1', 'alpha'), branch: 'shared' },
        { ...worktree('w2', 'r1', 'alpha'), branch: 'shared', last_activity_at: '2026-10-01T09:00:00Z' },
        { ...worktree('w3', 'r1', 'alpha'), branch: '' },
      ],
    })
    const branches = groupOf(searchPalette(model(doc), 'shared', NOW), 'branch')
    expect(branches.results).toHaveLength(1)
    expect(searchPalette(model(doc), 'task-w3', NOW).find((group) => group.kind === 'branch')).toBeUndefined()
  })
})

describe('resolveResult', () => {
  it('reads a remembered id again from the model, for every kind, and says nothing for what is gone', () => {
    const doc = goFleet()
    const current = model(doc)
    const all = searchPalette(current, 'go', NOW).flatMap((group) => group.results)
    for (const kind of ['task', 'repository', 'worktree', 'branch', 'agent']) {
      const shown = all.find((result) => result.kind === kind)!
      expect(resolveResult(current, shown.id), kind).toEqual(shown)
    }
    const machine = searchPalette(model(fleetDocument()), 'alpha', NOW).flatMap((group) => group.results).find((result) => result.kind === 'machine')!
    expect(resolveResult(model(fleetDocument()), machine.id)).toEqual(machine)
    for (const gone of ['task:nothing', 'repository:nothing', 'worktree:nothing', 'branch:x|y', 'agent:nothing', 'machine:nothing', 'nonsense:1']) {
      expect(resolveResult(current, gone), gone).toBeUndefined()
    }
  })

  it('builds a label and a detail only for the results it shows', () => {
    const agents = Array.from({ length: 30 }, (_, index) => agent(`a${index}`, 'r1', 'live', { runtime: 'golang' }))
    const current = model(fleetDocument({ agents }))
    searchPalette(current, 'zzzz', NOW)
    const spy = vi.spyOn(current, 'tasksOfAgent')
    const groups = searchPalette(current, 'golang', NOW)
    expect(groupOf(groups, 'agent').results).toHaveLength(8)
    // The agent detail asks for the agent's task: 8 times, not 30.
    expect(spy).toHaveBeenCalledTimes(8)
  })

  // A GitLab group/sub/project has no host/owner/name address; its result opens by entry id.
  it('links a repository whose name has more than two segments by its entry id', () => {
    const groups = searchPalette(model(fleetDocument({ repositories: [repository('gl-1', 'alpha', { host: 'gitlab.com', name: 'group/sub/project' })], worktrees: [], agents: [] })), 'project', NOW)
    expect(hrefOf(groupOf(groups, 'repository').results[0].link)).toBe('/repositories/gl-1')
  })
})
