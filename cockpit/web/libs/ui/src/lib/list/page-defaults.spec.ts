import { LIST_PAGES, FleetStore } from '@cockpit/fleet-data'
import { VOCABULARY, buildAgentRows, buildMachineRows, buildRepositoryRows, buildTaskRows, buildWorktreeRows } from '@cockpit/fleet-data/list'
import { agent, fleetDocument, worktree } from '@cockpit/fleet-data/testing'
import { TestBed } from '@angular/core/testing'
import { PAGE_NOUN, chipsOf, copyOf, detailOf, nameOf, rowsOf } from './page-defaults'
import { repository } from '@cockpit/fleet-data/testing'

describe('page defaults', () => {
  it('takes the chips of a page from the vocabulary, and names every page', () => {
    for (const page of LIST_PAGES) {
      expect(chipsOf(page)).toEqual(VOCABULARY[page].chips)
      expect(PAGE_NOUN[page]).toBeTruthy()
    }
    expect(chipsOf('worktrees').find((chip) => chip.id === 'unpushed')?.hint).toContain('not pushed')
  })

  it('takes the rows, the address of the page and the name of an entity from the page alone', () => {
    const store = TestBed.inject(FleetStore)
    store.document.set(fleetDocument({ worktrees: [{ ...worktree('w1', 'r1', 'alpha'), task: 'fix-ci' }], agents: [agent('a1', 'r1', 'running', { runtime: 'claude' })] }))
    const model = store.model()
    const first = (page: (typeof LIST_PAGES)[number]) => rowsOf(model, page)[0].item
    expect(rowsOf(model, 'tasks')).toBe(buildTaskRows(model))
    expect(rowsOf(model, 'repositories')).toBe(buildRepositoryRows(model))
    expect(rowsOf(model, 'worktrees')).toBe(buildWorktreeRows(model))
    expect(rowsOf(model, 'agents')).toBe(buildAgentRows(model))
    expect(rowsOf(model, 'machines')).toBe(buildMachineRows(model))
    expect(detailOf('tasks', first('tasks'))).toEqual({ path: '/tasks/detail', query: { task: 'fix-ci' } })
    expect(detailOf('worktrees', first('worktrees')).path).toBe('/worktrees/w1')
    expect(detailOf('repositories', first('repositories')).path).toMatch(/^\/repositories\/github\.com\/acme\/r1$/)
    expect(detailOf('agents', first('agents')).path).toBe('/agents/a1')
    expect(detailOf('machines', first('machines')).path).toBe('/machines/mach-alpha')
    expect(nameOf('tasks', first('tasks'))).toBe('fix-ci')
    expect(nameOf('worktrees', first('worktrees'))).toBe('fix-ci')
    expect(nameOf('repositories', first('repositories'))).toBe('acme/r1')
    expect(nameOf('agents', first('agents'))).toContain('claude')
    expect(nameOf('machines', first('machines'))).toBe('alpha')
  })

  // A GitLab group/sub/project has no /repositories/:host/:owner/:name address: its row opens by entry id.
  it('opens a repository whose name has more than two segments by its entry id, and every other by host, owner and name', () => {
    const store = TestBed.inject(FleetStore)
    store.document.set(fleetDocument({ repositories: [repository('gl-1', 'alpha', { host: 'gitlab.com', name: 'group/sub/project' }), repository('r2', 'alpha', { name: 'acme/r2' })] }))
    const rows = rowsOf(store.model(), 'repositories').map((row) => detailOf('repositories', row.item))
    expect(rows.map((link) => link.path).sort()).toEqual(['/repositories/github.com/acme/r2', '/repositories/gl-1'])
  })

  it('copies a name with c, except for an agent, whose run or session id is what a command takes', () => {
    expect(copyOf('agents', agent('a1', 'r1', 'running', { runtime: 'claude' }))).toBe('a1')
    expect(copyOf('agents', agent('a2', 'r1', 'running', { run_id: 'run-7' }))).toBe('run-7')
    expect(copyOf('repositories', { slug: 'acme/r1' })).toBe('acme/r1')
    expect(copyOf('tasks', { name: 'fix-ci' })).toBe('fix-ci')
  })
})
