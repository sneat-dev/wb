// The memoised view model (REQ:derived-collections-memoised). One FleetModel
// per fleet document: each derived collection is computed once, on first use,
// and kept for the document's identity, so a poll that returns 304 or an
// identical document recomputes nothing and a render reads the same arrays.

import { Agent, FleetDocument, Machine, MachineMetrics, PullRequest, Repository, Worktree, isRunning } from './fleet.types'
import { CopyCommand, SshRoute, cockpitExport, daemonStart, fleetStatus, pullRequestLand, remoteEnroll, remotePublish, selfUpdate } from './commands'
import { AGE_TERMS, ageTermOf, idleOver30Days, wholeDays } from './matcher'
import {
  AgentRowContext,
  ListRow,
  WorktreeRowContext,
  agentRows,
  machineRows,
  repositoryRows,
  taskRows,
  worktreeRows,
} from './list-rows'
import { MergedRepository, isStale, mergeRepositories, repositorySlug } from './repository-identity'
import {
  TaskInputs,
  blockingRuns,
  failedChecks,
  interimAtRiskWorktrees,
  isObserved,
  notReadyReasons,
  openObserved,
  startedAt,
  taskState,
  taskStateInfo,
} from './task-state'
import {
  Cleanup,
  FleetHealth,
  HealthItem,
  InFlightAgent,
  MachineLoad,
  MachineStateId,
  MachineView,
  NEEDS_YOU_VISIBLE,
  NeedsYou,
  NeedsYouItem,
  NotReadyRow,
  ReadyPullRequest,
  ReadyToLand,
  ReadyToLandRow,
  ScanErrorItem,
  TaskView,
  ThroughputSeries,
} from './view-types'
import { ageLink, agentDetailLink, chipLink, listLink, machineLink, selectionLink } from './vocabulary'

/** The derivations that are counted, so a test can prove each ran once per document. */
export type Derivation =
  | 'index'
  | 'repositories'
  | 'tasks'
  | 'needsYou'
  | 'readyToLand'
  | 'inFlight'
  | 'resume'
  | 'cleanup'
  | 'health'
  | 'throughput'
  | 'machines'
  | 'rows:tasks'
  | 'rows:repositories'
  | 'rows:worktrees'
  | 'rows:agents'
  | 'rows:machines'

export interface ModelOptions {
  /** The clock; the model reads it once, when it is created. */
  now?: () => number
  /** Called each time a derivation actually runs. */
  onDerive?: (derivation: Derivation) => void
  /** The SSH route of a machine, when it has one, for the commands of Fleet health. */
  sshRoute?: (machine: Machine) => SshRoute | undefined
}

/** How many tasks Resume lists. */
export const RESUME_COUNT = 5

const AGE_LABELS: Record<(typeof AGE_TERMS)[number], string> = {
  '<1d': 'today',
  '1-7d': '1 to 7 days',
  '8-30d': '8 to 30 days',
  '31-90d': '31 to 90 days',
  '>90d': 'over 90 days',
}

const MERGE_ATTENTION = new Set(['dirty', 'behind', 'blocked'])

/** Orders newest first, a missing time last; 0 for two missing times, so the next key decides. */
function newestFirst(a: number | undefined, b: number | undefined): number {
  return (b ?? -Infinity) - (a ?? -Infinity) || 0
}

/** Orders oldest first, a missing time last. */
function oldestFirst(a: number | undefined, b: number | undefined): number {
  return (a ?? Infinity) - (b ?? Infinity) || 0
}

function oldestCheck(pullRequests: readonly PullRequest[]): number | undefined {
  const times = pullRequests.map((pullRequest) => toTime(pullRequest.checked_at)).filter((time): time is number => time !== undefined)
  return times.length === 0 ? undefined : Math.min(...times)
}

function toTime(value: string | undefined): number | undefined {
  const time = value ? Date.parse(value) : Number.NaN
  return Number.isNaN(time) ? undefined : time
}

/** A WB version as numbers, or undefined when it is not dotted numbers (`v` and a suffix are tolerated). */
export function parseVersion(version: string | undefined): number[] | undefined {
  const match = /^v?(\d+(?:\.\d+)*)/.exec(version ?? '')
  return match ? match[1].split('.').map(Number) : undefined
}

