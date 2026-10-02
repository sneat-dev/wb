import { ChangeDetectionStrategy, Component, computed, input, inject } from '@angular/core'
import { RouterLink } from '@angular/router'
import { FleetStore } from '@cockpit/fleet-data'
import { WorktreePanelView } from './worktree-panel'

/**
 * One worktree as a page: the same content as the side panel of the Worktrees
 * list, because it is the same component (REQ:detail-routes-share-the-panel).
 * The route's path parameter is named `id` (see list-page.ts).
 */
@Component({
  selector: 'app-worktree-page',
  imports: [RouterLink, WorktreePanelView],
  templateUrl: './worktree-page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class WorktreePage {
  protected readonly store = inject(FleetStore)
  readonly id = input.required<string>()
  protected readonly exists = computed(() => this.store.model().worktreeById(this.id()) !== undefined)
}
