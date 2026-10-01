// The shapes the view model derives from the fleet document. Pages build only
// from these (and the list rows), never from the raw collections.

import { Agent, AgentActivity, Machine, MetricsRoute, PullRequest, Worktree } from './fleet.types'
import { AppLink } from './vocabulary'
import { TaskStateId, TaskStateInfo } from './task-state'
import { AgeTerm } from './matcher'

/** One task: the worktrees that share a name, with the pull requests and agents that belong to it. */
export interface TaskView {
  name: string
  worktrees: Worktree[]
  pullRequests: PullRequest[]
  agents: Agent[]
  state: TaskStateId
  stateInfo: TaskStateInfo
  /** Distinct `owner/name` of its worktrees' repositories, in order of appearance. */
  repositories: string[]
  machines: { id: string; name: string }[]
  /** The newest worktree activity, in milliseconds. */
  lastActivityAt?: number
  /** Its open pull requests that have been observed. */
  openPullRequests: PullRequest[]
  /** Its pull requests with no `checked_at`: never observed, and not part of its state. */
  unobservedPullRequests: number
}

export type NeedsYouKind = 'work-at-risk' | 'pr-checks-failed' | 'agent-blocked' | 'run-failed' | 'pr-needs-you' | 'agent-finished'

interface NeedsYouBase {
  /** Stable across polls: kind plus the task. */
  id: string
  kind: NeedsYouKind
  task: string
  /** The rank of the kind (REQ:home-needs-you): the order of the rows. */
  rank: number
  lastActivityAt?: number
  /** Where the row's primary action leads inside the application, when it does. */
  link: AppLink
}

/** One Home "Needs you" row: one per task, its worst kind. */
export type NeedsYouItem = NeedsYouBase &
  (
    | {
        kind: 'work-at-risk'
        /** The reason in words. */
        reason: string
        /** The worktrees concerned, one command each. */
        worktrees: { id: string; branch: string; repository: string }[]
      }
    | { kind: 'pr-checks-failed'; pullRequestId: string; repository: string; number: number; failedCheck?: string; url?: string }
    | { kind: 'agent-blocked'; agentId: string; machine: string; repository?: string }
    | { kind: 'run-failed'; agentId: string; machine: string; runState: string; exitCode?: number }
    | { kind: 'pr-needs-you'; pullRequestId: string; repository: string; number: number; reason: string; url?: string }
    | { kind: 'agent-finished'; agentId: string; machine: string }
  )

/** How many "Needs you" rows Home shows. */
export const NEEDS_YOU_VISIBLE = 5

/** The one row for blocked agents that work on no task. */
export interface BlockedWithoutTask {
  count: number
  agentIds: string[]
  /** Agents with chip `blocked`. */
  link: AppLink
}

export interface NeedsYou {
  /** One per task needing the operator, in order; the Home badge is their number. */
  items: NeedsYouItem[]
  /** The first few: at most NEEDS_YOU_VISIBLE. */
  shown: NeedsYouItem[]
  /** The "+n more": the tasks not shown. */
  more: number
  /** Where "+n more" leads: Tasks with chip `needs-you`. */
  moreLink: AppLink
  /** Shown after the task rows; undefined when no blocked agent is without a task. */
  withoutTask?: BlockedWithoutTask
}

export interface ReadyPullRequest {
  id: string
  /** `owner/name`, or undefined when the repository is not in the document. */
  repository?: string
  number: number
  url?: string
  checksPassed: number
  checksTotal: number
  /** When its checks were observed, in milliseconds. */
  checkedAt?: number
  /** `wb pr land <owner/repository>#<number>`, absent without a repository. */
  landCommand?: string
}

export interface ReadyToLandRow {
  task: string
  repositories: string[]
  pullRequests: ReadyPullRequest[]
  checksPassed: number
  checksTotal: number
  /** The oldest observation among its pull requests, in milliseconds. */
  checkedAt?: number
  lastActivityAt?: number
  link: AppLink
}

/** A task whose checks are not all in: shown muted, with how long ago they were read. */
export interface NotReadyRow {
  task: string
  /** The oldest observation among its open pull requests. */
  checkedAt?: number
  reasons: string[]
  link: AppLink
}

export interface ReadyToLand {
  ready: ReadyToLandRow[]
  notReady: NotReadyRow[]
}

export interface InFlightAgent {
  agent: Agent
  /** Runtime and model, "agent" for neither. */
  label: string
  task?: string
  repository?: string
  machine: string
  machineId: string
  /** On another machine: shown with the age of its snapshot and no action. */
  remote: boolean
  observedAt?: string
  startedAt?: number
  activity?: AgentActivity
  /** Stop and Log are offered only for a dispatched run on this machine. */
  controllable: boolean
}

/** Whether a machine can take another agent. */
export interface MachineLoad {
  state: 'free' | 'busy' | 'not-reported'
  route: MetricsRoute
  sampledAt?: number
  cpuPercent?: number
  memoryPercent?: number
}

export interface CleanupBar {
  term: AgeTerm
  label: string
  count: number
  link: AppLink
}

export interface Cleanup {
  safeCount: number
  lookCount: number
  safeIds: ReadonlySet<string>
  lookIds: ReadonlySet<string>
  /** The counts are indicative: the cleanup operation decides the authoritative set. */
  indicative: true
  reviewLink: AppLink
  /** Worktrees by the age of their last activity. */
  bars: CleanupBar[]
  /** Worktrees with no reported activity time, counted in no bar. */
  unknownAge: number
}

export interface HealthItem {
  machine: string
  machineId: string
  /** What is wrong, in words. */
  text: string
  /** The command to copy to fix it, with its "run on <machine>" label. */
  command: { text: string; label: string } | { reason: string }
  link: AppLink
}

export interface ScanErrorItem {
  /** `owner/name`. */
  repository: string
  command: { text: string } | { reason: string }
  link: AppLink
}

export interface FleetHealth {
  /** False when something is not OK: the line is shown only then. */
  ok: boolean
  staleMachines: HealthItem[]
  olderWb: HealthItem[]
  remoteErrors: HealthItem[]
  scanErrors: ScanErrorItem[]
}

export interface ThroughputSeries {
  windowDays: number
  /** One entry per day of the window, oldest first; days without a landing are 0. */
  perDay: { date: string; landed: number }[]
  totalLanded: number
  maxLanded: number
  /** The slowest tasks to land, slowest first. */
  slowest: { task: string; durationSeconds: number; landedAt: string }[]
}

export type MachineStateId = 'live' | 'cached' | 'stale'

export interface MachineView {
  machine: Machine
  local: boolean
  state: MachineStateId
  /** The age of the snapshot in milliseconds; undefined when not reported. */
  ageMs?: number
  /** Running an older WB than the newest in the fleet. */
  outdated: boolean
  runningAgents: number
  /** Time since `boot_time`, in milliseconds. */
  uptimeMs?: number
}
