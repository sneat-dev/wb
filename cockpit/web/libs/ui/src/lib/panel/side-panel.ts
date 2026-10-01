import { MediaMatcher } from '@angular/cdk/layout'
import { ChangeDetectionStrategy, Component, DestroyRef, ElementRef, afterNextRender, inject, input, output, signal, viewChild } from '@angular/core'
import { Glyph } from '../list/glyph'

/** Below this width the panel is a full-screen sheet; the same as the list's narrow layout. */
export const SHEET_QUERY = '(max-width: 47.99rem)'

const FOCUSABLE = 'a[href], button:not([disabled]), summary, input, select, textarea, [tabindex]:not([tabindex="-1"])'

/**
 * The host of the master-detail panel: a region beside the list that holds
 * whatever is projected into it, with a close button. On a phone it is a
 * full-screen sheet and then a modal dialog: Tab stays inside it. Esc closes it
 * through the shell's keyboard (the list registers the panel there), so this
 * component only says that it wants closing. `focusOnOpen` moves the focus into
 * the panel when it appears, for a selection the operator just made; a deep link
 * does not take the focus.
 */
@Component({
  selector: 'app-side-panel',
  imports: [Glyph],
  templateUrl: './side-panel.html',
  styleUrl: './side-panel.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class SidePanel {
  /** What assistive technology calls the panel. */
  readonly label = input.required<string>()
  readonly focusOnOpen = input(false)
  readonly closed = output<void>()

  private readonly panel = viewChild.required<ElementRef<HTMLElement>>('panel')
  /** Whether the panel is the phone's full-screen sheet. */
  protected readonly sheet = signal(false)

  constructor() {
    // The media query, not the observer of the layout module, which would bring its rxjs operators into the first page.
    const media = inject(MediaMatcher).matchMedia(SHEET_QUERY)
    const changed = (event: { matches: boolean }) => this.sheet.set(event.matches)
    this.sheet.set(media.matches)
    media.addListener(changed)
    inject(DestroyRef).onDestroy(() => media.removeListener(changed))
    afterNextRender(() => {
      if (this.focusOnOpen()) this.panel().nativeElement.focus()
    })
  }

  /** In the sheet, Tab wraps from the last control to the first and back. */
  protected trap(event: KeyboardEvent): void {
    if (!this.sheet() || event.key !== 'Tab') return
    const panel = this.panel().nativeElement
    const items = [...panel.querySelectorAll<HTMLElement>(FOCUSABLE)]
    const first = items[0]
    const last = items[items.length - 1]
    const active = panel.ownerDocument.activeElement
    if (event.shiftKey && (active === first || active === panel)) {
      last.focus()
      event.preventDefault()
    } else if (!event.shiftKey && active === last) {
      first.focus()
      event.preventDefault()
    }
  }
}
