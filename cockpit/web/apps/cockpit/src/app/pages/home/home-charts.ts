import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core'
import { BarsSpec, ChartView, HorizontalBarsSpec, StackedBarsSpec } from '@cockpit/ui/chart'
import { spanText } from './home-time'
import { ThroughputView } from './throughput-view'

/** "median 15 min · p90 15 h", with whichever of the two the daemon reported; undefined when neither. */
export function durationCaption(view: Pick<ThroughputView, 'medianSeconds' | 'p90Seconds'>): string | undefined {
  const parts = [
    view.medianSeconds === undefined ? undefined : `median ${spanText(view.medianSeconds * 1000)}`,
    view.p90Seconds === undefined ? undefined : `p90 ${spanText(view.p90Seconds * 1000)}`,
  ].filter((part): part is string => part !== undefined)
  return parts.length === 0 ? undefined : parts.join(' · ')
}

const HOUR = 3600

/** The two charts of REQ:home-charts from the throughput view. They link to nothing. */
export function throughputSpecs(view: ThroughputView): { slowest: HorizontalBarsSpec; perDay: StackedBarsSpec } {
  return {
    slowest: {
      kind: 'horizontal-bars',
      title: 'Time to finish',
      valueLabel: 'Hours from claim to finished',
      bars: view.slowest.map((entry) => ({ label: entry.task, value: Math.round((entry.durationSeconds / HOUR) * 10) / 10 })),
    },
    perDay: {
      kind: 'stacked-bars',
      title: `Finished per day, last ${view.windowDays} days`,
      valueLabel: 'Tasks per day',
      series: [
        { name: 'finished', tone: 'primary' },
        { name: 'dropped', tone: 'muted' },
      ],
      bars: view.perDay.map((day) => ({ label: day.date.slice(5), values: [day.finished, day.dropped] })),
    },
  }
}

/**
 * "Finished per day" (stacked: finished and dropped, with a text legend) and "Time to finish" (the
 * slowest five, with the median and p90 as its caption), drawn with Chart.js from the throughput
 * block, each with its hidden data table. The numbers come from sealed records that have no entries
 * in the fleet document, so no bar or number here is a link. The host creates this component only
 * when its section scrolls near the viewport, so the Chart.js chunk never costs the first page.
 */
@Component({
  selector: 'app-home-charts',
  imports: [ChartView],
  template: `<div class="charts-grid">
    <div class="home-card chart-card">
      <app-chart [spec]="specs().perDay" [height]="9" />
      <p class="chart-legend"><span class="swatch finished" aria-hidden="true"></span>finished <span class="swatch dropped" aria-hidden="true"></span>dropped</p>
    </div>
    <div class="home-card chart-card">
      <app-chart [spec]="specs().slowest" [height]="9" />
      @if (caption(); as text) {
        <p class="chart-legend">{{ text }}</p>
      }
      @if (view().capped) {
        <p class="chart-legend">The daemon capped the window: older finished tasks are not counted.</p>
      }
    </div>
  </div>`,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class HomeCharts {
  readonly view = input.required<ThroughputView>()

  protected readonly specs = computed(() => throughputSpecs(this.view()))
  protected readonly caption = computed(() => durationCaption(this.view()))
}
