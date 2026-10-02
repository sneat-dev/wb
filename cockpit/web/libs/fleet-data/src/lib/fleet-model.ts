// The memoised view model (REQ:derived-collections-memoised). One FleetModel
// per fleet document: each derived collection is computed once, on first use,
// and kept for the document's identity, so a poll that returns 304 or an
// identical document recomputes nothing and a render reads the same arrays.

import { Agent, FleetDocument, Machine, MachineMetrics, MachineRoute, PullRequest, Repository, Worktree, anyAgentsTruncated, isRunning } from './fleet.types'
import { CommandTarget, CopyCommand, SshRoute, commandTarget, pullRequestLand, sshRouteOf } from './command-core'
import { agentTitle } from './fleet-view'
import { isStale, repositorySlug } from './repository-identity'
import {
  TaskInputs,
  TaskStateId,
  blockingRuns,
  failedChecks,
  hasUnpushedWork,
  interimAtRiskWorktrees,
  isBlocked,
  isOpenPullRequest,
  isObserved,
  notReadyReasons,
  openObserved,
  startedAt,
  stateSourceOf,
  taskState,
  taskStateInfo,
} from './task-state'
import {
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
  TaskView,
} from './view-types'
import { agentDetailLink, chipLink, selectionLink } from './vocabulary'
import { webAddress } from './web-address'

/** The derivations that are counted, so a test can prove each ran once per document. */
export type Derivation =
  | 'index'
  | 'repositories'
  | 'tasks'
  | 'taskMap'
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
  /** The clock used when a model is made without a time. */
  now?: () => number
  /** Called each time a derivation actually runs. */
  onDerive?: (derivation: Derivation) => void
  /** Called when a derivation throws; it is not tried again for the model, which shows its empty value. */
  onError?: (derivation: Derivation, error: unknown) => void
  /** The SSH route of a machine, when it has one, for the commands of Fleet health. */
  sshRoute?: (machine: Machine) => SshRoute | undefined
  /** The routes of the session response (owner-only); with none, commands for other machines are labelled "run on <machine>". */
  machineRoutes?: readonly MachineRoute[]
}

const EMPTY_LINK = { path: '/', query: {} }
const EMPTY_NEEDS_YOU: NeedsYou = { items: [], shown: [], more: 0, moreLink: EMPTY_LINK }

/** The clock bucket of derivations that depend on time: ages, expiries and staleness are re-derived once a minute. */
export const CLOCK_BUCKET_MS = 60_000

export { agentTitle }

/** How many tasks Resume lists. */
export const RESUME_COUNT = 5

/**
 * "Needs you" is a signal, not a debt counter: a task at risk, or whose agent
 * finished with work not pushed, is listed only when its last activity is this
 * recent. Older ones are counted by the Cleanup line's "need a look".
 */
export const NEEDS_YOU_WINDOW_DAYS = 14
export const NEEDS_YOU_WINDOW_MS = NEEDS_YOU_WINDOW_DAYS * 86_400_000

/** The largest number a tab badge shows as it is; above it the badge reads "99+". */
export const BADGE_CAP = 99

/** A badge count as shown: the number, or "99+" above `BADGE_CAP`. */
export function badgeLabel(count: number): string {
  return count > BADGE_CAP ? `${BADGE_CAP}+` : String(count)
}

/** A badge count that is a least ("5+"): some of what it counts may have been left out. */
export function badgeLabelAtLeast(count: number): string {
  return count > BADGE_CAP ? `${BADGE_CAP}+` : `${count}+`
}

