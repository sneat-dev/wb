import { ChangeDetectionStrategy, Component, DestroyRef, ViewContainerRef, afterNextRender, inject, signal, viewChild } from '@angular/core'
import { RouterOutlet } from '@angular/router'
import { FleetStore } from '@cockpit/fleet-data'
import { FleetBanner } from './fleet-banner/fleet-banner'
import { PageTitle } from './shell/page-title'
import { SchemaMismatch } from './shell/schema-mismatch'
import { SkeletonRows } from './shell/skeleton-rows'
import { TopBar } from './shell/top-bar'
import { Shortcuts } from './shortcuts/shortcuts'

export { PAGE_LINKS } from './nav'

/**
 * The shell: the top bar, the page, the palette and the shortcut sheet. It
 * starts the one fleet store and the keyboard, fetches the palette and the
 * shortcut sheet as soon as it has rendered, shows the visually hidden `h1`
 * that names the page (the document title the route set), and shows the
 * schema-mismatch state instead of any data when the daemon speaks another
 * schema version.
 */
@Component({
  selector: 'app-root',
  imports: [RouterOutlet, FleetBanner, TopBar, SchemaMismatch, SkeletonRows],
  templateUrl: './app.html',
  styleUrl: './app.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class App {
  protected readonly store = inject(FleetStore)
  /** The name of the page, which is also the document title (REQ:no-visible-page-heading). */
  protected readonly pageTitle = inject(PageTitle).title

  /** Whether a page has been routed to yet. */
  protected readonly pageShown = signal(false)
  private readonly overlays = viewChild.required('overlays', { read: ViewContainerRef })

  constructor() {
    // The overlays are a lazy chunk, fetched right after the first render so that they are
    // there before they are wanted; they are not in the initial script (REQ:initial-script-size).
    const destroyed = inject(DestroyRef)
    let alive = true
    destroyed.onDestroy(() => (alive = false))
    afterNextRender(async () => {
      const { Overlays } = await import('./shell/overlays')
      if (alive) this.overlays().createComponent(Overlays)
    })
    // The one fleet store reads for as long as the shell is up.
    this.store.start()
    destroyed.onDestroy(() => this.store.stop())
    destroyed.onDestroy(inject(Shortcuts).attach())
  }
}
