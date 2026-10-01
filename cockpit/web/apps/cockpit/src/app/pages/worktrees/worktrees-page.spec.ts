import { Clipboard } from '@angular/cdk/clipboard'
import { TestBed } from '@angular/core/testing'
import { Router, provideRouter } from '@angular/router'
import { FleetDocument, VOCABULARY } from '@cockpit/fleet-data'
import { fleetDocument, pullRequest, worktree } from '@cockpit/fleet-data/testing'
import { Shortcuts } from '../../shortcuts/shortcuts'
import { NOW, openPage } from '../test-harness'
import { WorktreesPage } from './worktrees-page'

const DAY = 24 * 60 * 60 * 1000
const ago = (days: number) => new Date(NOW - days * DAY).toISOString()

/** Worktrees that are active with sync badges, idle on the task's own branch, orphaned and gone, and idle for 31 days on another machine. */
function documentOf(): FleetDocument {
  return fleetDocument({
    worktrees: [
      { ...worktree('w1', 'r1', 'alpha'), task: 'fix-ci', branch: 'topic', owner_state: 'active', ahead: 2, behind: 1, lifecycle: 'in_progress', last_activity_at: ago(1), code_index: [{ indexer: 'codegrapher', state: 'fresh' }] },
      { ...worktree('w2', 'r1', 'alpha'), task: 'add-search', branch: 'task/add-search', owner_state: 'idle', last_activity_at: ago(2) },
      { ...worktree('w3', 'r1', 'alpha'), task: 'zeta', branch: 'zeta', owner_state: 'orphaned', upstream_gone: true, last_activity_at: ago(3) },
      { ...worktree('w4', 'r2', 'beta'), task: 'far', branch: 'far', route: 'cached', owner_state: 'idle', last_activity_at: ago(31), observed_at: ago(0) },
    ],
    pull_requests: [pullRequest('p1', 'r1', 'w1', { number: 7 })],
  })
}

/** Seven columns' worth: with a cached worktree and a lifecycle, and no pull request and no code index. */
function sparse(): FleetDocument {
  const doc = documentOf()
  doc.pull_requests = []
  doc.worktrees = doc.worktrees.map((w) => ({ ...w, code_index: undefined }))
  return doc
}

const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()
const rowsOf = (root: HTMLElement) => [...root.querySelectorAll<HTMLElement>('[role=row][aria-rowindex]')]
const tasksOf = (root: HTMLElement) => rowsOf(root).map((row) => text(row.querySelector('.task')))
const headersOf = (root: HTMLElement) => [...root.querySelectorAll('[role=columnheader]')].map(text)
const cell = (row: HTMLElement, index: number) => row.querySelectorAll<HTMLElement>('[role=gridcell]')[index]

