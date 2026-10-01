import { TestBed } from '@angular/core/testing'
import { FleetDocument, FleetStore, hrefOf } from '@cockpit/fleet-data'
import { agent, fleetDocument, repository, worktree } from '@cockpit/fleet-data/testing'
import { PALETTE_KINDS, RESULTS_PER_KIND, searchPalette } from './palette-search'

const NOW = Date.parse('2026-10-01T10:05:00Z')

function model(document: FleetDocument) {
  const store = TestBed.inject(FleetStore)
  store.document.set(document)
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
    const repositories = groups.find((group) => group.kind === 'repository')!
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
    const first = (kind: string) => groups.find((group) => group.kind === kind)!.results[0]
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
    const machines = groups.find((group) => group.kind === 'machine')!
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

  it('uses the matcher: a glob, an exclusion, a field and an unknown field as plain text', () => {
    const doc = goFleet()
    expect(searchPalette(model(doc), '*go-*', NOW).find((group) => group.kind === 'repository')?.results).toHaveLength(8)
    const excluded = searchPalette(model(doc), 'go -go-1', NOW).find((group) => group.kind === 'repository')!
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
    const branches = searchPalette(model(doc), 'shared', NOW).find((group) => group.kind === 'branch')!
    expect(branches.results).toHaveLength(1)
    expect(searchPalette(model(doc), 'task-w3', NOW).find((group) => group.kind === 'branch')).toBeUndefined()
  })
})
