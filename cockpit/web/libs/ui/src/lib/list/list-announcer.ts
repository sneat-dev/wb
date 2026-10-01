import { Injectable, signal } from '@angular/core'

/**
 * The one polite live region of a list (or of a panel): the copy buttons in its
 * rows say "Copied" through it, instead of each row carrying a live region of its own.
 */
@Injectable()
export class ListAnnouncer {
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
