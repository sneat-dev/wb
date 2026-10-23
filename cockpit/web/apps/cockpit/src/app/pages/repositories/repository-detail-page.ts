import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core'
import { RouterLink } from '@angular/router'
import { FleetStore } from '@cockpit/fleet-data'
import { buildRepositories } from '@cockpit/fleet-data/list'
import { findByAddress } from './repository-address'
import { RepositoryPanelView } from './repository-panel'

/**
 * One repository as a page, `/repositories/:host/:owner/:name`: the same content as the side panel of
 * the Repositories list, because it is the same component (REQ:detail-routes-share-the-panel). A
 * repository with no host is written `-` for it. Its branches are read when it opens.
 */
@Component({
  selector: 'app-repository-detail-page',
  imports: [RouterLink, RepositoryPanelView],
  templateUrl: './repository-page.html',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RepositoryDetailPage {
  protected readonly store = inject(FleetStore)
  readonly host = input.required<string>()
  readonly owner = input.required<string>()
  readonly name = input.required<string>()
  protected readonly row = computed(() => findByAddress(buildRepositories(this.store.model()), this.host(), this.owner(), this.name()))
}
