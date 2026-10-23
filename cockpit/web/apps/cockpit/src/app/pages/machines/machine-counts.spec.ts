import { FleetModels } from '@cockpit/fleet-data'
import { agent, fleetDocument, machine, repository, worktree } from '@cockpit/fleet-data/testing'
import { NOW } from '../test-harness'
import { machineCounts } from './machine-counts'
import { machinesDocument } from './machines-fixture'

const modelOf = (document = machinesDocument()) => new FleetModels().forDocument(document, NOW)

describe('machineCounts', () => {
  it('counts the rows of each list that a machine filter keeps', () => {
    const counts = machineCounts(modelOf())
    expect(Object.fromEntries(counts)).toEqual({
      'mach-macbook': { repositories: 1, worktrees: 2, agents: 2 },
      'mach-vm': { repositories: 1, worktrees: 1, agents: 1 },
      'mach-oldmac': { repositories: 1, worktrees: 1, agents: 0 },
      'mach-nas': { repositories: 0, worktrees: 0, agents: 0 },
    })
  })

  it('counts a repository once for each machine that holds a checkout, as the merged list does', () => {
    const document = fleetDocument({ machines: [machine('a'), machine('b')], repositories: [repository('x', 'a', { name: 'acme/same' }), repository('y', 'b', { name: 'acme/same' })], worktrees: [worktree('w', 'x', 'a')], agents: [agent('g', 'x', 'idle')] })
    expect(machineCounts(modelOf(document)).get('mach-a')).toMatchObject({ repositories: 1 })
    expect(machineCounts(modelOf(document)).get('mach-b')).toMatchObject({ repositories: 1 })
  })

  it('ignores a row of a machine the document does not list', () => {
    const document = fleetDocument({ machines: [machine('a')], repositories: [repository('x', 'ghost')], worktrees: [], agents: [] })
    expect(Object.fromEntries(machineCounts(modelOf(document)))).toEqual({ 'mach-a': { repositories: 0, worktrees: 0, agents: 0 } })
  })
})
