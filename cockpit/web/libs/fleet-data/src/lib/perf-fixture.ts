// The performance fixture (REQ:fast-filtering and the budgets): 500 repository
// entries, 600 worktrees, 4,000 branches and 3 machines with realistic names and
// code_index entries that carry statistics, from a fixed seed so every run sees
// the same data. Branches are not in the document (schema 2); they are served
// per repository, as the lazy branches route does.

import {
  Agent,
  Branch,
  CodeIndex,
  CodeIndexState,
  FleetDocument,
  Machine,
  MachineMetrics,
  MetricsSample,
  OwnerState,
  PullRequest,
  Repository,
  SCHEMA_VERSION,
  Worktree,
} from './fleet.types'

export const PERF_COUNTS = { repositories: 500, worktrees: 600, branches: 4000, machines: 3 } as const

/** The fixture's "now": the document's snapshot time. */
export const PERF_NOW = Date.parse('2026-10-01T10:00:00Z')

const DAY = 86_400_000

/** A small deterministic generator (mulberry32). */
export function seeded(seed: number): () => number {
  let state = seed >>> 0
  return () => {
    state = (state + 0x6d2b79f5) >>> 0
    let t = state
    t = Math.imul(t ^ (t >>> 15), t | 1)
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61)
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

const OWNERS = ['sneat-co', 'sneat-dev', 'strongo', 'dal-go', 'ingitdb', 'bots-go-framework', 'datatug', 'sneat-games', 'chatwright', 'trakhimenok']
const STEMS = ['bots', 'sneat', 'dalgo', 'wb', 'cockpit', 'specscore', 'incident', 'prioritar', 'contact', 'paymentus', 'noticeboard', 'chess', 'reversi', 'trackus', 'anymeter', 'ovdb', 'cli', 'auth', 'geo', 'graph']
const SUFFIXES = ['go', 'web', 'api', 'core', 'ui', 'libs', 'apps', 'docs', 'tools', 'extensions', 'infra', 'bot']
const VERBS = ['fix', 'add', 'refactor', 'migrate', 'drop', 'speed-up', 'harden', 'document', 'split', 'rename']
const THINGS = ['ci', 'lint', 'auth', 'cache', 'routes', 'tests', 'docs', 'build', 'deploy', 'schema', 'search', 'matcher', 'palette', 'metrics', 'hooks']
const STATES: CodeIndexState[] = ['fresh', 'fresh', 'fresh', 'stale', 'pending', 'diverged', 'failed', 'never']
// Branch prefixes the agents use besides the plain task name.
const BRANCH_PREFIXES = ['agent', 'codex', 'fix']
const KINDS = ['function', 'method', 'type', 'interface', 'const', 'variable', 'package']

function pick<T>(random: () => number, values: readonly T[]): T {
  return values[Math.floor(random() * values.length)]
}

function codeIndex(random: () => number): CodeIndex[] {
  const state = pick(random, STATES)
  const files = 20 + Math.floor(random() * 900)
  const symbols = files * (8 + Math.floor(random() * 12))
  const indexed = state !== 'never'
  return [
    {
      indexer: 'codegrapher',
      state,
      behind: state === 'stale' ? 1 + Math.floor(random() * 40) : undefined,
      receipt_at: new Date(PERF_NOW - Math.floor(random() * 20) * DAY).toISOString(),
      statistics: {
        indexed,
        files: indexed ? files : 0,
        symbols: indexed ? symbols : 0,
        edges: indexed ? symbols * 3 : 0,
        kinds: indexed ? KINDS.map((kind) => ({ kind, count: Math.floor(symbols / KINDS.length) })) : [],
        error: state === 'failed' ? 'provider_unavailable' : undefined,
      },
    },
  ]
}

function entry(machine: Machine, id: string): Pick<Repository, 'id' | 'machine' | 'machine_id' | 'route' | 'observed_at'> {
  return { id, machine: machine.machine, machine_id: machine.machine_id, route: machine.route, observed_at: machine.observed_at }
}

export interface PerformanceFixture {
  document: FleetDocument
  /** The lazy branches, by the id of the repository entry that holds them. */
  branches: Map<string, Branch[]>
  /** One metrics answer per machine id: a local history, a live-remote one and a cached sample. */
  metrics: Map<string, MachineMetrics>
}

function samples(random: () => number, count: number, last: number): MetricsSample[] {
  return Array.from({ length: count }, (_, index) => ({
    cpu_percent: Math.round(random() * 1000) / 10,
    load1: Math.round(random() * 800) / 100,
    memory_used_bytes: Math.floor((0.3 + random() * 0.5) * 32e9),
    memory_total_bytes: 32e9,
    disk_free_bytes: Math.floor((0.2 + random() * 0.4) * 1e12),
    disk_total_bytes: 1e12,
    sampled_at: new Date(last - (count - 1 - index) * 10_000).toISOString(),
  }))
}

/** Builds the fixture. The same seed always gives the same data. */
export function performanceFixture(seed = 20261001): PerformanceFixture {
  const random = seeded(seed)
  const observed = new Date(PERF_NOW - 60_000).toISOString()
  const machines: Machine[] = [
    { id: 'mach-mac', machine: 'mac', machine_id: 'mach-mac', route: 'local', observed_at: observed, wb_version: '0.176.0', repository_count: 400, worktree_count: 500, os: 'darwin', arch: 'arm64', cpu_count: 12, boot_time: new Date(PERF_NOW - 9 * DAY).toISOString() },
    { id: 'mach-vm', machine: 'vm', machine_id: 'mach-vm', route: 'cached', observed_at: new Date(PERF_NOW - 20 * 60_000).toISOString(), wb_version: '0.176.0', repository_count: 70, worktree_count: 70, os: 'linux', arch: 'amd64', cpu_count: 8, transport: 'ssh' },
    { id: 'mach-old', machine: 'old', machine_id: 'mach-old', route: 'cached', observed_at: new Date(PERF_NOW - 25 * 60 * 60_000).toISOString(), wb_version: '0.170.2', repository_count: 30, worktree_count: 30 },
  ]
  const [mac, vm, old] = machines

  // 445 distinct repositories: 400 on mac, 70 on vm (40 of them also on mac), 30 on old (15 also on mac).
  const names: string[] = []
  const seen = new Set<string>()
  while (names.length < 445) {
    const name = `${pick(random, OWNERS)}/${pick(random, STEMS)}-${pick(random, SUFFIXES)}${names.length % 7 === 0 ? `-${names.length}` : ''}`
    if (!seen.has(name)) {
      seen.add(name)
      names.push(name)
    }
  }
  const placements: [Machine, string][] = [
    ...names.slice(0, 400).map((name): [Machine, string] => [mac, name]),
    ...names.slice(0, 40).map((name): [Machine, string] => [vm, name]),
    ...names.slice(400, 430).map((name): [Machine, string] => [vm, name]),
    ...names.slice(40, 55).map((name): [Machine, string] => [old, name]),
    ...names.slice(430, 445).map((name): [Machine, string] => [old, name]),
  ]
  const repositories: Repository[] = placements.map(([machine, name], index): Repository => {
    const local = machine.route === 'local'
    return {
      ...entry(machine, `repo-${index}`),
      host: 'github.com',
      name,
      default_branch: 'main',
      worktree_count: 0,
      local_branch_count: local ? 2 + Math.floor(random() * 14) : undefined,
      remote_branch_count: local ? Math.floor(random() * 6) : undefined,
      open_pull_request_count: local && random() < 0.05 ? 1 : undefined,
      active_agent_count: undefined,
      error: local && index % 97 === 0 ? 'scan_failed' : undefined,
      code_index: local ? codeIndex(random) : undefined,
      last_activity_at: local ? new Date(PERF_NOW - Math.floor(random() * 200) * DAY).toISOString() : undefined,
      remote_url_web: local ? `https://github.com/${name}` : undefined,
    }
  })

  // 600 worktrees over 455 tasks: 500 on mac, 70 on vm, 30 on old. Most of them are old, like a real fleet's.
  const taskNames = Array.from({ length: 455 }, (_, index) => `${pick(random, VERBS)}-${pick(random, THINGS)}-${index}`)
  const owners: (OwnerState | undefined)[] = ['active', 'idle', 'idle', 'idle', 'orphaned', 'unknown']
  const held: Record<string, Repository[]> = Object.fromEntries(machines.map((machine) => [machine.machine_id, repositories.filter((candidate) => candidate.machine_id === machine.machine_id)]))
  const worktrees: Worktree[] = Array.from({ length: PERF_COUNTS.worktrees }, (_, index): Worktree => {
    const machine = index < 500 ? mac : index < 570 ? vm : old
    const repository = pick(random, held[machine.machine_id])
    repository.worktree_count++
    const task = taskNames[index % taskNames.length]
    const local = machine.route === 'local'
    // Real `wb` data names a worktree's branch after its task; about a third are named otherwise.
    const branch = random() < 1 / 3 ? `${pick(random, BRANCH_PREFIXES)}/${pick(random, VERBS)}-${pick(random, THINGS)}-${index}` : task
    // Most work is old: about 2 in 100 worktrees were touched in the last 13 days, the rest 15 to 119 days ago.
    const idleDays = random() < 0.02 ? Math.floor(random() * 13) : 15 + Math.floor(random() * 105)
    return {
      ...entry(machine, `wt-${index}`),
      repository: repository.id,
      task,
      name: task,
      branch,
      lifecycle: random() < 0.15 ? 'merged' : 'working',
      owner_state: pick(random, owners),
      last_activity_at: new Date(PERF_NOW - idleDays * DAY - Math.floor(random() * DAY)).toISOString(),
      ahead: local ? Math.floor(random() * 3) : undefined,
      behind: local ? Math.floor(random() * 5) : undefined,
      has_upstream: local ? random() < 0.8 : undefined,
      upstream_gone: local && random() < 0.05 ? true : undefined,
      code_index: local ? codeIndex(random) : undefined,
    }
  })

  const pullRequests: PullRequest[] = Array.from({ length: 24 }, (_, index): PullRequest => {
    const worktree = worktrees[index * 7]
    const failed = index % 8 === 0
    return {
      ...entry(mac, `pr-${index}`),
      repository: worktree.repository,
      worktree: worktree.id,
      branch: worktree.branch,
      number: 100 + index,
      url: `https://github.com/acme/repo/pull/${100 + index}`,
      state: index % 6 === 5 ? 'merged' : 'open',
      mergeable: failed ? 'blocked' : 'clean',
      checks_total: 6,
      checks_passed: failed ? 4 : 6,
      checks_failed: failed ? 2 : 0,
      checks_pending: 0,
      checks_green: !failed,
      failed_check: failed ? 'go-ci / test' : undefined,
      checked_at: new Date(PERF_NOW - index * 60_000).toISOString(),
    }
  })

  const agents: Agent[] = [mac, mac, vm, old].map((machine, index): Agent => {
    const worktree = worktrees.find((candidate) => candidate.machine_id === machine.machine_id) as Worktree
    return {
      ...entry(machine, `agent-${index}`),
      kind: index === 1 ? 'run' : 'session',
      session_id: index === 1 ? undefined : `session-${index}`,
      run_id: index === 1 ? `run-${index}` : undefined,
      runtime: 'claude',
      model: 'opus',
      state: index === 1 ? 'running' : 'live',
      activity: index === 0 ? 'working' : index === 2 ? 'blocked' : undefined,
      task: worktree.task,
      worktrees: [worktree.id],
      repository: worktree.repository,
      started_at: new Date(PERF_NOW - (index + 1) * 600_000).toISOString(),
    }
  })

  const branches = new Map<string, Branch[]>()
  const holders = repositories.filter((repository) => repository.route === 'local')
  for (let index = 0; index < PERF_COUNTS.branches; index++) {
    const repository = holders[index % holders.length]
    const name = `${index % 5 === 0 ? 'feature' : 'task'}/${pick(random, VERBS)}-${pick(random, THINGS)}-${index}`
    branches.set(repository.id, [
      ...(branches.get(repository.id) ?? []),
      {
        ...entry(mac, `branch-${index}`),
        repository: repository.id,
        name,
        scope: index % 4 === 0 ? 'remote' : 'local',
        last_activity_at: new Date(PERF_NOW - Math.floor(random() * 300) * DAY).toISOString(),
      },
    ])
  }

  const document: FleetDocument = {
    schema_version: SCHEMA_VERSION,
    snapshot_at: observed,
    refresh_interval_seconds: 30,
    warming_up: false,
    repositories_total: repositories.length,
    repositories_scanned: repositories.length,
    diagnostics: 0,
    code_index_provider: 'codegrapher',
    machines,
    repositories,
    worktrees,
    pull_requests: pullRequests,
    agents,
    throughput: {
      window_days: 30,
      per_day: Array.from({ length: 12 }, (_, index) => ({ date: new Date(PERF_NOW - index * 2 * DAY).toISOString().slice(0, 10), finished: 1 + (index % 4), dropped: index % 3, ...(index % 2 === 0 ? { landed: 1 } : {}) })),
      slowest: Array.from({ length: 5 }, (_, index) => ({ task: taskNames[index], duration_seconds: 86_400 * (5 - index), landed_at: new Date(PERF_NOW - index * DAY).toISOString() })),
      median_seconds: 3 * 86_400,
      p90_seconds: 5 * 86_400,
    },
  }

  const metrics = new Map<string, MachineMetrics>([
    [mac.id, { machine: mac.id, route: 'local', samples: samples(random, 360, PERF_NOW) }],
    [vm.id, { machine: vm.id, route: 'live-remote', fetched_at: observed, samples: samples(random, 360, PERF_NOW - 20_000) }],
    [old.id, { machine: old.id, route: 'cached', samples: samples(random, 1, PERF_NOW - 30 * 60_000) }],
  ])
  return { document, branches, metrics }
}

/** `count` rows for the benchmark: the fixture's worktree rows repeated under new ids. */
export function repeatRows<T extends { id: string }>(rows: readonly T[], count: number): T[] {
  return Array.from({ length: count }, (_, index) => ({ ...rows[index % rows.length], id: `${rows[index % rows.length].id}#${index}` }))
}