/** A version as a string that sorts like the version: each number padded, so 0.9.0 sorts before 0.10.0. */
export function versionKey(version: string | undefined): string | undefined {
  return parseVersion(version)?.map((part) => String(part).padStart(8, '0')).join('.')
}

export function compareVersions(a: number[], b: number[]): number {
  for (let index = 0; index < Math.max(a.length, b.length); index++) {
    const difference = (a[index] ?? 0) - (b[index] ?? 0)
    if (difference !== 0) return difference
  }
  return 0
}

/** The agent's runtime and model, or "agent" for neither. */
export function agentTitle(agent: Agent): string {
  return [agent.runtime, agent.model].filter((part) => part).join(' ') || 'agent'
}

/** The latest sample's verdict: free below 70 percent CPU and 80 percent memory, busy otherwise. */
export function machineLoad(metrics: MachineMetrics | undefined): MachineLoad {
  const sample = metrics?.samples[metrics.samples.length - 1]
  if (metrics === undefined || sample === undefined || sample.memory_total_bytes <= 0) return { state: 'not-reported', route: metrics?.route ?? 'none' }
  const memoryPercent = (sample.memory_used_bytes / sample.memory_total_bytes) * 100
  return {
    state: sample.cpu_percent < 70 && memoryPercent < 80 ? 'free' : 'busy',
    route: metrics.route,
    sampledAt: toTime(sample.sampled_at),
    cpuPercent: sample.cpu_percent,
    memoryPercent,
  }
}

export class FleetModel {
  readonly now: number
  private readonly cache = new Map<string, unknown>()

  constructor(
    readonly document: FleetDocument,
    private readonly options: ModelOptions = {},
  ) {
    this.now = (options.now ?? Date.now)()
  }

  private memo<T>(name: Derivation, derive: () => T): T {
    if (!this.cache.has(name)) {
      this.options.onDerive?.(name)
      this.cache.set(name, derive())
    }
    return this.cache.get(name) as T
  }

  // ---- lookups ----

  private get index(): {
    repositoryById: Map<string, Repository>
    worktreeById: Map<string, Worktree>
    taskOfPullRequest: Map<string, string>
  } {
    return this.memo('index', () => {
      const repositoryById = new Map(this.document.repositories.map((repository) => [repository.id, repository]))
      const worktreeById = new Map(this.document.worktrees.map((worktree) => [worktree.id, worktree]))
      const byBranch = new Map(this.document.worktrees.map((worktree) => [`${worktree.repository}\u0000${worktree.branch}`, worktree.task]))
      const taskOfPullRequest = new Map<string, string>()
      for (const pullRequest of this.document.pull_requests) {
        const task =
          (pullRequest.worktree === undefined ? undefined : worktreeById.get(pullRequest.worktree)?.task) ??
          (pullRequest.repository !== undefined && pullRequest.branch !== undefined ? byBranch.get(`${pullRequest.repository}\u0000${pullRequest.branch}`) : undefined)
        if (task !== undefined) taskOfPullRequest.set(pullRequest.id, task)
      }
      return { repositoryById, worktreeById, taskOfPullRequest }
    })
  }

  /** A repository's `owner/name` for its id, or the id when the document does not list it. */
  repositoryName(id: string): string {
    const repository = this.index.repositoryById.get(id)
    return repository ? repositorySlug(repository) : id
  }

  /** The task a pull request belongs to: through its worktree, or its repository and branch. */
  taskOfPullRequest(pullRequest: PullRequest): string | undefined {
    return this.index.taskOfPullRequest.get(pullRequest.id)
  }

  /** The tasks an agent works on: its own `task`, or those of the worktrees it names. Never guessed. */
  tasksOfAgent(agent: Agent): string[] {
    if (agent.task !== undefined) return [agent.task]
    const tasks = (agent.worktrees ?? []).map((id) => this.index.worktreeById.get(id)?.task)
    return [...new Set(tasks.filter((task): task is string => task !== undefined))]
  }

  // ---- derived collections ----

  /** Repositories merged by identity across machines. */
  get repositories(): MergedRepository[] {
    return this.memo('repositories', () => mergeRepositories(this.document.repositories, this.now))
  }

