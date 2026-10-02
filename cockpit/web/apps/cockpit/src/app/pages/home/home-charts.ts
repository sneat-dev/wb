import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'
import { ThroughputSeries } from '@cockpit/fleet-data'
import { ChartView, HorizontalBarsSpec, StackedBarsSpec } from '@cockpit/ui/chart'
import { spanText } from './home-time'

/** Why no number of the charts is a link (REQ:every-number-is-a-link), as the title of each. */
export const NOT_A_LINK = 'Not a link: these numbers come from sealed records that have no entries in the fleet document, so no list can reproduce them'

/** "median 15 min · p90 15 h", with whichever of the two the daemon reported; undefined when neither. */
export function durationCaption(series: Pick<ThroughputSeries, 'medianSeconds' | 'p90Seconds'>): string | undefined {
  const parts = [
    series.medianSeconds === undefined ? undefined : `median ${spanText(series.medianSeconds * 1000)}`,
    series.p90Seconds === undefined ? undefined : `p90 ${spanText(series.p90Seconds * 1000)}`,
  ].filter((part): part is string => part !== undefined)
  return parts.length === 0 ? undefined : parts.join(' \u00b7 ')
}

const HOUR = 3600

/** What the stacked chart's series are called, in the order they stack and in the legend. */
export function seriesNames(series: Pick<ThroughputSeries, 'hasLanded'>): string[] {
  return series.hasLanded ? ['landed', 'finished', 'dropped'] : ['finished', 'dropped']
}

/**
 * The two charts of REQ:home-charts from the throughput series. They link to nothing. Landed work is
 * a subset of finished work, so with a `landed` count the finished bar is split in two ("landed" and
 * the rest, "finished") and nothing is counted twice.
 */
export function throughputSpecs(series: ThroughputSeries): { slowest: HorizontalBarsSpec; perDay: StackedBarsSpec } {
  const names = seriesNames(series)
  return {
    slowest: {
      kind: 'horizontal-bars',
      title: 'Time to finish',
      valueLabel: 'Hours from claim to finished',
      bars: series.slowest.map((entry) => ({ label: entry.task, value: Math.round((entry.durationSeconds / HOUR) * 10) / 10 })),
    },
    perDay: {
      kind: 'stacked-bars',
      title: `Finished per day, last ${series.windowDays} days`,
      valueLabel: 'Tasks per day',
      series: names.map((name) => ({ name, tone: name === 'landed' ? 'primary' : name === 'finished' ? (series.hasLanded ? 'soft' : 'primary') : 'muted' })),
      bars: series.perDay.map((day) => ({
        label: day.date.slice(5),
        values: series.hasLanded ? [day.landed, day.finished - day.landed, day.dropped] : [day.finished, day.dropped],
      })),
    },
  }
}

/**
 * "Finished per day" (stacked: finished and dropped, plus landed when the daemon counted any, with a
 * text legend) and "Time to finish" (the slowest five, with the median and p90 as its caption), drawn
 * with Chart.js from the throughput block, each with its hidden data table. The numbers come from
 * sealed records that have no entries in the fleet document, so no bar or number here is a link. The
 * host creates this component only when its section scrolls near the viewport, so the Chart.js chunk
 * never costs the first page.
 */
@Component({
  selector: 'app-home-charts',
  imports: [ChartView],
  template: `<div class="charts-grid">
    <div class="home-card chart-card" [attr.title]="notALink">
      <app-chart [spec]="specs().perDay" [height]="9" />
      <p class="chart-legend">
        @for (entry of specs().perDay.series; track entry.name) {
          <span [class]="'swatch ' + entry.tone" aria-hidden="true"></span>{{ entry.name }}
        }
      </p>
    </div>
    <div class="home-card chart-card" [attr.title]="notALink">
      <app-chart [spec]="specs().slowest" [height]="9" />
      @if (caption(); as text) {
        <p class="chart-legend">{{ text }}</p>
      }
      @if (series().capped) {
        <p class="chart-legend">The daemon capped its scan: older sealed tasks may be missing.</p>
      }
    </div>
  </div>`,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class HomeCharts {
  readonly series = input.required<ThroughputSeries>()

  protected readonly notALink = NOT_A_LINK

  protected readonly specs = computed(() => throughputSpecs(this.series()))
  protected readonly caption = computed(() => durationCaption(this.series()))
}
