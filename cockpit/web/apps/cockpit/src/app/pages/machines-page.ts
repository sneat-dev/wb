import { ChangeDetectionStrategy, Component, computed } from '@angular/core'
import { Machine, filterMachines, filterRepositories, filterWorktrees, repositoryLabel, worktreeLabel } from '@cockpit/fleet-data'
import { Count, FilterBar, RouteLabel } from '@cockpit/ui'
import { TableModule } from 'primeng/table'
import { ListPage } from './list-page'

/** This machine and every other machine the fleet has a snapshot for. */
@Component({
  selector: 'app-machines-page',
  imports: [TableModule, Count, FilterBar, RouteLabel],
  templateUrl: './machines-page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class MachinesPage extends ListPage {
  protected readonly rows = computed(() => {
    const document = this.store.document()
    return filterMachines(document.machines, { machine: this.machine(), repository: this.repository() }, document.repositories)
  })

  // Built with the filters the clicks open, so each number, hover card and list are one set.
  protected repositoryNames(machine: Machine): string[] {
    return filterRepositories(this.store.document().repositories, { machine: machine.id }).map(repositoryLabel)
  }

  protected worktreeNames(machine: Machine): string[] {
    return filterWorktrees(this.store.document().worktrees, { machine: machine.id }).map(worktreeLabel)
  }
}
