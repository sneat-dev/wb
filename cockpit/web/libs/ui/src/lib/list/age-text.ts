import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core'
import { formatAge } from '@cockpit/fleet-data'
import { UiClock } from '../control/ui-clock'

const DAY_MS = 24 * 60 * 60 * 1000

/** A time older than this many days renders muted (REQ:names-and-times-rendering). */
export const MUTED_AFTER_DAYS = 30

/**
 * A time as a relative age, "3 d ago", with the absolute timestamp in the
 * `title`, muted when it is more than 30 days old. `at` is epoch milliseconds
 * or an RFC 3339 string; none shows a dash. The age is measured against the
 * shared `UiClock`, so a row passes no clock down.
 */
@Component({
  selector: 'app-age',
  template: `<span class="age" [class.muted]="muted()" [attr.title]="absolute()">{{ text() }}</span>`,
  styles: `
    .age {
      font-variant-numeric: tabular-nums;
    }
    .muted {
      color: var(--text-3);
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class AgeText {
  readonly at = input<number | string | undefined>()

  private readonly clock = inject(UiClock).now
  private readonly iso = computed(() => {
    const at = this.at()
    const time = typeof at === 'number' ? at : Date.parse(at ?? '')
    return Number.isNaN(time) ? undefined : new Date(time).toISOString()
  })
  protected readonly absolute = computed(() => this.iso() ?? null)
  protected readonly text = computed(() => (this.iso() === undefined ? '—' : formatAge(this.iso(), this.clock())))
  protected readonly muted = computed(() => this.iso() === undefined || this.clock() - Date.parse(this.iso() as string) > MUTED_AFTER_DAYS * DAY_MS)
}
