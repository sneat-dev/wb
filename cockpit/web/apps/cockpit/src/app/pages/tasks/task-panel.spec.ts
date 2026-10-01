import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { FleetDocument, FleetStore, RegistryAction } from '@cockpit/fleet-data'
import { agent, pullRequest, registryAction, worktree } from '@cockpit/fleet-data/testing'
import { tasksDocument } from './tasks-fixture'
import { TaskPanelView } from './task-panel'

const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()

async function render(name: string, document: FleetDocument = tasksDocument(), page = false, registry?: ReadonlyMap<string, readonly RegistryAction[]>): Promise<HTMLElement> {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [provideRouter([])] })
  const store = TestBed.inject(FleetStore)
  store.document.set(document)
  store.now.set(Date.parse('2026-10-01T10:05:00Z'))
  const fixture = TestBed.createComponent(TaskPanelView)
  fixture.componentRef.setInput('name', name)
  fixture.componentRef.setInput('page', page)
  if (registry) fixture.componentRef.setInput('registry', registry)
  await fixture.whenStable()
  return fixture.nativeElement
}

describe('TaskPanelView', () => {
  it('renders nothing for a name the document does not list', async () => {
    expect((await render('nope')).querySelector('app-panel-content')).toBeNull()
  })

  it('is the detail page when asked', async () => {
    expect((await render('fix-ci', tasksDocument(), true)).querySelector('.content')?.classList.contains('page')).toBe(true)
  })

  // cockpit-views#ac:task-detail-shows-its-entities
  it('heads the panel with the state badge and, in words, why: ready, not ready, checks failed, at risk', async () => {
    const reason = async (name: string) => text((await render(name)).querySelector('.state'))
    expect(await reason('add-search')).toContain('ready to land')
    expect(await reason('add-search')).toContain('Ready to land: 2 pull requests green and mergeable')
    expect(await reason('fix-ci')).toContain('Not ready: r1#7 checks pending')
    expect(await reason('broken')).toContain('Checks failed: r4#131 has 1 failing check (build-linux)')
    expect(await reason('zeta')).toContain('At risk: 2 commits only on this machine in r3 (worktree idle)')
    expect(await reason('mystery')).toContain('State not reported')
  })

  it('says how many pull requests were not yet checked, and that the worktrees were all read from another machine', async () => {
    const doc = tasksDocument()
    doc.pull_requests = [...doc.pull_requests, { ...pullRequest('p9', 'r2', 'w7', { number: 3 }), checked_at: undefined, state: undefined }]
    const root = await render('far', doc)
    expect(text(root.querySelector('[aria-label="Pull requests"] .note'))).toBe('1 of 1 not yet checked.')
    expect(text(root.querySelector('[aria-label="Pull requests"] app-pr-chip'))).toContain('not yet checked')
    expect(text(root.querySelector('.note'))).toContain('read from another machine')
    expect((await render('add-search')).querySelector('.note')).toBeNull()
  })

  it('lists the repositories of the task, each linking to its page, the machines, and the last activity', async () => {
    const root = await render('fix-ci')
    const links = [...root.querySelectorAll('dl.facts dd.list')[0].querySelectorAll('a')]
    expect(links.map(text)).toEqual(['acme/r1', 'acme/r3', 'acme/r2'])
    expect(links.map((link) => link.getAttribute('href'))).toEqual(['/repositories/github.com/acme/r1', '/repositories/github.com/acme/r3', '/repositories/-/acme/r2'])
    const machines = text(root.querySelectorAll('dl.facts dd.list')[1])
    expect(machines).toContain('alpha')
    expect(machines).toContain('2 d · stale · ssh')
    expect(text(root.querySelector('dl.facts app-age'))).toMatch(/ago|just now/)
  })

  it('lists each pull request as the control surface\'s chip with its repository, and an action slot for those on this machine only', async () => {
    const doc = tasksDocument()
    doc.pull_requests = [...doc.pull_requests, { ...pullRequest('p5', 'r2', 'w3', { number: 21 }), route: 'cached', machine: 'beta', machine_id: 'mach-beta' }, { ...pullRequest('p6', 'r1', 'w1', { number: 22 }), repository: undefined }]
    const root = await render('fix-ci', doc)
    const items = [...root.querySelectorAll('[aria-label="Pull requests"] li')]
    expect(items).toHaveLength(3)
    expect(text(items[0])).toContain('r1')
    expect(text(items[0])).toContain('#7')
    expect(text(items[0])).toContain('3/4')
    expect(items[0].querySelector('app-action-slot')).not.toBeNull()
    expect(items[1].querySelector('app-action-slot')).toBeNull()
    expect(items[2].querySelector('.repo')).toBeNull()
    expect(items[0].querySelector('a.number')?.getAttribute('href')).toBe('https://github.com/acme/r1/pull/1')
  })

  // cockpit-views#ac:action-area-renders-the-registry-and-vanishes-without-it
  it('renders what the registry returned for each pull request and worktree of this machine, and nothing for a remote entry or an entity the registry does not know', async () => {
    const registry = new Map([
      ['pull_request:p1', [registryAction('pr.land', 'Land', { target_types: ['pull_request'], capability: 'pr.land' })]],
      ['worktree:w1', [registryAction('branch.push', 'Push', { safety: 'guarded' })]],
      ['worktree:w3', [registryAction('branch.push', 'Push remote')]],
    ])
    const root = await render('fix-ci', tasksDocument(), false, registry)
    const buttons = [...root.querySelectorAll('app-action-slot button')].map(text)
    expect(buttons).toEqual(['Land', 'Push'])
    expect(root.querySelector('[aria-label="Worktrees"] li:nth-child(3) app-action-slot')).toBeNull()
    // Nothing is rendered for a pull request the registry did not return anything for.
    expect(root.querySelectorAll('app-action-slot button')).toHaveLength(2)
  })

  it('says None for a task with no pull request', async () => {
    const root = await render('far')
    expect(text(root.querySelector('[aria-label="Pull requests"] li'))).toBe('None')
  })

  it('lists the worktrees compactly: repository linking to the worktree page, the branch when it differs, the machine, owner state and sync badges, last activity', async () => {
    const doc = tasksDocument()
    doc.worktrees[0] = { ...doc.worktrees[0], ahead: 2, behind: 1 }
    const root = await render('fix-ci', doc)
    const items = [...root.querySelectorAll('[aria-label="Worktrees"] li')]
    expect(items.map((item) => text(item.querySelector('a.repo-link')))).toEqual(['acme/r1', 'acme/r3', 'acme/r2'])
    expect(items[0].querySelector('a.repo-link')?.getAttribute('href')).toBe('/worktrees/w1')
    expect(items[0].querySelector('.branch')).toBeNull()
    expect(text(items[2].querySelector('.branch'))).toBe('topic/fix-ci')
    expect(text(items[0])).toContain('alpha')
    expect(text(items[2].querySelector('app-machine-cell'))).toBe('beta2 d · stale · ssh')
    expect(text(items[0])).toContain('active')
    expect(text(items[0])).toContain('↑2')
    expect(text(items[0])).toContain('↓1')
    // A worktree read from another machine has no sync badge and no action slot.
    expect(items[2].querySelector('app-sync-badges')).toBeNull()
    expect(items[2].querySelector('app-action-slot')).toBeNull()
    expect(items[0].querySelector('app-action-slot')).not.toBeNull()
    expect(text(items[0].querySelector('app-age'))).toMatch(/ago|just now/)
  })

  it('leaves the machine out of the rows on a fleet of one machine', async () => {
    const doc = tasksDocument()
    const single = { ...doc, machines: [doc.machines[0]], worktrees: doc.worktrees.filter((w) => w.machine === 'alpha'), agents: [agent('a1', 'r1', 'live', { worktrees: ['w1'], runtime: 'claude' })] }
    const root = await render('fix-ci', single)
    expect(root.querySelector('[aria-label="Worktrees"] app-machine-cell')).toBeNull()
    expect(root.querySelector('[aria-label="Agents"] app-machine-cell')).toBeNull()
  })

  it('lists the agents with their title linking to the agent, the activity or "not reported", and the machine', async () => {
    const doc = tasksDocument()
    doc.agents = [agent('a1', 'r1', 'live', { worktrees: ['w1'], runtime: 'claude', activity: 'working' }), agent('a2', 'r1', 'live', { worktrees: ['w1'], route: 'cached', machine: 'beta', machine_id: 'mach-beta' })]
    const root = await render('fix-ci', doc)
    const items = [...root.querySelectorAll('[aria-label="Agents"] li')]
    expect(items.map((item) => text(item.querySelector('a')))).toEqual(['claude', 'agent'])
    expect(items[0].querySelector('a')?.getAttribute('href')).toBe('/agents/a1')
    expect(text(items[0])).toContain('working')
    expect(text(items[1])).toContain('not reported')
    expect(text(items[1])).toContain('beta')
  })

  it('says None for a task with no agent', async () => {
    expect(text((await render('zeta')).querySelector('[aria-label="Agents"] li'))).toBe('None')
  })

  // cockpit-views#ac:copy-command-uses-only-existing-commands-and-identifiers
  it('offers the library\'s task commands and wb pr land for each open pull request, labelled where each runs', async () => {
    const root = await render('fix-ci')
    const entries = [...root.querySelectorAll('[aria-label="Copy command"] li')]
    expect(entries.map((entry) => text(entry.querySelector('.title')))).toEqual(['List worktrees', 'Commit and open pull request', 'Plan cleanup (dry run)', 'Land acme/r1#7'])
    expect(text(entries[3].querySelector('code'))).toContain('wb pr land')
    expect(text(entries[3].querySelector('code'))).toContain("'acme/r1#7'")
    expect(text(entries[1])).toContain('edit before running')
    expect(root.textContent).not.toMatch(/--apply/)
    const far = await render('far')
    expect(text(far.querySelector('[aria-label="Copy command"] li .where'))).toContain('run on beta')
    const merged = tasksDocument()
    merged.pull_requests = [pullRequest('p1', 'r1', 'w1', { number: 7, state: 'merged' })]
    expect([...(await render('fix-ci', merged)).querySelectorAll('[aria-label="Copy command"] li')]).toHaveLength(3)
  })

  // cockpit-views#ac:task-state-ready-to-land
  it('says a task only another machine reports is as reported by that machine, with its machine chip, and offers no land, push or action slot', async () => {
    const doc = tasksDocument()
    doc.pull_requests = [{ ...pullRequest('p9', 'r2', 'w7', { number: 40 }), route: 'cached', machine: 'beta', machine_id: 'mach-beta' }]
    // Even a registry that offers actions for its entries gets no slot: nothing here is on this machine.
    const offered = new Map([['pull_request:p9', [registryAction('pr.land', 'Land', { target_types: ['pull_request'] })]], ['worktree:w7', [registryAction('branch.push', 'Push')]]])
    const root = await render('far', doc, false, offered)
    expect(text(root.querySelector('.state .why'))).toContain('Ready to land: 1 pull request')
    expect(text(root.querySelector('.state .source'))).toBe('As reported bybeta2 d · stale · ssh(not this machine): nothing to land or push from here.')
    expect(root.querySelector('app-action-slot')).toBeNull()
    const titles = [...root.querySelectorAll('[aria-label="Copy command"] li .title')].map(text)
    expect(titles).toEqual(['List worktrees', 'Plan cleanup (dry run)'])
    expect(root.textContent).not.toContain('wb pr land')
    expect(root.textContent).not.toContain('wb pr create')
    expect(text(root.querySelector('[aria-label="Copy command"]'))).toContain('run on beta')
  })

  // cockpit-views#ac:task-state-ready-to-land
  it('does not let a remote entry of the same name make a local task ready or landed, and labels the remote facts with their machine', async () => {
    const doc = tasksDocument()
    // `add-search` is local and has no pull request here; beta reports a ready and a merged pull request for the same name.
    doc.pull_requests = [
      { ...pullRequest('p8', 'r2', undefined, { number: 50 }), route: 'cached', machine: 'beta', machine_id: 'mach-beta', repository: 'r2', worktree: 'w-remote' },
      { ...pullRequest('p9', 'r2', undefined, { number: 51, state: 'merged' }), route: 'cached', machine: 'beta', machine_id: 'mach-beta', worktree: 'w-remote' },
    ]
    doc.worktrees = [...doc.worktrees, { ...worktree('w-remote', 'r2', 'beta'), task: 'add-search', route: 'cached', owner_state: 'idle', observed_at: '2026-09-29T10:00:00Z' }]
    const root = await render('add-search', doc)
    const header = text(root.querySelector('.state'))
    expect(header).toContain('not ready')
    expect(header).not.toContain('ready to land')
    expect(header).toContain('no open pull request is on this machine')
    expect(root.querySelector('.source')).toBeNull()
    const prs = [...root.querySelectorAll('[aria-label="Pull requests"] li')]
    expect(prs).toHaveLength(2)
    for (const item of prs) expect(text(item.querySelector('app-machine-cell'))).toContain('beta')
    expect(prs.every((item) => item.querySelector('app-action-slot') === null)).toBe(true)
  })

  it('says a local task whose only claim to landing is a remote merged pull request is idle', async () => {
    const doc = tasksDocument()
    doc.pull_requests = [{ ...pullRequest('p9', 'r2', undefined, { number: 51, state: 'merged' }), route: 'cached', machine: 'beta', machine_id: 'mach-beta', worktree: 'w-remote' }]
    doc.worktrees = [...doc.worktrees, { ...worktree('w-remote', 'r2', 'beta'), task: 'zeta2', route: 'cached', owner_state: 'idle' }, { ...worktree('w-local', 'r1', 'alpha'), task: 'zeta2', owner_state: 'idle', ahead: 0, has_upstream: true }]
    const root = await render('zeta2', doc)
    expect(text(root.querySelector('.state'))).toContain('Idle:')
    expect(text(root.querySelector('.state'))).not.toContain('Landed')
  })

  it('ends with the collapsed Raw data holding the task\'s entries as sent', async () => {
    const root = await render('zeta')
    const details = root.querySelector('details.raw') as HTMLDetailsElement
    expect(details.open).toBe(false)
    expect(root.querySelector('pre')).toBeNull()
    details.open = true
    details.dispatchEvent(new Event('toggle'))
    TestBed.tick()
    expect(root.querySelector('pre')?.textContent).toContain('"id": "w5"')
  })

  it('shows no action area when the registry offers nothing, and no gap', async () => {
    const root = await render('zeta')
    const slots = [...root.querySelectorAll('app-action-slot')]
    expect(slots.every((slot) => (slot.textContent ?? '').trim() === '')).toBe(true)
    expect(root.querySelector('section.actions')?.children).toHaveLength(0)
  })

  it('handles a task with a worktree in a repository the document does not list', async () => {
    const doc = tasksDocument()
    doc.worktrees = [{ ...worktree('wx', 'r-gone', 'alpha'), task: 'lost', owner_state: 'idle' }]
    doc.pull_requests = []
    doc.agents = []
    const root = await render('lost', doc)
    expect(text(root.querySelector('dl.facts a'))).toBe('r-gone')
    expect(root.querySelector('dl.facts a')?.getAttribute('href')).toBe('/repositories/-/r-gone')
  })
})
