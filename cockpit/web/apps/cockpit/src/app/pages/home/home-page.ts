import { ChangeDetectionStrategy, Component, computed, inject } from '@angular/core'
import { Params } from '@angular/router'
import {
  FleetStore,
  RUNNING_STATE,
  filterAgents,
  mostRecentWorktrees,
} from '@cockpit/fleet-data'
import { RouterLink } from '@angular/router'
import { RouteLabel } from '@cockpit/ui/route-label'
import { watchMetrics } from '../../metrics/metrics-poller'

/** How many of the most recently active worktrees the dashboard lists. */
export const RECENT_WORKTREES = 10

interface Tile {
  title: string
  label: string
  count: number
  target: string
  query: Params
}

/** The fleet at a glance: one tile per collection, the machines, and recent work. */
@Component({
  selector: 'app-home-page',
  imports: [RouterLink, RouteLabel],
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
      { title: 'Machines', label: 'machines', count: document.machines.length, target: '/machines', query: {} },
      { title: 'Repositories', label: 'repositories', count: document.repositories.length, target: '/repositories', query: {} },
      { title: 'Worktrees', label: 'worktrees', count: document.worktrees.length, target: '/worktrees', query: {} },
      { title: 'Agents', label: 'agents', count: document.agents.length, target: '/agents', query: {} },
      {
        title: 'Running agents',
        label: 'running agents',
        count: filterAgents(document.agents, { state: RUNNING_STATE }).length,
        target: '/agents',
        query: { state: 'running' },
      },
    ]
  })

  protected readonly recentWorktrees = computed(() => mostRecentWorktrees(this.store.document().worktrees, RECENT_WORKTREES))
}
