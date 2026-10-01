import { ChangeDetectionStrategy, Component, ViewEncapsulation, computed, input } from '@angular/core'
import { FleetModel } from '@cockpit/fleet-data'
import { buildCleanup, buildHealth } from '@cockpit/fleet-data/home-details'
import { CleanupSection } from './cleanup-section'
import { HealthSection } from './health-section'
import { healthRows } from './health-rows'
import { LazyMount } from './lazy-mount'
import { throughputView } from './throughput-view'

/** The charts (and Chart.js with them) load only when their section scrolls near the viewport. */
const loadCharts = () => import('./home-charts').then((module) => module.HomeCharts)

/**
 * What Home shows below the fold: Cleanup, Fleet health (only when something is wrong) and the
 * throughput charts. This component and the library's `home-details` entry are a lazy chunk the
 * page defers until the browser is idle, so none of it is part of the first page; the charts
 * (and Chart.js with them) wait further, until their section scrolls near the viewport.
 */
@Component({
  selector: 'app-home-more',
  imports: [CleanupSection, HealthSection, LazyMount],
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

  protected readonly loadCharts = loadCharts
  protected readonly cleanup = computed(() => buildCleanup(this.model()))
  protected readonly health = computed(() => healthRows(buildHealth(this.model()), this.dropped()))
  protected readonly throughput = computed(() => throughputView(this.model()))
}
