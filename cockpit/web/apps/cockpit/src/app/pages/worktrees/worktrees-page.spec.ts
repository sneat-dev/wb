import { TestBed } from '@angular/core/testing'
import { Router, provideRouter } from '@angular/router'
import { ClipboardWriter } from '@cockpit/ui/control'
import { FleetDocument } from '@cockpit/fleet-data'
import { VOCABULARY } from '@cockpit/fleet-data/list'
import { fleetDocument, pullRequest, worktree } from '@cockpit/fleet-data/testing'
import { LIST_SHORTCUTS } from '@cockpit/ui/list-host'
import { Shortcuts } from '../../shortcuts/shortcuts'
import { NOW, openPage } from '../test-harness'
import { WorktreesPage } from './worktrees-page'

const DAY = 24 * 60 * 60 * 1000
const ago = (days: number) => new Date(NOW - days * DAY).toISOString()

/** Worktrees that are active with sync badges and a pull request, idle on the task's own branch, orphaned and gone, and idle for 31 days on another machine. */
function documentOf(): FleetDocument {
  return fleetDocument({
    worktrees: [
      { ...worktree('w1', 'r1', 'alpha'), task: 'fix-ci', branch: 'topic', owner_state: 'active', ahead: 2, behind: 1, lifecycle: 'in_progress', last_activity_at: ago(1), code_index: [{ indexer: 'codegrapher', state: 'fresh' }] },
      { ...worktree('w2', 'r1', 'alpha'), task: 'add-search', branch: 'add-search', owner_state: 'idle', last_activity_at: ago(2) },
      { ...worktree('w3', 'r1', 'alpha'), task: 'zeta', branch: 'zeta', owner_state: 'orphaned', upstream_gone: true, last_activity_at: ago(3) },
      { ...worktree('w4', 'r2', 'beta'), task: 'far', branch: 'far', route: 'cached', owner_state: 'idle', last_activity_at: ago(31), observed_at: ago(0) },
    ],
    pull_requests: [pullRequest('p1', 'r1', 'w1', { number: 7 })],
  })
}

const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()
const rowsOf = (root: HTMLElement) => [...root.querySelectorAll<HTMLElement>('[role=row][data-index]')]
const tasksOf = (root: HTMLElement) => rowsOf(root).map((row) => text(row.querySelector('.name')))
const headersOf = (root: HTMLElement) => [...root.querySelectorAll('.head [role=columnheader]:not(.open-cell)')].map(text)
const cell = (row: HTMLElement, index: number) => row.querySelectorAll<HTMLElement>('[role=gridcell]')[index]