/** A count in words: "5", or "at least 5" when some of what it counts may have been left out. */
export function countWords(count: number, atLeast: boolean): string {
  return atLeast ? `at least ${count}` : String(count)
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

/** The state of the owners of the at-risk worktrees in words. An at-risk worktree's owner is idle, orphaned or not recorded (`interimAtRiskWorktrees`). */
function ownerWords(risky: readonly Worktree[]): string {
  const states = new Set(risky.map((worktree) => worktree.owner_state))
  if (states.has('orphaned')) return 'its owner process is gone'
  if (states.has('idle')) return 'it has no running owner'
  return 'no owner process is recorded'
}

/** The sum of the counts that are reported; undefined when none is. */
function sumReported(values: (number | undefined)[]): number | undefined {
  const known = values.filter((value): value is number => value !== undefined)
  return known.length === 0 ? undefined : known.reduce((total, value) => total + value, 0)
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

export function compareVersions(a: number[], b: number[]): number {
  for (let index = 0; index < Math.max(a.length, b.length); index++) {
    const difference = (a[index] ?? 0) - (b[index] ?? 0)
    if (difference !== 0) return difference
  }
  return 0
}

/** How old a sample may be and still say how loaded a machine is now: older is "load unknown", never "free". */
export const CURRENT_SAMPLE_MS = 5 * 60_000

/**
 * The latest sample's verdict: free below 70 percent CPU and 80 percent memory, busy otherwise. A sample that is not
 * current at `now` (older than `CURRENT_SAMPLE_MS`, or with no time) says nothing about the machine now, so it is
 * `not-reported` (shown "load unknown") and marked `stale`, with the time it was taken for its age to be said.
 */
export function machineLoad(metrics: MachineMetrics | undefined, now: number): MachineLoad {
  const sample = metrics?.samples[metrics.samples.length - 1]
  const reported = (value: number | undefined): value is number => typeof value === 'number' && Number.isFinite(value)
  // `unknown`, never `free` (and not a guess of `busy`), without the CPU or the memory of the sample: the first sample
  // after a daemon start has no `cpu_percent`.
  if (metrics === undefined || sample === undefined || !reported(sample.cpu_percent) || !reported(sample.memory_used_bytes) || !reported(sample.memory_total_bytes) || sample.memory_total_bytes <= 0) {
    return { state: 'not-reported', route: metrics?.route ?? 'none' }
  }
  const sampledAt = toTime(sample.sampled_at)
  if (sampledAt === undefined || now - sampledAt > CURRENT_SAMPLE_MS) return { state: 'not-reported', route: metrics.route, stale: true, sampledAt }
  const memoryPercent = (sample.memory_used_bytes / sample.memory_total_bytes) * 100
  return {
    state: sample.cpu_percent < 70 && memoryPercent < 80 ? 'free' : 'busy',
    route: metrics.route,
    sampledAt,
    cpuPercent: sample.cpu_percent,
    memoryPercent,
  }
}

export class FleetModel {
  readonly now: number
  private readonly cache = new Map<string, unknown>()
  private readonly failed: Derivation[] = []

  constructor(
    readonly document: FleetDocument,
    private readonly options: ModelOptions = {},
    now?: number,
  ) {
    this.now = now ?? (options.now ?? Date.now)()
  }

  /** The derivations that threw: each shows its empty value and is not tried again for this model. */
  get failedDerivations(): readonly Derivation[] {
    return this.failed
  }

  /**
   * Computes a derivation once for this model (counted by `onDerive`, contained by
   * `onError`). Public for the lazy entry points, which memoise their own views
   * here: `/list` the row sets, `/panel` nothing (its views are cheap).
   */
  memo<T>(name: Derivation, fallback: T, derive: () => T): T {
    if (!this.cache.has(name)) {
      this.options.onDerive?.(name)
      try {
        this.cache.set(name, derive())
      } catch (error) {
        // A throwing derivation must not throw again on every render.
        this.failed.push(name)
        this.options.onError?.(name, error)
        this.cache.set(name, fallback)
      }
    }
    return this.cache.get(name) as T
  }

  /** The routes of the session, for the commands of entities on other machines. */
  get machineRoutes(): readonly MachineRoute[] | undefined {
    return this.options.machineRoutes
  }

  /** The SSH route of a machine, when it has one: the options' own, else the session's (owner-only). */
  sshRouteFor(machine: Machine): SshRoute | undefined {
    return this.options.sshRoute?.(machine) ?? sshRouteOf(this.options.machineRoutes, machine.id)
  }

  /** Where a command for an entry runs: here, through the machine's SSH route, or labelled "run on <machine>". */
  targetOf(entry: { route: string; machine: string; machine_id: string }): CommandTarget {
    return commandTarget(entry, this.options.machineRoutes)
  }

  // ---- lookups ----

  private get index(): {
    repositoryById: Map<string, Repository>
    worktreeById: Map<string, Worktree>
    pullRequestById: Map<string, PullRequest>
    agentById: Map<string, Agent>
    machineById: Map<string, Machine>
    taskOfPullRequest: Map<string, string>
    worktreePullRequests: Map<string, PullRequest[]>
  } {
    return this.memo(
      'index',
      {
        repositoryById: new Map<string, Repository>(),
        worktreeById: new Map<string, Worktree>(),
        pullRequestById: new Map<string, PullRequest>(),
        agentById: new Map<string, Agent>(),
        machineById: new Map<string, Machine>(),
        taskOfPullRequest: new Map<string, string>(),
        worktreePullRequests: new Map<string, PullRequest[]>(),
      },
      () => {
        const repositoryById = new Map(this.document.repositories.map((repository) => [repository.id, repository]))
        const worktreeById = new Map(this.document.worktrees.map((worktree) => [worktree.id, worktree]))
        const byBranch = new Map<string, Worktree[]>()
        for (const worktree of this.document.worktrees) {
          const key = `${worktree.repository}\u0000${worktree.branch}`
          byBranch.set(key, [...(byBranch.get(key) ?? []), worktree])
        }
        // The one worktree-to-pull-request join: a pull request that names a worktree of the document belongs to
        // that worktree; one that does not belongs to the worktrees of its repository entry and branch.
        const taskOfPullRequest = new Map<string, string>()
        const worktreePullRequests = new Map<string, PullRequest[]>()
        for (const pullRequest of this.document.pull_requests) {
          const named = pullRequest.worktree === undefined ? undefined : worktreeById.get(pullRequest.worktree)
          const joined = named !== undefined ? [named] : pullRequest.repository !== undefined && pullRequest.branch !== undefined ? (byBranch.get(`${pullRequest.repository}\u0000${pullRequest.branch}`) ?? []) : []
          if (joined.length > 0) taskOfPullRequest.set(pullRequest.id, joined[0].task)
          for (const worktree of joined) worktreePullRequests.set(worktree.id, [...(worktreePullRequests.get(worktree.id) ?? []), pullRequest])
        }
        const pullRequestById = new Map(this.document.pull_requests.map((pullRequest) => [pullRequest.id, pullRequest]))
        const agentById = new Map(this.document.agents.map((agent) => [agent.id, agent]))
        const machineById = new Map(this.document.machines.map((machine) => [machine.id, machine]))
        return { repositoryById, worktreeById, pullRequestById, agentById, machineById, taskOfPullRequest, worktreePullRequests }
      },
    )
  }

  /**
   * The pull requests of each worktree, by worktree id (a worktree with none has no entry): the one join the
   * `pr` chip of Worktrees, the list rows and the worktree panel all read. A pull request that names a worktree
   * of the document joins that worktree only; one that does not joins the worktrees of its repository entry and
   * branch. In document order.
   */
  get worktreePullRequests(): ReadonlyMap<string, readonly PullRequest[]> {
    return this.index.worktreePullRequests
  }

  worktreeById(id: string): Worktree | undefined {
    return this.index.worktreeById.get(id)
  }

  agentById(id: string): Agent | undefined {
    return this.index.agentById.get(id)
  }

  pullRequestById(id: string): PullRequest | undefined {
    return this.index.pullRequestById.get(id)
  }

  machineById(id: string): Machine | undefined {
    return this.index.machineById.get(id)
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

  /** One task per name, with its state. */
  get tasks(): TaskView[] {
    return this.memo('tasks', [], () => {
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
        const reporters = new Map([...inputs.worktrees, ...inputs.pullRequests, ...inputs.agents].map((entry) => [entry.machine_id, entry.machine]))
        return {
          name,
          worktrees: [...inputs.worktrees],
          pullRequests: [...inputs.pullRequests],
          agents: [...inputs.agents],
          state,
          stateInfo: taskStateInfo(state),
          stateSource: stateSourceOf(inputs),
          reportedBy: [...reporters].map(([id, name]) => ({ id, name })),
          repositories: [...new Set(inputs.worktrees.map((worktree) => this.repositoryName(worktree.repository)))],
          machines: [...machines].map(([id, machine]) => ({ id, name: machine })),
          lastActivityAt: times.length === 0 ? undefined : Math.max(...times),
          openPullRequests: openObserved(inputs.pullRequests),
          unobservedPullRequests: inputs.pullRequests.filter((pullRequest) => !isObserved(pullRequest)).length,
        }
      })
    })
  }

  /** Tasks by name. */
  get taskMap(): Map<string, TaskView> {
    return this.memo('taskMap', new Map<string, TaskView>(), () => new Map(this.tasks.map((task) => [task.name, task])))
  }

  taskNamed(name: string): TaskView | undefined {
    return this.taskMap.get(name)
  }

  /** The Home badge: the number of tasks that need the operator (the whole number; `homeBadgeLabel` is what is shown). */
  get homeBadge(): number {
    return this.needsYou.items.length
  }

  /** The Home badge as shown: the number, or "99+" above 99. */
  get homeBadgeLabel(): string {
    return badgeLabel(this.homeBadge)
  }

  /** The task's last activity is within `NEEDS_YOU_WINDOW_DAYS`; no recorded activity is not recent. */
  isRecent(task: TaskView): boolean {
    return task.lastActivityAt !== undefined && this.now - task.lastActivityAt <= NEEDS_YOU_WINDOW_MS
  }

  /** Whether any machine's agents were cut: the counts of agents are then "at least" (`countWords`). */
  get agentsCut(): boolean {
    return anyAgentsTruncated(this.document)
  }

  /** The Agents tab badge: the running agents on every machine. */
  get runningAgentCount(): number {
    return this.inFlight.length
  }

  /** Home "Needs you": one row per task, its worst kind; blocked agents with no task are one row after them. */
  get needsYou(): NeedsYou {
    return this.memo('needsYou', EMPTY_NEEDS_YOU, () => {
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
    const base = { task: task.name, stateSource: task.stateSource, lastActivityAt: task.lastActivityAt, link: selectionLink('tasks', task.name) }
    // Older at-risk work is a cleanup matter, counted by Cleanup's "need a look"; what else the task needs still counts.
    const state = task.state === 'at-risk' && !this.isRecent(task) ? this.stateBelowAtRisk(task) : task.state
    switch (state) {
      case 'at-risk': {
        const risky = interimAtRiskWorktrees(task.worktrees)
        const ahead = risky.reduce((total, worktree) => total + (worktree.ahead ?? 0), 0)
        const owner = ownerWords(risky)
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
          url: webAddress(pullRequest.url),
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
        const run = blockingRuns(task.agents, this.now, task.stateSource === 'local')[0]
        return { ...base, id: `run-failed:${task.name}`, kind: 'run-failed', rank: 2, agentId: run.id, machine: run.machine, runState: run.state, exitCode: run.exit_code, link: agentDetailLink(run.id) }
      }
      default:
        return this.softNeedsYouItem(task, base)
    }
  }

  /** The state an at-risk task would have without the at-risk row: `checks-failed`, `blocked`, or `idle` for the kinds beyond the failures. */
  private stateBelowAtRisk(task: TaskView): TaskStateId {
    if (task.openPullRequests.some((pullRequest) => failedChecks(pullRequest) > 0)) return 'checks-failed'
    return isBlocked(task.agents, this.now, task.stateSource === 'local') ? 'blocked' : 'idle'
  }

  /** The two kinds beyond the failures: a green pull request that cannot merge, and an agent that finished (within the window) with work not pushed. */
  private softNeedsYouItem(task: TaskView, base: { task: string; stateSource: TaskView['stateSource']; lastActivityAt?: number; link: NeedsYouItem['link'] }): NeedsYouItem | undefined {
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
        url: webAddress(stuck.url),
      }
    }
    const finished = task.agents.find((agent) => agent.activity === 'done' || agent.activity === 'idle')
    const unpushed = task.worktrees.some(hasUnpushedWork)
    if (finished && task.openPullRequests.length === 0 && task.state !== 'landed' && unpushed && this.isRecent(task)) {
      return { ...base, id: `agent-finished:${task.name}`, kind: 'agent-finished', rank: 4, agentId: finished.id, machine: finished.machine }
    }
    return undefined
  }

  private pullRequestRepository(pullRequest: PullRequest): string {
    return pullRequest.repository === undefined ? '' : this.repositoryName(pullRequest.repository)
  }

  /** Home "Ready to land": the tasks that are ready, and the tasks whose checks are not all in, muted. */
  get readyToLand(): ReadyToLand {
    return this.memo('readyToLand', { ready: [], notReady: [] }, () => {
      const ready: ReadyToLandRow[] = []
      const notReady: NotReadyRow[] = []
      for (const task of this.tasks) {
        if (task.state === 'ready') ready.push(this.readyRow(task))
        else if (task.state === 'not-ready') {
          // Only the tasks that wait on checks alone are listed, muted.
          const reasons = [...new Set(task.pullRequests.filter(isOpenPullRequest).flatMap(notReadyReasons))]
          // Never vacuously: a task with no reason at all is not shown as waiting.
          if (reasons.length > 0 && reasons.every((reason) => reason === 'checks pending')) {
            notReady.push({ task: task.name, stateSource: task.stateSource, checkedAt: oldestCheck(task.openPullRequests), reasons, link: selectionLink('tasks', task.name) })
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
      // A task decided by another machine's report offers no land action from here.
      const command: CopyCommand | undefined = repository === undefined || task.stateSource === 'remote' ? undefined : pullRequestLand(repository, pullRequest.number, this.targetOf(pullRequest))
      return {
        id: pullRequest.id,
        repository,
        number: pullRequest.number,
        url: webAddress(pullRequest.url),
        machine: pullRequest.machine,
        machineId: pullRequest.machine_id,
        remote: pullRequest.route !== 'local',
        // A count the daemon did not report stays absent: never 0 of 0.
        checksPassed: pullRequest.checks_passed,
        checksTotal: pullRequest.checks_total,
        checkedAt: toTime(pullRequest.checked_at),
        landCommand: command?.ok ? command.text : undefined,
        landLabel: command?.ok ? command.label : undefined,
      }
    })
    const repositories = [...new Set(pullRequests.flatMap((pullRequest) => (pullRequest.repository === undefined ? [] : [pullRequest.repository])))]
    return {
      task: task.name,
      stateSource: task.stateSource,
      reportedBy: task.reportedBy,
      repositories,
      pullRequests,
      checkedAt: oldestCheck(task.openPullRequests),
      checksPassed: sumReported(pullRequests.map((pullRequest) => pullRequest.checksPassed)),
      checksTotal: sumReported(pullRequests.map((pullRequest) => pullRequest.checksTotal)),
      unobservedPullRequests: task.unobservedPullRequests,
      lastActivityAt: task.lastActivityAt,
      link: selectionLink('tasks', task.name),
    }
  }

  /** Home "In flight": the running agents on every machine that publishes them. */
  get inFlight(): InFlightAgent[] {
    return this.memo('inFlight', [], () =>
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
    return this.memo('resume', [], () =>
      [...this.tasks].sort((a, b) => newestFirst(a.lastActivityAt, b.lastActivityAt) || a.name.localeCompare(b.name)).slice(0, RESUME_COUNT),
    )
  }

  /** The machines with their state, and the newest WB version in the fleet. */
  get machines(): MachineView[] {
    return this.memo('machines', [], () => {
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

}

/**
 * Models by document identity and clock bucket: the same document within the
 * same minute (and with the same session routes) gives the same model, so a
 * poll that changes nothing derives nothing; a later minute re-derives what
 * depends on the clock.
 */
export class FleetModels {
  private readonly models = new WeakMap<FleetDocument, { bucket: number; routes: readonly MachineRoute[] | undefined; model: FleetModel }>()

  constructor(private readonly options: ModelOptions = {}) {}

  forDocument(document: FleetDocument, now?: number, routes?: readonly MachineRoute[]): FleetModel {
    const time = now ?? (this.options.now ?? Date.now)()
    const bucket = Math.floor(time / CLOCK_BUCKET_MS)
    const held = this.models.get(document)
    if (held !== undefined && held.bucket === bucket && held.routes === routes) return held.model
    const model = new FleetModel(document, routes === undefined ? this.options : { ...this.options, machineRoutes: routes }, time)
    this.models.set(document, { bucket, routes, model })
    return model
  }
}
