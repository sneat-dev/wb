// Per-entity panel views (REQ:side-panel): for one worktree, task, repository,
// agent, machine or pull request, the summary facts, the related entities, the
// "Copy command" list (already quoted, each flagged `needsEdit` and labelled with
// where it runs) and the raw entries for the "Raw data" block. Built from the
// view model, so a panel and its detail page render the same thing.

import {
  CommandTarget,
  CopyCommand,
  PLACEHOLDERS,
  agentLogs,
  agentStatus,
  agentStop,
  branchList,
  fleetStatus,
  pullRequestCreate,
  pullRequestLand,
  sessionList,
  sessionSend,
  worktreeCleanup,
  worktreeCreate,
  worktreeList,
} from './commands'
import type { FleetModel } from './fleet-model'
import { agentTitle } from './fleet-view'
import { Agent, Machine, PullRequest, Worktree, isRunning } from './fleet.types'
import { buildRepositories } from './model-repositories'
import { MergedRepository } from './repository-identity'
import { webAddress } from './web-address'
import { isObserved, isOpenPullRequest, notReadyReasons } from './task-state'
import { MachineView, TaskView } from './view-types'

/** One "Copy command" entry of a panel. */
export interface PanelCommand {
  title: string
  command: CopyCommand
}

/** What every panel carries beside its own facts. */
export interface PanelBase {
  commands: PanelCommand[]
  /** The entries exactly as the read model sent them, for the "Raw data" block. */
  raw: readonly unknown[]
}

export interface WorktreePanel extends PanelBase {
  summary: {
    id: string
    task: string
    repository: string
    branch: string
    machine: string
    route: string
    ownerState?: string
    lifecycle?: string
    lastActivityAt?: number
    ahead?: number
    behind?: number
    upstreamGone?: boolean
    hasUpstream?: boolean
  }
  related: { task?: TaskView; repository?: MergedRepository; pullRequests: PullRequest[]; agents: Agent[] }
}

export interface TaskPanel extends PanelBase {
  summary: {
    name: string
    state: string
    label: string
    repositories: string[]
    machines: { id: string; name: string }[]
    lastActivityAt?: number
    /** Pull requests never observed, which take no part in the state. */
    unobservedPullRequests: number
    /** Why its open pull requests are not ready, in words. */
    notReadyReasons: string[]
  }
  related: { worktrees: Worktree[]; pullRequests: PullRequest[]; agents: Agent[] }
}

export interface RepositoryPanel extends PanelBase {
  summary: MergedRepository
  related: { worktrees: Worktree[]; pullRequests: PullRequest[]; agents: Agent[] }
}

export interface AgentPanel extends PanelBase {
  summary: {
    id: string
    kind: string
    label: string
    state: string
    activity?: string
    machine: string
    machineId: string
    remote: boolean
    startedAt?: number
    finishedAt?: number
    exitCode?: number
    task?: string
    repository?: string
    /** A session that cannot be controlled says so plainly: it offers no command. */
    controllable: boolean
    /** For a session that waits for input: what to do next, and where. */
    nextStep?: string
  }
  related: { task?: TaskView; worktrees: Worktree[]; pullRequests: PullRequest[] }
}

export interface MachinePanel extends PanelBase {
  summary: MachineView
  related: { runningAgents: Agent[]; recentWorktrees: Worktree[] }
}

export interface PullRequestPanel extends PanelBase {
  summary: {
    id: string
    repository?: string
    number: number
    /** Only a checked web address (https, plain host); never the raw value. */
    url?: string
    state?: string
    mergeable?: string
    observed: boolean
    checksTotal?: number
    checksPassed?: number
    checksFailed?: number
    checksSkipped?: number
    checksPending?: number
    checksGreen?: boolean
    failedCheck?: string
    checkedAt?: number
    notReadyReasons: string[]
  }
  related: { worktree?: Worktree; task?: TaskView }
}

/** How many recent worktrees a machine panel lists. */
export const MACHINE_RECENT_WORKTREES = 5

const time = (value: string | undefined): number | undefined => {
  const parsed = value ? Date.parse(value) : Number.NaN
  return Number.isNaN(parsed) ? undefined : parsed
}

/**
 * A pull request as a panel shows it: its `url` only when it is a checked web
 * address (`webAddress`), so a page can bind it to an `href`. The panel's `raw`
 * entries stay exactly as the read model sent them, for the Raw data block.
 */
function checkedPullRequest(pullRequest: PullRequest): PullRequest {
  return { ...pullRequest, url: webAddress(pullRequest.url) }
}

const entry = (title: string, command: CopyCommand): PanelCommand => ({ title, command })

/**
 * A command that changes something: listed only when the library built it, which it does for this machine's own
 * entries alone (`onThisMachine`); for another machine's entry the refusal is dropped, so the panel keeps its
 * reading commands and shows no push, land, stop or send.
 */
const changing = (title: string, command: CopyCommand): PanelCommand[] => (command.ok ? [entry(title, command)] : [])

