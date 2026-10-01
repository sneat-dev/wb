import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { FleetStore } from '@cockpit/fleet-data'
import { fleetDocument, pullRequest, run, worktree } from '@cockpit/fleet-data/testing'
import { WorktreePanelView } from './worktree-panel'

const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()

function render(id: string, page = false, extra: Record<string, unknown> = {}) {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [provideRouter([])] })
  const store = TestBed.inject(FleetStore)
  store.document.set(
    fleetDocument({
      worktrees: [{ ...worktree('w1', 'r1', 'alpha'), task: 'fix-ci', branch: 'topic', ...extra }],
      pull_requests: [pullRequest('p1', 'r1', 'w1', { number: 5 }), pullRequest('p2', 'r1', 'w1', { number: 6, state: undefined, url: 'http://insecure.example/6' })],
      agents: [run('run-1', 'running', { worktrees: ['w1'] })],
    }),
  )
  const fixture = TestBed.createComponent(WorktreePanelView)
  fixture.componentRef.setInput('id', id)
  fixture.componentRef.setInput('page', page)
  return fixture.whenStable().then(() => fixture.nativeElement as HTMLElement)
}

describe('WorktreePanelView', () => {
  it('renders nothing for an id the document does not list', async () => {
    expect((await render('nope')).querySelector('app-panel-content')).toBeNull()
  })

  it('relates the worktree to its task, its pull requests (an address only when it is secure) and its agents', async () => {
    const root = await render('w1')
    expect(text(root.querySelector('[aria-label="Task"] li'))).toContain('fix-ci (')
    expect([...root.querySelectorAll('[aria-label="Pull requests"] li')].map(text)).toEqual(['#5 open', '#6'])
    expect(root.querySelectorAll('[aria-label="Pull requests"] a')).toHaveLength(1)
    expect(root.querySelector('[aria-label="Agents"] a')?.getAttribute('href')).toBe('/agents/run-1')
  })

  it('says in words which sync facts this machine knows', async () => {
    const sync = async (extra: Record<string, unknown>) => text((await render('w1', false, extra)).querySelectorAll('dd')[6])
    expect(await sync({ ahead: 2 })).toBe('2 ahead')
    expect(await sync({ behind: 3 })).toBe('3 behind')
    expect(await sync({ upstream_gone: true })).toBe('upstream gone')
    expect(await sync({ ahead: 1, behind: 1, upstream_gone: true })).toBe('1 ahead, 1 behind, upstream gone')
    expect(await sync({})).toBe('in sync')
  })

  it('is the detail page when asked', async () => {
    expect((await render('w1', true)).querySelector('.content')?.classList.contains('page')).toBe(true)
  })
})
