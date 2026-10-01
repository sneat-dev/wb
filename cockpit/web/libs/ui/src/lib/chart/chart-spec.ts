import type { AppLink, MetricsSample } from '@cockpit/fleet-data'

// What a chart shows, as plain data with no chart library in it: the three
// presets the pages need (a time series, bars by day, horizontal bars) and the
// text alternative every chart carries. The chart component turns a spec into a
// canvas; the same spec gives the visually hidden data table.

interface Base {
  /** What the chart shows; the caption of the chart and of its data table. */
  title: string
  /** The name of the value, the second column of the data table, for example "CPU %". */
  valueLabel: string
}

/** A value over time, with a gap (`null`) where no sample exists. */
export interface TimeSeriesSpec extends Base {
  kind: 'time-series'
  /** Appended to a value: "%" or "". */
  unit: string
  /** The fixed top of the value axis, for a percentage. */
  max?: number
  /** The time window shown, in epoch milliseconds, so that missing samples at either end stay visible. */
  from: number
  to: number
  points: { at: number; value: number | null }[]
}

/** One bar per day. */
export interface BarsSpec extends Base {
  kind: 'bars'
  bars: { label: string; value: number }[]
}

/** One horizontal bar per bucket; a bar with a `link` is clickable and emits it. */
export interface HorizontalBarsSpec extends Base {
  kind: 'horizontal-bars'
  bars: { label: string; value: number; link?: AppLink }[]
}

/**
 * One stacked bar per day, with one value per series (bottom first). `tone` picks the series' colour
 * from the chart tokens: `primary` is the accent, `muted` the neutral one. The legend is the caller's
 * (text beside the chart); the data table names every series in each row.
 */
export interface StackedBarsSpec extends Base {
  kind: 'stacked-bars'
  series: { name: string; tone: 'primary' | 'muted' }[]
  bars: { label: string; values: number[] }[]
}

export type ChartSpec = TimeSeriesSpec | BarsSpec | HorizontalBarsSpec | StackedBarsSpec

/** The data table of a chart: the text alternative, always present. */
export interface ChartTable {
  caption: string
  columns: [string, string]
  rows: { label: string; value: string; link?: AppLink }[]
}

const HOUR = 3_600_000

/** A clock time, `14:05`, in the viewer's time zone. */
export function clockTime(at: number): string {
  const date = new Date(at)
  return `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`
}

/** A value with its unit and at most one decimal. */
export function formatValue(value: number, unit: string): string {
  return `${Math.round(value * 10) / 10}${unit}`
}

/** A spec without what a chart cannot draw: a value that is not a finite number is a gap in a series and no bar in a bar chart, and a point with no time is dropped. */
export function cleanSpec<T extends ChartSpec>(spec: T): T {
  const finite = (value: unknown): value is number => typeof value === 'number' && Number.isFinite(value)
  if (spec.kind === 'time-series') {
    return { ...spec, points: spec.points.filter((point) => finite(point.at)).map((point) => ({ at: point.at, value: finite(point.value) ? point.value : null })) }
  }
  if (spec.kind === 'stacked-bars') return { ...spec, bars: spec.bars.filter((bar) => bar.values.every(finite)) } as T
  return { ...spec, bars: spec.bars.filter((bar) => finite(bar.value)) } as T
}

/** Whether there is anything to draw. */
export function hasData(spec: ChartSpec): boolean {
  if (spec.kind === 'stacked-bars') return spec.bars.some((bar) => bar.values.some((value) => value > 0))
  return spec.kind === 'time-series' ? spec.points.some((point) => point.value !== null) : spec.bars.length > 0
}

/** More points than this are summarised in the data table. */
export const TABLE_FULL_LIMIT = 60
/** In a summarised table, every this-many-th point is listed. */
export const TABLE_STEP = 10

/** The data table of a spec. A long series is its minimum, average, maximum and latest value, then every tenth point. */
export function chartTable(spec: ChartSpec): ChartTable {
  const heading = spec.kind === 'time-series' ? 'Time' : spec.kind === 'bars' || spec.kind === 'stacked-bars' ? 'Day' : 'Bucket'
  const rows =
    spec.kind === 'time-series'
      ? seriesRows(spec)
      : spec.kind === 'bars'
        ? spec.bars.map((bar) => ({ label: bar.label, value: String(bar.value) }))
        : spec.kind === 'stacked-bars'
          ? spec.bars.map((bar) => ({ label: bar.label, value: spec.series.map((series, index) => `${bar.values[index] ?? 0} ${series.name}`).join(', ') }))
          : spec.bars.map((bar) => ({ label: bar.label, value: String(bar.value), link: bar.link }))
  return { caption: spec.title, columns: [heading, spec.valueLabel], rows }
}

