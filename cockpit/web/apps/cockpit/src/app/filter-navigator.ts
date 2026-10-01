import { Injectable, inject } from '@angular/core'
import { Router } from '@angular/router'
import { FilterChange } from '@cockpit/ui'

/**
 * Applies a filter change to the address: the filters live in the URL query,
 * so every filtered list is a link that can be copied and shared. An empty
 * command list keeps the current page; a null value drops the parameter.
 */
@Injectable({ providedIn: 'root' })
export class FilterNavigator {
  private readonly router = inject(Router)

  apply(change: FilterChange): Promise<boolean> {
    return this.router.navigate([], {
      queryParams: { [change.key]: change.value },
      queryParamsHandling: 'merge',
    })
  }
}
