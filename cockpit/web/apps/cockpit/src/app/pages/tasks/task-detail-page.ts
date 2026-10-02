import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core'
import { RouterLink } from '@angular/router'
import { FleetStore } from '@cockpit/fleet-data'
import { TaskPanelView } from './task-panel'

/**
 * One task as a page, `/tasks/detail?task=<name>` (the name percent-encoded in the query): the same content as
 * the side panel of the Tasks list, because it is the same component (REQ:detail-routes-share-the-panel).
 */
@Component({
  selector: 'app-task-detail-page',
  imports: [RouterLink, TaskPanelView],
  templateUrl: './task-detail-page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class TaskDetailPage {
  protected readonly store = inject(FleetStore)
  /** The `task` query parameter, which the router binds. */
  readonly task = input<string>()
  /** The name of the task, when the document lists it. */
  protected readonly found = computed(() => {
    const name = this.task()
    return name !== undefined && this.store.model().taskNamed(name) !== undefined ? name : undefined
  })
}
