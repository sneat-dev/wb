import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'
import { formatAge } from '@cockpit/fleet-data'

/**
 * A time as its age ("5 min ago") in a `<time>` element, with the exact time as
 * the tooltip. An absent or unreadable time says "age unknown" and carries no
 * `datetime`. The caller owns the clock: the age is measured against `now`.
 */
@Component({
  selector: 'app-relative-time',
  template: `<time [attr.datetime]="valid() ? at() : null" [attr.title]="valid() ? at() : null">{{ text() }}</time>`,
  styles: `
    :host {
      color: var(--text-3);
      font-size: var(--fs-xs);
      font-variant-numeric: tabular-nums;
      white-space: nowrap;
    }
  `,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RelativeTime {
  readonly at = input<string | undefined>()
  /** The clock the age is measured against, in epoch milliseconds. */
  readonly now = input.required<number>()

  protected readonly valid = computed(() => this.at() !== undefined && !Number.isNaN(Date.parse(this.at() as string)))
  protected readonly text = computed(() => formatAge(this.at(), this.now()))
}
