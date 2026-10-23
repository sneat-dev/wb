import { MediaMatcher } from '@angular/cdk/layout'
import { DestroyRef, Injectable, inject, signal } from '@angular/core'

/** Below this width the panel is a full-screen sheet; the same as the list's narrow layout. */
export const SHEET_QUERY = '(max-width: 47.99rem)'

/**
 * Whether the screen is phone-sized, so the side panel is a full-screen sheet and
 * the list behind it is inert. The media query, not the layout module's
 * observer, which would bring its rxjs operators into the first page.
 */
@Injectable({ providedIn: 'root' })
export class SheetMode {
  readonly active = signal(false)

  constructor() {
    const media = inject(MediaMatcher).matchMedia(SHEET_QUERY)
    const changed = (event: { matches: boolean }) => this.active.set(event.matches)
    this.active.set(media.matches)
    media.addListener(changed)
    inject(DestroyRef).onDestroy(() => media.removeListener(changed))
  }
}
