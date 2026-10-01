// The fleets Home is photographed against (tools/shots.mjs, tools/lib/shot-plan.mjs): small and
// hand-made, each shaped as the daemon's document is, so a screenshot shows one situation clearly.
// Loaded through Vite like the library's fixtures (tools/fixtures.mjs).
import type { Agent, FleetDocument, Machine, MachineMetrics, PullRequest, Repository, Worktree } from '@cockpit/fleet-data'

/** The instant of every snapshot below, in milliseconds. */
export const SNAPSHOT = Date.parse('2026-10-01T10:00:00Z')
const MIN = 60_000
const HOUR = 60 * MIN
const DAY = 24 * HOUR
const at = (ago: number): string => new Date(SNAPSHOT - ago).toISOString()

const mac: Machine = { id: 'mach-mac', machine: 'mac', machine_id: 'mach-mac', route: 'local', observed_at: at(10_000), wb_version: '0.176.0', repository_count: 3, worktree_count: 0, os: 'darwin', arch: 'arm64', cpu_count: 12, boot_time: at(9 * DAY) }
const vm: Machine = { id: 'mach-vm', machine: 'vm', machine_id: 'mach-vm', route: 'live-remote', observed_at: at(20_000), wb_version: '0.176.0', repository_count: 2, worktree_count: 0, os: 'linux', arch: 'amd64', cpu_count: 8, transport: 'ssh' }
const old: Machine = { id: 'mach-old', machine: 'old', machine_id: 'mach-old', route: 'cached', observed_at: at(25 * HOUR), wb_version: '0.170.2', repository_count: 1, worktree_count: 0 }

const entry = (machine: Machine) => ({ machine: machine.machine, machine_id: machine.machine_id, route: machine.route, observed_at: machine.observed_at })
const repository = (id: string, name: string, machine: Machine = mac, extra: Partial<Repository> = {}): Repository => ({ id, ...entry(machine), host: 'github.com', name, default_branch: 'main', worktree_count: 0, ...extra })
const repositories = [repository('r-wb', 'sneat-dev/wb'), repository('r-go', 'sneat-co/sneat-go'), repository('r-web', 'acme/web')]

let counter = 0
function worktree(task: string, repositoryId: string, extra: Partial<Worktree> = {}, machine: Machine = mac): Worktree {
  counter++
  return { id: `wt-${counter}`, ...entry(machine), repository: repositoryId, task, branch: `task/${task}`, owner_state: 'idle', last_activity_at: at(2 * HOUR), ahead: 0, behind: 0, has_upstream: true, ...extra }
}
function pull(id: string, repositoryId: string, number: number, worktreeId: string | undefined, extra: Partial<PullRequest> = {}): PullRequest {
  return { id, ...entry(mac), repository: repositoryId, worktree: worktreeId, number, url: `https://github.com/example/${repositoryId}/pull/${number}`, state: 'open', mergeable: 'clean', checks_total: 5, checks_passed: 5, checks_failed: 0, checks_pending: 0, checks_green: true, checked_at: at(4 * MIN), ...extra }
}
function agent(id: string, extra: Partial<Agent> = {}, machine: Machine = mac): Agent {
  return { id, ...entry(machine), kind: 'run', run_id: id, runtime: 'claude', model: 'opus', state: 'running', started_at: at(35 * MIN), ...extra }
}

function throughput(days: number, drift = 0): NonNullable<FleetDocument['throughput']> {
  const per_day = Array.from({ length: days }, (_, index) => {
    const date = new Date(SNAPSHOT - (days - 1 - index) * DAY).toISOString().slice(0, 10)
    return { date, finished: (index * 7 + drift) % 5, dropped: (index * 3 + drift) % 4 === 0 ? 2 : (index + drift) % 3 }
  })
  return {
    window_days: days,
    per_day,
    slowest: [
      { task: 'migrate-auth', duration_seconds: 15 * 3600, landed_at: at(2 * DAY) },
      { task: 'rewrite-index', duration_seconds: 9 * 3600, landed_at: at(5 * DAY) },
      { task: 'bump-deps', duration_seconds: 4 * 3600, landed_at: at(1 * DAY) },
      { task: 'fix-ci-race', duration_seconds: 95 * 60, landed_at: at(3 * DAY) },
      { task: 'add-search', duration_seconds: 40 * 60, landed_at: at(DAY) },
    ],
    median_seconds: 15 * 60,
    p90_seconds: 15 * 3600,
  } as NonNullable<FleetDocument['throughput']>
}

