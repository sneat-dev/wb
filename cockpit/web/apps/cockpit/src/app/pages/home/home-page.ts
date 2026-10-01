import { ChangeDetectionStrategy, Component, computed, inject } from '@angular/core'
import { Params } from '@angular/router'
import {
  FleetStore,
  agentLabel,
  RUNNING_STATE,
  filterAgents,
  mostRecentWorktrees,
  repositoryLabel,
  worktreeLabel,
} from '@cockpit/fleet-data'
import { Count, RouteLabel } from '@cockpit/ui'
import { watchMetrics } from '../../metrics/metrics-poller'
import { TableModule } from 'primeng/table'

/** How many of the most recently active worktrees the dashboard lists. */
export const RECENT_WORKTREES = 10

interface Tile {
  title: string
  label: string
  names: string[]
  target: string
  query: Params
}

/** The fleet at a glance: one tile per collection, the machines, and recent work. */
@Component({
  selector: 'app-home-page',
  imports: [TableModule, Count, RouteLabel],
  templateUrl: './home-page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class HomePage {
  protected readonly store = inject(FleetStore)

  constructor() {
    // The machines are polled every 10 seconds while Home is shown (REQ:machine-metrics-polling).
    watchMetrics(() => this.store.document().machines.map((machine) => machine.id))
  }

  protected readonly tiles = computed<Tile[]>(() => {
    const document = this.store.document()
    return [
      { title: 'Machines', label: 'machines', names: document.machines.map((m) => m.machine), target: '/machines', query: {} },
      { title: 'Repositories', label: 'repositories', names: document.repositories.map(repositoryLabel), target: '/repositories', query: {} },
      { title: 'Worktrees', label: 'worktrees', names: document.worktrees.map(worktreeLabel), target: '/worktrees', query: {} },
      { title: 'Agents', label: 'agents', names: document.agents.map(agentLabel), target: '/agents', query: {} },
      {
        title: 'Running agents',
        label: 'running agents',
        names: filterAgents(document.agents, { state: RUNNING_STATE }).map(agentLabel),
        target: '/agents',
        query: { state: 'running' },
      },
    ]
  })

  protected readonly recentWorktrees = computed(() => mostRecentWorktrees(this.store.document().worktrees, RECENT_WORKTREES))
}
