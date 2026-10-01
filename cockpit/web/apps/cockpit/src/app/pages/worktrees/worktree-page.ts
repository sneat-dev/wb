import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core'
import { RouterLink } from '@angular/router'
import { FleetStore, formatAge } from '@cockpit/fleet-data'
import { CodeIndexLabel, CodeIndexPanel, RouteLabel } from '@cockpit/ui'

/**
 * One worktree: the fleet document's metadata for it and its code-index panel.
 * The route's path parameter is named `id` (see list-page.ts).
 */
@Component({
  selector: 'app-worktree-page',
  imports: [RouterLink, RouteLabel, CodeIndexLabel, CodeIndexPanel],
  templateUrl: './worktree-page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class WorktreePage {
  protected readonly store = inject(FleetStore)

  readonly id = input.required<string>()

  protected readonly entry = computed(() => this.store.document().worktrees.find((worktree) => worktree.id === this.id()))
  protected readonly age = formatAge
}