describe('WorktreesPage', () => {
  it('renders on its own over an empty, warming-up fleet, with placeholder rows', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([]), { provide: LIST_SHORTCUTS, useValue: { registerFilter: () => () => undefined, registerPanel: () => () => undefined } }] })
    const fixture = TestBed.createComponent(WorktreesPage)
    await fixture.whenStable()
    expect(fixture.nativeElement.querySelectorAll('.skeleton').length).toBeGreaterThan(0)
  })

  it('lists every worktree newest activity first, with the count', async () => {
    const { root } = await openPage('/worktrees', WorktreesPage, documentOf())
    expect(tasksOf(root)).toEqual(['fix-ci', 'add-search', 'zeta', 'far'])
    expect(text(root.querySelector('.count'))).toBe('4 of 4')
  })

  it('declares the legacy repository filter as a visible filter, and drops it from the address', async () => {
    const { root, harness } = await openPage('/worktrees?repository=r2', WorktreesPage, documentOf())
    await harness.fixture.whenStable()
    expect(TestBed.inject(Router).url).toBe('/worktrees?q=repo:%22acme%2Fr2%22')
    expect(tasksOf(root)).toEqual(['far'])
    expect(root.querySelector('input')?.value).toBe('repo:"acme/r2"')
  })

  it('adds the legacy repository filter to a filter that is there, and only drops one that cannot be written', async () => {
    const withQ = await openPage('/worktrees?repository=r1&q=fix', WorktreesPage, documentOf())
    await withQ.harness.fixture.whenStable()
    expect(decodeURIComponent(TestBed.inject(Router).url)).toBe('/worktrees?q=fix repo:"acme/r1"')
    expect(tasksOf(withQ.root)).toEqual(['fix-ci'])
    const quoted = await openPage('/worktrees?repository=a%22b', WorktreesPage, documentOf())
    await quoted.harness.fixture.whenStable()
    expect(TestBed.inject(Router).url).toBe('/worktrees')
  })

  it('waits for the fleet before turning the legacy filter into one', async () => {
    const { store, harness } = await openPage('/worktrees', WorktreesPage, documentOf())
    store.loaded.set(false)
    await TestBed.inject(Router).navigateByUrl('/worktrees?repository=r2')
    await harness.fixture.whenStable()
    expect(TestBed.inject(Router).url).toBe('/worktrees?repository=r2')
    store.loaded.set(true)
    await harness.fixture.whenStable()
    await harness.fixture.whenStable()
    expect(decodeURIComponent(TestBed.inject(Router).url)).toBe('/worktrees?q=repo:"acme/r2"')
  })

  // cockpit-views#ac:worktree-identity-cell
  it('shows the task in strong type with the repository muted, the task part leading to the task page and the row end to the worktree page', async () => {
    const { root } = await openPage('/worktrees', WorktreesPage, documentOf())
    const identity = cell(rowsOf(root)[0], 0)
    expect(text(identity.querySelector('a.name'))).toBe('fix-ci')
    expect(text(identity.querySelector('.secondary'))).toBe('acme/r1')
    expect(identity.querySelector('a.name')?.getAttribute('href')).toBe('/tasks/detail?task=fix-ci')
    // No link covers the cell: a click anywhere else selects the row.
    expect(identity.querySelectorAll('a')).toHaveLength(1)
    expect(rowsOf(root)[0].querySelector('a.open')?.getAttribute('href')).toBe('/worktrees/w1')
    expect(headersOf(root)).not.toContain('Task')
    expect(headersOf(root)[0]).toBe('Worktree')
  })

  // cockpit-views#ac:worktrees-columns-and-badges
  it('shows exactly the columns of the requirement: Worktree, Branch when it differs, Machine, State, PR, Code index, Last activity', async () => {
    const { root } = await openPage('/worktrees', WorktreesPage, documentOf())
    expect(headersOf(root)).toEqual(['Worktree', 'Branch', 'Machine', 'State', 'PR', 'Code index', 'Last activity'])
    expect(root.querySelectorAll('.head [role=columnheader]')[3].getAttribute('title')).toContain('this machine only')
  })

  it('hides Branch when every branch is its task, shows it when one differs (a task/ branch differs), hides Machine on a one-machine fleet and PR and Code index when none has them', async () => {
    const doc = documentOf()
    doc.worktrees = doc.worktrees.map((w) => ({ ...w, branch: w.task, code_index: undefined }))
    doc.pull_requests = []
    const same = await openPage('/worktrees', WorktreesPage, doc)
    expect(headersOf(same.root)).toEqual(['Worktree', 'Machine', 'State', 'Last activity'])
    const prefixed = { ...doc, worktrees: doc.worktrees.map((w) => (w.id === 'w2' ? { ...w, branch: 'task/add-search' } : w)) }
    const prefix = await openPage('/worktrees', WorktreesPage, prefixed)
    expect(headersOf(prefix.root)).toContain('Branch')
    const oneMachine = { ...doc, machines: [doc.machines[0]], worktrees: doc.worktrees.filter((w) => w.machine === 'alpha') }
    const single = await openPage('/worktrees', WorktreesPage, oneMachine)
    expect(headersOf(single.root)).toEqual(['Worktree', 'State', 'Last activity'])
  })

  it('shows the owner state badge and the sync badges for this machine only, and the machine as its chip with the age of a cached snapshot', async () => {
    const { root } = await openPage('/worktrees', WorktreesPage, documentOf())
    const state = (index: number) => cell(rowsOf(root)[index], 3)
    expect(text(state(0))).toContain('active')
    expect(text(state(0))).toContain('↑2')
    expect(text(state(0))).toContain('↓1')
    expect(text(state(2))).toContain('orphaned')
    expect(text(state(2))).toContain('gone')
    expect(text(state(3))).toContain('idle')
    expect(state(3).querySelector('app-sync-badges')).toBeNull()
    expect(text(cell(rowsOf(root)[0], 2))).toContain('alpha')
    expect(cell(rowsOf(root)[0], 2).querySelector('.chip')).toBeNull()
    expect(text(cell(rowsOf(root)[3], 2))).toContain('beta')
    expect(cell(rowsOf(root)[3], 2).querySelector('.chip')?.textContent).toMatch(/\d+ [mhd]/)
    expect(state(3).querySelector('app-state-badge')).toBeNull()
    expect(state(3).querySelector('.idle')?.textContent).toContain('idle')
  })

  it('shows the pull request as the control surface\'s chip, linked only when the address is a web address, and the panel agrees with the row', async () => {
    const doc = documentOf()
    doc.pull_requests = [pullRequest('p1', 'r1', 'w1', { number: 7 }), pullRequest('p2', 'r1', 'w2', { number: 8, url: 'javascript:alert(1)' }), pullRequest('p3', 'r1', 'w1', { number: 9 }), pullRequest('p4', 'r1', undefined, { number: 10 })]
    const { root } = await openPage('/worktrees', WorktreesPage, doc)
    const pr = (index: number) => cell(rowsOf(root)[index], 4)
    expect(pr(0).querySelector('a.number')?.getAttribute('href')).toBe('https://github.com/acme/r1/pull/1')
    expect(text(pr(0))).toContain('#7')
    expect(text(pr(0).querySelector('.more'))).toBe('+1')
    expect(pr(1).querySelector('a')).toBeNull()
    expect(text(pr(1))).toContain('#8')
    expect(pr(3).textContent?.trim()).toBe('')
    const panel = await openPage('/worktrees?sel=w1', WorktreesPage, doc)
    expect([...panel.root.querySelectorAll('app-side-panel [aria-label="Pull requests"] li')].map((item) => text(item).split(' ')[0])).toEqual(['#7', '#9'])
    const hostile = await openPage('/worktrees?sel=w2', WorktreesPage, doc)
    expect(hostile.root.querySelector('app-side-panel [aria-label="Pull requests"] a')).toBeNull()
  })

  it('hides the PR column when no worktree has a pull request', async () => {
    const doc = documentOf()
    doc.pull_requests = []
    const { root } = await openPage('/worktrees', WorktreesPage, doc)
    expect(headersOf(root)).not.toContain('PR')
  })

  // cockpit-views#ac:worktrees-quick-filters
  it('has the chips of the vocabulary, and each leaves exactly the worktrees that satisfy it', async () => {
    const { root } = await openPage('/worktrees', WorktreesPage, documentOf())
    expect([...root.querySelectorAll('[aria-label="Quick filters"] button')]).toHaveLength(VOCABULARY.worktrees.chips.length)
    const expected: Record<string, string[]> = {
      active: ['fix-ci'],
      orphaned: ['zeta'],
      unpushed: ['fix-ci'],
      gone: ['zeta'],
      pr: ['fix-ci'],
      idle30: ['far'],
    }
    for (const [chip, tasks] of Object.entries(expected)) {
      const page = await openPage(`/worktrees?chips=${chip}`, WorktreesPage, documentOf())
      expect(tasksOf(page.root), chip).toEqual(tasks)
    }
    for (const chip of ['safe', 'look']) {
      const page = await openPage(`/worktrees?chips=${chip}`, WorktreesPage, documentOf())
      expect(page.root.querySelector('[aria-label="Quick filters"] [aria-pressed=true]')).not.toBeNull()
    }
  })

  // cockpit-views#ac:copy-buttons-copy-the-full-value
  it('copies the full task and branch names from the row', async () => {
    const long = documentOf()
    long.worktrees[0] = { ...long.worktrees[0], task: 't'.repeat(80), branch: 'b'.repeat(90) }
    const { root } = await openPage('/worktrees', WorktreesPage, long)
    const copy = vi.spyOn(TestBed.inject(ClipboardWriter), 'copy').mockResolvedValue(true)
    const buttons = [...rowsOf(root)[0].querySelectorAll('app-copy-icon button')] as HTMLButtonElement[]
    expect(buttons.map((button) => button.getAttribute('aria-label'))).toEqual(['Copy task name', 'Copy branch name'])
    expect(buttons.every((button) => button.getAttribute('tabindex') === '-1')).toBe(true)
    for (const button of buttons) button.click()
    await vi.waitFor(() => expect(copy).toHaveBeenCalledTimes(2))
    expect(copy.mock.calls.map((call) => call[0])).toEqual(['t'.repeat(80), 'b'.repeat(90)])
  })

  // cockpit-views#ac:side-panel-shows-summary-actions-commands-and-raw-data
  it('opens the worktree\'s panel for a selection: summary, related entities, commands and collapsed raw data', async () => {
    const { root } = await openPage('/worktrees?sel=w1', WorktreesPage, documentOf())
    const panel = root.querySelector('app-side-panel') as HTMLElement
    expect(panel.querySelector('aside')?.getAttribute('aria-label')).toBe('Worktree fix-ci')
    expect(text(panel.querySelector('h2'))).toBe('fix-ci')
    const facts = Object.fromEntries([...panel.querySelectorAll('dt')].map((term) => [text(term), text(term.nextElementSibling)]))
    expect(facts).toMatchObject({ Task: 'fix-ci', Repository: 'acme/r1', Branch: 'topic', Machine: 'alpha', Source: 'local', State: 'active, in_progress', 'Sync (this machine)': '2 ahead, 1 behind' })
    expect(text(panel.querySelector('[aria-label="Task"]'))).toContain('fix-ci (')
    expect(panel.querySelector('app-copy-command-list')).not.toBeNull()
    expect(panel.querySelector('section.actions')).not.toBeNull()
    expect((panel.querySelector('details') as HTMLDetailsElement).open).toBe(false)
    expect(text(panel.querySelector('app-code-index-panel'))).toContain('Code index')
  })

  it('shows what is not known as such in the panel: no repository link, in sync, no activity, a cached worktree without sync', async () => {
    const doc = documentOf()
    doc.worktrees = [
      { ...worktree('w9', 'r-gone', 'alpha'), task: 'lone', branch: 'lone' },
      { ...worktree('w8', 'r2', 'beta'), task: 'elsewhere', branch: 'elsewhere', route: 'cached' },
    ]
    doc.pull_requests = []
    const lone = await openPage('/worktrees?sel=w9', WorktreesPage, doc)
    const facts = (root: HTMLElement) => Object.fromEntries([...root.querySelectorAll('app-side-panel dt')].map((term) => [text(term), text(term.nextElementSibling)]))
    expect(facts(lone.root)).toMatchObject({ Repository: 'r-gone', State: 'unknown', 'Sync (this machine)': 'in sync', 'Last activity': '—' })
    expect(lone.root.querySelector('app-side-panel dd a[href^="/repositories"]')).toBeNull()
    const cached = await openPage('/worktrees?sel=w8', WorktreesPage, doc)
    expect(facts(cached.root)['Sync (this machine)']).toBeUndefined()
    expect(cached.root.querySelector('app-side-panel dd a[href^="/repositories/-/acme/r2"]')).not.toBeNull()
  })

  // cockpit-views#ac:shortcuts-navigate-and-respect-typing
  it('answers the shell\'s / and Esc: / focuses the filter, Esc clears a filled one first, then closes the panel, and an empty one lets Esc through', async () => {
    const { root, harness } = await openPage('/worktrees?sel=w1', WorktreesPage, documentOf())
    const shortcuts = TestBed.inject(Shortcuts)
    const input = root.querySelector('input') as HTMLInputElement
    const press = (key: string, init: KeyboardEventInit = {}, target: EventTarget = document.body) => {
      const event = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...init })
      Object.defineProperty(event, 'target', { value: target })
      shortcuts.handle(event)
      return event
    }
    expect(press('/').defaultPrevented).toBe(true)
    expect(document.activeElement).toBe(input)
    input.value = 'fix'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await harness.fixture.whenStable()
    expect(tasksOf(root)).toEqual(['fix-ci'])
    expect(press('Escape', {}, input).defaultPrevented).toBe(true)
    await harness.fixture.whenStable()
    expect(input.value).toBe('')
    expect(root.querySelector('app-side-panel')).not.toBeNull()
    expect(press('Escape', {}, input).defaultPrevented).toBe(true)
    await harness.fixture.whenStable()
    expect(root.querySelector('app-side-panel')).toBeNull()
    expect(TestBed.inject(Router).url).not.toContain('sel=')
    input.focus()
    expect(press('g', {}, input).defaultPrevented).toBe(false)
    expect(press('/', {}, input).defaultPrevented).toBe(false)
    expect(press('/', { isComposing: true }).defaultPrevented).toBe(false)
  })

  it('writes the selection in the address when a row is clicked', async () => {
    const { root, harness } = await openPage('/worktrees', WorktreesPage, documentOf())
    ;(rowsOf(root)[1].querySelectorAll('[role=gridcell]')[1] as HTMLElement).click()
    await harness.fixture.whenStable()
    expect(text(root.querySelector('app-side-panel h2'))).toBe('add-search')
  })
})