function document(extra: Partial<FleetDocument> & Record<string, unknown>): FleetDocument {
  return { schema_version: 2, snapshot_at: at(0), refresh_interval_seconds: 30, warming_up: false, repositories_total: 3, repositories_scanned: 3, diagnostics: 0, machines: [mac, vm, old], repositories, worktrees: [], pull_requests: [], agents: [], ...extra } as FleetDocument
}

/** Samples for a machine: the last of them is the load the strip shows. */
function metrics(machine: Machine, route: MachineMetrics['route'], cpu: number, memory: number, ago = 6_000): MachineMetrics {
  const total = 32 * 1024 ** 3
  return {
    machine: machine.id,
    route,
    samples: [3, 2, 1, 0].map((step) => ({ cpu_percent: cpu - step, load1: cpu / 20, memory_used_bytes: Math.round((total * memory) / 100), memory_total_bytes: total, disk_free_bytes: 200 * 1024 ** 3, disk_total_bytes: 500 * 1024 ** 3, sampled_at: at(ago + step * 10_000) })),
  }
}

function busy(): FleetDocument {
  const [wb, go, web] = repositories
  const w = {
    risk: worktree('refactor-cache', wb.id, { ahead: 2, owner_state: 'orphaned', last_activity_at: at(HOUR) }),
    riskGo: worktree('refactor-cache', go.id, { ahead: 1, has_upstream: false, owner_state: 'orphaned', last_activity_at: at(3 * HOUR) }),
    failing: worktree('fix-ci-race', wb.id, { last_activity_at: at(40 * MIN) }),
    blocked: worktree('migrate-auth', go.id, { last_activity_at: at(50 * MIN) }, vm),
    broken: worktree('bump-deps', web.id, { last_activity_at: at(5 * HOUR) }),
    dirty: worktree('drop-legacy', wb.id, { last_activity_at: at(6 * HOUR) }),
    finished: worktree('add-search', go.id, { ahead: 2, last_activity_at: at(8 * HOUR) }),
    riskTwo: worktree('tidy-logging', web.id, { ahead: 3, owner_state: 'idle', last_activity_at: at(21 * DAY) }),
    riskThree: worktree('rename-flags', wb.id, { ahead: 1, owner_state: 'idle', last_activity_at: at(30 * DAY) }),
    ready: worktree('improve-docs', wb.id, { last_activity_at: at(20 * MIN) }),
    readyGo: worktree('improve-docs', go.id, { last_activity_at: at(25 * MIN) }),
    readyOne: worktree('speed-up-index', web.id, { last_activity_at: at(4 * HOUR) }),
    waiting: worktree('add-telemetry', wb.id, { last_activity_at: at(90 * MIN) }),
    landed: Array.from({ length: 9 }, (_, index) => worktree(`landed-${index}`, wb.id, { last_activity_at: at((3 + index * 4) * DAY), owner_state: 'unknown' })),
    stale: Array.from({ length: 4 }, (_, index) => worktree(`idle-${index}`, go.id, { last_activity_at: at((40 + index * 30) * DAY), owner_state: 'idle' })),
    old: worktree('old-experiment', go.id, { last_activity_at: at(120 * DAY), owner_state: 'idle' }, old),
  }
  const worktrees = Object.values(w).flat()
  const pull_requests = [
    pull('pr-fail', wb.id, 131, w.failing.id, { checks_green: false, checks_passed: 3, checks_failed: 1, failed_check: 'build-linux' }),
    pull('pr-dirty', wb.id, 128, w.dirty.id, { mergeable: 'dirty' }),
    pull('pr-r1', wb.id, 12, w.ready.id, { checked_at: at(4 * MIN) }),
    pull('pr-r2', go.id, 7, w.readyGo.id, { checked_at: at(9 * MIN) }),
    pull('pr-r3', web.id, 41, w.readyOne.id, { checked_at: at(6 * MIN) }),
    pull('pr-wait', wb.id, 133, w.waiting.id, { checks_green: false, checks_passed: 3, checks_pending: 2, checked_at: at(4 * MIN) }),
    ...w.landed.map((tree, index) => pull(`pr-landed-${index}`, wb.id, 200 + index, tree.id, { state: 'merged', checks_green: true })),
  ]
  const agents = [
    agent('run-speed', { task: 'speed-up-index', worktrees: [w.readyOne.id], activity: 'working', started_at: at(35 * MIN) }),
    agent('sess-1', { kind: 'session', run_id: undefined, session_id: 'sess-1', runtime: 'codex', model: undefined, state: 'live', started_at: at(2 * HOUR) }),
    agent('vm-blocked', { kind: 'session', run_id: undefined, session_id: 'sess-vm', task: 'migrate-auth', worktrees: [w.blocked.id], state: 'live', activity: 'blocked', started_at: at(70 * MIN), observed_at: at(20 * MIN) }, vm),
    agent('run-failed', { task: 'bump-deps', worktrees: [w.broken.id], state: 'failed', exit_code: 2, started_at: at(5 * HOUR), finished_at: at(4 * HOUR) }),
    agent('mac-finished', { task: 'add-search', worktrees: [w.finished.id], state: 'completed', activity: 'done', started_at: at(9 * HOUR), finished_at: at(8 * HOUR) }),
    agent('old-run', { task: 'old-experiment', state: 'running', activity: 'working', started_at: at(39 * MIN), observed_at: at(25 * HOUR) }, old),
    ...[1, 2, 3].map((n) => agent(`orphan-${n}`, { kind: 'session', run_id: undefined, session_id: `sess-o${n}`, state: 'finished', activity: 'blocked' })),
  ]
  return document({ worktrees, pull_requests, agents, throughput: throughput(30) })
}

