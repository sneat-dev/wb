import { ChangeDetectionStrategy, Component, computed } from '@angular/core'
import { filterWorktrees, formatAge } from '@cockpit/fleet-data'
import { CodeIndexLabel, FilterBar, RouteLabel } from '@cockpit/ui'
import { RouterLink } from '@angular/router'
import { TableModule } from 'primeng/table'
import { ListPage } from '../list-page'

/** Every WB task worktree on every machine. */
@Component({
  selector: 'app-worktrees-page',
  imports: [RouterLink, TableModule, FilterBar, RouteLabel, CodeIndexLabel],
  templateUrl: './worktrees-page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class WorktreesPage extends ListPage {
  protected readonly rows = computed(() =>
    filterWorktrees(this.store.document().worktrees, { machine: this.machine(), repository: this.repository() }),
  )

  protected readonly age = formatAge
}
