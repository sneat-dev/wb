import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { FETCH } from '@cockpit/fleet-data'
import { ClipboardWriter } from '@cockpit/ui/control'
import { FIXED_CLOCK, fleet, modelOf } from './home-testing'
import { InFlightSection, flightRows } from './in-flight-section'

async function render(document = fleet(), warming = false) {
  const copy = vi.fn().mockResolvedValue(true)
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({
    providers: [FIXED_CLOCK, provideRouter([]), { provide: ClipboardWriter, useValue: { copy } }, { provide: FETCH, useValue: async () => new Response('{}', { status: 404 }) }],
  })
  const fixture = TestBed.createComponent(InFlightSection)
  fixture.componentRef.setInput('model', modelOf(document))
  fixture.componentRef.setInput('warming', warming)
  await fixture.whenStable()
  const root: HTMLElement = fixture.nativeElement
  return { fixture, root, copy, rows: [...root.querySelectorAll<HTMLElement>('.home-row')] }
}

const text = (element: Element | null | undefined) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()

describe('flightRows', () => {
  it("lists the running agents on every machine, this machine's first, with what a row needs", () => {
    const rows = flightRows(modelOf(fleet()))
    expect(rows.map((row) => [row.id, row.label, row.work, row.remote, row.controllable])).toEqual([
      ['run-speed', 'claude opus', 'speed-up-index', false, true],
      ['sess-1', 'codex', 'session, started 2 h ago', false, false],
      ['old-run', 'claude opus', 'old-experiment', true, false],
      ['vm-blocked', 'claude opus', 'migrate-auth', true, false],
    ])
    expect(rows.map((row) => row.runningFor)).toEqual(['35 min', '2 h', '39 min', '1 h 10 min'])
    expect(rows[0].link).toEqual({ path: '/agents/run-speed', query: {} })
  })

  it('says "session" when a session has no start and no task, and has no running time or machine words for what it does not know', () => {
    const document = fleet()
    const session = document.agents.find((agent) => agent.id === 'sess-1')!
    session.started_at = undefined
    session.machine_id = 'mach-ghost'
    const row = flightRows(modelOf(document)).find((candidate) => candidate.id === 'sess-1')!
    expect(row.work).toBe('session')
    expect(row.runningFor).toBeUndefined()
    expect(row.machine).toBeUndefined()
  })

  it('builds the stop and log commands of a dispatched run when asked', async () => {
    const [run] = flightRows(modelOf(fleet()))
    expect(await run.stop()).toMatchObject({ ok: true, text: "wb agent stop 'run-speed'" })
    expect(await run.log()).toMatchObject({ ok: true, text: "wb agent logs 'run-speed'" })
  })
})

describe('InFlightSection', () => {
  it('lists each running agent with its runtime and model, work, machine, running time and activity or "state not reported"', async () => {
    const { root, rows } = await render()
    expect(text(root.querySelector('h2'))).toBe('In flight 4')
    expect(rows).toHaveLength(4)
    expect(text(rows[0].querySelector('.home-task'))).toBe('claude opus')
    expect(rows[0].querySelector('a.home-task')?.getAttribute('href')).toBe('/agents/run-speed')
    expect(text(rows[0].querySelector('.home-reason'))).toBe('speed-up-index')
    expect(text(rows[0].querySelector('.flight-meta'))).toContain('mac')
    expect(text(rows[0].querySelector('.flight-meta'))).toContain('running 35 min')
    expect(text(rows[0].querySelector('app-state-badge'))).toContain('working')
    expect(text(rows[1].querySelector('.home-reason'))).toBe('session, started 2 h ago')
    expect(text(rows[1].querySelector('app-state-badge'))).toContain('state not reported')
  })

  it('shows an agent of another machine with the age of its snapshot, its machine and no action', async () => {
    const { rows } = await render()
    expect(text(rows[2].querySelector('.flight-meta'))).toContain('snapshot 1 d ago')
    expect(text(rows[2].querySelector('.home-chip'))).toBe('1 d · stale')
    expect(rows[2].querySelector('.home-chip')?.classList.contains('stale')).toBe(true)
    expect(rows[3].querySelector('.home-chip')?.textContent).toBe('ssh')
    expect(rows[3].querySelector('button')).toBeNull()
  })

  it('offers "Copy stop" and "Copy log" only for a dispatched run on this machine, and copies the library\'s commands', async () => {
    const { fixture, rows, copy } = await render()
    const buttons = [...rows[0].querySelectorAll<HTMLButtonElement>('button')]
    expect(buttons.map((button) => text(button))).toEqual(['Copy stop', 'Copy log'])
    expect(rows[1].querySelector('button')).toBeNull()
    buttons[0].click()
    await new Promise((done) => setTimeout(done, 10))
    buttons[1].click()
    await new Promise((done) => setTimeout(done, 10))
    await fixture.whenStable()
    expect(copy.mock.calls.map((call) => call[0])).toEqual(["wb agent stop 'run-speed'", "wb agent logs 'run-speed'"])
  })

  it('names the machine of each row, and says which machines report no agents', async () => {
    const quiet = fleet()
    quiet.agents = quiet.agents.filter((agent) => agent.id !== 'vm-blocked')
    const { root, rows } = await render(quiet)
    expect(text(root.querySelector('.home-note'))).toBe('No agents reported on vm.')
    const one = fleet()
    one.machines = one.machines.filter((machine) => machine.machine === 'mac')
    one.agents = one.agents.filter((agent) => agent.machine === 'mac')
    const single = await render(one)
    expect(single.root.querySelector('.home-machine-name')?.textContent).toBe('mac')
    expect(single.root.querySelector('.home-chip')).toBeNull()
    expect(single.root.querySelector('.home-note')).toBeNull()
    expect(rows.length).toBeGreaterThan(0)
  })

  it('says no agents run, in one calm line, but keeps the machine strip; or shows a skeleton while scanning', async () => {
    const calm = await render(fleet('healthy'))
    expect(text(calm.root.querySelector('.home-calm'))).toBe('No agents running.')
    expect(calm.root.querySelector('.home-count')).toBeNull()
    expect(calm.root.querySelectorAll('.home-tile')).toHaveLength(2)
    const warming = await render(fleet('warming'), true)
    expect(warming.root.querySelector('.home-calm')).toBeNull()
    expect(warming.root.querySelector('app-skeleton-rows')).not.toBeNull()
  })
})
