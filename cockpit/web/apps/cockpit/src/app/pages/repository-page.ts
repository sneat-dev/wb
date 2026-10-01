import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core'
import { RouterLink } from '@angular/router'
import { FleetStore, RUNNING_STATE, Repository, agentLabel, codeBrowserLink, filterAgents, filterWorktrees, repositoryLabel, worktreeLabel } from '@cockpit/fleet-data'
import { CodeIndexLabel, CodeIndexPanel, Count, RouteLabel } from '@cockpit/ui'
import { ReadmeSection } from '../readme/readme-section'

/**
 * One repository: the fleet document's metadata for it, its code-index panel
 * and its README. The README is read only with an owner session. The route's
 * path parameter is named `id` (see list-page.ts).
 */
@Component({
  selector: 'app-repository-page',
  imports: [RouterLink, Count, RouteLabel, CodeIndexLabel, CodeIndexPanel, ReadmeSection],
  templateUrl: './repository-page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RepositoryPage {
  protected readonly store = inject(FleetStore)

  readonly id = input.required<string>()

  protected readonly entry = computed(() => this.store.repositoryById().get(this.id()))
  protected readonly label = repositoryLabel

  protected link(repository: Repository): string | null {
    return codeBrowserLink(this.store.codeBrowserUrl(), repository)
  }

  protected readonly worktreeNames = computed(() => filterWorktrees(this.store.document().worktrees, { repository: this.id() }).map(worktreeLabel))
  protected readonly runningAgentNames = computed(() => filterAgents(this.store.document().agents, { repository: this.id(), state: RUNNING_STATE }).map(agentLabel))
}
