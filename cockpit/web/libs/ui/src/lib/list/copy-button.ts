import { Clipboard } from '@angular/cdk/clipboard'
import { ChangeDetectionStrategy, Component, DestroyRef, inject, input, signal } from '@angular/core'
import { Glyph } from './glyph'

/** How long the button shows that it copied, in milliseconds. */
export const COPIED_MS = 1500

/**
 * A button that puts the full `value` in the clipboard (REQ:row-keyboard-and-copy),
 * however much of it the cell shows. It says "Copied" for a moment, in text for
 * assistive technology and as a tick for the eye. A click does not reach the
 * row, so copying never selects it.
 */
@Component({
  selector: 'app-copy-button',
  imports: [Glyph],
  template: `<button type="button" class="copy" [attr.aria-label]="label()" [title]="label()" [attr.tabindex]="tabbable() ? null : -1" (click)="copy($event)">
      <app-glyph [name]="copied() ? 'check' : 'copy'" />
    </button>
    <span class="visually-hidden" aria-live="polite">{{ copied() ? 'Copied' : '' }}</span>`,
  styles: `
    :host {
      display: inline-flex;
      flex: none;
    }
    .copy {
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
      opacity: 0.6;
    }
    .copy:hover,
    .copy:focus-visible,
    :host-context(.row:hover) .copy,
    :host-context(.content) .copy {
      opacity: 1;
    }
    .copy:hover {
      background: var(--surface-hover);
      color: var(--text);
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CopyButton {
  readonly value = input.required<string>()
  /** What assistive technology and the tooltip say, for example "Copy task name". */
  readonly label = input.required<string>()
  /** Whether Tab reaches it: not in a list row, where the keyboard has its own way. */
  readonly tabbable = input(true)

  private readonly clipboard = inject(Clipboard)
  protected readonly copied = signal(false)
  private timer: ReturnType<typeof setTimeout> | undefined

  constructor() {
    inject(DestroyRef).onDestroy(() => clearTimeout(this.timer))
  }

  protected copy(event: Event): void {
    event.stopPropagation()
    if (!this.clipboard.copy(this.value())) return
    this.copied.set(true)
    clearTimeout(this.timer)
    this.timer = setTimeout(() => this.copied.set(false), COPIED_MS)
  }
}
