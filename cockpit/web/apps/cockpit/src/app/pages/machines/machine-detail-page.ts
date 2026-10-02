import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core'
import { RouterLink } from '@angular/router'
import { FleetStore } from '@cockpit/fleet-data'
import { MachinePanelView } from './machine-panel'

/**
 * One machine as a page, `/machines/:id`: the same content as the side panel of the Machines list, because it is the
 * same component (REQ:detail-routes-share-the-panel, REQ:machine-detail).
 */
@Component({
  selector: 'app-machine-detail-page',
  imports: [RouterLink, MachinePanelView],
  templateUrl: './machine-detail-page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class MachineDetailPage {
  protected readonly store = inject(FleetStore)
  readonly id = input.required<string>()
  protected readonly exists = computed(() => this.store.model().machineById(this.id()) !== undefined)
}