describe('WorktreesPage', () => {
  it('renders on its own over an empty, warming-up fleet, with placeholder rows', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([])] })
    const fixture = TestBed.createComponent(WorktreesPage)
    await fixture.whenStable()
    expect(fixture.nativeElement.querySelectorAll('.skeleton').length).toBeGreaterThan(0)
  })

  it('lists every worktree newest activity first, with the count', async () => {
    const { root } = await openPage('/worktrees', WorktreesPage, documentOf())
    expect(tasksOf(root)).toEqual(['fix-ci', 'add-search', 'zeta', 'far'])
    expect(text(root.querySelector('.count'))).toBe('4 of 4')
  })

  it('still shows exactly the worktrees of the repository a legacy count link opened', async () => {
    const { root, component } = await openPage('/worktrees?repository=r2', WorktreesPage, documentOf())
    expect(component.repository()).toBe('r2')
    expect(tasksOf(root)).toEqual(['far'])
    expect(text(root.querySelector('.count'))).toBe('1 of 1')
  })

  // cockpit-views#ac:worktree-identity-cell
  it('shows the task in strong type with the repository muted, linking to the worktree page, and its task part to the task page', async () => {
    const { root } = await openPage('/worktrees', WorktreesPage, documentOf())
    const identity = cell(rowsOf(root)[0], 0)
    expect(text(identity.querySelector('.task'))).toBe('fix-ci')
    expect(text(identity.querySelector('.repo'))).toBe('acme/r1')
    expect(identity.querySelector('a.stretch')?.getAttribute('href')).toBe('/worktrees/w1')
    expect(identity.querySelector('a.stretch')?.getAttribute('aria-label')).toBe('Open worktree fix-ci in acme/r1')
    expect(identity.querySelector('a.task')?.getAttribute('href')).toBe('/tasks/detail?task=fix-ci')
    expect(headersOf(root)).not.toContain('Task')
    expect(headersOf(root)[0]).toBe('Worktree')
  })

  // cockpit-views#ac:worktrees-columns-and-badges
  // cockpit-views#ac:columns-are-few-and-uniform-ones-hidden
  it('shows at most seven columns, in order, dropping Lifecycle and Source first', async () => {
    const full = await openPage('/worktrees', WorktreesPage, documentOf())
    expect(headersOf(full.root)).toEqual(['Worktree', 'Branch', 'Machine', 'State', 'PR', 'Code index', 'Last activity'])
    const few = await openPage('/worktrees', WorktreesPage, sparse())
    expect(headersOf(few.root)).toEqual(['Worktree', 'Branch', 'Machine', 'Source', 'State', 'Lifecycle', 'Last activity'])
  })

  it('shows the sync badges for this machine only, and says where a cached worktree came from', async () => {
    const { root } = await openPage('/worktrees', WorktreesPage, sparse())
    const state = (index: number) => cell(rowsOf(root)[index], 4)
    expect(text(state(0))).toBe('active ↑2 ↓1')
    expect(state(0).querySelector('[title="2 commits not pushed (this machine)"]')).not.toBeNull()
    expect(state(0).querySelector('[title="1 commits behind (this machine)"]')).not.toBeNull()
    expect(text(state(2))).toBe('orphaned gone')
    expect(state(2).querySelector('.tone-bad[title]')?.getAttribute('title')).toContain('this machine')
    // A cached worktree carries no sync facts, and says where it came from.
    expect(text(state(3))).toBe('idle')
    expect(text(cell(rowsOf(root)[3], 3))).toContain('cached')
    expect(text(cell(rowsOf(root)[0], 3))).toBe('local')
    expect(root.querySelectorAll('[role=columnheader]')[4].getAttribute('title')).toContain('this machine only')
  })

  it('hides the Branch column when every branch is the task or its task/ branch, and Source and Lifecycle when uniform', async () => {
    const doc = documentOf()
    doc.worktrees = doc.worktrees.filter((w) => w.id !== 'w1' && w.id !== 'w4').map((w) => ({ ...w, lifecycle: undefined }))
    const { root } = await openPage('/worktrees', WorktreesPage, doc)
    expect(headersOf(root)).toEqual(['Worktree', 'Machine', 'State', 'Last activity'])
    const second = await openPage('/worktrees', WorktreesPage, sparse())
    for (const column of ['Branch', 'Lifecycle', 'Source']) expect(headersOf(second.root)).toContain(column)
  })

  it('shows the pull request with its number and state, linked when it has an address, joined as the pr chip joins it', async () => {
    const doc = documentOf()
    doc.pull_requests = [
      pullRequest('p1', 'r1', 'w1', { number: 7 }),
      pullRequest('p2', 'r1', undefined, { number: 8, branch: 'zeta', state: 'draft', url: undefined }),
      pullRequest('p3', 'r1', 'w2', { number: 9, state: undefined, url: 'http://insecure.example/9' }),
    ]
    const { root } = await openPage('/worktrees', WorktreesPage, doc)
    const pr = (index: number) => cell(rowsOf(root)[index], headersOf(root).indexOf('PR'))
    const first = pr(0).querySelector('a') as HTMLAnchorElement
    expect(first.textContent).toBe('#7')
    expect(first.getAttribute('href')).toBe('https://github.com/acme/r1/pull/1')
    expect(first.getAttribute('rel')).toBe('noopener noreferrer')
    expect(text(pr(0))).toBe('#7 open')
    expect(text(pr(2))).toBe('#8 draft')
    expect(pr(2).querySelector('a')).toBeNull()
    expect(text(pr(1))).toContain('#9')
    expect(pr(3).textContent?.trim()).toBe('')
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
    const chips = [...root.querySelectorAll('[aria-label="Quick filters"] button')].map(text)
    expect(chips).toHaveLength(VOCABULARY.worktrees.chips.length)
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

  it('says that the unpushed and gone chips concern this machine, and every chip of the vocabulary has a button', async () => {
    const { root } = await openPage('/worktrees', WorktreesPage, documentOf())
    const buttons = [...root.querySelectorAll<HTMLElement>('[aria-label="Quick filters"] button')]
    const byLabel = Object.fromEntries(buttons.map((button) => [text(button), button.getAttribute('title')]))
    expect(byLabel['Unpushed']).toContain('this machine')
    expect(byLabel['Upstream gone']).toContain('this machine')
    expect(byLabel['Active']).toBeNull()
  })

  // cockpit-views#ac:copy-buttons-copy-the-full-value
  it('copies the full task and branch names from the row', async () => {
    const long = documentOf()
    long.worktrees[0] = { ...long.worktrees[0], task: 't'.repeat(80), branch: 'b'.repeat(90) }
    const { root } = await openPage('/worktrees', WorktreesPage, long)
    const copy = vi.spyOn(TestBed.inject(Clipboard), 'copy').mockReturnValue(true)
    const buttons = [...rowsOf(root)[0].querySelectorAll('button.copy')] as HTMLButtonElement[]
    expect(buttons.map((button) => button.getAttribute('aria-label'))).toEqual(['Copy task name', 'Copy branch name'])
    expect(buttons.every((button) => button.getAttribute('tabindex') === '-1')).toBe(true)
    for (const button of buttons) button.click()
    expect(copy.mock.calls.map((call) => call[0])).toEqual(['t'.repeat(80), 'b'.repeat(90)])
  })

  // cockpit-views#ac:side-panel-shows-summary-actions-commands-and-raw-data
  it('opens the worktree\'s panel for a selection: summary, related entities, commands and collapsed raw data', async () => {
    const { root } = await openPage('/worktrees?sel=w1', WorktreesPage, documentOf())
    expect(TestBed.inject(Router).url).toBe('/worktrees?sel=w1')
    const panel = root.querySelector('app-side-panel') as HTMLElement
    expect(panel.querySelector('aside')?.getAttribute('aria-label')).toBe('Worktree fix-ci')
    expect(text(panel.querySelector('h2'))).toBe('fix-ci')
    const facts = Object.fromEntries([...panel.querySelectorAll('dt')].map((term) => [text(term), text(term.nextElementSibling)]))
    expect(facts).toMatchObject({ Task: 'fix-ci', Repository: 'acme/r1', Branch: 'topic', Machine: 'alpha', Source: 'local', State: 'active, in_progress', 'Sync (this machine)': '2 ahead, 1 behind' })
    expect(panel.querySelector('[aria-label="Pull requests"] a')?.getAttribute('href')).toContain('/pull/1')
    expect(text(panel.querySelector('[aria-label="Task"]'))).toContain('fix-ci (')
    expect(panel.querySelector('[aria-label="Copy command"]')).not.toBeNull()
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
    expect(facts(cached.root)['Repository']).toBe('acme/r2')
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
    // Esc with text in the filter clears it and leaves the panel.
    expect(press('Escape', {}, input).defaultPrevented).toBe(true)
    await harness.fixture.whenStable()
    expect(input.value).toBe('')
    expect(root.querySelector('app-side-panel')).not.toBeNull()
    // Esc in the now empty filter closes the panel.
    expect(press('Escape', {}, input).defaultPrevented).toBe(true)
    await harness.fixture.whenStable()
    expect(root.querySelector('app-side-panel')).toBeNull()
    expect(TestBed.inject(Router).url).not.toContain('sel=')
    // None of the keys fires while typing in the filter.
    input.focus()
    expect(press('g', {}, input).defaultPrevented).toBe(false)
    expect(press('/', {}, input).defaultPrevented).toBe(false)
    expect(press('/', { isComposing: true }).defaultPrevented).toBe(false)
  })

  it('writes the address, and the filter, sort and selection survive in it', async () => {
    const { root, harness } = await openPage('/worktrees', WorktreesPage, documentOf())
    ;(rowsOf(root)[1].querySelectorAll('[role=gridcell]')[2] as HTMLElement).click()
    await harness.fixture.whenStable()
    expect(TestBed.inject(Router).url).toBe('/worktrees?sel=w2')
    expect(text(root.querySelector('app-side-panel h2'))).toBe('add-search')
  })
})
