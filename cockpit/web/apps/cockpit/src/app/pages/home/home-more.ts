import { ChangeDetectionStrategy, Component, ViewEncapsulation, computed, input } from '@angular/core'
import { FleetModel } from '@cockpit/fleet-data'
import { buildCleanup, buildHealth } from '@cockpit/fleet-data/home-details'
import { CleanupSection } from './cleanup-section'
import { HealthSection } from './health-section'
import { healthRows } from './health-rows'
import { ThroughputSection, hasThroughput } from './throughput-section'

/**
 * What Home shows below the fold: Cleanup, Fleet health (only when something is wrong) and, when the
 * document has no throughput block, the calm line "No charts" (with a block, Throughput is the first
 * section of Home, REQ:home-charts). This component and the library's `home-details` entry are a lazy
 * chunk the page defers until the browser is idle, so none of it is part of the first page.
 */
@Component({
  selector: 'app-home-more',
  imports: [CleanupSection, HealthSection, ThroughputSection],
  templateUrl: './home-more.html',
  styleUrl: './home-more.css',
  // Shares the row styles of the page that hosts it.
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class HomeMore {
  readonly model = input.required<FleetModel>()
  /** How many entries of the last document were left out as invalid. */
  readonly dropped = input(0)

  protected readonly cleanup = computed(() => buildCleanup(this.model()))
  protected readonly health = computed(() => healthRows(buildHealth(this.model()), this.dropped()))
  /** With a block the section is on top of Home, and this chunk shows the "No charts" line only when there is none. */
  protected readonly hasThroughput = computed(() => hasThroughput(this.model()))
}
