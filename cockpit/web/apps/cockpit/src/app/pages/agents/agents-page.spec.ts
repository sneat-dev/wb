import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { VOCABULARY } from '@cockpit/fleet-data/list'
import { fleetDocument, machine, run } from '@cockpit/fleet-data/testing'
import { LIST_SHORTCUTS } from '@cockpit/ui/list-host'
import { openPage } from '../test-harness'
import { agentsDocument } from './agents-fixture'
import { AgentsPage } from './agents-page'

const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()
const rowsOf = (root: HTMLElement) => [...root.querySelectorAll<HTMLElement>('[role=row][data-index]')]
const namesOf = (root: HTMLElement) => rowsOf(root).map((row) => text(row.querySelector('.name')))
const headersOf = (root: HTMLElement) => [...root.querySelectorAll('.head [role=columnheader]:not(.open-cell)')].map(text)
const cell = (root: HTMLElement, row: number, header: string) => rowsOf(root)[row].querySelectorAll<HTMLElement>('[role=gridcell]')[headersOf(root).indexOf(header)]
const workOf = (row: HTMLElement) => [...row.querySelectorAll('.work > *')].map(text).join(' ')
const idsOf = (root: HTMLElement) => rowsOf(root).map((row) => row.querySelector('a.open')?.getAttribute('href')?.replace('/agents/', ''))