/** The entry the task's commands run on: this machine's worktree if any, else the first. */
function anchor(worktrees: readonly Worktree[]): Worktree | undefined {
  return worktrees.find((worktree) => worktree.route === 'local') ?? worktrees[0]
}

/**
 * The task's commands: reading ones always, and for a task decided on this machine (`stateSource` `local`)
 * the ones that change something: committing and opening a pull request, and landing each open pull request.
 * A task only another machine reports has nothing to push or land from here, so those are withheld (not
 * hidden by the page): its read-only commands stay.
 */
function taskCommands(model: FleetModel, task: TaskView): PanelCommand[] {
  const where = anchor(task.worktrees)
  const target = where === undefined ? {} : model.targetOf(where)
  const reading = [entry('List worktrees', worktreeList(task.name, target)), entry('Plan cleanup (dry run)', worktreeCleanup(task.name, target))]
  if (task.stateSource === 'remote') return reading
  const land = task.pullRequests.filter(isOpenPullRequest).flatMap((pr) => {
    const panel = buildPullRequestPanel(model, pr.id) as PullRequestPanel
    // A pull request with no repository has no command to copy.
    return panel.commands.map((command) => ({ ...command, title: `${command.title} ${panel.summary.repository as string}#${pr.number}` }))
  })
  return [reading[0], ...changing('Commit and open pull request', pullRequestCreate(task.name, PLACEHOLDERS.message, target)), reading[1], ...land]
}

/** A worktree's panel commands: its task's reading and pushing commands, run where this worktree is. */
function worktreeCommands(model: FleetModel, worktree: Worktree): PanelCommand[] {
  const target = model.targetOf(worktree)
  return [
    entry('List worktrees', worktreeList(worktree.task, target)),
    ...changing('Commit and open pull request', pullRequestCreate(worktree.task, PLACEHOLDERS.message, target)),
    entry('Plan cleanup (dry run)', worktreeCleanup(worktree.task, target)),
  ]
}

export function buildWorktreePanel(model: FleetModel, id: string): WorktreePanel | undefined {
  const worktree = model.worktreeById(id)
  if (worktree === undefined) return undefined
  // Every worktree's task is in the model.
  const task = model.taskNamed(worktree.task) as TaskView
  return {
    summary: {
      id: worktree.id,
      task: worktree.task,
      repository: model.repositoryName(worktree.repository),
      branch: worktree.branch,
      machine: worktree.machine,
      route: worktree.route,
      ownerState: worktree.owner_state,
      lifecycle: worktree.lifecycle,
      lastActivityAt: time(worktree.last_activity_at),
      ahead: worktree.ahead,
      behind: worktree.behind,
      upstreamGone: worktree.upstream_gone,
      hasUpstream: worktree.has_upstream,
    },
    related: {
      task,
      repository: buildRepositories(model).find((row) => row.checkouts.some((checkout) => checkout.repository.id === worktree.repository)),
      pullRequests: (model.worktreePullRequests.get(worktree.id) ?? []).map(checkedPullRequest),
      agents: task.agents,
    },
    commands: worktreeCommands(model, worktree),
    raw: [worktree],
  }
}

export function buildTaskPanel(model: FleetModel, name: string): TaskPanel | undefined {
  const task = model.taskNamed(name)
  if (task === undefined) return undefined
  return {
    summary: {
      name: task.name,
      state: task.state,
      label: task.stateInfo.label,
      repositories: task.repositories,
      machines: task.machines,
      lastActivityAt: task.lastActivityAt,
      unobservedPullRequests: task.unobservedPullRequests,
      notReadyReasons: [...new Set(task.pullRequests.filter(isOpenPullRequest).flatMap(notReadyReasons))],
    },
    related: { worktrees: task.worktrees, pullRequests: task.pullRequests.map(checkedPullRequest), agents: task.agents },
    commands: taskCommands(model, task),
    raw: [...task.worktrees, ...task.pullRequests, ...task.agents],
  }
}

export function buildRepositoryPanel(model: FleetModel, key: string): RepositoryPanel | undefined {
  const repository = buildRepositories(model).find((row) => row.key === key)
  if (repository === undefined) return undefined
  const ids = new Set(repository.checkouts.map((checkout) => checkout.repository.id))
  const target = model.targetOf(repository.checkouts[0].repository)
  return {
    summary: repository,
    related: {
      worktrees: model.document.worktrees.filter((worktree) => ids.has(worktree.repository)),
      pullRequests: model.document.pull_requests.filter((pullRequest) => ids.has(pullRequest.repository ?? '')).map(checkedPullRequest),
      agents: model.document.agents.filter((agent) => ids.has(agent.repository ?? '')),
    },
    commands: [
      // Creating a worktree changes things: only where this machine has the checkout (another machine's is created from New task).
      ...(repository.checkouts.some((checkout) => checkout.repository.route === 'local') ? [entry('Create a task worktree', worktreeCreate(PLACEHOLDERS.task, [repository.slug], {}))] : []),
      entry('List branches', branchList(repository.slug, undefined, target)),
      entry('Fleet status', fleetStatus(repository.slug, target)),
    ],
    raw: repository.checkouts.map((checkout) => checkout.repository),
  }
}

