import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { VOCABULARY } from '@cockpit/fleet-data/list'
import { fleetDocument, run, worktree } from '@cockpit/fleet-data/testing'
import { ClipboardWriter } from '@cockpit/ui/control'
import { LIST_SHORTCUTS } from '@cockpit/ui/list-host'
import { openPage } from '../test-harness'
import { tasksDocument } from './tasks-fixture'
import { TasksPage } from './tasks-page'

const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()
const rowsOf = (root: HTMLElement) => [...root.querySelectorAll<HTMLElement>('[role=row][data-index]')]
const tasksOf = (root: HTMLElement) => rowsOf(root).map((row) => text(row.querySelector('a.name')))
const headersOf = (root: HTMLElement) => [...root.querySelectorAll('.head [role=columnheader]:not(.open-cell)')].map(text)
const rowOf = (root: HTMLElement, task: string) => rowsOf(root).find((row) => text(row.querySelector('a.name')) === task) as HTMLElement
/** The cell of a row under a header. */
const cell = (root: HTMLElement, task: string, header: string) => rowOf(root, task).querySelectorAll<HTMLElement>('[role=gridcell]')[headersOf(root).indexOf(header)]

describe('TasksPage', () => {
  it('renders on its own over an empty, warming-up fleet, with placeholder rows', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([]), { provide: LIST_SHORTCUTS, useValue: { registerFilter: () => () => undefined, registerPanel: () => () => undefined } }] })
    const fixture = TestBed.createComponent(TasksPage)
    await fixture.whenStable()
    expect(fixture.nativeElement.querySelectorAll('.skeleton').length).toBeGreaterThan(0)
  })

  // cockpit-views#ac:default-sorts
  it('lists one row per task, the newest activity first, with the count', async () => {
    const { root } = await openPage('/tasks', TasksPage, tasksDocument())
    expect(tasksOf(root)).toEqual(['fix-ci', 'add-search', 'zeta', 'broken', 'far', 'mystery', 'fix/ci 100%', 'release.js', 'old'])
    expect(text(root.querySelector('.count'))).toBe('9 of 9')
  })

  it('says that nothing has been observed for a fleet with no task', async () => {
    const { root } = await openPage('/tasks', TasksPage, fleetDocument({ worktrees: [], pull_requests: [], agents: [] }))
    expect(rowsOf(root)).toHaveLength(0)
    expect(text(root)).toContain('Nothing has been observed')
  })

  it('orders by state, worst first, and the second direction reverses it', async () => {
    const asc = await openPage('/tasks?sort=state&dir=asc', TasksPage, tasksDocument())
    expect(tasksOf(asc.root).slice(0, 4)).toEqual(['zeta', 'broken', 'add-search', 'fix-ci'])
    const desc = await openPage('/tasks?sort=state&dir=desc', TasksPage, tasksDocument())
    expect(tasksOf(desc.root)[0]).toBe('mystery')
    const header = desc.root.querySelector('.head [role=columnheader]:nth-child(2)')
    expect(text(header)).toContain('State')
  })

  // cockpit-views#ac:tasks-list-aggregates-worktrees
  it('names a task over three repositories on two machines: two repositories and +1, 3 worktrees, 2 machines, its agent, the pull request with 3/4 and its badge, newest activity', async () => {
    const { root } = await openPage('/tasks?chips=multirepo,agent', TasksPage, tasksDocument())
    expect(tasksOf(root)).toEqual(['fix-ci'])
    expect(text(cell(root, 'fix-ci', 'Task').querySelector('.secondary'))).toBe('acme/r1, acme/r3 +1')
    expect(text(cell(root, 'fix-ci', 'Worktrees'))).toBe('3')
    const machines = text(cell(root, 'fix-ci', 'Machines'))
    expect(machines).toContain('alpha')
    expect(machines).toContain('beta')
    expect(machines).toMatch(/2 d · stale · ssh/)
    expect(text(cell(root, 'fix-ci', 'Agents'))).toContain('claude')
    expect(text(cell(root, 'fix-ci', 'Agents').querySelector('app-state-badge'))).toContain('running')
    expect(text(cell(root, 'fix-ci', 'Pull requests'))).toContain('#7')
    expect(text(cell(root, 'fix-ci', 'Pull requests'))).toContain('3/4')
    expect(text(cell(root, 'fix-ci', 'State'))).toContain('not ready')
    expect(text(cell(root, 'fix-ci', 'Last activity'))).toMatch(/\d+ ?[a-z]+ ago|just now/)
  })

  it('opens the side panel with the worktrees, pull requests and agents of the selected task', async () => {
    const { root } = await openPage('/tasks?chips=multirepo,agent&sel=fix-ci', TasksPage, tasksDocument())
    const panel = root.querySelector('app-side-panel') as HTMLElement
    expect(panel.querySelector('aside')?.getAttribute('aria-label')).toBe('Task fix-ci')
    expect(text(panel.querySelector('h2'))).toBe('fix-ci')
    expect(panel.querySelectorAll('[aria-label="Worktrees"] li')).toHaveLength(3)
    expect(panel.querySelectorAll('[aria-label="Pull requests"] li')).toHaveLength(1)
    expect(panel.querySelectorAll('[aria-label="Agents"] li')).toHaveLength(1)
  })

  it('selects the row on a click, and the panel follows', async () => {
    const { root, harness } = await openPage('/tasks', TasksPage, tasksDocument())
    cell(root, 'zeta', 'State').click()
    await harness.fixture.whenStable()
    expect(text(root.querySelector('app-side-panel h2'))).toBe('zeta')
  })

  it('shows the columns of the requirement, and hides Machines on a fleet of one machine and Agents and Pull requests when none applies', async () => {
    const { root } = await openPage('/tasks', TasksPage, tasksDocument())
    expect(headersOf(root)).toEqual(['Task', 'State', 'Pull requests', 'Agents', 'Worktrees', 'Machines', 'Last activity'])
    const doc = tasksDocument()
    const local = { ...doc, machines: [doc.machines[0]], worktrees: doc.worktrees.filter((w) => w.machine === 'alpha'), pull_requests: [], agents: [] }
    const single = await openPage('/tasks', TasksPage, local)
    expect(headersOf(single.root)).toEqual(['Task', 'State', 'Worktrees', 'Last activity'])
  })

  it('has the chips of the vocabulary, and each leaves exactly the tasks that satisfy it, needs-you being the set Home lists', async () => {
    const { root, store } = await openPage('/tasks', TasksPage, tasksDocument())
    expect([...root.querySelectorAll('[aria-label="Quick filters"] button')]).toHaveLength(VOCABULARY.tasks.chips.length)
    const homeSet = store
      .model()
      .needsYou.items.map((item) => item.task)
      .sort()
    expect(homeSet).toEqual(['broken', 'zeta'])
    const expected: Record<string, string[]> = {
      'needs-you': homeSet,
      ready: ['add-search'],
      working: [],
      agent: ['fix-ci', 'old'],
      pr: ['fix-ci', 'add-search', 'broken'],
      multirepo: ['fix-ci'],
      idle30: ['old'],
    }
    expect(Object.keys(expected).sort()).toEqual(VOCABULARY.tasks.chips.map((chip) => chip.id).sort())
    for (const [chip, tasks] of Object.entries(expected)) {
      const page = await openPage(`/tasks?chips=${chip}`, TasksPage, tasksDocument())
      expect(tasksOf(page.root).sort(), chip).toEqual([...tasks].sort())
    }
  })

  it('links the task name to its page with the name encoded, and the worktree count to the Worktrees list with a quoted task filter', async () => {
    const { root } = await openPage('/tasks', TasksPage, tasksDocument())
    expect(rowOf(root, 'fix/ci 100%').querySelector('a.name')?.getAttribute('href')).toBe('/tasks/detail?task=fix%2Fci%20100%25')
    expect(rowOf(root, 'release.js').querySelector('a.name')?.getAttribute('href')).toBe('/tasks/detail?task=release.js')
    expect(decodeURIComponent(cell(root, 'fix/ci 100%', 'Worktrees').querySelector('a')?.getAttribute('href') as string)).toContain('/worktrees?q=task:"fix/ci 100%"')
    expect(rowOf(root, 'zeta').querySelector('a.open')?.getAttribute('href')).toBe('/tasks/detail?task=zeta')
  })

  it('shows a worktree count that cannot be written as a link as plain text with the reason', async () => {
    const doc = tasksDocument()
    doc.worktrees[3] = { ...doc.worktrees[3], task: 'say "hi"' }
    const { root } = await openPage('/tasks', TasksPage, doc)
    const count = cell(root, 'say "hi"', 'Worktrees')
    expect(count.querySelector('a')).toBeNull()
    expect(count.querySelector('span')?.getAttribute('title')).toBeTruthy()
  })

  it('shows at most two pull requests, open ones first, then +n', async () => {
    const doc = tasksDocument()
    const [first] = doc.pull_requests
    doc.pull_requests = [
      { ...first, id: 'm1', number: 1, state: 'merged' },
      { ...first, id: 'o1', number: 3, url: 'javascript:alert(1)' },
      { ...first, id: 'o2', number: 4 },
    ]
    const { root } = await openPage('/tasks', TasksPage, doc)
    const prs = cell(root, 'fix-ci', 'Pull requests')
    expect(prs.querySelectorAll('.pr')).toHaveLength(2)
    expect([...prs.querySelectorAll('.number')].map(text)).toEqual(['#3', '#4'])
    expect(prs.querySelectorAll('a.number')).toHaveLength(1)
    expect(text(prs.querySelector('.more'))).toBe('+1')
  })

  it('says not checked for a pull request never observed, and the state of a merged one', async () => {
    const doc = tasksDocument()
    const [first] = doc.pull_requests
    doc.pull_requests = [
      { ...first, id: 'm1', number: 1, state: 'merged' },
      { ...first, id: 'u1', number: 2, checked_at: undefined, state: undefined },
    ]
    const { root } = await openPage('/tasks', TasksPage, doc)
    const prs = cell(root, 'fix-ci', 'Pull requests')
    expect(text(prs)).toContain('merged')
    expect(text(prs)).toContain('not checked')
    expect(prs.querySelector('.more')).toBeNull()
  })

  it('shows a running agent with its runtime, +n for more, "agent" without a runtime, and nothing for a task with none running', async () => {
    const doc = tasksDocument()
    doc.agents = [run('run-1', 'running', { worktrees: ['w1'], runtime: 'claude' }), run('run-2', 'running', { worktrees: ['w1'] }), run('run-3', 'completed', { worktrees: ['w4'] })]
    const { root } = await openPage('/tasks', TasksPage, doc)
    expect(text(cell(root, 'fix-ci', 'Agents'))).toContain('claude')
    expect(text(cell(root, 'fix-ci', 'Agents'))).toContain('+1')
    expect(cell(root, 'add-search', 'Agents').textContent?.trim()).toBe('')
    const single = tasksDocument()
    single.agents = [run('run-2', 'running', { worktrees: ['w1'], runtime: undefined })]
    const one = await openPage('/tasks', TasksPage, single)
    expect(text(cell(one.root, 'fix-ci', 'Agents'))).toContain('agent')
    expect(cell(one.root, 'fix-ci', 'Agents').querySelector('.more')).toBeNull()
  })

  it('names two machines and +n for more, and nothing for a task that is purely local', async () => {
    const doc = tasksDocument()
    doc.machines = [...doc.machines, { ...doc.machines[1], id: 'mach-gamma', machine: 'gamma', machine_id: 'mach-gamma' }, { ...doc.machines[1], id: 'mach-delta', machine: 'delta', machine_id: 'mach-delta' }]
    doc.worktrees = [...doc.worktrees, { ...worktree('wg', 'r1', 'gamma'), task: 'fix-ci', route: 'cached' }, { ...worktree('wd', 'r1', 'delta'), task: 'fix-ci', route: 'cached' }]
    const { root } = await openPage('/tasks', TasksPage, doc)
    expect(text(cell(root, 'fix-ci', 'Machines'))).toContain('+2')
    expect(cell(root, 'add-search', 'Machines').textContent?.trim()).toBe('')
  })

  it('shows the state badge of every state with its label and a glyph, never colour alone', async () => {
    const { root } = await openPage('/tasks', TasksPage, tasksDocument())
    const states: Record<string, string> = { 'fix-ci': 'not ready', 'add-search': 'ready to land', zeta: 'at risk', broken: 'checks failed', mystery: 'state not reported', old: 'idle' }
    for (const [task, label] of Object.entries(states)) expect(text(cell(root, task, 'State')), task).toContain(label)
    expect(cell(root, 'zeta', 'State').querySelector('svg')).not.toBeNull()
  })

  // cockpit-views#ac:task-state-ready-to-land
  it('marks a task that only another machine reports as via that machine, in the row and with its machine chip, and not a task decided here', async () => {
    const { root } = await openPage('/tasks', TasksPage, tasksDocument())
    expect(text(cell(root, 'far', 'State'))).toContain('via beta')
    expect(cell(root, 'far', 'State').querySelector('.via')?.getAttribute('title')).toBe('As reported by beta, not by this machine')
    expect(text(cell(root, 'far', 'Machines'))).toBe('beta2 d · stale · ssh')
    expect(cell(root, 'fix-ci', 'State').querySelector('.via')).toBeNull()
    expect(cell(root, 'zeta', 'State').querySelector('.via')).toBeNull()
  })

  // cockpit-views#ac:copy-buttons-copy-the-full-value
  it('copies the full task name from the row', async () => {
    const long = tasksDocument()
    long.worktrees[3] = { ...long.worktrees[3], task: 't'.repeat(80) }
    const { root } = await openPage('/tasks', TasksPage, long)
    const copy = vi.spyOn(TestBed.inject(ClipboardWriter), 'copy').mockResolvedValue(true)
    const button = rowOf(root, 't'.repeat(80)).querySelector('app-copy-icon button') as HTMLButtonElement
    expect(button.getAttribute('aria-label')).toBe('Copy task name')
    button.click()
    await vi.waitFor(() => expect(copy).toHaveBeenCalledWith('t'.repeat(80)))
  })
})
