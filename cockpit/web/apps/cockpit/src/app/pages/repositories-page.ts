import { ChangeDetectionStrategy, Component, computed } from '@angular/core'
import {
  Repository,
  RUNNING_STATE,
  agentLabel,
  codeBrowserLink,
  filterAgents,
  filterRepositories,
  filterWorktrees,
  repositoryLabel,
  worktreeLabel,
} from '@cockpit/fleet-data'
import { CodeIndexLabel, Count, FilterBar, RouteLabel } from '@cockpit/ui'
import { RouterLink } from '@angular/router'
import { TableModule } from 'primeng/table'
import { ListPage } from './list-page'

/** Every repository on every machine, with counts that open their entities. */
@Component({
  selector: 'app-repositories-page',
  imports: [RouterLink, TableModule, Count, FilterBar, RouteLabel, CodeIndexLabel],
  templateUrl: './repositories-page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RepositoriesPage extends ListPage {
  protected readonly rows = computed(() =>
    filterRepositories(this.store.document().repositories, { machine: this.machine(), repository: this.repository() }),
  )

  protected readonly label = repositoryLabel

  protected link(repository: Repository): string | null {
    return codeBrowserLink(this.store.codeBrowserUrl(), repository)
  }

  // Each count's names are built with the filter its click opens, so the
  // number, the hover card and the list are one set.
  protected worktreeNames(repository: Repository): string[] {
    return filterWorktrees(this.store.document().worktrees, { repository: repository.id }).map(worktreeLabel)
  }

  protected runningAgentNames(repository: Repository): string[] {
    return filterAgents(this.store.document().agents, { repository: repository.id, state: RUNNING_STATE }).map(agentLabel)
  }
}
