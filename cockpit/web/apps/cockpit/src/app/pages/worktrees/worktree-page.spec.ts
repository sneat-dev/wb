import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { fleetDocument, worktree } from '@cockpit/fleet-data/testing'
import { WorktreePage } from './worktree-page'
import { openPage } from '../test-harness'

const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()
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
    ],
  })
}

describe('WorktreePage', () => {
  // cockpit-views#ac:detail-routes-render-the-same-panel
  it('is the side panel\'s content, from the same component, ending with the collapsed Raw data block', async () => {
    const page = await openPage('/worktrees/w1', WorktreePage, documentWith())
    const list = await openPage('/worktrees?sel=w1', (await import('./worktrees-page')).WorktreesPage, documentWith())
    const content = (root: HTMLElement) => root.querySelector('app-worktree-panel') as HTMLElement
    // The same facts, related entities and commands in the page as in the panel.
    for (const selector of ['dl.facts', '[aria-label="Task"]', '[aria-label="Copy command"]', 'app-code-index-panel']) {
      expect(text(content(page.root).querySelector(selector)), selector).toBe(text(content(list.root).querySelector(selector)))
    }
    expect(content(page.root).querySelector('.content')?.classList.contains('page')).toBe(true)
    expect(content(list.root).querySelector('.content')?.classList.contains('page')).toBe(false)
    expect(text(content(page.root).querySelector('h2'))).toBe('task-w1')
    expect(page.root.querySelector('.back a')?.getAttribute('href')).toBe('/worktrees')
    const blocks = [...content(page.root).querySelectorAll('section')]
    expect(blocks.at(-1)?.getAttribute('aria-label')).toBe('Raw data')
    expect((content(page.root).querySelector('details') as HTMLDetailsElement).open).toBe(false)
  })

  // cockpit-views#ac:code-index-panel
  it('opens with no owner session and shows the totals and the per-kind breakdown', async () => {
    const { root } = await openPage('/worktrees/w1', WorktreePage, documentWith())
    const panel = root.querySelector('app-code-index-panel') as HTMLElement
    expect([...panel.querySelectorAll('.totals div')].map(text)).toEqual(['Files13', 'Symbols41', 'Edges91'])
    expect([...panel.querySelectorAll('.kinds li')].map(text)).toEqual(['method41'])
  })

  it('says another machine\'s worktree has no code index here, and links the repository', async () => {
    const cached = await openPage('/worktrees/w3', WorktreePage, documentWith())
    expect(text(cached.root.querySelector('app-code-index-panel'))).toContain('another machine')
    expect(cached.root.querySelector('dl.facts a[href="/repositories/-/acme/r2"]')).not.toBeNull()
  })

  it('says it is waiting for the snapshot, and that the worktree is not there once it is complete', async () => {
    const warming = await openPage('/worktrees/w1', WorktreePage, fleetDocument({ warming_up: true, worktrees: [] }))
    expect(text(warming.root)).toContain('Waiting for the fleet snapshot')
    const gone = await openPage('/worktrees/w-gone', WorktreePage, documentWith())
    expect(text(gone.root)).toContain('This worktree is not in the fleet document')
    expect(gone.root.querySelector('app-worktree-panel')).toBeNull()
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
    expect(fixture.nativeElement.textContent).toContain('Waiting for the fleet snapshot')
  })
})
