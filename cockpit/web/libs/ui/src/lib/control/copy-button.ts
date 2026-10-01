import { ChangeDetectionStrategy, Component, DestroyRef, inject, input, output, signal } from '@angular/core'
import { ClipboardWriter } from './clipboard'
import { Glyph } from './glyph'

/** How long "Copied" stays on the button, in milliseconds. */
export const COPIED_FEEDBACK_MS = 2000

/**
 * A button that copies `text` and says so: the word changes to "Copied" with a
 * check, or to "Copy failed" when the browser refused, and returns after two
 * seconds. The button keeps one width, so the feedback moves nothing. The
 * result is also announced politely for assistive technology.
 */
@Component({
  selector: 'app-copy-button',
  imports: [Glyph],
  template: `<button type="button" class="copy" [class.done]="state() === 'copied'" [class.failed]="state() === 'failed'" [attr.aria-label]="label()" (click)="copy()">
      <app-glyph [name]="state() === 'copied' ? 'check' : 'copy'" />
      <span aria-hidden="true">{{ word() }}</span>
    </button>
    <span class="visually-hidden" role="status">{{ state() === 'copied' ? 'Copied' : state() === 'failed' ? 'Copy failed' : '' }}</span>`,
  styles: `
    :host {
      display: inline-flex;
      flex: none;
    }
    .copy {
      --glyph-size: 0.875rem;
      display: inline-flex;
      gap: 6px;
      align-items: center;
      justify-content: center;
      min-width: 5.5rem;
      height: 1.75rem;
      padding: 0 var(--space-2);
      border: 1px solid var(--border-strong);
      border-radius: var(--radius-md);
      background: var(--surface);
      color: var(--text);
      font-size: var(--fs-xs);
      font-weight: var(--fw-medium);
      cursor: pointer;
      transition:
        background var(--transition),
        border-color var(--transition);
    }
    .copy:hover {
      background: var(--surface-hover);
    }
    .copy.done {
      border-color: var(--ok-border);
      background: var(--ok-soft);
      color: var(--ok);
    }
    .copy.failed {
      border-color: var(--bad-border);
      background: var(--bad-soft);
      color: var(--bad);
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CopyButton {
  /** The text that goes on the clipboard. */
  readonly text = input.required<string>()
  /** The accessible name, which says what is copied. */
  readonly label = input('Copy')
  /** Emits the copied text once it is on the clipboard. */
  readonly copied = output<string>()

  private readonly clipboard = inject(ClipboardWriter)
  protected readonly state = signal<'idle' | 'copied' | 'failed'>('idle')
  private timer: ReturnType<typeof setTimeout> | undefined

  constructor() {
    inject(DestroyRef).onDestroy(() => clearTimeout(this.timer))
  }

  protected word(): string {
    return { idle: 'Copy', copied: 'Copied', failed: 'Copy failed' }[this.state()]
  }

  protected async copy(): Promise<void> {
    const text = this.text()
    const ok = await this.clipboard.copy(text)
    this.state.set(ok ? 'copied' : 'failed')
    if (ok) this.copied.emit(text)
    clearTimeout(this.timer)
    this.timer = setTimeout(() => this.state.set('idle'), COPIED_FEEDBACK_MS)
  }
}