  /** One task per name, with its state. */
  get tasks(): TaskView[] {
    return this.memo('tasks', () => {
      const worktreesByTask = new Map<string, Worktree[]>()
      for (const worktree of this.document.worktrees) worktreesByTask.set(worktree.task, [...(worktreesByTask.get(worktree.task) ?? []), worktree])
      const pullRequestsByTask = new Map<string, PullRequest[]>()
      for (const pullRequest of this.document.pull_requests) {
        const task = this.taskOfPullRequest(pullRequest)
        if (task !== undefined) pullRequestsByTask.set(task, [...(pullRequestsByTask.get(task) ?? []), pullRequest])
      }
      const agentsByTask = new Map<string, Agent[]>()
      for (const agent of this.document.agents) {
        for (const task of this.tasksOfAgent(agent)) agentsByTask.set(task, [...(agentsByTask.get(task) ?? []), agent])
      }
      const names = [...new Set([...worktreesByTask.keys(), ...agentsByTask.keys()])]
      return names.map((name): TaskView => {
        const inputs: TaskInputs = {
          worktrees: worktreesByTask.get(name) ?? [],
          pullRequests: pullRequestsByTask.get(name) ?? [],
          agents: agentsByTask.get(name) ?? [],
        }
        const state = taskState(inputs, this.now)
        const times = inputs.worktrees.map((worktree) => toTime(worktree.last_activity_at)).filter((time): time is number => time !== undefined)
        const machines = new Map(inputs.worktrees.map((worktree) => [worktree.machine_id, worktree.machine]))
        return {
          name,
          worktrees: [...inputs.worktrees],
          pullRequests: [...inputs.pullRequests],
          agents: [...inputs.agents],
          state,
          stateInfo: taskStateInfo(state),
          repositories: [...new Set(inputs.worktrees.map((worktree) => this.repositoryName(worktree.repository)))],
          machines: [...machines].map(([id, machine]) => ({ id, name: machine })),
          lastActivityAt: times.length === 0 ? undefined : Math.max(...times),
          openPullRequests: openObserved(inputs.pullRequests),
          unobservedPullRequests: inputs.pullRequests.filter((pullRequest) => !isObserved(pullRequest)).length,
        }
      })
    })
  }

  taskNamed(name: string): TaskView | undefined {
    return this.tasks.find((task) => task.name === name)
  }

  /** Home "Needs you": one row per task, its worst kind; blocked agents with no task are one row after them. */
  get needsYou(): NeedsYou {
    return this.memo('needsYou', () => {
      const items: NeedsYouItem[] = []
      for (const task of this.tasks) {
        const item = this.needsYouItem(task)
        if (item) items.push(item)
      }
      items.sort((a, b) => a.rank - b.rank || newestFirst(a.lastActivityAt, b.lastActivityAt) || a.task.localeCompare(b.task))
      const blocked = this.document.agents.filter((agent) => agent.activity === 'blocked' && this.tasksOfAgent(agent).length === 0)
      return {
        items,
        shown: items.slice(0, NEEDS_YOU_VISIBLE),
        more: Math.max(0, items.length - NEEDS_YOU_VISIBLE),
        moreLink: chipLink('tasks', 'needs-you'),
        withoutTask: blocked.length === 0 ? undefined : { count: blocked.length, agentIds: blocked.map((agent) => agent.id), link: chipLink('agents', 'blocked') },
      }
    })
  }

