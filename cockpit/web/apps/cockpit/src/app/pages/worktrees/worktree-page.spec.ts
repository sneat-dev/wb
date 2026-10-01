import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { fleetDocument, worktree } from '@cockpit/fleet-data/testing'
import { WorktreePage } from './worktree-page'
import { fields, openPage } from '../test-harness'

const text = (element: Element | null) => (element?.textContent as string).replace(/\s+/g, ' ').trim()

const stats = { indexed: true, files: 13, symbols: 41, edges: 91, kinds: [{ kind: 'method', count: 41 }] }

function documentWith() {
  return fleetDocument({
    code_index_provider: 'codegrapher',
    worktrees: [
      {
        ...worktree('w1', 'r1', 'alpha'),
        stream: 'stream-a',
        lifecycle: 'in_progress',
        owner_state: 'active',
        last_activity_at: '2026-10-01T10:00:00Z',
        code_index: [{ indexer: 'codegrapher', state: 'fresh', statistics: stats }],
      },
      { ...worktree('w3', 'r2', 'beta'), route: 'cached' as const },
      worktree('w4', 'r-gone', 'alpha'),
    ],
  })
}

describe('WorktreePage', () => {
  it('shows the entry\'s metadata from the fleet document, linking to its repository', async () => {
    const { root, component } = await openPage('/worktrees/w1', WorktreePage, documentWith())
    expect(component.id()).toBe('w1')
    expect(root.querySelector('h2')?.textContent).toBe('task-w1')
    expect(fields(root)).toEqual({
      Branch: 'branch-w1',
      Stream: 'stream-a',
      Repository: 'github.com/acme/r1',
      Machine: 'alpha',
      Source: 'local',
      Lifecycle: 'in_progress',
      Owner: 'active',
      'Last activity': '5 min ago',
      'Code index': 'fresh',
    })
    expect(root.querySelector('a.row-link')?.getAttribute('href')).toBe('/repositories/r1')
    expect(root.querySelector('.back a')?.getAttribute('href')).toBe('/worktrees')
  })

  // cockpit#ac:code-index-panel
  it('opens with no owner session and shows the totals and the per-kind breakdown', async () => {
    const { root } = await openPage('/worktrees/w1', WorktreePage, documentWith())
    const panel = root.querySelector('app-code-index-panel') as HTMLElement
    expect([...panel.querySelectorAll('.totals div')].map(text)).toEqual(['Files13', 'Symbols41', 'Edges91'])
    expect([...panel.querySelectorAll('.kinds li')].map(text)).toEqual(['method41'])
  })

  it('shows dashes for what is not known, a repository that is not listed as plain text, and a cached worktree as not known', async () => {
    const cached = await openPage('/worktrees/w3', WorktreePage, documentWith())
    expect(text(cached.root.querySelector('app-code-index-panel'))).toContain('another machine')
    expect(fields(cached.root)).toMatchObject({ Stream: '—', Repository: 'acme/r2', Machine: 'beta', Lifecycle: '—', Owner: '—', 'Last activity': '—' })
    const unlisted = await openPage('/worktrees/w4', WorktreePage, documentWith())
    expect(unlisted.root.querySelector('a.row-link')).toBeNull()
    expect(fields(unlisted.root)['Repository']).toBe('r-gone')
  })

  it('says not indexed for a checkout with no statistics, and no provider when none is configured', async () => {
    const plain = await openPage('/worktrees/w4', WorktreePage, documentWith())
    expect(text(plain.root.querySelector('app-code-index-panel'))).toContain('Not indexed.')
    const none = await openPage('/worktrees/w1', WorktreePage, { ...documentWith(), code_index_provider: undefined })
    expect(text(none.root.querySelector('app-code-index-panel'))).toContain('No code-index provider is configured.')
  })

  it('says it is waiting for the snapshot, and that the worktree is not there once it is complete', async () => {
    const warming = await openPage('/worktrees/w1', WorktreePage, fleetDocument({ warming_up: true, worktrees: [] }))
    expect(text(warming.root)).toContain('Waiting for the fleet snapshot')
    const gone = await openPage('/worktrees/w-gone', WorktreePage, documentWith())
    expect(text(gone.root)).toContain('This worktree is not in the fleet document')
    const unread = await openPage('/worktrees/w1', WorktreePage, fleetDocument({ worktrees: [] }))
    unread.store.loaded.set(false)
    unread.harness.detectChanges()
    expect(text(unread.root)).toContain('Waiting for the fleet snapshot')
  })

  it('renders on its own over an empty, warming-up fleet', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([])] })
    const fixture = TestBed.createComponent(WorktreePage)
    fixture.componentRef.setInput('id', 'anything')
    await fixture.whenStable()
    expect(fixture.nativeElement.querySelector('h2')?.textContent).toBe('Worktree')
    expect(fixture.nativeElement.textContent).toContain('Waiting for the fleet snapshot')
  })
})
