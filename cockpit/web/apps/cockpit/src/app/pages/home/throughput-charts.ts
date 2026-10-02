import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'
import type { FleetModel } from '@cockpit/fleet-data'
import { buildThroughput } from '@cockpit/fleet-data/home-details'
import { HomeCharts } from './home-charts'

/**
 * What the Throughput section loads: the throughput series of the model (the `home-details` entry) and
 * the two charts. One lazy chunk, so neither the series code nor Chart.js is part of Home's first page.
 */
@Component({
  selector: 'app-throughput-charts',
  imports: [HomeCharts],
  template: `@if (series(); as throughput) {
    <app-home-charts [series]="throughput" />
  }`,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class ThroughputCharts {
  readonly model = input.required<FleetModel>()

  protected readonly series = computed(() => buildThroughput(this.model()))
}