  private needsYouItem(task: TaskView): NeedsYouItem | undefined {
    const base = { task: task.name, lastActivityAt: task.lastActivityAt, link: selectionLink('tasks', task.name) }
    switch (task.state) {
      case 'at-risk': {
        const risky = interimAtRiskWorktrees(task.worktrees)
        const ahead = risky.reduce((total, worktree) => total + (worktree.ahead ?? 0), 0)
        const owner = risky.some((worktree) => worktree.owner_state === 'orphaned') ? 'its owner process is gone' : 'it has no running owner'
        const unpushed = ahead > 0 ? `${ahead} unpushed commit${ahead === 1 ? '' : 's'}` : 'a branch with no upstream'
        return {
          ...base,
          id: `work-at-risk:${task.name}`,
          kind: 'work-at-risk',
          rank: 0,
          reason: `${unpushed} and ${owner}`,
          worktrees: risky.map((worktree) => ({ id: worktree.id, branch: worktree.branch, repository: this.repositoryName(worktree.repository) })),
        }
      }
      case 'checks-failed': {
        const failed = task.openPullRequests.filter((pullRequest) => failedChecks(pullRequest) > 0)
        failed.sort((a, b) => oldestFirst(toTime(a.checked_at), toTime(b.checked_at)))
        const pullRequest = failed[0]
        return {
          ...base,
          id: `pr-checks-failed:${task.name}`,
          kind: 'pr-checks-failed',
          rank: 1,
          pullRequestId: pullRequest.id,
          repository: this.pullRequestRepository(pullRequest),
          number: pullRequest.number,
          failedCheck: pullRequest.failed_check,
          url: pullRequest.url,
        }
      }
      case 'blocked': {
        const blocked = task.agents.find((agent) => agent.activity === 'blocked')
        if (blocked) {
          return {
            ...base,
            id: `agent-blocked:${task.name}`,
            kind: 'agent-blocked',
            rank: 2,
            agentId: blocked.id,
            machine: blocked.machine,
            repository: blocked.repository === undefined ? undefined : this.repositoryName(blocked.repository),
            link: agentDetailLink(blocked.id),
          }
        }
        const run = blockingRuns(task.agents, this.now)[0]
        return { ...base, id: `run-failed:${task.name}`, kind: 'run-failed', rank: 2, agentId: run.id, machine: run.machine, runState: run.state, exitCode: run.exit_code, link: agentDetailLink(run.id) }
      }
      default:
        return this.softNeedsYouItem(task, base)
    }
  }

  /** The two kinds beyond the failures: a green pull request that cannot merge, and an agent that finished with work not pushed. */
  private softNeedsYouItem(task: TaskView, base: { task: string; lastActivityAt?: number; link: NeedsYouItem['link'] }): NeedsYouItem | undefined {
    const stuck = task.openPullRequests.find((pullRequest) => pullRequest.checks_green === true && pullRequest.mergeable !== undefined && MERGE_ATTENTION.has(pullRequest.mergeable))
    if (stuck) {
      return {
        ...base,
        id: `pr-needs-you:${task.name}`,
        kind: 'pr-needs-you',
        rank: 3,
        pullRequestId: stuck.id,
        repository: this.pullRequestRepository(stuck),
        number: stuck.number,
        reason: notReadyReasons(stuck).join(', '),
        url: stuck.url,
      }
    }
    const finished = task.agents.find((agent) => agent.activity === 'done' || agent.activity === 'idle')
    const unpushed = task.worktrees.some((worktree) => (worktree.ahead ?? 0) > 0 || worktree.has_upstream === false)
    if (finished && task.openPullRequests.length === 0 && task.state !== 'landed' && unpushed) {
      return { ...base, id: `agent-finished:${task.name}`, kind: 'agent-finished', rank: 4, agentId: finished.id, machine: finished.machine }
    }
    return undefined
  }

  private pullRequestRepository(pullRequest: PullRequest): string {
    return pullRequest.repository === undefined ? '' : this.repositoryName(pullRequest.repository)
  }

  /** Home "Ready to land": the tasks that are ready, and the tasks whose checks are not all in, muted. */
  get readyToLand(): ReadyToLand {
    return this.memo('readyToLand', () => {
      const ready: ReadyToLandRow[] = []
      const notReady: NotReadyRow[] = []
      for (const task of this.tasks) {
        if (task.state === 'ready') ready.push(this.readyRow(task))
        else if (task.state === 'not-ready') {
          // Only the tasks that wait on checks alone are listed, muted.
          const reasons = [...new Set(task.openPullRequests.flatMap(notReadyReasons))]
          if (reasons.every((reason) => reason === 'checks pending')) {
            notReady.push({ task: task.name, checkedAt: oldestCheck(task.openPullRequests), reasons, link: selectionLink('tasks', task.name) })
          }
        }
      }
      const newest = (a: { lastActivityAt?: number; task: string }, b: { lastActivityAt?: number; task: string }): number =>
        newestFirst(a.lastActivityAt, b.lastActivityAt) || a.task.localeCompare(b.task)
      ready.sort(newest)
      const activity = new Map(this.tasks.map((task) => [task.name, task.lastActivityAt]))
      notReady.sort((a, b) => newest({ task: a.task, lastActivityAt: activity.get(a.task) }, { task: b.task, lastActivityAt: activity.get(b.task) }))
      return { ready, notReady }
    })
  }

