import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core'
import { formatAge } from '@cockpit/fleet-data'
import { UiClock } from './ui-clock'

/**
 * A time as its age ("5 min ago") in a `<time>` element. `datetime` is the
 * normalised ISO time; the tooltip is the time in the viewer's zone with the UTC
 * value. An absent or unreadable time says "age unknown" and carries neither. The
 * age is measured against the shared `UiClock`, so rows pass no clock down.
 */
@Component({
  selector: 'app-relative-time',
  template: `<time [attr.datetime]="iso()" [attr.title]="title()">{{ text() }}</time>`,
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
  private readonly now = inject(UiClock).now

  private readonly time = computed(() => Date.parse(this.at() ?? ''))
  protected readonly iso = computed(() => (Number.isNaN(this.time()) ? null : new Date(this.time()).toISOString()))
  protected readonly title = computed(() => (this.iso() === null ? null : `${new Date(this.time()).toLocaleString()} (UTC ${this.iso()})`))
  protected readonly text = computed(() => formatAge(this.at(), this.now()))
}
