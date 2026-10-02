import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core'
import { RouterLink } from '@angular/router'
import { FleetStore } from '@cockpit/fleet-data'
import { buildRepositories, findMergedRepository } from '@cockpit/fleet-data/list'
import { RepositoryPanelView } from './repository-panel'

/**
 * A repository by the id of one of its entries, the address that worked before the Repositories page
 * merged the machines (`/repositories/:id`, REQ:repository-detail): it still opens the repository's
 * page, which is the merged panel, from whichever checkout's id it carries. The route's path
 * parameter is named `id` (see list-page.ts).
 */
@Component({
  selector: 'app-repository-page',
  imports: [RouterLink, RepositoryPanelView],
  templateUrl: './repository-page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RepositoryPage {
  protected readonly store = inject(FleetStore)
  readonly id = input.required<string>()
  protected readonly row = computed(() => findMergedRepository(buildRepositories(this.store.model()), this.id()))
}
