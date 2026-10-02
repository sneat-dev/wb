import { signal } from '@angular/core'
import { FleetDocument, FleetModel, FleetModels } from '@cockpit/fleet-data'
import { UiClock } from '@cockpit/ui/control'
import { SNAPSHOT, homeStates } from './home-fixtures'

/** The clock of the specs: ten seconds after the snapshot of the fleets in home-fixtures.ts. */
export const NOW = SNAPSHOT + 10_000

/** A fleet of home-fixtures.ts by name, as a deep copy a spec may change. */
export function fleet(name: 'busy' | 'healthy' | 'warming' | 'throttled' | 'remote-error' | 'no-throughput' | 'only-dropped' = 'busy'): FleetDocument {
  return structuredClone(homeStates()[name].document)
}

/** The view model of a document at the specs' clock. */
export function modelOf(document: FleetDocument, now = NOW): FleetModel {
  return new FleetModels().forDocument(document, now)
}

/** The busy fleet reduced to the named tasks: their worktrees, pull requests and agents. */
export function only(...tasks: string[]): FleetDocument {
  const document = fleet()
  const worktrees = document.worktrees.filter((worktree) => tasks.includes(worktree.task))
  const ids = new Set(worktrees.map((worktree) => worktree.id))
  return {
    ...document,
    worktrees,
    pull_requests: document.pull_requests.filter((pullRequest) => pullRequest.worktree !== undefined && ids.has(pullRequest.worktree)),
    agents: document.agents.filter((agent) => agent.task !== undefined && tasks.includes(agent.task)),
  }
}

/** A provider that sets the shared clock of every relative time to the specs' clock. */
export const FIXED_CLOCK = { provide: UiClock, useValue: { now: signal(NOW) } }
