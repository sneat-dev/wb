// A task's computed state (REQ:task-state) as a pure function of the fleet
// document. The rows of the decision table are evaluated top to bottom and the
// first match wins, which makes the order worst first. A field the daemon
// omitted is "not reported" and is never guessed: a pull request that carries
// no `checked_at` has never been observed and takes no part in rows 2, 4 and 5.

import { Agent, PullRequest, Worktree } from './fleet.types'

export type TaskStateId =
  | 'at-risk'
  | 'checks-failed'
  | 'blocked'
  | 'ready'
  | 'not-ready'
  | 'working'
  | 'landed'
  | 'idle'
  | 'not-reported'

export interface TaskStateInfo {
  id: TaskStateId
  label: string
  /** 0 is the worst; sorting by rank puts the worst first. */
  rank: number
}

/** The table, worst first. */
export const TASK_STATES: readonly TaskStateInfo[] = [
  { id: 'at-risk', label: 'at risk', rank: 0 },
  { id: 'checks-failed', label: 'checks failed', rank: 1 },
  { id: 'blocked', label: 'blocked', rank: 2 },
  { id: 'ready', label: 'ready to land', rank: 3 },
  { id: 'not-ready', label: 'not ready', rank: 4 },
  { id: 'working', label: 'working', rank: 5 },
  { id: 'landed', label: 'landed', rank: 6 },
  { id: 'idle', label: 'idle', rank: 7 },
  { id: 'not-reported', label: 'state not reported', rank: 8 },
]

export const TASK_STATE_IDS: readonly TaskStateId[] = TASK_STATES.map((info) => info.id)

export function taskStateInfo(id: TaskStateId): TaskStateInfo {
  return TASK_STATES[TASK_STATE_IDS.indexOf(id)]
}

/** The worktrees, pull requests and agents of one task. */
export interface TaskInputs {
  worktrees: readonly Worktree[]
  pullRequests: readonly PullRequest[]
  agents: readonly Agent[]
}

/** A failed run stops making a task blocked after this long. */
export const BLOCKED_RUN_EXPIRY_MS = 24 * 60 * 60 * 1000

const MERGEABLE_READY = new Set(['clean', 'has_hooks'])

/** An open pull request: `open`, or an open draft. An unreported state is never open. */
export function isOpenPullRequest(pullRequest: PullRequest): boolean {
  return pullRequest.state === 'open' || pullRequest.state === 'draft'
}

/** The failed checks of a pull request: none when not reported. */
export function failedChecks(pullRequest: PullRequest): number {
  return pullRequest.checks_failed ?? 0
}

/** The pending checks of a pull request: none when not reported. */
export function pendingChecks(pullRequest: PullRequest): number {
  return pullRequest.checks_pending ?? 0
}

/** Whether an observation of the pull request ever succeeded (it carries `checked_at`). */
export function isObserved(pullRequest: PullRequest): boolean {
  return pullRequest.checked_at !== undefined
}

/** The open pull requests that have been observed: the only ones rows 2, 4 and 5 consider. */
export function openObserved(pullRequests: readonly PullRequest[]): PullRequest[] {
  return pullRequests.filter((pullRequest) => isObserved(pullRequest) && isOpenPullRequest(pullRequest))
}

/**
 * The interim at-risk worktrees of row 1, until the work-loss-risk read model
 * replaces the rule: on this machine (a local entry), an `owner_state` that is
 * reported and not `active`, and commits ahead or no upstream. A value this page does
 * not know is "not reported", never "not active" (it must not put a task at risk).
 */
/** The owner states that say nobody is working in the worktree. */
const NOT_ACTIVE: readonly string[] = ['idle', 'orphaned', 'unknown']

export function interimAtRiskWorktrees(worktrees: readonly Worktree[]): Worktree[] {
  return worktrees.filter(
    (worktree) =>
      worktree.route === 'local' &&
      NOT_ACTIVE.includes(worktree.owner_state as string) &&
      hasUnpushedWork(worktree),
  )
}

