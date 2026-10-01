import { LIST_PAGES, FleetStore } from '@cockpit/fleet-data'
import { VOCABULARY, buildAgentRows, buildMachineRows, buildRepositoryRows, buildTaskRows, buildWorktreeRows } from '@cockpit/fleet-data/list'
import { agent, fleetDocument, worktree } from '@cockpit/fleet-data/testing'
import { TestBed } from '@angular/core/testing'
import { PAGE_NOUN, chipsOf, detailOf, nameOf, rowsOf } from './page-defaults'

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
})
