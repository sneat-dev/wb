import { By } from '@angular/platform-browser'
import { Router, provideRouter } from '@angular/router'
import { TestBed } from '@angular/core/testing'
import { CodeIndex } from '@cockpit/fleet-data'
import { fleetDocument, worktree } from '@cockpit/fleet-data/testing'
import { FilterBar } from '@cockpit/ui'
import { WorktreesPage } from './worktrees-page'
import { bodyRows, openPage } from './test-harness'

describe('WorktreesPage', () => {
  it('lists every worktree with its repository, machine and route', async () => {
    const { root } = await openPage('/worktrees', WorktreesPage)
    expect(bodyRows(root).map((row) => row.slice(0, 6))).toEqual([
      ['task-w1', 'branch-w1', 'github.com/acme/r1', 'alpha', 'local', '—'],
      ['task-w2', 'branch-w2', 'github.com/acme/r1', 'alpha', 'local', '—'],
      ['task-w3', 'branch-w3', 'acme/r2', 'beta', 'local', '—'],
    ])
  })

  it('links each worktree to its page', async () => {
    const { root } = await openPage('/worktrees', WorktreesPage)
    expect([...root.querySelectorAll('a.row-link')].map((link) => link.getAttribute('href'))).toEqual(['/worktrees/w1', '/worktrees/w2', '/worktrees/w3'])
  })

  // cockpit#ac:code-index-freshness-appears
  it('shows fresh, stale with its count, and never, and a dash when not known', async () => {
    const withIndex = (id: string, code_index?: CodeIndex[]) => ({ ...worktree(id, 'r1', 'alpha'), code_index })
    const doc = fleetDocument({
      worktrees: [
        withIndex('w1', [{ indexer: 'codegrapher', state: 'fresh' }]),
        withIndex('w2', [{ indexer: 'codegrapher', state: 'stale', behind: 3 }]),
        withIndex('w3', [{ indexer: 'codegrapher', state: 'never' }]),
        withIndex('w4'),
      ],
    })
    const { root } = await openPage('/worktrees', WorktreesPage, doc)
    expect(bodyRows(root).map((row) => row[5])).toEqual(['fresh', 'stale, 3 behind', 'never', '—'])
  })

  it('shows lifecycle, owner and the age of the last activity, or a dash', async () => {
    const full = { ...worktree('w1', 'r1', 'alpha'), lifecycle: 'in_progress', owner_state: 'active' as const, last_activity_at: '2026-10-01T10:00:00Z' }
    const { root } = await openPage('/worktrees', WorktreesPage, fleetDocument({ worktrees: [full, worktree('w2', 'r1', 'alpha')] }))
    expect(bodyRows(root).map((row) => row.slice(6))).toEqual([
      ['in_progress', 'active', '5 min ago'],
      ['—', '—', '—'],
    ])
  })

  it('shows exactly the worktrees of the repository a count opened', async () => {
    const { root, component } = await openPage('/worktrees?repository=r1', WorktreesPage)
    expect(bodyRows(root)).toHaveLength(2)
    expect(component.repository()).toBe('r1')
  })

  it('filters by machine, and says when nothing matches or nothing exists', async () => {
    const { root } = await openPage('/worktrees?machine=mach-beta', WorktreesPage)
    expect(bodyRows(root).map((row) => row[0])).toEqual(['task-w3'])
    const none = await openPage('/worktrees?machine=mach-gamma', WorktreesPage)
    expect(bodyRows(none.root)).toEqual([['No worktrees match the filters.']])
    const empty = await openPage('/worktrees', WorktreesPage, fleetDocument({ worktrees: [] }))
    expect(bodyRows(empty.root)).toEqual([['No worktrees yet.']])
  })

  it('names the repository by its id when the document does not list it', async () => {
    const { root } = await openPage('/worktrees', WorktreesPage, fleetDocument({ worktrees: [worktree('w1', 'r-gone', 'alpha')] }))
    expect(bodyRows(root)[0][2]).toBe('r-gone')
  })

  it('writes a changed filter into the URL query', async () => {
    const { harness } = await openPage('/worktrees', WorktreesPage)
    harness.fixture.debugElement.query(By.directive(FilterBar)).componentInstance.changed.emit({ key: 'repository', value: 'r2' })
    await harness.fixture.whenStable()
    expect(TestBed.inject(Router).url).toBe('/worktrees?repository=r2')
  })

  it('renders on its own over an empty, warming-up fleet', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([])] })
    const fixture = TestBed.createComponent(WorktreesPage)
    await fixture.whenStable()
    expect(fixture.nativeElement.querySelector('h2')?.textContent).toBe('Worktrees')
    expect(fixture.nativeElement.querySelectorAll('tbody tr').length).toBeGreaterThan(0)
  })
})
