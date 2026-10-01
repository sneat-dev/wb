import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'
import { Entry, routeLabel } from '@cockpit/fleet-data'

/**
 * The route of an entry: "local", or "cached" with how old the other machine's
 * snapshot is. The exact time is the tooltip.
 */
@Component({
  selector: 'app-route-label',
  templateUrl: './route-label.html',
  styleUrl: './route-label.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class RouteLabel {
  readonly entry = input.required<Entry>()
  /** The clock the age is measured against, in epoch milliseconds. */
  readonly now = input.required<number>()

  protected readonly text = computed(() => routeLabel(this.entry(), this.now()))
}
