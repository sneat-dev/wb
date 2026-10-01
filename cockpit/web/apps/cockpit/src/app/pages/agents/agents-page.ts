import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'
import { filterAgents } from '@cockpit/fleet-data'
import { agentLabel } from '@cockpit/fleet-data/list'
import { FilterBar, RouteLabel } from '@cockpit/ui'
import { TableModule } from 'primeng/table'
import { ListPage } from '../list-page'

/** Every registered agent session and dispatched run. */
@Component({
  selector: 'app-agents-page',
  imports: [TableModule, FilterBar, RouteLabel],
  templateUrl: './agents-page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class AgentsPage extends ListPage {
  /** An agent state, for example the "running" a count of running agents opens. */
  readonly state = input<string>()

  protected readonly rows = computed(() =>
    filterAgents(this.store.document().agents, {
      machine: this.machine(),
      repository: this.repository(),
      state: this.state(),
    }),
  )

  protected readonly label = agentLabel
}
