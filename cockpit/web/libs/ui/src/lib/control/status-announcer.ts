import { ChangeDetectionStrategy, Component, Injectable, inject, signal } from '@angular/core'

/**
 * The one polite live region the copy buttons share (REQ:accessibility): a button says "Copied" through it, instead
 * of each button (and there can be hundreds in a list) carrying a live region of its own. It is rendered once, by the
 * shell, as `<app-status-region>`; without one the words are simply not spoken.
 */
@Injectable({ providedIn: 'root' })
export class StatusAnnouncer {
  readonly message = signal('')
  private timer: ReturnType<typeof setTimeout> | undefined

  /** Says `text`, and clears it after a moment so the same words can be said again. */
  say(text: string): void {
    // A repeated message needs a change in between to be announced again.
    this.message.set('')
    queueMicrotask(() => this.message.set(text))
    clearTimeout(this.timer)
    this.timer = setTimeout(() => this.message.set(''), 2000)
  }
}

/** The shared status region: put it once in the shell. */
@Component({
  selector: 'app-status-region',
  template: `<span class="visually-hidden" role="status">{{ announcer.message() }}</span>`,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class StatusRegion {
  protected readonly announcer = inject(StatusAnnouncer)
}