/**
 * A session's commands. One that waits for input (`blocked`) gets the next step: `wb session send` with its message
 * to edit, for a session of this machine; for another machine's, where to run it, and the reading command over ssh
 * (or labelled with the machine). Any other session has no command: it cannot be controlled from here.
 */
function sessionCommands(agent: Agent, target: CommandTarget): PanelCommand[] {
  if (agent.activity !== 'blocked' || agent.session_id === undefined) return []
  return [...changing('Send a message (edit it first)', sessionSend(agent.session_id, PLACEHOLDERS.message, target)), entry('List sessions', sessionList(target))]
}

function nextStepOf(agent: Agent): string | undefined {
  if (agent.kind !== 'session' || agent.activity !== 'blocked') return undefined
  return agent.route === 'local'
    ? 'This session waits for input. Answer it with the command below: replace the message, then run it in a terminal on this machine.'
    : `This session waits for input on ${agent.machine}. Answering it changes things there, so run "wb session send" in a terminal on ${agent.machine}; from here you can only read it with the command below.`
}

export function buildAgentPanel(model: FleetModel, id: string): AgentPanel | undefined {
  const agent = model.agentById(id)
  if (agent === undefined) return undefined
  const worktrees = (agent.worktrees ?? []).flatMap((worktreeId) => model.worktreeById(worktreeId) ?? [])
  const taskName = model.tasksOfAgent(agent)[0]
  const target = model.targetOf(agent)
  // A dispatched run has the agent verbs; any other session has none (it cannot be controlled from here).
  const runId = agent.run_id ?? agent.id
  const run = agent.kind === 'run'
  return {
    summary: {
      id: agent.id,
      kind: agent.kind,
      label: agentTitle(agent),
      state: agent.state,
      activity: agent.activity,
      machine: agent.machine,
      machineId: agent.machine_id,
      remote: agent.route !== 'local',
      startedAt: time(agent.started_at),
      finishedAt: time(agent.finished_at),
      exitCode: agent.exit_code,
      task: taskName,
      repository: agent.repository === undefined ? undefined : model.repositoryName(agent.repository),
      controllable: run,
      nextStep: nextStepOf(agent),
    },
    related: {
      task: taskName === undefined ? undefined : model.taskNamed(taskName),
      worktrees,
      pullRequests: [...new Set((agent.worktrees ?? []).flatMap((worktreeId) => model.worktreePullRequests.get(worktreeId) ?? []))].map(checkedPullRequest),
    },
    commands: run ? [entry('Status', agentStatus(runId, target)), entry('Logs', agentLogs(runId, target)), ...changing('Stop', agentStop(runId, target))] : sessionCommands(agent, target),
    raw: [agent],
  }
}

export function buildMachinePanel(model: FleetModel, id: string): MachinePanel | undefined {
  const view = model.machines.find((row) => row.machine.id === id)
  if (view === undefined) return undefined
  return {
    summary: view,
    related: {
      runningAgents: model.document.agents.filter((agent) => agent.machine_id === id && isRunning(agent)),
      recentWorktrees: model.document.worktrees
        .filter((worktree) => worktree.machine_id === id)
        .sort((a, b) => (time(b.last_activity_at) ?? -Infinity) - (time(a.last_activity_at) ?? -Infinity) || a.id.localeCompare(b.id))
        .slice(0, MACHINE_RECENT_WORKTREES),
    },
    // The manifest has no command for a machine that the read model's identifiers can fill.
    commands: [],
    raw: [view.machine as Machine],
  }
}

export function buildPullRequestPanel(model: FleetModel, id: string): PullRequestPanel | undefined {
  const pullRequest = model.pullRequestById(id)
  if (pullRequest === undefined) return undefined
  const repository = pullRequest.repository === undefined ? undefined : model.repositoryName(pullRequest.repository)
  const taskName = model.taskOfPullRequest(pullRequest)
  return {
    summary: {
      id: pullRequest.id,
      repository,
      number: pullRequest.number,
      url: webAddress(pullRequest.url),
      state: pullRequest.state,
      mergeable: pullRequest.mergeable,
      observed: isObserved(pullRequest),
      checksTotal: pullRequest.checks_total,
      checksPassed: pullRequest.checks_passed,
      checksFailed: pullRequest.checks_failed,
      checksSkipped: pullRequest.checks_skipped,
      checksPending: pullRequest.checks_pending,
      checksGreen: pullRequest.checks_green,
      failedCheck: pullRequest.failed_check,
      checkedAt: time(pullRequest.checked_at),
      notReadyReasons: isOpenPullRequest(pullRequest) ? notReadyReasons(pullRequest) : [],
    },
    related: {
      worktree: pullRequest.worktree === undefined ? undefined : model.worktreeById(pullRequest.worktree),
      task: taskName === undefined ? undefined : model.taskNamed(taskName),
    },
    commands: repository === undefined ? [] : changing('Land', pullRequestLand(repository, pullRequest.number, model.targetOf(pullRequest))),
    raw: [pullRequest],
  }
}
