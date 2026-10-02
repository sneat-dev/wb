import { MachineLoad, MachineView, machineLoad } from '@cockpit/fleet-data'
import { MetricsEntry } from '../../metrics/metrics-poller'
import { NOW } from '../test-harness'
import { ago, sample } from './machines-fixture'
import { barsOf, gigabytes, latestWords, metricsView, percent, reachWords, sampleWords, uptimeWords } from './machine-text'

const view = (machine: Partial<MachineView['machine']>, extra: Partial<MachineView> = {}): MachineView => ({
  machine: { id: 'm', machine: 'm', machine_id: 'm', route: 'cached', repository_count: 0, worktree_count: 0, ...machine },
  local: machine.route === 'local',
  state: 'cached',
  outdated: false,
  runningAgents: 0,
  ...extra,
})

describe('reachWords', () => {
  it('says local, live with its transport and age, and cached with its age', () => {
    expect(reachWords(view({ route: 'local' }), NOW)).toBe('local')
    expect(reachWords(view({ route: 'live-remote', transport: 'ssh', observed_at: ago(5 * 60_000) }), NOW)).toBe('live over ssh, 5 min ago')
    expect(reachWords(view({ route: 'live-remote', observed_at: ago(10_000) }), NOW)).toBe('live, just now')
    expect(reachWords(view({ route: 'cached', observed_at: ago(3 * 3_600_000) }), NOW)).toBe('cached, 3 h ago')
    expect(reachWords(view({ route: 'cached' }), NOW)).toBe('cached, age unknown')
  })
})

describe('numbers', () => {
  it('rounds a percentage into 0 to 100', () => {
    expect([percent(-3), percent(49.6), percent(250)]).toEqual([0, 50, 100])
  })

  it('writes bytes as gigabytes with at most one decimal', () => {
    expect([gigabytes(2 ** 30), gigabytes(1.25 * 2 ** 30), gigabytes(0)]).toEqual(['1 GB', '1.3 GB', '0 GB'])
  })

  it('has bars only for a usable sample', () => {
    expect(barsOf(machineLoad({ machine: 'm', route: 'local', samples: [sample(0, 42.4, 4)] }))).toEqual({ cpu: 42, memory: 25 })
    expect(barsOf({ state: 'not-reported', route: 'none' })).toBeUndefined()
    expect(barsOf({ state: 'free', route: 'local', cpuPercent: 3 })).toBeUndefined()
  })

  it('says the route and age of the sample behind a load, or that there is none', () => {
    const load = (extra: Partial<MachineLoad>): MachineLoad => ({ state: 'free', route: 'local', ...extra })
    expect(sampleWords(load({ route: 'live-remote', sampledAt: NOW - 120_000 }), NOW)).toBe('live, 2 min ago')
    expect(sampleWords(load({}), NOW)).toBe('local')
    expect(sampleWords({ state: 'not-reported', route: 'none' }, NOW)).toBe('no usable sample')
    expect(sampleWords(load({ route: 'cached', sampledAt: NOW }), NOW)).toBe('cached, just now')
    expect(sampleWords(load({ route: 'none' }), NOW)).toBe('no samples')
    // A sample too old to say how loaded the machine is now: its age is said, and that it is too old; with no time, that.
    expect(sampleWords({ state: 'not-reported', route: 'cached', stale: true, sampledAt: NOW - 2 * 3_600_000 }, NOW)).toBe('cached, 2 h ago: too old to say')
    expect(sampleWords({ state: 'not-reported', route: 'live-remote', stale: true }, NOW)).toBe('live, no time: too old to say')
  })

  it('writes the uptime from the time since boot, or nothing', () => {
    expect(uptimeWords(view({}, { uptimeMs: 3 * 24 * 3_600_000 }))).toBe('3 d')
    expect(uptimeWords(view({}))).toBeUndefined()
  })
})

