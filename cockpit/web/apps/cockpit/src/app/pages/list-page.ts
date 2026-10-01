import { Directive, computed, inject, input } from '@angular/core'
import { FleetStore } from '@cockpit/fleet-data'
import { repositoryOptions } from '@cockpit/fleet-data/list'
import { FilterChange } from '@cockpit/ui'
import { FilterNavigator } from '../filter-navigator'

/**
 * What every list page shares: the store, the machine and repository filters
 * (bound from the URL query by the router), the options the filter bar offers,
 * and how a changed filter reaches the URL.
 *
 * `machine` holds a machine id and `repository` a repository id. A future
 * detail route must name its path parameter `id`: component input binding
 * would otherwise hand a path parameter to the `repository` input.
 */
@Directive()
export abstract class ListPage {
  protected readonly store = inject(FleetStore)
  private readonly navigator = inject(FilterNavigator)

  readonly machine = input<string>()
  readonly repository = input<string>()

  /** The repositories the filter bar offers, narrowed to the selected machine. */
  protected readonly repositoryOptions = computed(() => repositoryOptions(this.store.document().repositories, this.machine()))

  protected filter(change: FilterChange): void {
    void this.navigator.apply(change)
  }
}