function seriesRows(spec: TimeSeriesSpec): ChartTable['rows'] {
  const row = (point: TimeSeriesSpec['points'][number]) => ({ label: clockTime(point.at), value: point.value === null ? 'no sample' : formatValue(point.value, spec.unit) })
  if (spec.points.length <= TABLE_FULL_LIMIT) return spec.points.map(row)
  const values = spec.points.flatMap((point) => (point.value === null ? [] : [point.value]))
  const summary = (label: string, value: number | undefined) => ({ label, value: value === undefined ? 'no sample' : formatValue(value, spec.unit) })
  const average = values.length === 0 ? undefined : values.reduce((sum, value) => sum + value, 0) / values.length
  return [
    summary('Minimum', values.length === 0 ? undefined : Math.min(...values)),
    summary('Average', average),
    summary('Maximum', values.length === 0 ? undefined : Math.max(...values)),
    summary('Latest', values[values.length - 1]),
    ...spec.points.filter((_, index) => index % TABLE_STEP === 0).map(row),
  ]
}

/** One sentence that stands for the chart, for its accessible name. */
export function chartSummary(spec: ChartSpec): string {
  if (spec.kind === 'time-series') {
    const values = spec.points.flatMap((point) => (point.value === null ? [] : [point.value]))
    if (values.length === 0) return `${spec.title}: no samples in the last hour`
    const latest = values[values.length - 1]
    return `${spec.title}: latest ${formatValue(latest, spec.unit)}, from ${formatValue(Math.min(...values), spec.unit)} to ${formatValue(Math.max(...values), spec.unit)} over ${values.length} samples`
  }
  if (spec.kind === 'stacked-bars') {
    const totals = spec.series.map((series, index) => `${spec.bars.reduce((sum, bar) => sum + (bar.values[index] ?? 0), 0)} ${series.name}`)
    return `${spec.title}: ${spec.bars.length} days, ${totals.join(', ')}`
  }
  if (spec.bars.length === 0) return `${spec.title}: no data`
  const total = spec.bars.reduce((sum, bar) => sum + bar.value, 0)
  return `${spec.title}: ${spec.bars.length} ${spec.kind === 'bars' ? 'days' : 'buckets'}, ${total} in all`
}

/** The median of the gaps between consecutive times, or `fallback` for fewer than two. */
function typicalInterval(times: number[], fallback: number): number {
  const gaps = times.slice(1).map((time, index) => time - times[index]).sort((a, b) => a - b)
  return gaps.length === 0 ? fallback : gaps[Math.floor(gaps.length / 2)]
}

/**
 * Points of one series in `[from, to]`, oldest first, with a `null` point after
 * any stretch with no sample (more than two and a half typical intervals), so
 * the line breaks there instead of joining two readings across an absence.
 */
export function withGaps(points: { at: number; value: number }[], from: number, to: number): TimeSeriesSpec['points'] {
  const inWindow = points.filter((point) => point.at >= from && point.at <= to).sort((a, b) => a.at - b.at)
  const interval = typicalInterval(inWindow.map((point) => point.at), 60_000)
  const result: TimeSeriesSpec['points'] = []
  inWindow.forEach((point, index) => {
    const previous = inWindow[index - 1]
    if (previous !== undefined && point.at - previous.at > interval * 2.5) result.push({ at: previous.at + interval, value: null })
    result.push(point)
  })
  return result
}

function percent(part: number, whole: number): number | undefined {
  return whole > 0 ? (part / whole) * 100 : undefined
}

/**
 * The four machine charts of REQ:machine-detail-metrics-charts over the last
 * hour ending at `now`: CPU %, load, memory used % and disk used %. A reading
 * the machine did not report (a total of zero) is an absent sample.
 */
export function machineMetricSpecs(samples: readonly MetricsSample[], now: number): TimeSeriesSpec[] {
  const from = now - HOUR
  const series = (title: string, valueLabel: string, unit: string, max: number | undefined, pick: (sample: MetricsSample) => number | undefined): TimeSeriesSpec => {
    const readings = samples.flatMap((sample) => {
      const value = pick(sample)
      return value === undefined ? [] : [{ at: Date.parse(sample.sampled_at), value }]
    })
    return { kind: 'time-series', title, valueLabel, unit, max, from, to: now, points: withGaps(readings.filter((reading) => !Number.isNaN(reading.at)), from, now) }
  }
  return [
    series('CPU', 'CPU %', '%', 100, (sample) => sample.cpu_percent),
    series('Load (1 minute)', 'Load', '', undefined, (sample) => sample.load1),
    series('Memory used', 'Memory used %', '%', 100, (sample) => percent(sample.memory_used_bytes, sample.memory_total_bytes)),
    series('Disk used', 'Disk used %', '%', 100, (sample) => percent(sample.disk_total_bytes - sample.disk_free_bytes, sample.disk_total_bytes)),
  ]
}
