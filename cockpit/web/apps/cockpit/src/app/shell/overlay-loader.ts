import { Injectable, InjectionToken, Type, ViewContainerRef, inject } from '@angular/core'
import { ShellState } from './shell-state'

/** How long to wait before the one retry of a failed fetch of the overlays, in milliseconds. */
export const OVERLAY_RETRY_MS = 1000

/** Fetches the overlays chunk; a test replaces it. */
export const OVERLAYS_IMPORT = new InjectionToken<() => Promise<{ Overlays: Type<unknown> }>>('overlays import', {
  providedIn: 'root',
  factory: () => () => import('./overlays'),
})

/**
 * Fetches the palette and the shortcut sheet (a lazy chunk) and puts them in the
 * shell. The shell asks for them when the browser is idle after the first render,
 * and at once if one is opened before. A failed fetch is retried once; if that
 * fails too, whatever was opened is closed again (so the keyboard shortcuts are not
 * left disabled by an overlay that is not there) and the next attempt starts afresh.
 */
@Injectable({ providedIn: 'root' })
export class OverlayLoader {
  private readonly shell = inject(ShellState)
  private readonly importer = inject(OVERLAYS_IMPORT)
  private host: ViewContainerRef | undefined
  private loaded: Type<unknown> | undefined
  private created = false
  private pending: Promise<void> | undefined

  /** Where the overlays go; the returned function lets go of it. */
  attach(host: ViewContainerRef): () => void {
    this.host = host
    this.create()
    return () => {
      this.host = undefined
    }
  }

  ensure(): Promise<void> {
    this.pending ??= this.fetch()
    return this.pending
  }

  private async fetch(): Promise<void> {
    for (let attempt = 0; this.loaded === undefined; attempt++) {
      try {
        this.loaded = (await this.importer()).Overlays
      } catch (error) {
        if (attempt > 0) {
          console.error('the search and shortcut overlays could not be loaded', error)
          this.shell.closePalette()
          this.shell.closeSheet()
          this.pending = undefined
          return
        }
        await new Promise((resolve) => setTimeout(resolve, OVERLAY_RETRY_MS))
      }
    }
    this.create()
  }

  private create(): void {
    if (this.host && this.loaded && !this.created) {
      this.host.createComponent(this.loaded)
      this.created = true
    }
  }
}