/** When the agent started, in milliseconds; undefined when not reported. */
export function startedAt(agent: Agent): number | undefined {
  const time = agent.started_at ? Date.parse(agent.started_at) : Number.NaN
  return Number.isNaN(time) ? undefined : time
}

function endedAt(agent: Agent): number | undefined {
  const time = agent.finished_at ? Date.parse(agent.finished_at) : Number.NaN
  return Number.isNaN(time) ? startedAt(agent) : time
}

export function isRun(agent: Agent): boolean {
  return agent.kind === 'run'
}

/**
 * The dispatched runs that make a task blocked: ended `failed` or `timeout`,
 * with no agent of the task (a run or a session) that started later, and not
 * ended 24 hours ago or more (from `finished_at`, else `started_at`). Without
 * start times the order is not known and is not guessed (no later agent), and a
 * run with no time at all is not reported as blocking. When the task has an
 * entry of this machine (`local`, by default an agent of this machine), a later agent of another machine does not
 * clear a failed run of this machine: a remote entry may worsen a local task's state, never lift it (REQ:task-state
 * trust rule). A failed run that another machine reported can still be superseded by a later agent.
 */
export function blockingRuns(agents: readonly Agent[], now: number, local: boolean = agents.some(isLocal)): Agent[] {
  return agents.filter((run) => {
    if (!isRun(run) || (run.state !== 'failed' && run.state !== 'timeout')) return false
    const started = startedAt(run)
    const supersedes = (other: Agent): boolean => other !== run && (!local || isLocal(other) || !isLocal(run)) && (startedAt(other) ?? -Infinity) > (started as number)
    if (started !== undefined && agents.some(supersedes)) return false
    // A run with no time at all is not reported as blocking.
    const ended = endedAt(run)
    return ended !== undefined && now - ended < BLOCKED_RUN_EXPIRY_MS
  })
}

/** Row 3: an agent whose `activity` is `blocked`, or a blocking run. */
export function isBlocked(agents: readonly Agent[], now: number, local: boolean = agents.some(isLocal)): boolean {
  return agents.some((agent) => agent.activity === 'blocked') || blockingRuns(agents, now, local).length > 0
}

/** The reasons a pull request can be not ready, in words (REQ:task-state). */
export const NOT_READY_REASONS = [
  'draft',
  'checks failed',
  'checks pending',
  'review',
  'checks not reported',
  'not mergeable',
  'behind',
  'unstable',
  'merge state not reported',
  'pull request not yet checked',
] as const

/**
 * Why a pull request is not ready to land, in words, from the list of
 * NOT_READY_REASONS; empty exactly when it is ready. A pull request never
 * observed is "pull request not yet checked". `review` is only said for a
 * verdict that is reported as not green, or a merge state of `blocked`; an
 * absent verdict or merge state says so instead of guessing.
 */
export function notReadyReasons(pullRequest: PullRequest): string[] {
  if (!isObserved(pullRequest)) return ['pull request not yet checked']
  const reasons = new Set<string>()
  if (pullRequest.state === 'draft') reasons.add('draft')
  if (failedChecks(pullRequest) > 0) reasons.add('checks failed')
  else if (pendingChecks(pullRequest) > 0) reasons.add('checks pending')
  else if (pullRequest.checks_green === false) reasons.add('review')
  else if (pullRequest.checks_green === undefined) reasons.add('checks not reported')
  switch (pullRequest.mergeable) {
    case 'dirty':
      reasons.add('not mergeable')
      break
    case 'behind':
      reasons.add('behind')
      break
    case 'blocked':
      reasons.add('review')
      break
    case 'unstable':
      reasons.add('unstable')
      break
    case 'draft':
      reasons.add('draft')
      break
    case 'clean':
    case 'has_hooks':
      break
    default:
      reasons.add('merge state not reported')
  }
  return [...reasons]
}

