import { DOCUMENT } from '@angular/common'
import { ChangeDetectionStrategy, Component, DestroyRef, ViewEncapsulation, computed, inject, input, signal } from '@angular/core'
import { ThroughputSeries } from '@cockpit/fleet-data'
import { ChartView, HorizontalBarsSpec, StackedBarsSpec } from '@cockpit/ui/chart'
import { PHONE_QUERY } from './home-phone'
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

/** The daemon's scan of sealed tasks stopped at its cap: said in a word on the caption's line, and in full to a screen reader and as a tooltip, so no line is added to a card of fixed height. */
export const CAPPED_NOTE = 'The daemon capped its scan: older sealed tasks may be missing.'

const HOUR = 3600

/** The height of a plot in rem: Throughput is the first section of Home, so on a phone it is shorter and "Needs you" stays near the top. */
export const PLOT_HEIGHT = 9
/** On a phone the "Time to finish" plot is as tall as its five rows of 11 px labels need (with the value axis), and "Finished per day" gets a little less than on a wide screen, with its axis fitted to its data (`fitAxis`). */
export const COMPACT_SLOWEST_HEIGHT = 5.75
export const COMPACT_PER_DAY_HEIGHT = 5.5

/** What the stacked chart's series are called, in the order they stack and in the legend. */
export function seriesNames(series: Pick<ThroughputSeries, 'hasLanded'>): string[] {
  return series.hasLanded ? ['landed', 'finished', 'dropped'] : ['finished', 'dropped']
}

/**
 * The two charts of REQ:home-charts from the throughput series. They link to nothing. Landed work is
 * a subset of finished work, so with a `landed` count the finished bar is split in two ("landed" and
 * the rest, "finished") and nothing is counted twice.
 */
export function throughputSpecs(series: ThroughputSeries, compact = false): { slowest: HorizontalBarsSpec; perDay: StackedBarsSpec } {
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
      fitAxis: compact,
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
 * Throughput section creates this component from a lazy chunk right after the first paint, so
 * Chart.js never costs the first page; the cards are as tall as the section's slot says
 * (`--chart-card-1` and `--chart-card-2`, home.css), whatever they hold.
 */
@Component({
  selector: 'app-home-charts',
  imports: [ChartView],
  template: `<div class="charts-grid">
    <div class="home-card chart-card" [attr.title]="notALink">
      <app-chart [spec]="specs().perDay" [height]="perDayHeight()" />
      <p class="chart-legend">
        @for (entry of specs().perDay.series; track entry.name) {
          <span [class]="'swatch ' + entry.tone" aria-hidden="true"></span>{{ entry.name }}
        }
      </p>
    </div>
    <div class="home-card chart-card" [attr.title]="notALink">
      <app-chart [spec]="specs().slowest" [height]="slowestHeight()" />
      @if (caption(); as text) {
        <p class="chart-legend" [attr.title]="series().capped ? cappedNote : null">
          {{ text }}
          @if (series().capped) {
            <span class="visually-hidden">{{ cappedNote }}</span>
          }
        </p>
      }
    </div>
  </div>`,
  styleUrl: './home-charts.css',
  // The card and legend classes are written here and styled here; nothing else in Home uses them.
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class HomeCharts {
  readonly series = input.required<ThroughputSeries>()

  protected readonly notALink = NOT_A_LINK
  protected readonly cappedNote = CAPPED_NOTE

  private readonly compact = signal(false)
  protected readonly perDayHeight = computed(() => (this.compact() ? COMPACT_PER_DAY_HEIGHT : PLOT_HEIGHT))
  protected readonly slowestHeight = computed(() => (this.compact() ? COMPACT_SLOWEST_HEIGHT : PLOT_HEIGHT))
  protected readonly specs = computed(() => throughputSpecs(this.series(), this.compact()))
  protected readonly caption = computed(() => [durationCaption(this.series()), this.series().capped ? 'scan capped' : undefined].filter((part) => part !== undefined).join(' \u00b7 ') || undefined)

  constructor() {
    const query = inject(DOCUMENT).defaultView?.matchMedia?.(PHONE_QUERY)
    if (query !== undefined) {
      this.compact.set(query.matches)
      const changed = (event: MediaQueryListEvent): void => this.compact.set(event.matches)
      query.addEventListener('change', changed)
      inject(DestroyRef).onDestroy(() => query.removeEventListener('change', changed))
    }
  }
}
