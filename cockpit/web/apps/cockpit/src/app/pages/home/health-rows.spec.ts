import { buildHealth } from '@cockpit/fleet-data/home-details'
import { fleet, modelOf } from './home-testing'
import { healthRows } from './health-rows'

describe('healthRows', () => {
  it('has no row for a fleet with nothing wrong', () => {
    expect(healthRows(buildHealth(modelOf(fleet('healthy'))), 0)).toEqual([])
  })

  it('has one row per problem with the command that fixes it and where it runs', () => {
    const document = fleet()
    document.repositories[0].error = 'scan_failed'
    const rows = healthRows(buildHealth(modelOf(document)), 0)
    expect(rows.map((row) => row.id)).toEqual(['stale:mach-old', 'older:mach-old', 'scan:sneat-dev/wb'])
    expect(rows[0]).toMatchObject({
      text: 'old has not published for over 24 hours',
      link: { path: '/machines', query: { chips: 'stale' } },
      where: 'run on old',
      command: { text: 'wb remote publish', needsEdit: false },
    })
    expect(rows[1]).toMatchObject({ where: 'run on old', command: { text: 'wb self-update' } })
    expect(rows[2]).toMatchObject({ text: 'Scan error in sneat-dev/wb', where: undefined, command: { text: "wb fleet status --filter='sneat-dev/wb'", needsEdit: false } })
  })

  it('says why a scan error has no command when the library refuses to write one for that repository name', () => {
    const document = fleet()
    document.repositories[0].name = '-odd/name'
    document.repositories[0].error = 'scan_failed'
    const row = healthRows(buildHealth(modelOf(document)), 0).find((candidate) => candidate.id.startsWith('scan:'))!
    expect(row.command).toEqual({ reason: expect.stringContaining('-') })
  })

  it('names a remote error with its fix, or says there is nothing to run', () => {
    const document = fleet('remote-error')
    expect(healthRows(buildHealth(modelOf(document)), 0).find((row) => row.id === 'remote:mach-vm')).toMatchObject({
      text: expect.stringContaining('vm'),
      command: { text: expect.stringContaining('wb') },
    })
    document.machines.find((machine) => machine.machine === 'vm')!.remote_error = 'remote_warming_up'
    const warming = healthRows(buildHealth(modelOf(document)), 0).find((row) => row.id === 'remote:mach-vm')
    expect(warming).toMatchObject({ where: undefined, command: { reason: expect.stringContaining('nothing to run') } })
  })

  it('lists the machines whose export left entries out, and the entries this page dropped', () => {
    const document = fleet()
    document.machines.find((machine) => machine.machine === 'vm')!.export_dropped = 3
    const rows = healthRows(buildHealth(modelOf(document)), 2)
    expect(rows.find((row) => row.id === 'export:mach-vm')?.text).toBe("3 entries left out of vm's export")
    expect(rows.at(-1)).toEqual({ id: 'dropped', text: '2 entries of the fleet document were invalid and left out', link: undefined, where: undefined, command: undefined })
    expect(healthRows(buildHealth(modelOf(fleet('healthy'))), 1)[0].text).toBe('1 entry of the fleet document was invalid and left out')
  })
})
