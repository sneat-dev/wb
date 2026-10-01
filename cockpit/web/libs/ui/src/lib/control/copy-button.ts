import { ChangeDetectionStrategy, Component, DestroyRef, inject, input, output, signal } from '@angular/core'
import { ClipboardWriter } from './clipboard'
import { Glyph } from './glyph'
import { GLYPH_CHECK, GLYPH_COPY } from './glyphs'

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
      <app-glyph [paths]="state() === 'copied' ? check : copyGlyph" />
      <span aria-hidden="true">{{ word() }}</span>
    </button>
    <span class="visually-hidden" role="status">{{ status() }}</span>`,
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
      min-width: 7rem;
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
  /** The word on the button while idle: "Copy", or "Copy template" for a command with parts to edit. */
  readonly idleWord = input('Copy')
  /** What is announced once the text is on the clipboard. */
  readonly doneStatus = input('Copied')
  /** Emits the copied text once it is on the clipboard. */
  readonly copied = output<string>()

  private readonly clipboard = inject(ClipboardWriter)
  protected readonly state = signal<'idle' | 'copied' | 'failed'>('idle')
  private timer: ReturnType<typeof setTimeout> | undefined
  private alive = true
  protected readonly check = GLYPH_CHECK
  protected readonly copyGlyph = GLYPH_COPY

  constructor() {
    inject(DestroyRef).onDestroy(() => {
      this.alive = false
      clearTimeout(this.timer)
    })
  }

  protected word(): string {
    return { idle: this.idleWord(), copied: 'Copied', failed: 'Copy failed' }[this.state()]
  }

  protected status(): string {
    return { idle: '', copied: this.doneStatus(), failed: 'Copy failed' }[this.state()]
  }

  protected async copy(): Promise<void> {
    const text = this.text()
    const ok = await this.clipboard.copy(text)
    // The page may have moved on while the browser answered.
    if (!this.alive) return
    this.state.set(ok ? 'copied' : 'failed')
    if (ok) this.copied.emit(text)
    clearTimeout(this.timer)
    this.timer = setTimeout(() => this.state.set('idle'), COPIED_FEEDBACK_MS)
  }
}
