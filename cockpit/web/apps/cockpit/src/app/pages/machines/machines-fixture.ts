import { Type } from '@angular/core'
import { expect, vi } from 'vitest'
import { TestBed } from '@angular/core/testing'
import { FleetDocument, MachineMetrics, MetricsSample, Session } from '@cockpit/fleet-data'
import { agent, fleetDocument, machine, pullRequest, repository, run, worktree } from '@cockpit/fleet-data/testing'
import { CHART_ENGINE } from '@cockpit/ui/chart'
import { MetricsPoller } from '../../metrics/metrics-poller'
import { NOW, SESSION, openPage } from '../test-harness'

const MINUTE = 60_000
const HOUR = 60 * MINUTE
export const ago = (milliseconds: number): string => new Date(NOW - milliseconds).toISOString()

/**
 * Four machines:
 * - `macbook`: this machine, WB 1.2.0, with its hardware facts and a boot three days ago;
 * - `vm`: read live over http (10 s ago), WB 1.2.0;
 * - `oldmac`: a snapshot 30 hours old (stale), WB 1.0.0 (older than the fleet's newest), its daemon not running;
 * - `nas`: a snapshot 50 hours old (stale) that reports no WB version, still warming up.
 * and a few repositories, worktrees and agents on them.
 */
export function machinesDocument(): FleetDocument {
  const macbook = { ...machine('macbook'), wb_version: '1.2.0', os: 'darwin', arch: 'arm64', cpu_count: 10, boot_time: ago(3 * 24 * HOUR), observed_at: ago(0) }
  const vm = { ...machine('vm', 'cached'), route: 'live-remote' as const, transport: 'http' as const, wb_version: '1.2.0', observed_at: ago(10_000), os: 'linux', arch: 'amd64', cpu_count: 4, export_dropped: 3 }
  const oldmac = { ...machine('oldmac', 'cached'), wb_version: '1.0.0', observed_at: ago(30 * HOUR), transport: 'ssh' as const, remote_error: 'daemon_not_running' }
  const nas = { ...machine('nas', 'cached'), observed_at: ago(50 * HOUR), remote_error: 'remote_warming_up' }
  const onMac = { machine: 'macbook', machine_id: 'mach-macbook' }
  const onVm = { machine: 'vm', machine_id: 'mach-vm', route: 'live-remote' as const, observed_at: ago(10_000) }
  return fleetDocument({
    machines: [macbook, vm, oldmac, nas],
    repositories: [repository('r1', 'macbook'), repository('r2', 'vm', { ...onVm, name: 'acme/r2' }), repository('r3', 'oldmac', { route: 'cached', name: 'acme/r3' })],
    worktrees: [
      { ...worktree('w1', 'r1', 'macbook'), task: 'fix-ci', last_activity_at: ago(HOUR) },
      { ...worktree('w2', 'r1', 'macbook'), task: 'add-search', last_activity_at: ago(5 * HOUR) },
      { ...worktree('w3', 'r2', 'vm'), ...onVm, last_activity_at: ago(2 * HOUR) },
      { ...worktree('w4', 'r3', 'oldmac'), route: 'cached', observed_at: ago(30 * HOUR) },
    ],
    pull_requests: [pullRequest('p1', 'r1', 'w1')],
    agents: [
      run('run-1', 'running', { ...onMac, repository: 'r1', model: 'sonnet-5-5', worktrees: ['w1'], activity: 'working' }),
      agent('s-idle', 'r1', 'idle', onMac),
      agent('s-vm', 'r2', 'live', { ...onVm, runtime: 'codex' }),
    ],
  })
}

/** One reading of a machine's resources. */
export function sample(minutesAgo: number, cpu: number, memoryUsedGb = 8, overrides: Partial<MetricsSample> = {}): MetricsSample {
  return {
    cpu_percent: cpu,
    load1: cpu / 20,
    memory_used_bytes: memoryUsedGb * 2 ** 30,
    memory_total_bytes: 16 * 2 ** 30,
    disk_free_bytes: 200 * 2 ** 30,
    disk_total_bytes: 500 * 2 ** 30,
    sampled_at: ago(minutesAgo * MINUTE),
    ...overrides,
  }
}

/**
 * What the daemon answers for each machine: 360 local samples (one every 10 s, the last busy), a live history of vm
 * (idle), one cached sample 30 minutes old for oldmac and no source for nas.
 */
export function metricsAnswers(): Record<string, MachineMetrics> {
  return {
    'mach-macbook': { machine: 'mach-macbook', route: 'local', samples: Array.from({ length: 360 }, (_, index) => sample((359 - index) / 6, index === 359 ? 88 : 20, index === 359 ? 14 : 8)) },
    'mach-vm': { machine: 'mach-vm', route: 'live-remote', fetched_at: ago(10_000), samples: Array.from({ length: 30 }, (_, index) => sample(29 - index, 10, 4)) },
    'mach-oldmac': { machine: 'mach-oldmac', route: 'cached', samples: [sample(30, 35, 6)] },
    'mach-nas': { machine: 'mach-nas', route: 'none', samples: [], reason: 'this daemon does not sample' },
  }
}

/** A `fetch` that answers the machine-metrics route from `answers` and refuses anything else. */
export function metricsFetch(answers: Record<string, MachineMetrics> = metricsAnswers()): typeof fetch {
  return (async (input: RequestInfo | URL) => {
    const machineId = new URL(String(input), 'http://cockpit.test').searchParams.get('machine') as string
    const answer = answers[machineId]
    return answer === undefined ? new Response('{}', { status: 404 }) : new Response(JSON.stringify(answer), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch
}

/** What a spec of the machine page decides: the fleet, what the daemon answers and who is looking. */
export interface OpenOptions {
  document?: FleetDocument
  /** The machine-metrics answers; `'never'` leaves every read pending. */
  answers?: Record<string, MachineMetrics> | 'never'
  session?: Session
}

/**
 * Opens a page of `/machines` through the application's routes with a fake chart engine, and, unless the answers
 * are `'never'`, waits for the first round of metrics reads. `create` is the engine's chart constructor.
 */
export async function openMachines<T>(url: string, page: Type<T>, options: OpenOptions = {}) {
  const document = options.document ?? machinesDocument()
  const create = vi.fn((_canvas: HTMLCanvasElement, _config: unknown) => ({ update: vi.fn(), destroy: vi.fn() }))
  const fetcher = options.answers === 'never' ? ((() => new Promise<Response>(() => undefined)) as typeof fetch) : metricsFetch(options.answers ?? metricsAnswers())
  const opened = await openPage(url, page, document, options.session ?? SESSION, fetcher, [{ provide: CHART_ENGINE, useValue: async () => ({ create }) }])
  if (options.answers !== 'never' && document.machines.length > 0) await vi.waitFor(() => expect(TestBed.inject(MetricsPoller).entries().size).toBeGreaterThan(0))
  await opened.harness.fixture.whenStable()
  return { ...opened, create }
}
