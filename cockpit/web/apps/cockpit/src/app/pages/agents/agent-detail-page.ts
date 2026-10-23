import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core'
import { RouterLink } from '@angular/router'
import { FleetStore } from '@cockpit/fleet-data'
import { AgentPanelView } from './agent-panel'

/**
 * One agent as a page, `/agents/:id`: the same content as the side panel of the Agents list, because it is the same
 * component (REQ:detail-routes-share-the-panel). The route's path parameter is named `id` (see list-page.ts).
 */
@Component({
  selector: 'app-agent-detail-page',
  imports: [RouterLink, AgentPanelView],
  templateUrl: './agent-detail-page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class AgentDetailPage {
  protected readonly store = inject(FleetStore)
  readonly id = input.required<string>()
  protected readonly exists = computed(() => this.store.model().agentById(this.id()) !== undefined)
}
