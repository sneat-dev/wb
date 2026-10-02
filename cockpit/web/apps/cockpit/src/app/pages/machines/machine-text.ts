import { MachineLoad, MachineView, MetricsSample, formatAge } from '@cockpit/fleet-data'
import type { MetricsEntry } from '../../metrics/metrics-poller'
import { spanText } from '../home/home-time'

/** How a machine is reached, in words: "local", "live over ssh, just now", "cached, 5 min ago". */
export function reachWords(view: MachineView, now: number): string {
  const { machine } = view
  if (machine.route === 'local') return 'local'
  const age = formatAge(machine.observed_at, now)
  if (machine.route === 'cached') return `cached, ${age}`
  return `live${machine.transport ? ` over ${machine.transport}` : ''}, ${age}`
}

/** Whole percent from 0 to 100. */
export const percent = (value: number): number => Math.min(100, Math.max(0, Math.round(value)))

/** `12.4 GB`; a binary gigabyte, one decimal. */
export function gigabytes(bytes: number): string {
  return `${Math.round((bytes / 2 ** 30) * 10) / 10} GB`
}

/** The mini bars of the latest sample, whole percent; none without a usable sample (never a zero). */
export function barsOf(load: MachineLoad): { cpu: number; memory: number } | undefined {
  return load.cpuPercent === undefined || load.memoryPercent === undefined ? undefined : { cpu: percent(load.cpuPercent), memory: percent(load.memoryPercent) }
}

const ROUTE_WORDS = { local: 'local', 'live-remote': 'live', cached: 'cached', none: 'no samples' } as const

/** The words of the sample behind a machine's load: its route and age, or that there is none. */
export function sampleWords(load: MachineLoad, now: number): string {
  if (load.stale) return `${ROUTE_WORDS[load.route]}${load.sampledAt === undefined ? ', no time' : `, ${formatAge(new Date(load.sampledAt).toISOString(), now)}`}: too old to say`
  if (load.state === 'not-reported') return 'no usable sample'
  return `${ROUTE_WORDS[load.route]}${load.sampledAt === undefined ? '' : `, ${formatAge(new Date(load.sampledAt).toISOString(), now)}`}`
}

/** The latest sample as the lines the panel prints; a reading the machine did not report says so. */
export interface Latest {
  cpu: string
  load: string
  memory: string
  disk: string
}

const finite = (value: number | undefined): value is number => typeof value === 'number' && Number.isFinite(value)

export function latestWords(sample: MetricsSample): Latest {
  const { memory_used_bytes: used, memory_total_bytes: total, disk_free_bytes: free, disk_total_bytes: diskTotal, cpu_percent: cpu, load1 } = sample
  const memory = finite(total) && total > 0 && finite(used) ? `${gigabytes(used)} of ${gigabytes(total)} (${percent((used / total) * 100)}%)` : 'not reported'
  const disk = finite(diskTotal) && diskTotal > 0 && finite(free) ? `${gigabytes(free)} free of ${gigabytes(diskTotal)}` : 'not reported'
  return {
    cpu: finite(cpu) ? `${percent(cpu)}%` : 'not reported',
    load: finite(load1) ? String(Math.round(load1 * 100) / 100) : 'not reported',
    memory,
    disk,
  }
}

/** What the Metrics block of a machine says: where the numbers come from, the latest sample, and whether a history chart is drawn. */
export interface MetricsView {
  kind: 'pending' | 'local' | 'live-remote' | 'cached' | 'none' | 'failed'
  /** The source stated plainly. */
  source: string
  /** Why there are no metrics, when the daemon gave a reason. */
  reason?: string
  /** The last read failed but an earlier answer is kept. */
  stale: boolean
  latest?: Latest
  samples: MetricsSample[]
  /** History charts: only for a route that carries a history. */
  charts: boolean
  /** When the last read finished, which ends the charts' hour. */
  readAt: number
}

export function metricsView(entry: MetricsEntry | undefined, now: number): MetricsView {
  if (entry === undefined) return { kind: 'pending', source: 'Reading the metrics of this machine…', stale: false, samples: [], charts: false, readAt: now }
  const metrics = entry.metrics
  if (metrics === undefined) return { kind: 'failed', source: `The metrics of this machine could not be read: ${entry.error ?? 'unknown error'}.`, stale: false, samples: [], charts: false, readAt: entry.readAt }
  const base = { stale: entry.error !== undefined, samples: metrics.samples, readAt: entry.readAt }
  const sample = metrics.samples[metrics.samples.length - 1]
  const latest = sample === undefined ? undefined : latestWords(sample)
  const count = `${metrics.samples.length} ${metrics.samples.length === 1 ? 'sample' : 'samples'}`
  if (metrics.route === 'none' || sample === undefined) {
    return { ...base, kind: 'none', source: 'Metrics are not reported for this machine.', reason: metrics.reason, charts: false }
  }
  const taken = formatAge(sample.sampled_at, now)
  if (metrics.route === 'cached') return { ...base, kind: 'cached', source: `cached: the latest sample of its published snapshot, ${taken}`, latest, charts: false }
  if (metrics.route === 'live-remote') return { ...base, kind: 'live-remote', source: `live: ${count}, fetched ${formatAge(metrics.fetched_at, now)}`, latest, charts: true }
  return { ...base, kind: 'local', source: `local: this machine's own history, ${count}, latest ${taken}`, latest, charts: true }
}

/** Uptime from the time since boot, or none. */
export function uptimeWords(view: MachineView): string | undefined {
  return view.uptimeMs === undefined ? undefined : spanText(view.uptimeMs)
}