  private readyRow(task: TaskView): ReadyToLandRow {
    const pullRequests = task.openPullRequests.map((pullRequest): ReadyPullRequest => {
      const repository = pullRequest.repository === undefined ? undefined : this.repositoryName(pullRequest.repository)
      const command: CopyCommand | undefined = repository === undefined ? undefined : pullRequestLand(repository, pullRequest.number)
      return {
        id: pullRequest.id,
        repository,
        number: pullRequest.number,
        url: pullRequest.url,
        checksPassed: pullRequest.checks_passed ?? 0,
        checksTotal: pullRequest.checks_total ?? 0,
        checkedAt: toTime(pullRequest.checked_at),
        landCommand: command?.ok ? command.text : undefined,
      }
    })
    const repositories = [...new Set(pullRequests.flatMap((pullRequest) => (pullRequest.repository === undefined ? [] : [pullRequest.repository])))]
    return {
      task: task.name,
      repositories,
      pullRequests,
      checkedAt: oldestCheck(task.openPullRequests),
      checksPassed: pullRequests.reduce((total, pullRequest) => total + pullRequest.checksPassed, 0),
      checksTotal: pullRequests.reduce((total, pullRequest) => total + pullRequest.checksTotal, 0),
      lastActivityAt: task.lastActivityAt,
      link: selectionLink('tasks', task.name),
    }
  }

  /** Home "In flight": the running agents on every machine that publishes them. */
  get inFlight(): InFlightAgent[] {
    return this.memo('inFlight', () =>
      this.document.agents
        .filter(isRunning)
        .map((agent): InFlightAgent => ({
          agent,
          label: agentTitle(agent),
          task: this.tasksOfAgent(agent)[0],
          repository: agent.repository === undefined ? undefined : this.repositoryName(agent.repository),
          machine: agent.machine,
          machineId: agent.machine_id,
          remote: agent.route !== 'local',
          observedAt: agent.observed_at,
          startedAt: startedAt(agent),
          activity: agent.activity,
          controllable: agent.kind === 'run' && agent.route === 'local',
        }))
        .sort((a, b) => Number(a.remote) - Number(b.remote) || newestFirst(a.startedAt, b.startedAt) || a.agent.id.localeCompare(b.agent.id)),
    )
  }

  /** Home "Resume": the last five tasks by last activity. */
  get resume(): TaskView[] {
    return this.memo('resume', () =>
      [...this.tasks].sort((a, b) => newestFirst(a.lastActivityAt, b.lastActivityAt) || a.name.localeCompare(b.name)).slice(0, RESUME_COUNT),
    )
  }

  /** Home "Cleanup": the indicative safe and look counts, the sets behind them, and the age bars. */
  get cleanup(): Cleanup {
    return this.memo('cleanup', () => {
      const landed = new Set(this.tasks.filter((task) => task.state === 'landed').map((task) => task.name))
      const safeIds = new Set<string>()
      const lookIds = new Set<string>()
      const counts = AGE_TERMS.map(() => 0)
      let unknownAge = 0
      for (const worktree of this.document.worktrees) {
        const activity = toTime(worktree.last_activity_at)
        if (landed.has(worktree.task) && worktree.ahead === 0 && worktree.owner_state !== 'active') safeIds.add(worktree.id)
        else if (worktree.owner_state === 'orphaned' || worktree.owner_state === 'unknown' || idleOver30Days(activity, this.now)) lookIds.add(worktree.id)
        if (activity === undefined) unknownAge++
        else {
          counts[AGE_TERMS.indexOf(ageTermOf(wholeDays(activity, this.now)))]++
        }
      }
      return {
        safeCount: safeIds.size,
        lookCount: lookIds.size,
        safeIds,
        lookIds,
        indicative: true,
        reviewLink: chipLink('worktrees', 'safe'),
        bars: AGE_TERMS.map((term, index) => ({ term, label: AGE_LABELS[term], count: counts[index], link: ageLink('worktrees', term) })),
        unknownAge,
      }
    })
  }

