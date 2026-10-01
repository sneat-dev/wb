import { ChangeDetectionStrategy, Component, DestroyRef, inject } from '@angular/core'
import { RouterOutlet } from '@angular/router'
import { FleetStore } from '@cockpit/fleet-data'
import { FleetBanner } from './fleet-banner/fleet-banner'
import { CommandPalette } from './palette/command-palette'
import { PageTitle } from './shell/page-title'
import { SchemaMismatch } from './shell/schema-mismatch'
import { SkeletonRows } from './shell/skeleton-rows'
import { TopBar } from './shell/top-bar'
import { ShortcutSheet } from './shortcuts/shortcut-sheet'
import { Shortcuts } from './shortcuts/shortcuts'

export { PAGE_LINKS } from './nav'

/**
 * The shell: the top bar, the page, the palette and the shortcut sheet. It
 * starts the one fleet store and the keyboard, shows the visually hidden `h1`
 * that names the page (the document title the route set), and shows the
 * schema-mismatch state instead of any data when the daemon speaks another
 * schema version.
 */
@Component({
  selector: 'app-root',
  imports: [RouterOutlet, FleetBanner, TopBar, CommandPalette, ShortcutSheet, SchemaMismatch, SkeletonRows],
  templateUrl: './app.html',
  styleUrl: './app.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class App {
  protected readonly store = inject(FleetStore)
  /** The name of the page, which is also the document title (REQ:no-visible-page-heading). */
  protected readonly pageTitle = inject(PageTitle).title

  constructor() {
    const destroyed = inject(DestroyRef)
    // The one fleet store reads for as long as the shell is up.
    this.store.start()
    destroyed.onDestroy(() => this.store.stop())
    destroyed.onDestroy(inject(Shortcuts).attach())
  }
}