describe('latestWords', () => {
  it('writes each reading, and says "not reported" for one the machine did not report', () => {
    expect(latestWords(sample(0, 12.4, 4))).toEqual({ cpu: '12%', load: '0.62', memory: '4 GB of 16 GB (25%)', disk: '200 GB free of 500 GB' })
    expect(latestWords(sample(0, 1, 4, { memory_total_bytes: 0, disk_total_bytes: 0, cpu_percent: Number.NaN, load1: Number.NaN }))).toEqual({ cpu: 'not reported', load: 'not reported', memory: 'not reported', disk: 'not reported' })
    expect(latestWords(sample(0, 1, 4, { memory_used_bytes: Number.NaN, disk_free_bytes: Number.NaN }))).toMatchObject({ memory: 'not reported', disk: 'not reported' })
  })

  // The daemon omits what it could not read: the first sample after a start has no cpu_percent, never a zero.
  it('says "not reported" for a measurement the sample leaves out, and never writes a zero for it', () => {
    const bare = { sampled_at: '2026-10-01T10:00:00Z' }
    expect(latestWords(bare)).toEqual({ cpu: 'not reported', load: 'not reported', memory: 'not reported', disk: 'not reported' })
    const { cpu_percent: _cpu, ...noCpu } = sample(0, 12.4, 4)
    expect(latestWords(noCpu)).toMatchObject({ cpu: 'not reported', load: '0.62', memory: '4 GB of 16 GB (25%)' })
    expect(latestWords({ ...sample(0, 12.4, 4), memory_total_bytes: undefined, disk_total_bytes: undefined })).toMatchObject({ cpu: '12%', memory: 'not reported', disk: 'not reported' })
  })
})

describe('metricsView', () => {
  const entry = (extra: Partial<MetricsEntry>): MetricsEntry => ({ readAt: NOW, ...extra })

  it('is pending before the first read, and failed when the read failed with nothing before it', () => {
    expect(metricsView(undefined, NOW)).toMatchObject({ kind: 'pending', charts: false, readAt: NOW, source: 'Reading the metrics of this machine…' })
    expect(metricsView(entry({ error: 'boom' }), NOW)).toMatchObject({ kind: 'failed', source: 'The metrics of this machine could not be read: boom.', charts: false })
    expect(metricsView(entry({}), NOW).source).toContain('unknown error')
  })

  it('says the route in words, and draws history only for a route that has one', () => {
    const local = metricsView(entry({ metrics: { machine: 'm', route: 'local', samples: [sample(2, 5), sample(1, 6)] } }), NOW)
    expect(local).toMatchObject({ kind: 'local', charts: true, stale: false, source: "local: this machine's own history, 2 samples, latest 1 min ago" })
    const live = metricsView(entry({ metrics: { machine: 'm', route: 'live-remote', fetched_at: ago(30_000), samples: [sample(1, 6)] } }), NOW)
    expect(live).toMatchObject({ kind: 'live-remote', charts: true, source: 'live: 1 sample, fetched just now' })
    const cached = metricsView(entry({ metrics: { machine: 'm', route: 'cached', samples: [sample(30, 6)] } }), NOW)
    expect(cached).toMatchObject({ kind: 'cached', charts: false, source: 'cached: the latest sample of its published snapshot, 30 min ago' })
    expect(cached.latest?.cpu).toBe('6%')
  })

  it('says none, with the reason, for a route with no source or no sample, and marks an answer kept after a failed read', () => {
    expect(metricsView(entry({ metrics: { machine: 'm', route: 'none', samples: [], reason: 'unsupported' } }), NOW)).toMatchObject({ kind: 'none', charts: false, reason: 'unsupported', source: 'Metrics are not reported for this machine.' })
    expect(metricsView(entry({ metrics: { machine: 'm', route: 'none', samples: [] } }), NOW).latest).toBeUndefined()
    expect(metricsView(entry({ metrics: { machine: 'm', route: 'local', samples: [] } }), NOW).kind).toBe('none')
    expect(metricsView(entry({ error: 'late', metrics: { machine: 'm', route: 'local', samples: [sample(1, 1)] } }), NOW).stale).toBe(true)
  })
})