  /** The machines with their state, and the newest WB version in the fleet. */
  get machines(): MachineView[] {
    return this.memo('machines', () => {
      const newest = this.newestVersion
      const running = new Map<string, number>()
      for (const agent of this.document.agents) if (isRunning(agent)) running.set(agent.machine_id, (running.get(agent.machine_id) ?? 0) + 1)
      return this.document.machines.map((machine): MachineView => {
        const observed = toTime(machine.observed_at)
        const version = parseVersion(machine.wb_version)
        const boot = toTime(machine.boot_time)
        const state: MachineStateId = machine.route !== 'cached' ? 'live' : isStale(machine.route, machine.observed_at, this.now) ? 'stale' : 'cached'
        return {
          machine,
          local: machine.route === 'local',
          state,
          ageMs: observed === undefined ? undefined : Math.max(0, this.now - observed),
          outdated: newest !== undefined && version !== undefined && compareVersions(version, newest) < 0,
          runningAgents: running.get(machine.id) ?? 0,
          uptimeMs: boot === undefined ? undefined : Math.max(0, this.now - boot),
        }
      })
    })
  }

  private get newestVersion(): number[] | undefined {
    let newest: number[] | undefined
    for (const machine of this.document.machines) {
      const version = parseVersion(machine.wb_version)
      if (version !== undefined && (newest === undefined || compareVersions(version, newest) > 0)) newest = version
    }
    return newest
  }

  /** Home "Fleet health": shown only when `ok` is false. */
  get health(): FleetHealth {
    return this.memo('health', () => {
      const staleMachines: HealthItem[] = []
      const olderWb: HealthItem[] = []
      const remoteErrors: HealthItem[] = []
      for (const view of this.machines) {
        const machine = view.machine
        const base = { machine: machine.machine, machineId: machine.id, link: machineLink('machines', machine.id) }
        if (view.state === 'stale') {
          staleMachines.push({ ...base, text: `${machine.machine} has not published for over 24 hours`, command: healthCommand(remotePublish(), machine.machine), link: chipLink('machines', 'stale') })
        }
        if (view.outdated) {
          olderWb.push({ ...base, text: `${machine.machine} runs an older WB (${machine.wb_version})`, command: healthCommand(selfUpdate(), machine.machine), link: chipLink('machines', 'outdated') })
        }
        if (machine.remote_error !== undefined) {
          remoteErrors.push({ ...base, text: `${machine.machine}: ${remoteErrorText(machine.remote_error)}`, command: healthCommand(remoteFix(machine.remote_error, this.options.sshRoute?.(machine)), machine.machine) })
        }
      }
      const scanErrors: ScanErrorItem[] = this.repositories
        .filter((repository) => repository.errors.length > 0)
        .map((repository) => {
          const command = fleetStatus(repository.slug)
          return { repository: repository.slug, command: command.ok ? { text: command.text } : { reason: command.reason }, link: chipLink('repositories', 'errors') }
        })
      return { ok: staleMachines.length + olderWb.length + remoteErrors.length + scanErrors.length === 0, staleMachines, olderWb, remoteErrors, scanErrors }
    })
  }

  /** The two Home charts' numbers; undefined when the document has no throughput block. */
  get throughput(): ThroughputSeries | undefined {
    return this.memo('throughput', () => {
      const block = this.document.throughput
      if (block === undefined) return undefined
      const landed = new Map(block.per_day.map((day) => [day.date, day.landed]))
      const perDay: { date: string; landed: number }[] = []
      for (let offset = block.window_days - 1; offset >= 0; offset--) {
        const date = isoDay(new Date(this.now - offset * 86_400_000))
        perDay.push({ date, landed: landed.get(date) ?? 0 })
      }
      return {
        windowDays: block.window_days,
        perDay,
        totalLanded: perDay.reduce((total, day) => total + day.landed, 0),
        maxLanded: Math.max(0, ...perDay.map((day) => day.landed)),
        slowest: [...block.slowest].sort((a, b) => b.duration_seconds - a.duration_seconds).slice(0, 5).map((entry) => ({ task: entry.task, durationSeconds: entry.duration_seconds, landedAt: entry.landed_at })),
      }
    })
  }

