import { DOCUMENT } from '@angular/common'
import { ChangeDetectionStrategy, Component, DestroyRef, ViewContainerRef, afterNextRender, effect, inject, signal, viewChild } from '@angular/core'
import { RouterOutlet } from '@angular/router'
import { FleetStore } from '@cockpit/fleet-data'
import { FleetBanner } from './fleet-banner/fleet-banner'
import { whenIdle } from './shell/idle'
import { OverlayLoader } from './shell/overlay-loader'
import { PageTitle } from './shell/page-title'
import { ShellState } from './shell/shell-state'
import { SkeletonRows } from './shell/skeleton-rows'
import { TopBar } from './shell/top-bar'
import { Shortcuts } from './shortcuts/shortcuts'

export { PAGE_LINKS } from './nav'

/**
 * The shell: the top bar, the page, the palette and the shortcut sheet. It
 * starts the one fleet store and the keyboard, fetches the palette and the
 * shortcut sheet when the browser is idle after it has rendered, shows the visually hidden `h1`
 * that names the page (the document title the route set), and shows the
 * schema version.
 */
@Component({
  selector: 'app-root',
  imports: [RouterOutlet, FleetBanner, TopBar, SkeletonRows],
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
    const destroyed = inject(DestroyRef)
    const loader = inject(OverlayLoader)
    const shell = inject(ShellState)
    const win = inject(DOCUMENT).defaultView as Window
    // The overlays are a lazy chunk, fetched when the browser is idle after the first render, so
    // that it never competes with the first page; it is not part of the first page's script
    // (REQ:initial-script-size). Opening one before then fetches it at once.
    afterNextRender(() => {
      destroyed.onDestroy(loader.attach(this.overlays()))
      destroyed.onDestroy(whenIdle(win, () => void loader.ensure()))
    })
    effect(() => {
      if (shell.modalOpen() || this.store.schemaMismatch() !== null) void loader.ensure()
    })
    // The one fleet store reads for as long as the shell is up.
    this.store.start()
    destroyed.onDestroy(() => this.store.stop())
    destroyed.onDestroy(inject(Shortcuts).attach())
  }
}
