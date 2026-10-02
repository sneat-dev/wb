import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { VOCABULARY } from '@cockpit/fleet-data/list'
import { fleetDocument, machine } from '@cockpit/fleet-data/testing'
import { LIST_SHORTCUTS } from '@cockpit/ui/list-host'
import { MetricsPoller } from '../../metrics/metrics-poller'
import { openPage } from '../test-harness'
import { machinesDocument, metricsAnswers, metricsFetch, openMachines } from './machines-fixture'
import { MachinesPage } from './machines-page'

const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()
const rowsOf = (root: HTMLElement) => [...root.querySelectorAll<HTMLElement>('[role=row][data-index]')]
const namesOf = (root: HTMLElement) => rowsOf(root).map((row) => text(row.querySelector('.name')))
const headersOf = (root: HTMLElement) => [...root.querySelectorAll('.head [role=columnheader]:not(.open-cell)')].map(text)
const cell = (root: HTMLElement, row: number, header: string) => rowsOf(root)[row].querySelectorAll<HTMLElement>('[role=gridcell]')[headersOf(root).indexOf(header)]
const open = async (url = '/machines', document = machinesDocument(), answers = metricsAnswers()) => {
  const page = await openPage(url, MachinesPage, document, undefined, metricsFetch(answers))
  // The first read of the metrics comes just after the page is created.
  if (document.machines.length > 0) await vi.waitFor(() => expect(TestBed.inject(MetricsPoller).entries().size).toBeGreaterThan(0))
  await page.harness.fixture.whenStable()
  return page
}

