import { FleetModel } from '@cockpit/fleet-data'
import { buildThroughput } from '@cockpit/fleet-data/home-details'

/** What the two Home charts draw: the one place that knows the shape of the throughput block. */
export interface ThroughputView {
  windowDays: number
  /** One entry per day of the window, oldest first. `dropped` is work that was sealed as removed, not landed. */
  perDay: { date: string; finished: number; dropped: number }[]
  /** The slowest finished tasks, slowest first (at most five). */
  slowest: { task: string; durationSeconds: number }[]
  medianSeconds: number | undefined
  p90Seconds: number | undefined
  /** The daemon capped the window, so older finished tasks are not counted. */
  capped: boolean
}

/**
 * The throughput block as the daemon now sends it: `per_day[{date, finished, dropped, landed?}]`,
 * `slowest` (the five longest finished tasks), `median_seconds`, `p90_seconds` and `capped`.
 * TODO(fleet-data): the library's `Throughput` type and `buildThroughput` still have the older block
 * (`per_day[{date, landed}]`, no median, no p90, no `capped`); when `/home-details` carries these
 * fields, this adapter becomes `buildThroughput(model)` and nothing else in Home changes.
 */
interface ThroughputBlock {
  per_day?: { date: string; finished?: number; dropped?: number; landed?: number }[]
  median_seconds?: number
  p90_seconds?: number
  capped?: boolean
}

/** The block as the charts take it; undefined when the document has none. */
export function throughputView(model: FleetModel): ThroughputView | undefined {
  const series = buildThroughput(model)
  if (series === undefined) return undefined
  const block = model.document.throughput as ThroughputBlock
  const days = new Map((block.per_day ?? []).map((day) => [day.date, day]))
  return {
    windowDays: series.windowDays,
    perDay: series.perDay.map((day) => ({ date: day.date, finished: days.get(day.date)?.finished ?? day.landed, dropped: days.get(day.date)?.dropped ?? 0 })),
    slowest: series.slowest.map((entry) => ({ task: entry.task, durationSeconds: entry.durationSeconds })),
    medianSeconds: block.median_seconds,
    p90Seconds: block.p90_seconds,
    capped: block.capped === true,
  }
}
