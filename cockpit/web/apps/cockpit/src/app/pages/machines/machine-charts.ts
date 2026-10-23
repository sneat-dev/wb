import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'
import { MetricsSample } from '@cockpit/fleet-data'
import { ChartView, machineMetricSpecs } from '@cockpit/ui/chart'

/**
 * The last-hour charts of a machine (REQ:machine-detail), each the library's chart with its title and its hidden data
 * table. This component is a lazy chunk, created by a viewport mount when it scrolls near the viewport, so Chart.js
 * is fetched only by a viewer who looks at the charts. The latest readings are in words in the panel above them.
 */
@Component({
  selector: 'app-machine-charts',
  imports: [ChartView],
  template: `<div class="grid">
    @for (spec of specs(); track spec.title) {
      <div class="card"><app-chart [spec]="spec" [height]="6.5" /></div>
    }
  </div>`,
  styleUrl: './machine-charts.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class MachineCharts {
  /** Oldest first, as the route returns them. */
  readonly samples = input.required<readonly MetricsSample[]>()
  /** The end of the hour shown, in epoch milliseconds. */
  readonly now = input.required<number>()

  protected readonly specs = computed(() => machineMetricSpecs(this.samples(), this.now()))
}
