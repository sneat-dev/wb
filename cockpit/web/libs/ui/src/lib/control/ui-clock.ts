import { DestroyRef, Injectable, inject, signal } from '@angular/core'

/** How often the shared clock moves, in milliseconds: ages are shown in minutes, so a quarter of a minute is plenty. */
export const UI_CLOCK_TICK_MS = 15_000

/**
 * The one clock every relative time reads. A row of a list does not carry a `now`
 * down to its cells: each `app-relative-time` reads this signal, so one timer
 * moves them all and a test replaces the service to set the time.
 */
@Injectable({ providedIn: 'root' })
export class UiClock {
  readonly now = signal(Date.now())

  constructor() {
    const timer = setInterval(() => this.now.set(Date.now()), UI_CLOCK_TICK_MS)
    inject(DestroyRef).onDestroy(() => clearInterval(timer))
  }
}
