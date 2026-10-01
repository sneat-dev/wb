import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'
import { MetricsSample } from '@cockpit/fleet-data'
import { ChartView, TimeSeriesSpec, machineMetricSpecs, withGaps } from '@cockpit/ui/chart'

const HOUR = 3_600_000
const GIGABYTE = 2 ** 30

/**
 * Free disk on the projects root over the hour, in gigabytes, with a gap where a sample did not report it.
 * TODO(ui chart): the library's `machineMetricSpecs` draws disk USED percent; REQ:machine-detail asks for disk FREE,
 * so this one is local until the preset offers it.
 */
export function diskFreeSpec(samples: readonly MetricsSample[], now: number): TimeSeriesSpec {
  const from = now - HOUR
  const readings = samples.flatMap((sample) => {
    const at = Date.parse(sample.sampled_at)
    return sample.disk_total_bytes > 0 && Number.isFinite(sample.disk_free_bytes) && !Number.isNaN(at) ? [{ at, value: sample.disk_free_bytes / GIGABYTE }] : []
  })
  return { kind: 'time-series', title: 'Disk free', valueLabel: 'Disk free GB', unit: ' GB', from, to: now, points: withGaps(readings, from, now) }
}

/** The four charts: CPU percent, load, memory used (percent of the total) and free disk. */
export function machineChartSpecs(samples: readonly MetricsSample[], now: number): TimeSeriesSpec[] {
  const [cpu, load, memory] = machineMetricSpecs(samples, now)
  return [cpu, load, memory, diskFreeSpec(samples, now)]
}

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

  protected readonly specs = computed(() => machineChartSpecs(this.samples(), this.now()))
}
