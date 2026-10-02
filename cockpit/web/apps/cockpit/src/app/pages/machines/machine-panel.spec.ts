import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { MachineMetrics, Session } from '@cockpit/fleet-data'
import { agent, fleetDocument, machine, repository, worktree } from '@cockpit/fleet-data/testing'
import { MetricsPoller } from '../../metrics/metrics-poller'
import { MachineDetailPage } from './machine-detail-page'
import { MachinePanelView } from './machine-panel'
import { MachinesPage } from './machines-page'
import { machinesDocument, metricsAnswers, openMachines, sample } from './machines-fixture'

const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()
const open = async (id: string, options: Parameters<typeof openMachines>[2] = {}) => {
  const page = await openMachines(`/machines/${id}`, MachineDetailPage, options)
  return { ...page, panel: page.root.querySelector('app-machine-panel') as HTMLElement }
}
const facts = (panel: HTMLElement, selector = '.aside > dl.facts') => Object.fromEntries([...panel.querySelectorAll(`${selector} > dt`)].map((term) => [text(term), text(term.nextElementSibling)]))
const section = (panel: HTMLElement, label: string) => panel.querySelector(`[aria-label="${label}"]`) as HTMLElement

describe('MachinePanelView', () => {
  it('renders nothing for an id the fleet does not list', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([])] })
    const fixture = TestBed.createComponent(MachinePanelView)
    fixture.componentRef.setInput('id', 'nope')
    await fixture.whenStable()
    expect(fixture.nativeElement.textContent.trim()).toBe('')
  })

  // cockpit-views#ac:machine-detail-metrics-charts
  it('shows the summary of the local machine: OS, architecture, CPUs, WB version, how it is reached and the uptime from boot_time', async () => {
    const { panel } = await open('mach-macbook')
    expect(text(panel.querySelector('h2'))).toBe('macbook')
    expect(text(panel.querySelector('app-panel-state'))).toContain('local')
    expect(text(panel.querySelector('app-panel-state'))).not.toContain('stale')
    expect(facts(panel)).toMatchObject({ OS: 'darwin', Architecture: 'arm64', CPUs: '10', 'WB version': '1.2.0', Uptime: '3 d', 'Reached by': 'this machine' })
  })

  // cockpit-views#ac:machine-detail-metrics-charts
  it('states the source of the metrics and draws four charts of the last hour with their latest readings, in a lazy chunk', async () => {
    const { panel, create } = await open('mach-macbook')
    expect(text(section(panel, 'Metrics').querySelector('.source'))).toMatch(/^local: this machine's own history, 360 samples, latest /)
    expect(section(panel, 'Metrics').querySelector('.source')?.getAttribute('data-source')).toBe('local')
    expect(facts(panel, '.latest')).toEqual({ CPU: '88%', 'Load (1 min)': '4.4', Memory: '14 GB of 16 GB (88%)', 'Disk free': '200 GB free of 500 GB' })
    await vi.waitFor(() => expect(panel.querySelectorAll('app-machine-charts app-chart')).toHaveLength(4))
    expect([...panel.querySelectorAll('app-machine-charts .title')].map(text)).toEqual(['CPU', 'Load (1 minute)', 'Memory used', 'Disk free'])
    expect(create).toHaveBeenCalledTimes(4)
    // Each chart carries its hidden data table, the text alternative.
    expect(panel.querySelectorAll('app-machine-charts table')).toHaveLength(4)
  })

  it('names the counts with links to the filtered lists, the running agents and the most recent worktrees', async () => {
    const { panel } = await open('mach-macbook')
    const links = (label: string) => [...section(panel, label).querySelectorAll('a')]
    expect(links('On this machine').map(text)).toEqual(['1 repository', '2 worktrees', '2 agents, 1 running'])
    expect(links('On this machine').map((link) => link.getAttribute('href'))).toEqual(['/repositories?machine=mach-macbook', '/worktrees?machine=mach-macbook', '/agents?machine=mach-macbook'])
    expect(links('Running agents').map(text)).toEqual(['claude sonnet-5-5'])
    expect(links('Running agents')[0].getAttribute('href')).toBe('/agents/run-1')
    expect(section(panel, 'Running agents').querySelector('li')?.getAttribute('title')).toBe('Agent state: running')
    expect(links('Recent worktrees').map(text)).toEqual(['fix-ci in acme/r1', 'add-search in acme/r1'])
    expect(links('Recent worktrees')[0].getAttribute('href')).toBe('/worktrees?sel=w1')
  })

  it('shows a count of nothing as plain text, not a link', async () => {
    const { panel } = await open('mach-nas')
    expect([...section(panel, 'On this machine').querySelectorAll('li')].map(text)).toEqual(['0 repositories', '0 worktrees', '0 agents, 0 running'])
    expect(section(panel, 'On this machine').querySelector('a')).toBeNull()
    expect(text(section(panel, 'Running agents'))).toContain('None')
  })

  it('ends with the collapsed Raw data block', async () => {
    const { panel } = await open('mach-macbook')
    const sections = panel.querySelectorAll('section')
    expect(sections[sections.length - 1].getAttribute('aria-label')).toBe('Raw data')
    expect((panel.querySelector('details') as HTMLDetailsElement).open).toBe(false)
  })

  // cockpit-views#ac:machine-without-metrics-says-so
  it('says that metrics are not reported for a machine whose route answers none, with no chart and no zero', async () => {
    const { panel } = await open('mach-nas')
    expect(text(section(panel, 'Metrics').querySelector('.source'))).toBe('Metrics are not reported for this machine.')
    expect(text(section(panel, 'Metrics'))).toContain('this daemon does not sample')
    expect(panel.querySelector('.latest')).toBeNull()
    expect(panel.querySelector('app-viewport-mount, app-machine-charts, app-chart')).toBeNull()
    expect(text(panel.querySelector('app-panel-state'))).toContain('load unknown')
    expect(text(section(panel, 'Metrics'))).not.toMatch(/\b0\b/)
    expect(facts(panel)).toMatchObject({ OS: 'not reported', Architecture: 'not reported', CPUs: 'not reported', 'WB version': 'not reported', Uptime: 'not reported' })
  })

  // cockpit-views#ac:machine-without-metrics-says-so
  it('shows the one cached sample with its age and the word cached, and draws no history chart', async () => {
    const { panel } = await open('mach-oldmac')
    expect(text(section(panel, 'Metrics').querySelector('.source'))).toBe('cached: the latest sample of its published snapshot, 30 min ago')
    expect(facts(panel, '.latest')).toEqual({ CPU: '35%', 'Load (1 min)': '1.75', Memory: '6 GB of 16 GB (38%)', 'Disk free': '200 GB free of 500 GB' })
    expect(panel.querySelector('app-viewport-mount, app-machine-charts, app-chart')).toBeNull()
    expect(text(panel.querySelector('app-panel-state'))).toContain('free')
  })

  it('names a live machine by its transport and age, its history, and how many entries its export left out', async () => {
    const { panel } = await open('mach-vm')
    expect(text(panel.querySelector('app-panel-state .why'))).toBe('live over http, just now')
    expect(text(section(panel, 'Metrics').querySelector('.source'))).toBe('live-remote: 30 samples, fetched just now')
    expect(facts(panel)).toMatchObject({ 'Reached by': 'http', Observed: 'just now', 'Left out': '3 entries of its export' })
    await vi.waitFor(() => expect(panel.querySelectorAll('app-machine-charts app-chart')).toHaveLength(4))
  })

  it('says "1 entry" for one entry left out and a capped agent list when the document says so', async () => {
    const document = machinesDocument()
    document.machines[1] = { ...document.machines[1], export_dropped: 1 }
    document.agents_truncated = true
    const { panel } = await open('mach-vm', { document })
    expect(facts(panel)['Left out']).toBe('1 entry of its export')
    expect(facts(panel)['Agents']).toBe('The agent list is capped at the first 200 of each machine.')
    expect(facts((await open('mach-macbook')).panel)['Left out']).toBeUndefined()
  })

  // cockpit-views#ac:machines-table-title-and-links
  it('marks a stale and an older machine in words, with the commands to fix them and where each runs', async () => {
    const { panel } = await open('mach-oldmac')
    expect(text(panel.querySelector('.mark.stale'))).toBe('stale')
    expect(text(panel.querySelector('.mark.older'))).toBe('older WB')
    expect([...section(panel, 'Needs attention').querySelectorAll('.warning')].map(text)).toEqual(['oldmac has not published for over 24 hours', 'oldmac runs an older WB (1.0.0)', 'oldmac: its daemon is not running'])
    const commands = [...panel.querySelectorAll('app-copy-command-list li')].map(text)
    expect(commands).toHaveLength(3)
    expect(commands.join(' | ')).toContain('wb remote publish')
    expect(commands.join(' | ')).toContain('wb self-update')
    expect(commands.join(' | ')).toContain('wb daemon start')
    expect(commands.every((command) => command.includes('run on oldmac'))).toBe(true)
  })

  it('offers the ssh form of the command, run here, to an owner session that has a route to the machine', async () => {
    const session: Session = { principal: 'owner', capabilities: ['fleet.read'], code_browser_url: 'https://codegrapher.dev/', machine_routes: [{ machine_id: 'mach-oldmac', ssh: { host: 'old.example', user: 'me' } }] }
    const { panel } = await open('mach-oldmac', { session })
    const commands = text(panel.querySelector('app-copy-command-list'))
    expect(commands).toContain('ssh me@old.example wb daemon start')
    expect(commands).toContain('run here')
  })

  it('says why there is nothing to copy for a remote error that clears itself', async () => {
    const { panel } = await open('mach-nas')
    const notes = [...section(panel, 'Needs attention').querySelectorAll('li')].map(text)
    expect(notes[notes.length - 1]).toContain('nas: is still warming up and has no export yet')
    expect(notes[notes.length - 1]).toContain('nothing to run')
    expect(panel.querySelectorAll('app-copy-command-list li')).toHaveLength(1)
  })

  it('has no attention section for a machine with nothing wrong', async () => {
    const { panel } = await open('mach-macbook')
    expect(section(panel, 'Needs attention')).toBeNull()
    expect(panel.querySelector('.mark')).toBeNull()
  })

  it('says it is reading before the first answer, and why when the read failed', async () => {
    const pending = await open('mach-macbook', { answers: 'never' })
    expect(text(section(pending.panel, 'Metrics').querySelector('.source'))).toBe('Reading the metrics of this machine…')
    TestBed.inject(MetricsPoller).entries.set(new Map([['mach-macbook', { error: 'daemon refused', readAt: 0 }]]))
    await pending.harness.fixture.whenStable()
    expect(text(section(pending.panel, 'Metrics').querySelector('.source'))).toBe('The metrics of this machine could not be read: daemon refused.')
    TestBed.inject(MetricsPoller).entries.set(new Map([['mach-macbook', { error: undefined, readAt: 0 }]]))
    await pending.harness.fixture.whenStable()
    expect(text(section(pending.panel, 'Metrics').querySelector('.source'))).toContain('unknown error')
  })

  it('keeps the earlier answer while a later read fails, and says so', async () => {
    const answer: MachineMetrics = { machine: 'mach-macbook', route: 'local', samples: [sample(1, 40)] }
    const { panel, harness } = await open('mach-macbook', { answers: 'never' })
    TestBed.inject(MetricsPoller).entries.set(new Map([['mach-macbook', { metrics: answer, error: 'timeout', readAt: 0 }]]))
    await harness.fixture.whenStable()
    expect(text(section(panel, 'Metrics'))).toContain('The last read failed; this is the earlier answer.')
    expect(facts(panel, '.latest').CPU).toBe('40%')
    // One local sample is a source of one sample, and its history still draws.
    expect(text(section(panel, 'Metrics').querySelector('.source'))).toContain('1 sample,')
  })

  it('does not say a sample is the only one when the machine reports a history', async () => {
    const answers = metricsAnswers()
    answers['mach-vm'] = { ...answers['mach-vm'], samples: [] }
    const { panel } = await open('mach-vm', { answers })
    expect(text(section(panel, 'Metrics').querySelector('.source'))).toBe('Metrics are not reported for this machine.')
  })

  it('is the content of the side panel of the Machines list, from the same component', async () => {
    const page = await open('mach-oldmac')
    const list = await openMachines('/machines?sel=mach-oldmac', MachinesPage)
    const side = list.root.querySelector('app-side-panel app-machine-panel') as HTMLElement
    for (const selector of ['.state', 'dl.facts', '[aria-label="Metrics"] .source', '[aria-label="Needs attention"]', '[aria-label="Counts"]', 'app-copy-command-list']) {
      expect(text(side.querySelector(selector)), selector).toBe(text(page.panel.querySelector(selector)))
    }
    expect(page.panel.querySelector('.content')?.classList.contains('page')).toBe(true)
    expect(side.querySelector('.content')?.classList.contains('page')).toBe(false)
  })

  it('lists nothing as "None" for a machine with no running agent and no worktree', async () => {
    const document = fleetDocument({ machines: [machine('alone')], repositories: [repository('r', 'alone')], worktrees: [], agents: [agent('a', 'r', 'idle', { machine: 'alone', machine_id: 'mach-alone' })] })
    const { panel } = await open('mach-alone', { document, answers: 'never' })
    expect(text(section(panel, 'Running agents'))).toContain('None')
    expect(text(section(panel, 'Recent worktrees'))).toContain('None')
    expect(worktree('w', 'r', 'alone').task).toBe('task-w')
  })
})
