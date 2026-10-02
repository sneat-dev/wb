import { ChangeDetectionStrategy, Component, DestroyRef, inject, input, signal } from '@angular/core'
import { ClipboardWriter } from '../control/clipboard'
import { Glyph } from '../control/glyph'
import { GLYPH_CHECK, GLYPH_COPY } from '../control/glyphs'
import { ListAnnouncer } from './list-announcer'

/** How long the icon shows a tick after it copied, in milliseconds. */
export const COPIED_MS = 1500

/**
 * A small icon button that puts the full `value` in the clipboard
 * (REQ:row-keyboard-and-copy), however much of it the cell shows. It shows a
 * tick for a moment and says "Copied" through the list's one live region. In a
 * list row it is out of the tab order (the list has one tab stop; `c` copies the
 * focused row's name) and is shown on the hovered or focused row, and always on
 * a touch screen. A click does not reach the row, so copying never selects it.
 */
@Component({
  selector: 'app-copy-icon',
  imports: [Glyph],
  template: `<button type="button" class="copy" [attr.aria-label]="label()" [title]="label()" [attr.tabindex]="tabbable() ? null : -1" (click)="copy($event)">
    <app-glyph [paths]="copied() ? check : copyGlyph" />
  </button>`,
  styles: `
    :host {
      display: inline-flex;
      flex: none;
    }
    .copy {
      --glyph-size: 0.875rem;
      display: inline-flex;
      align-items: center;
      justify-content: center;
      width: 1.25rem;
      height: 1.25rem;
      padding: 0;
      border: 0;
      border-radius: var(--radius-sm);
      background: none;
      color: var(--text-3);
      cursor: pointer;
    }
    .copy:hover {
      background: var(--surface-hover);
      color: var(--text);
    }
    /* In a row it shows with the hovered or focused row; on touch there is no hover, so always. */
    @media (hover: hover) {
      :host-context(.row:not(:hover, .focused, .selected)) .copy:not(:focus-visible) {
        opacity: 0;
      }
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CopyIcon {
  readonly value = input.required<string>()
  /** What assistive technology and the tooltip say, for example "Copy task name". */
  readonly label = input.required<string>()
  /** Whether Tab reaches it: not in a list row. */
  readonly tabbable = input(true)

  private readonly clipboard = inject(ClipboardWriter)
  private readonly announcer = inject(ListAnnouncer, { optional: true })
  protected readonly copied = signal(false)
  protected readonly check = GLYPH_CHECK
  protected readonly copyGlyph = GLYPH_COPY
  private timer: ReturnType<typeof setTimeout> | undefined

  constructor() {
    inject(DestroyRef).onDestroy(() => clearTimeout(this.timer))
  }

  protected async copy(event: Event): Promise<void> {
    event.stopPropagation()
    if (!(await this.clipboard.copy(this.value()))) return this.announcer?.say('Copy failed')
    this.copied.set(true)
    this.announcer?.say('Copied')
    clearTimeout(this.timer)
    this.timer = setTimeout(() => this.copied.set(false), COPIED_MS)
  }
}