/** Where a task's state is decided: on this machine (it has an entry of this machine) or only as another machine reported it. */
export type StateSource = 'local' | 'remote'

/** Whether any worktree, pull request or agent of the task is an entry of this machine (REQ:task-state, trust rule). */
export function hasLocalEntry(inputs: TaskInputs): boolean {
  return inputs.worktrees.some(isLocal) || inputs.pullRequests.some(isLocal) || inputs.agents.some(isLocal)
}

/** The source of a task's state: `local` when it has any entry of this machine, else `remote`. */
export function stateSourceOf(inputs: TaskInputs): StateSource {
  return hasLocalEntry(inputs) ? 'local' : 'remote'
}

function isLocal(entry: { route: string }): boolean {
  return entry.route === 'local'
}

/** Row 4 for one pull request: observed, not a draft, a green verdict and a merge state of `clean` or `has_hooks`. */
function isReadyToLand(pullRequest: PullRequest): boolean {
  return (
    isObserved(pullRequest) &&
    pullRequest.state !== 'draft' &&
    pullRequest.checks_green === true &&
    pullRequest.mergeable !== undefined &&
    MERGEABLE_READY.has(pullRequest.mergeable)
  )
}

function isWorking(inputs: TaskInputs): boolean {
  return (
    inputs.agents.some((agent) => agent.activity === 'working' || (isRun(agent) && agent.state === 'running')) ||
    inputs.worktrees.some((worktree) => worktree.owner_state === 'active')
  )
}

/** A worktree with work that is not pushed: commits ahead, or a branch with no upstream. */
export function hasUnpushedWork(worktree: Worktree): boolean {
  return (worktree.ahead ?? 0) > 0 || worktree.has_upstream === false
}

/**
 * Landed: a merged pull request, or every worktree merged, with no open pull request and no worktree of the task
 * holding unpushed work. When the task has an entry of this machine, only this machine's pull requests and
 * worktrees count (a remote entry never decides a local task's good state); a task with none is read from what
 * the other machines reported.
 */
function isLanded(inputs: TaskInputs, local: boolean): boolean {
  const pullRequests = local ? inputs.pullRequests.filter(isLocal) : inputs.pullRequests
  const worktrees = local ? inputs.worktrees.filter(isLocal) : inputs.worktrees
  const merged =
    pullRequests.some((pullRequest) => isObserved(pullRequest) && pullRequest.state === 'merged') ||
    (worktrees.length > 0 && worktrees.every((worktree) => worktree.lifecycle === 'merged'))
  return merged && !worktrees.some(hasUnpushedWork)
}

function anythingReported(inputs: TaskInputs): boolean {
  return (
    inputs.worktrees.some((worktree) => worktree.owner_state !== undefined) ||
    inputs.pullRequests.some((pullRequest) => isObserved(pullRequest) && pullRequest.state !== undefined) ||
    inputs.agents.some((agent) => agent.activity !== undefined)
  )
}

/** The state of one task at `now`. */
export function taskState(inputs: TaskInputs, now: number): TaskStateId {
  if (interimAtRiskWorktrees(inputs.worktrees).length > 0) return 'at-risk'
  const local = hasLocalEntry(inputs)
  const open = inputs.pullRequests.filter(isOpenPullRequest)
  if (openObserved(inputs.pullRequests).some((pullRequest) => failedChecks(pullRequest) > 0)) return 'checks-failed'
  if (isBlocked(inputs.agents, now, local)) return 'blocked'
  // Ready only when every open pull request, observed or not, is observed and ready; when the task has an entry of
  // this machine, one of its own open pull requests must be among them (a remote one alone never makes it ready).
  if (open.length > 0) return open.some((pullRequest) => !local || isLocal(pullRequest)) && open.every(isReadyToLand) ? 'ready' : 'not-ready'
  if (isWorking(inputs)) return 'working'
  if (isLanded(inputs, local)) return 'landed'
  return anythingReported(inputs) ? 'idle' : 'not-reported'
}