describe('AgentsPage', () => {
  it('renders on its own over an empty, warming-up fleet, with placeholder rows', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([]), { provide: LIST_SHORTCUTS, useValue: { registerFilter: () => () => undefined, registerPanel: () => () => undefined } }] })
    const fixture = TestBed.createComponent(AgentsPage)
    await fixture.whenStable()
    expect(fixture.nativeElement.querySelectorAll('.skeleton').length).toBeGreaterThan(0)
  })

  // cockpit-views#ac:default-sorts
  it('lists running agents first and then the newest start, with the count', async () => {
    const { root } = await openPage('/agents', AgentsPage, agentsDocument())
    expect(idsOf(root)).toEqual(['run-1', 's-block', 's-free', 's-beta', 'run-fail', 's-parked', 'run-bare'])
    expect(text(root.querySelector('.count'))).toBe('7 of 7')
  })

  it('says that nothing has been observed for a fleet with no agent', async () => {
    const { root } = await openPage('/agents', AgentsPage, fleetDocument({ agents: [] }))
    expect(rowsOf(root)).toHaveLength(0)
    expect(text(root)).toContain('Nothing has been observed')
  })

  // cockpit-views#ac:agents-list-describes-the-work
  it('labels an agent by runtime and model with its task, shows its state and activity, machine and running time, and keeps the id out of the row', async () => {
    const { root } = await openPage('/agents', AgentsPage, agentsDocument())
    const first = rowsOf(root)[0]
    expect(text(first.querySelector('.name'))).toBe('claude · sonnet-5-5')
    expect(text(first.querySelector('.work a.task'))).toBe('fix-ci')
    expect(first.querySelector('.work a.task')?.getAttribute('href')).toBe('/tasks?sel=fix-ci')
    expect(workOf(first)).toBe('fix-ci acme/r1')
    expect(text(cell(root, 0, 'State'))).toContain('running')
    expect(text(cell(root, 0, 'State'))).toContain('working')
    expect(text(cell(root, 0, 'Running for'))).toBe('1 h')
    expect(text(cell(root, 0, 'Machine'))).toBe('alpha')
    expect(text(first)).not.toContain('run-1')
    expect(root.querySelector('[role=row] app-copy-icon')).toBeNull()
  })

  // cockpit-views#ac:agent-label-fallback
  it('says "session, started 2 h ago" for a session with no work link, quietly says state not reported, and counts it as running', async () => {
    const { root } = await openPage('/agents?chips=running', AgentsPage, agentsDocument())
    const row = rowsOf(root)[2]
    expect(text(row.querySelector('.name'))).toBe('claude · opus')
    expect(workOf(row)).toBe('session, started 2 h ago')
    expect(text(cell(root, 2, 'State'))).toBe('Agent state: livestate not reported')
    expect(namesOf(root)).toContain('claude · opus')
  })

  it('shows the activity badge of a parked session, no "not reported" for an ended run, and an exit code for a failed one', async () => {
    const { root } = await openPage('/agents', AgentsPage, agentsDocument())
    expect(text(cell(root, 5, 'State'))).toBe('Agent state: parkedAgent activity: idle')
    expect(text(cell(root, 4, 'State'))).toBe('Agent state: failed')
    expect(text(cell(root, 4, 'Running for'))).toBe('3 h agoexit 1')
    expect(cell(root, 4, 'Running for').querySelector('.exit')?.getAttribute('title')).toBe('Exit code 1')
    expect(text(cell(root, 6, 'State'))).toBe('Agent state: completed')
    expect(text(cell(root, 6, 'Running for'))).toBe('')
    expect(text(cell(root, 5, 'Running for'))).toBe('started 3 d ago')
  })

  it('names a session of several tasks by their count, and shows an exit code only for a run that did not complete', async () => {
    const doc = agentsDocument()
    doc.agents[6] = { ...doc.agents[6], exit_code: 0 }
    const { root } = await openPage('/agents', AgentsPage, doc)
    expect(workOf(rowsOf(root)[1])).toBe('2 tasks acme/r1')
    expect(cell(root, 6, 'Running for').querySelector('.exit')).toBeNull()
  })

  it('marks an agent of another machine with its machine chip and the age of its snapshot', async () => {
    const { root } = await openPage('/agents', AgentsPage, agentsDocument())
    expect(text(cell(root, 3, 'Machine'))).toBe('beta5 h · ssh')
    expect(workOf(rowsOf(root)[3])).toBe('fix-ci acme/r2')
  })

  it('says that agents are reported only by machines that publish them, naming the silent ones', async () => {
    const { root } = await openPage('/agents', AgentsPage, agentsDocument())
    expect(text(root.querySelector('.note'))).toBe('Agents are reported only by machines that publish them; gamma reports none.')
    const doc = agentsDocument()
    doc.machines = [...doc.machines, machine('delta', 'cached')]
    const two = await openPage('/agents', AgentsPage, doc)
    expect(text(two.root.querySelector('.note'))).toContain('gamma, delta report none')
    const all = agentsDocument()
    all.machines = all.machines.slice(0, 2)
    const none = await openPage('/agents', AgentsPage, all)
    expect(none.root.querySelector('.note')).toBeNull()
  })

  it('notes that the agents of a machine are capped when the document says so', async () => {
    const doc = agentsDocument()
    doc.agents_truncated = true
    const { root } = await openPage('/agents', AgentsPage, doc)
    expect(text(root.querySelector('.note[role=note]'))).toContain('Showing the first 200 agents of each machine')
    const plain = await openPage('/agents', AgentsPage, agentsDocument())
    expect(plain.root.querySelector('.note[role=note]')).toBeNull()
  })

  it('shows Kind only when both kinds are listed, and Machine only when the fleet has several machines or an agent is remote', async () => {
    const { root } = await openPage('/agents', AgentsPage, agentsDocument())
    expect(headersOf(root)).toEqual(['Agent', 'State', 'Running for', 'Machine', 'Kind'])
    expect(text(cell(root, 0, 'Kind'))).toBe('run')
    const doc = agentsDocument()
    const single = await openPage('/agents', AgentsPage, { ...doc, machines: [doc.machines[0]], agents: [run('r1', 'running', { started_at: '2026-10-01T09:00:00Z' })] })
    expect(headersOf(single.root)).toEqual(['Agent', 'State', 'Running for'])
  })

  it('has the chips of the vocabulary and one for each runtime, and each leaves exactly the agents that satisfy it', async () => {
    const { root } = await openPage('/agents', AgentsPage, agentsDocument())
    const chips = [...root.querySelectorAll('[aria-label="Quick filters"] button')].map(text)
    expect(chips).toEqual([...VOCABULARY.agents.chips.map((chip) => chip.label), 'claude', 'codex'])
    const expected: Record<string, string[]> = {
      running: ['run-1', 's-block', 's-free', 's-beta'],
      blocked: ['s-block'],
      'runtime-claude': ['run-1', 's-block', 's-free', 'run-fail'],
      'runtime-codex': ['s-beta'],
    }
    for (const [chip, ids] of Object.entries(expected)) {
      const page = await openPage(`/agents?chips=${chip}`, AgentsPage, agentsDocument())
      expect(idsOf(page.root), chip).toEqual(ids)
    }
  })

  it('offers no runtime chip for a runtime that cannot be one, and sorts the ones it offers', async () => {
    const doc = agentsDocument()
    doc.agents = [run('a', 'running', { runtime: 'Zed' }), run('b', 'running', { runtime: 'claude code' }), run('c', 'running', { runtime: undefined })]
    const { root } = await openPage('/agents', AgentsPage, doc)
    expect([...root.querySelectorAll('[aria-label="Quick filters"] button')].map(text).slice(-1)).toEqual(['zed'])
  })

  it('opens the side panel of the selected agent, from a click on the row too', async () => {
    const { root, harness } = await openPage('/agents?sel=s-free', AgentsPage, agentsDocument())
    const panel = root.querySelector('app-side-panel') as HTMLElement
    expect(panel.querySelector('aside')?.getAttribute('aria-label')).toBe('Agent claude · opus')
    expect(text(panel.querySelector('.control'))).toContain('cannot be stopped or messaged from here')
    cell(root, 0, 'State').click()
    await harness.fixture.whenStable()
    expect(text(root.querySelector('app-side-panel h2'))).toBe('claude · sonnet-5-5')
  })
})