describe('MachinesPage', () => {
  it('renders on its own over an empty, warming-up fleet, with placeholder rows', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([]), { provide: LIST_SHORTCUTS, useValue: { registerFilter: () => () => undefined, registerPanel: () => () => undefined } }] })
    const fixture = TestBed.createComponent(MachinesPage)
    await fixture.whenStable()
    expect(fixture.nativeElement.querySelectorAll('.skeleton').length).toBeGreaterThan(0)
  })

  // cockpit-views#ac:default-sorts
  it('lists this machine first and then the others by name', async () => {
    const { root } = await open()
    expect(namesOf(root)).toEqual(['macbook', 'nas', 'oldmac', 'vm'])
    expect(text(root.querySelector('.count'))).toBe('4 of 4')
  })

  it('says that nothing has been observed for a fleet with no machine', async () => {
    const { root } = await open('/machines', fleetDocument({ machines: [], repositories: [], worktrees: [], agents: [] }))
    expect(rowsOf(root)).toHaveLength(0)
    expect(text(root)).toContain('Nothing has been observed')
  })

  // cockpit-views#ac:no-layout-shift-on-arrival: the columns do not change when the first metrics arrive
  it('keeps the CPU and Memory columns while the first read is out, with nothing in their cells, and no bar before a sample', async () => {
    const { root } = await openMachines('/machines', MachinesPage, { answers: 'never' })
    expect(headersOf(root)).toContain('CPU')
    expect(headersOf(root)).toContain('Memory')
    expect(rowsOf(root).length).toBeGreaterThan(0)
    expect(cell(root, 0, 'CPU').querySelector('.meter')).toBeNull()
  })

  // cockpit-views#ac:machines-table-title-and-links
  it('has "Machines" as its first column header and no separate heading, and each name links to its page', async () => {
    const { root } = await open()
    expect(headersOf(root)).toEqual(['Machines', 'State', 'WB version', 'Repositories', 'Worktrees', 'Agents', 'Load', 'CPU', 'Memory'])
    expect(root.querySelectorAll('h1, h2, h3, .page-title')).toHaveLength(0)
    expect(rowsOf(root).map((row) => row.querySelector('a.name')?.getAttribute('href'))).toEqual(['/machines/mach-macbook', '/machines/mach-nas', '/machines/mach-oldmac', '/machines/mach-vm'])
  })

  it('shows this machine\'s publish_error as a warning in words, and nothing when publishing works', async () => {
    const document = machinesDocument()
    document.machines[0] = { ...document.machines[0], publish_error: 'collect_failed' }
    const { root } = await open('/machines', document)
    expect(text(cell(root, 0, 'State'))).toContain('publish: the scan or the GitHub login failed')
    expect(cell(root, 0, 'State').querySelector('.warning')?.getAttribute('title')).toBe('macbook could not publish: the scan or the GitHub login failed')
    expect(text(cell(root, 3, 'State'))).not.toContain('publish:')
  })

  // cockpit-views#ac:machines-table-title-and-links
  it('says how each machine is reached, marks the stale ones with their age, and marks the older version', async () => {
    const { root } = await open()
    expect(text(cell(root, 0, 'State'))).toBe('local')
    expect(text(cell(root, 3, 'State'))).toBe('live over http, just now')
    expect(text(cell(root, 2, 'State'))).toContain('cached, 1 d ago')
    expect(text(cell(root, 2, 'State'))).toContain('stale')
    expect(text(cell(root, 1, 'State'))).toContain('cached, 2 d ago')
    expect(text(cell(root, 1, 'State'))).toContain('stale')
    expect(root.querySelectorAll('.mark.stale')).toHaveLength(2)
    expect([...root.querySelectorAll('.version')].map(text)).toEqual(['1.2.0', '1.0.0 older', '1.2.0'])
    expect(text(cell(root, 1, 'WB version'))).toBe('—')
  })

  it('shows a remote error as a quiet warning with its text, and nothing for a machine that has none', async () => {
    const { root } = await open()
    expect(text(cell(root, 2, 'State'))).toContain('warning: its daemon is not running')
    expect(text(cell(root, 1, 'State'))).toContain('warning: is still warming up and has no export yet')
    expect(cell(root, 0, 'State').querySelector('.warning')).toBeNull()
    expect(cell(root, 2, 'State').querySelector('.warning')?.getAttribute('title')).toBe('oldmac: its daemon is not running')
  })

  it('counts repositories, worktrees and agents with links that open the lists those numbers count', async () => {
    const { root } = await open()
    const counts = (row: number) => ['Repositories', 'Worktrees', 'Agents'].map((header) => text(cell(root, row, header)))
    expect(counts(0)).toEqual(['1', '2', '2'])
    expect(counts(3)).toEqual(['1', '1', '1'])
    expect(counts(1)).toEqual(['0', '0', '0'])
    expect(cell(root, 0, 'Repositories').querySelector('a')?.getAttribute('href')).toBe('/repositories?machine=mach-macbook')
    expect(cell(root, 0, 'Worktrees').querySelector('a')?.getAttribute('href')).toBe('/worktrees?machine=mach-macbook')
    expect(cell(root, 0, 'Agents').querySelector('a')?.getAttribute('href')).toBe('/agents?machine=mach-macbook')
    expect(cell(root, 1, 'Agents').querySelector('a')).toBeNull()
    expect(cell(root, 1, 'Agents').querySelector('.quiet')?.getAttribute('title')).toBe('No agents')
  })

  // cockpit-views#ac:machines-table-title-and-links
  it('shows the CPU and memory of the latest sample, a free or busy verdict, and nothing for a machine with no metrics', async () => {
    const { root } = await open()
    const meters = (row: number) => [text(cell(root, row, 'CPU')), text(cell(root, row, 'Memory'))]
    expect(text(cell(root, 0, 'Load'))).toContain('busy')
    expect(meters(0)).toEqual(['88%', '88%'])
    expect(text(cell(root, 3, 'Load'))).toContain('free')
    expect(meters(3)).toEqual(['10%', '25%'])
    // A cached sample 30 minutes old says nothing about the machine now: not "free", no bars, and its age is said.
    expect(text(cell(root, 2, 'Load'))).toContain('load unknown')
    expect(text(cell(root, 2, 'Load'))).not.toContain('free')
    expect(cell(root, 2, 'Load').querySelector('app-state-badge')?.getAttribute('title')).toBe('Latest sample: cached, 30 min ago: too old to say')
    expect(meters(2)).toEqual(['', ''])
    // No source: the load is unknown and there are no bars, and never a zero.
    expect(text(cell(root, 1, 'Load'))).toContain('load unknown')
    expect(meters(1)).toEqual(['', ''])
    expect(cell(root, 1, 'CPU').querySelector('.bar')).toBeNull()
    expect(cell(root, 1, 'Load').querySelector('app-state-badge')?.getAttribute('title')).toBe('Latest sample: no usable sample')
  })

  it('says load unknown for a machine the daemon has no metrics answer for', async () => {
    const { root } = await open('/machines', machinesDocument(), { 'mach-vm': metricsAnswers()['mach-vm'] })
    expect(text(cell(root, 0, 'Load'))).toContain('load unknown')
    expect(text(cell(root, 3, 'Load'))).toContain('free')
  })

  it('says load unknown, with no bar, before the first read of the metrics', async () => {
    const { root } = await openPage('/machines', MachinesPage, machinesDocument(), undefined, (async () => new Response('{}', { status: 500 })) as typeof fetch)
    expect(text(cell(root, 0, 'Load'))).toContain('load unknown')
    expect(root.querySelectorAll('.meter')).toHaveLength(0)
  })

  // cockpit-views#ac:machines-filter-and-stale-chip
  it('has the chips stale and outdated, and each leaves exactly the machines that satisfy it', async () => {
    const { root } = await open()
    expect([...root.querySelectorAll('[aria-label="Quick filters"] button')].map(text)).toEqual(VOCABULARY.machines.chips.map((chip) => chip.label))
    for (const [chip, names] of Object.entries({ stale: ['nas', 'oldmac'], outdated: ['oldmac'] })) {
      expect(namesOf((await open(`/machines?chips=${chip}`)).root), chip).toEqual(names)
    }
  })

  // cockpit-views#ac:machines-filter-and-stale-chip
  it('narrows the rows by machine name with the filter box, and the chips toggle in turn', async () => {
    const { root, harness } = await open()
    const input = root.querySelector('input') as HTMLInputElement
    input.value = 'mac'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await vi.waitFor(async () => {
      await harness.fixture.whenStable()
      expect(namesOf(root)).toEqual(['macbook', 'oldmac'])
    })
    const toggle = async (label: string) => {
      ;([...root.querySelectorAll('[aria-label="Quick filters"] button')].find((button) => text(button) === label) as HTMLElement).click()
      await harness.fixture.whenStable()
    }
    await toggle(VOCABULARY.machines.chips[0].label)
    await vi.waitFor(() => expect(namesOf(root)).toEqual(['oldmac']))
    await toggle(VOCABULARY.machines.chips[0].label)
    await toggle(VOCABULARY.machines.chips[1].label)
    await vi.waitFor(() => expect(namesOf(root)).toEqual(['oldmac']))
  })

  it('opens the side panel of the selected machine', async () => {
    const { root } = await open('/machines?sel=mach-vm')
    const panel = root.querySelector('app-side-panel') as HTMLElement
    expect(panel.querySelector('.side-panel')?.getAttribute('aria-label')).toBe('Machine vm')
    expect(text(panel.querySelector('h2'))).toBe('vm')
  })

  it('polls the machines it lists, and with a machine chip only that one', async () => {
    const requested: string[] = []
    const fetcher = (async (input: RequestInfo | URL) => {
      requested.push(String(input))
      return new Response(JSON.stringify({ machine: 'x', route: 'none', samples: [] }), { status: 200 })
    }) as typeof fetch
    await openPage('/machines', MachinesPage, machinesDocument(), undefined, fetcher)
    await vi.waitFor(() => expect(requested.length).toBeGreaterThanOrEqual(4))
    expect(requested.slice(0, 4).map((url) => decodeURIComponent(url.split('machine=')[1])).sort()).toEqual(['mach-macbook', 'mach-nas', 'mach-oldmac', 'mach-vm'])
    requested.length = 0
    await openPage('/machines?machine=mach-vm', MachinesPage, machinesDocument(), undefined, fetcher)
    await vi.waitFor(() => expect(requested.length).toBeGreaterThanOrEqual(1))
    expect(requested).toEqual(['/api/v1/cockpit/machine-metrics?machine=mach-vm'])
  })

  it('keeps a machine of the fleet with one alone: no stale, no older, no warning', async () => {
    const { root } = await open('/machines', fleetDocument({ machines: [machine('alpha')], repositories: [], worktrees: [], agents: [] }))
    expect(namesOf(root)).toEqual(['alpha'])
    expect(text(cell(root, 0, 'State'))).toBe('local')
  })
})
