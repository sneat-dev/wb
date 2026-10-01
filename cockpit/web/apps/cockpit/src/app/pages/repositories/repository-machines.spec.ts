import { FleetModels, MachineView, MergedRepository } from '@cockpit/fleet-data'
import { buildRepositories } from '@cockpit/fleet-data/list'
import { fleetDocument, machine, repository } from '@cockpit/fleet-data/testing'
import { MAX_CHIPS, machineChips } from './repository-machines'

const NOW = Date.parse('2026-10-01T10:05:00Z')
const HOUR = 60 * 60 * 1000
const ago = (hours: number) => new Date(NOW - hours * HOUR).toISOString()

function rowOf(machines: ReturnType<typeof machine>[], repositories: ReturnType<typeof repository>[]): { row: MergedRepository; views: MachineView[] } {
  const model = new FleetModels().forDocument(fleetDocument({ machines, repositories }), NOW)
  return { row: buildRepositories(model)[0], views: model.machines }
}

describe('machineChips', () => {
  it('names this machine alone, with no age and no mark', () => {
    const { row, views } = rowOf([machine('alpha')], [repository('r1', 'alpha', { name: 'acme/x' })])
    expect(machineChips(row.checkouts, views, NOW)).toEqual([{ id: 'mach-alpha', name: 'alpha', detail: '', stale: false, title: 'alpha: this machine' }])
  })

  it('gives a cached checkout the age of its snapshot, and a stale mark once it is older than a day', () => {
    const { row, views } = rowOf(
      [machine('alpha'), machine('beta', 'cached'), machine('gamma', 'cached')],
      [
        repository('r1', 'alpha', { name: 'acme/x' }),
        repository('r2', 'beta', { name: 'acme/x', route: 'cached', observed_at: ago(3) }),
        repository('r3', 'gamma', { name: 'acme/x', route: 'cached', observed_at: ago(49) }),
      ],
    )
    const [, beta, gamma] = machineChips(row.checkouts, views, NOW)
    expect(beta).toMatchObject({ name: 'beta', detail: '3 h', stale: false, title: 'beta: cached, 3 h ago' })
    expect(gamma).toMatchObject({ name: 'gamma', detail: '2 d · stale', stale: true })
    expect(gamma.title).toBe('gamma: cached, 2 d ago; older than the freshness window')
  })

  it('leaves out the age of a snapshot whose time cannot be read', () => {
    const { row, views } = rowOf([machine('alpha'), machine('beta', 'cached')], [repository('r2', 'beta', { name: 'acme/x', route: 'cached', observed_at: 'never' })])
    expect(machineChips(row.checkouts, views, NOW)[0]).toMatchObject({ detail: '', stale: false })
  })

  it('says a live remote is read live, with its transport when the machine has one', () => {
    const live = { ...machine('beta'), route: 'live-remote' as const, transport: 'ssh' as const }
    const { row, views } = rowOf([machine('alpha'), live], [repository('r2', 'beta', { name: 'acme/x', route: 'live-remote' })])
    expect(machineChips(row.checkouts, views, NOW)[0]).toMatchObject({ detail: 'ssh', title: 'beta: read live over ssh' })
    expect(machineChips(row.checkouts, [], NOW)[0]).toMatchObject({ detail: '', title: 'beta: read live' })
  })

  it('shows at most three chips before "+n"', () => {
    expect(MAX_CHIPS).toBe(3)
  })
})
