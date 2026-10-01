import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { FleetDocument, FleetStore } from '@cockpit/fleet-data'
import { agent, pullRequest, worktree } from '@cockpit/fleet-data/testing'
import { tasksDocument } from './tasks-fixture'
import { TaskPanelView } from './task-panel'

const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()

async function render(name: string, document: FleetDocument = tasksDocument(), page = false): Promise<HTMLElement> {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [provideRouter([])] })
  const store = TestBed.inject(FleetStore)
  store.document.set(document)
  store.now.set(Date.parse('2026-10-01T10:05:00Z'))
  const fixture = TestBed.createComponent(TaskPanelView)
  fixture.componentRef.setInput('name', name)
  fixture.componentRef.setInput('page', page)
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
    expect(await reason('add-search')).toBe('ready to land Task state: Ready to land: 2 pull requests green and mergeable'.replace('Task state: ', '').replace(/^ready to land /, 'Task state: ready to land '))
    expect(await reason('fix-ci')).toContain('Not ready: acme/r1#7 checks pending'.replace('acme/r1', 'r1'))
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
    expect(machines).toContain('beta · 2 d · stale · ssh')
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
    expect(text(items[2])).toContain('beta · 2 d · stale · ssh')
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
    expect(getComputedStyle(root.querySelector('section.actions') as Element).display).toBe('none')
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
