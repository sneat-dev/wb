import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { FleetStore } from '@cockpit/fleet-data'
import { RegistryAction } from '@cockpit/fleet-data'
import { fleetDocument, pullRequest, registryAction, run, worktree } from '@cockpit/fleet-data/testing'
import { WorktreePanelView } from './worktree-panel'

const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()

function render(id: string, page = false, extra: Record<string, unknown> = {}, registry?: ReadonlyMap<string, readonly RegistryAction[]>) {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [provideRouter([])] })
  const store = TestBed.inject(FleetStore)
  store.document.set(
    fleetDocument({
      worktrees: [{ ...worktree('w1', 'r1', 'alpha'), task: 'fix-ci', branch: 'topic', ...extra }],
      pull_requests: [pullRequest('p1', 'r1', 'w1', { number: 5 }), pullRequest('p2', 'r1', 'w1', { number: 6, state: undefined, url: 'javascript:alert(1)' })],
      agents: [run('run-1', 'running', { worktrees: ['w1'] })],
    }),
  )
  const fixture = TestBed.createComponent(WorktreePanelView)
  fixture.componentRef.setInput('id', id)
  fixture.componentRef.setInput('page', page)
  if (registry) fixture.componentRef.setInput('registry', registry)
  return fixture.whenStable().then(() => fixture.nativeElement as HTMLElement)
}

describe('WorktreePanelView', () => {
  it('renders nothing for an id the document does not list', async () => {
    expect((await render('nope')).querySelector('app-panel-content')).toBeNull()
  })

  it('relates the worktree to its task, its pull requests (an address only when it is a web address) and its agents', async () => {
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

  // cockpit-views#ac:worktree-panel-words
  it('says the owner and the lifecycle in words, never the raw values', async () => {
    const state = async (extra: Record<string, unknown>) => text((await render('w1', false, extra)).querySelectorAll('dd')[5])
    expect(await state({ owner_state: 'active', lifecycle: 'working' })).toBe('active; in progress')
    expect(await state({ owner_state: 'orphaned', lifecycle: 'review' })).toBe('orphaned, its owner process is gone; in review')
    expect(await state({ owner_state: 'idle', lifecycle: 'merged' })).toBe('idle, no running owner; merged')
    expect(await state({ owner_state: 'unknown', lifecycle: 'superseded' })).toBe('owner not recorded; superseded')
    // Not reported, or outside the vocabulary: said so, and no raw value on the page.
    expect(await state({ owner_state: undefined, lifecycle: undefined })).toBe('owner not reported')
    const odd = await render('w1', false, { owner_state: 'zombie', lifecycle: 'in_progress' })
    expect(text(odd.querySelectorAll('dd')[5])).toBe('owner not reported')
    expect(odd.textContent).not.toContain('in_progress')
    expect(odd.textContent).not.toContain('zombie')
  })

  // cockpit-views#ac:copy-command-uses-only-existing-commands-and-identifiers
  it('offers the worktree commands, this branch\'s commands as a dry-run plan, and wb pr land for each open pull request', async () => {
    const root = await render('w1')
    const entries = [...root.querySelectorAll('[aria-label="Copy command"] li')]
    expect(entries.map((entry) => text(entry.querySelector('.title')))).toEqual(['List worktrees', 'Commit and open pull request', 'Plan cleanup (dry run)', 'List this branch', 'Plan branch cleanup (dry run)', 'Land acme/r1#5'])
    const code = entries.map((entry) => text(entry.querySelector('code')))
    expect(code[3]).toContain("wb branch list --repo='acme/r1' --branch='topic'")
    expect(code[4]).toContain("wb branch cleanup --repo='acme/r1' --branch='topic'")
    expect(code[5]).toContain("wb pr land 'acme/r1#5'")
    expect(root.textContent).not.toContain('--apply')
  })

  // cockpit-views#ac:action-area-renders-the-registry-and-vanishes-without-it
  it('gives the slots of this worktree and its pull requests the registry\'s answers, draws no button for them without a handler, and has no action area without them', async () => {
    const registry = new Map([
      ['worktree:w1', [registryAction('branch.push', 'Push')]],
      ['pull_request:p1', [registryAction('pr.land', 'Land', { target_types: ['pull_request'] })]],
    ])
    const root = await render('w1', false, {}, registry)
    expect(root.querySelectorAll('section.actions app-action-slot')).toHaveLength(2)
    // No page handles an action: a slot is its Copy control or nothing, never the registry's button.
    expect(root.querySelectorAll('section.actions app-action-slot button')).toHaveLength(0)
    expect((await render('w1')).querySelector('section.actions')?.children).toHaveLength(0)
    expect((await render('w1', false, {}, new Map())).querySelector('section.actions')?.children).toHaveLength(0)
    // A worktree read from another machine has no slot of its own; its pull request on this machine keeps one.
    expect((await render('w1', false, { route: 'cached' }, registry)).querySelectorAll('section.actions app-action-slot')).toHaveLength(1)
  })

  it('is the detail page when asked', async () => {
    expect((await render('w1', true)).querySelector('.content')?.classList.contains('page')).toBe(true)
  })
})
