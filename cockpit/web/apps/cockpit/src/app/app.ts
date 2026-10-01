import { ChangeDetectionStrategy, Component, DestroyRef, inject } from '@angular/core'
import { RouterLink, RouterLinkActive, RouterOutlet } from '@angular/router'
import { FleetStore } from '@cockpit/fleet-data'
import { FleetBanner } from './fleet-banner/fleet-banner'

/** The pages the shell links to, in order. */
export const PAGE_LINKS = [
  { path: '/dashboard', label: 'Dashboard' },
  { path: '/repositories', label: 'Repositories' },
  { path: '/worktrees', label: 'Worktrees' },
  { path: '/agents', label: 'Agents' },
  { path: '/machines', label: 'Machines' },
]

@Component({
  selector: 'app-root',
  imports: [RouterOutlet, RouterLink, RouterLinkActive, FleetBanner],
  templateUrl: './app.html',
  styleUrl: './app.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class App {
  protected readonly links = PAGE_LINKS

  constructor() {
    // The one fleet store reads for as long as the shell is up.
    const store = inject(FleetStore)
    store.start()
    inject(DestroyRef).onDestroy(() => store.stop())
  }
}
