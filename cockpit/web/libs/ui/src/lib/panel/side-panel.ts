import { DOCUMENT } from '@angular/common'
import { ChangeDetectionStrategy, Component, ElementRef, afterNextRender, effect, inject, input, output, viewChild } from '@angular/core'
import { Glyph } from '../control/glyph'
import { GLYPH_X } from '../control/glyphs'
import { SheetMode } from './sheet-mode'

const FOCUSABLE = 'a[href], button:not([disabled]), summary, input, select, textarea, [tabindex]:not([tabindex="-1"])'

/**
 * The host of the master-detail panel: a region beside the list that holds
 * whatever is projected into it, with a close button. Beside the list the focus
 * stays in the list (`j` and `k` keep browsing and the panel follows) and Tab
 * reaches the panel next. On a phone it is a full-screen modal dialog: the focus
 * moves into it when it opens, Tab stays inside it, the page behind does not
 * scroll and the list is inert (the list sets that). Esc closes it through the
 * shell's keyboard (the list registers the panel there), so this component only
 * says that it wants closing.
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
  readonly closed = output<void>()

  private readonly panel = viewChild.required<ElementRef<HTMLElement>>('panel')
  protected readonly sheet = inject(SheetMode).active
  protected readonly cross = GLYPH_X

  constructor() {
    const body = inject(DOCUMENT).body
    afterNextRender(() => {
      if (this.sheet()) this.focus()
    })
    // The page behind a sheet does not scroll.
    effect((onCleanup) => {
      if (!this.sheet()) return
      const before = body.style.overflow
      body.style.overflow = 'hidden'
      onCleanup(() => (body.style.overflow = before))
    })
  }

  /** Moves the focus into the panel. */
  focus(): void {
    this.panel().nativeElement.focus()
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