  // ---- list rows, one set per page ----

  /** The tasks that have a "Needs you" row: the same set as the chip `needs-you`. */
  private get needsYouTasks(): ReadonlySet<string> {
    return new Set(this.needsYou.items.map((item) => item.task))
  }

  get taskRows(): ListRow<TaskView>[] {
    return this.memo('rows:tasks', () => taskRows(this.tasks, this.needsYouTasks))
  }

  get repositoryRows(): ListRow<MergedRepository>[] {
    return this.memo('rows:repositories', () => repositoryRows(this.repositories))
  }

  get worktreeRows(): ListRow<Worktree>[] {
    return this.memo('rows:worktrees', () => {
      const withPullRequest = new Set<string>()
      for (const pullRequest of this.document.pull_requests) {
        if (pullRequest.worktree !== undefined) withPullRequest.add(pullRequest.worktree)
        else {
          const task = this.taskOfPullRequest(pullRequest)
          for (const worktree of this.document.worktrees) {
            if (worktree.task === task && worktree.repository === pullRequest.repository && worktree.branch === pullRequest.branch) withPullRequest.add(worktree.id)
          }
        }
      }
      const context: WorktreeRowContext = {
        repositoryName: (id) => this.repositoryName(id),
        withPullRequest,
        safeIds: this.cleanup.safeIds,
        lookIds: this.cleanup.lookIds,
      }
      return worktreeRows(this.document.worktrees, context)
    })
  }

  get agentRows(): ListRow<Agent>[] {
    return this.memo('rows:agents', () => {
      const context: AgentRowContext = { taskOf: (agent) => this.tasksOfAgent(agent)[0], repositoryName: (id) => this.repositoryName(id) }
      return agentRows(this.document.agents, context)
    })
  }

  get machineRows(): ListRow<MachineView>[] {
    return this.memo('rows:machines', () => machineRows(this.machines, (machine) => versionKey(machine.wb_version)))
  }
}

function isoDay(date: Date): string {
  return date.toISOString().slice(0, 10)
}

function healthCommand(command: CopyCommand, machine: string): HealthItem['command'] {
  return command.ok ? { text: command.text, label: `run on ${machine}` } : { reason: command.reason }
}

/** A `remote_error` in words; a code this page does not know is an unknown error. */
export function remoteErrorText(code: string): string {
  switch (code) {
    case 'http_unavailable':
      return 'cannot be read over HTTP (connection error, timeout, 404, 429, 5xx or a redirect)'
    case 'http_auth_failed':
      return 'the HTTP read was refused (401 or 403) or has no credential'
    case 'ssh_unavailable':
      return 'cannot reach it over ssh (no ssh here, or the host is unreachable)'
    case 'auth_failed':
      return 'ssh login was refused'
    case 'timeout':
      return 'its export timed out'
    case 'wb_missing':
      return 'wb is not found on it'
    case 'wb_too_old':
      return 'its wb is too old to export'
    case 'daemon_not_running':
      return 'its daemon is not running'
    case 'export_refused':
      return 'its daemon refuses anonymous reads'
    case 'bad_payload':
      return 'its export was refused as invalid'
  }
  return 'unknown error'
}

/**
 * The command that fixes (or lets the operator investigate) a `remote_error`:
 * enrolling for a refused HTTP read, starting the daemon, updating wb, and for
 * the others the export to try, through ssh when the machine has an SSH route.
 */
export function remoteFix(code: string, ssh: SshRoute | undefined): CopyCommand {
  if (code === 'http_auth_failed') return remoteEnroll()
  if (code === 'daemon_not_running') return daemonStart()
  if (code === 'wb_too_old') return selfUpdate()
  return cockpitExport(ssh === undefined ? {} : { ssh })
}

/** Models by document identity: the same document gives the same model, so nothing is derived twice. */
export class FleetModels {
  private readonly models = new WeakMap<FleetDocument, FleetModel>()

  constructor(private readonly options: ModelOptions = {}) {}

  forDocument(document: FleetDocument): FleetModel {
    let model = this.models.get(document)
    if (model === undefined) {
      model = new FleetModel(document, this.options)
      this.models.set(document, model)
    }
    return model
  }
}