const METRICS = new Map<string, MachineMetrics>([
  ['mach-mac', metrics(mac, 'local', 41, 52)],
  ['mach-vm', metrics(vm, 'live-remote', 86, 73)],
  ['mach-old', metrics(old, 'cached', 20, 30, 30 * MIN)],
])

/** The states Home is photographed in: a document, its machine metrics and what the preview serves around it. */
export function homeStates(): Record<string, { document: FleetDocument; metrics: Map<string, MachineMetrics>; branches: Map<string, never[]> }> {
  const base = busy()
  const calm = document({
    machines: [mac, vm],
    worktrees: [worktree('ship-readme', 'r-wb', { last_activity_at: at(2 * DAY), owner_state: 'idle' }), worktree('add-search', 'r-go', { last_activity_at: at(5 * DAY), owner_state: 'idle' })],
  })
  const warming = { ...base, warming_up: true, repositories_total: 438, repositories_scanned: 40, worktrees: [], pull_requests: [], agents: [], throughput: undefined }
  const state = (doc: FleetDocument, samples = METRICS) => ({ document: doc, metrics: samples, branches: new Map<string, never[]>() })
  return {
    busy: state(base),
    healthy: state(calm, new Map([['mach-mac', metrics(mac, 'local', 12, 30)], ['mach-vm', metrics(vm, 'live-remote', 18, 40)]])),
    warming: state(warming as FleetDocument),
    throttled: state({ ...base, pull_requests_throttled: true } as FleetDocument),
    'remote-error': state({ ...base, machines: [mac, { ...vm, remote_error: 'ssh_unavailable', route: 'cached', observed_at: at(3 * HOUR) }, old] } as FleetDocument),
    'no-throughput': state({ ...base, throughput: undefined }),
    'only-dropped': state({ ...base, throughput: { ...throughput(30), per_day: throughput(30).per_day.map((day) => ({ ...day, finished: 0, dropped: 1 })), slowest: [], median_seconds: undefined, p90_seconds: undefined } as FleetDocument['throughput'] }),
  }
}
