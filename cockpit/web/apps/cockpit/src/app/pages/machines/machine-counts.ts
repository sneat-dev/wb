import { FleetModel } from '@cockpit/fleet-data'
import { buildAgentRows, buildRepositoryRows, buildWorktreeRows } from '@cockpit/fleet-data/list'

/** How many rows of each list a machine's count opens (the rows the link's `machine` filter keeps). */
export interface MachineCounts {
  repositories: number
  worktrees: number
  agents: number
}

/**
 * The counts of every machine, from the same rows the lists show, so each number is the size of the list its link
 * opens: a repository counts for every machine that has a checkout of it, a worktree or agent for its own machine.
 */
export function machineCounts(model: FleetModel): ReadonlyMap<string, MachineCounts> {
  const counts = new Map<string, MachineCounts>(model.document.machines.map((machine) => [machine.id, { repositories: 0, worktrees: 0, agents: 0 }]))
  const tally = (rows: readonly { machineIds: readonly string[] }[], key: keyof MachineCounts): void => {
    for (const row of rows) for (const id of row.machineIds) {
      const entry = counts.get(id)
      if (entry !== undefined) entry[key]++
    }
  }
  tally(buildRepositoryRows(model), 'repositories')
  tally(buildWorktreeRows(model), 'worktrees')
  tally(buildAgentRows(model), 'agents')
  return counts
}
