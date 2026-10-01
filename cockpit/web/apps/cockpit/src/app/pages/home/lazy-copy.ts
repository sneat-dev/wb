import { ChangeDetectionStrategy, Component, DestroyRef, inject, input, signal } from '@angular/core'
import type { CopyCommand } from '@cockpit/fleet-data'
import { ClipboardWriter, COPIED_FEEDBACK_MS, Glyph } from '@cockpit/ui/control'
import { GLYPH_CHECK, GLYPH_COPY } from '@cockpit/ui/state'

/**
 * A "Copy command" button whose command is built when it is pressed, so the command templates (a
 * lazy entry point of the library) are not part of the first page. It copies what the library
 * built and says so; a command the library refused is said in words, with its reason, and nothing
 * is copied. Executes nothing (REQ:copy-the-command).
 */
@Component({
  selector: 'app-lazy-copy',
  imports: [Glyph],
  template: `<button type="button" class="home-copy" [class.done]="state() === 'copied'" [class.failed]="state() === 'failed'" [attr.aria-label]="label()" [attr.title]="reason() ?? null" (click)="copy()">
      <app-glyph [paths]="state() === 'copied' ? check : glyph" />
      <span aria-hidden="true">{{ word() }}</span>
    </button>
    <span class="visually-hidden" role="status">{{ status() }}</span>`,
  styleUrl: './lazy-copy.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class LazyCopy {
  /** Builds the command: an `import()` of the templates, then the template. */
  readonly build = input.required<() => Promise<CopyCommand>>()
  /** The word on the idle button: "Copy command", or "Copy template" for a command with parts to edit. */
  readonly idleWord = input('Copy command')
  /** The accessible name, which says what is copied. */
  readonly label = input('Copy command')

  private readonly clipboard = inject(ClipboardWriter)
  protected readonly state = signal<'idle' | 'copied' | 'failed' | 'refused'>('idle')
  protected readonly reason = signal<string | undefined>(undefined)
  protected readonly check = GLYPH_CHECK
  protected readonly glyph = GLYPH_COPY
  private timer: ReturnType<typeof setTimeout> | undefined
  private alive = true

  constructor() {
    inject(DestroyRef).onDestroy(() => {
      this.alive = false
      clearTimeout(this.timer)
    })
  }

  protected word(): string {
    return { idle: this.idleWord(), copied: 'Copied', failed: 'Copy failed', refused: 'Not copyable' }[this.state()]
  }

  protected status(): string {
    return { idle: '', copied: 'Copied', failed: 'Copy failed', refused: this.reason() ?? '' }[this.state()]
  }

  protected async copy(): Promise<void> {
    const command = await this.build()()
    if (!this.alive) return
    if (!command.ok) {
      this.reason.set(command.reason)
      this.settle('refused')
      return
    }
    const copied = await this.clipboard.copy(command.text)
    if (!this.alive) return
    this.settle(copied ? 'copied' : 'failed')
  }

  private settle(state: 'copied' | 'failed' | 'refused'): void {
    this.state.set(state)
    clearTimeout(this.timer)
    this.timer = setTimeout(() => this.state.set('idle'), COPIED_FEEDBACK_MS)
  }
}
