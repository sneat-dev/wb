import { Agent, AppLink, CommandTarget, FleetModel, NeedsYouItem, TaskStateId, TaskView, Worktree, formatAge } from '@cockpit/fleet-data'
import { isoOf } from './home-format'
import { MachineWords, machineWords } from './machine-words'

/** The one primary action at the end of a "Needs you" row (REQ:home-needs-you). */
export type RowAction =
  | { kind: 'route'; text: string; label: string; link: AppLink }
  | { kind: 'external'; text: string; label: string; href: string }

/** What work at risk offers besides opening its task: the registry's Push per worktree when it offers one, otherwise a "Copy template" icon button. */
export interface WorkOffer {
  task: string
  /** Where the task's commands run (REQ:copy-the-command): a mutating one is refused for another machine's work. */
  target: CommandTarget
  worktrees: { id: string; branch: string; repository: string }[]
}

/** A repository, and for work at risk the branch of the worktree, a row says "where" with. */
export interface RowPlace {
  repository: string
  branch?: string
}

export interface NeedsYouRow {
  id: string
  task: string
  state: TaskStateId | undefined
  /** The reason in plain words. */
  reason: string
  places: RowPlace[]
  /** The machine of the agent: the name alone for this machine, with a chip for another. */
  machine: MachineWords | undefined
  /** The machines that reported the task, when its state is theirs and not this machine's ("as reported by vm"); such a row offers no push or copy. */
  reportedBy: string | undefined
  /** When the task was last active, as an ISO time and as its age ("2 h ago"). */
  at: string | undefined
  age: string | undefined
  action: RowAction
  /** Work at risk only: the secondary action (push or copy), after the primary "Open task". */
  work: WorkOffer | undefined
}

function openTask(item: NeedsYouItem): RowAction {
  return {
    kind: 'route',
    text: 'Open task',
    label: `Open task ${item.task}`,
    link: item.link,
  }
}

/** What the row's one primary action is: a link, to the task panel (where the commands live) for work at risk. */
function actionOf(item: NeedsYouItem): RowAction {
  switch (item.kind) {
    case 'work-at-risk':
      return openTask(item)
    case 'pr-checks-failed':
      // A pull request with no address that is safe to open is named and not linked; the row still opens its task.
      return item.url === undefined
        ? openTask(item)
        : {
            kind: 'external',
            text: 'Open failure',
            label: `Open failure of ${item.repository}#${item.number}`,
            href: item.url,
          }
    case 'agent-blocked':
    case 'run-failed':
      return {
        kind: 'route',
        text: 'Open agent',
        label: `Open agent of ${item.task}`,
        link: item.link,
      }
    case 'pr-needs-you':
      return item.url === undefined
        ? openTask(item)
        : {
            kind: 'external',
            text: 'Open pull request',
            label: `Open pull request ${item.repository}#${item.number}`,
            href: item.url,
          }
    case 'agent-finished':
      return openTask(item)
  }
}

/**
 * Where the work's commands run: here only when every worktree is this machine's; otherwise the machine of the
 * first worktree that is not, so that a mutating command is refused and never built for another machine.
 */
export function targetOfWork(model: FleetModel, ids: readonly string[]): CommandTarget {
  const entries = ids.map((id) => model.worktreeById(id)).filter((entry): entry is Worktree => entry !== undefined)
  const targets = entries.map((entry) => model.targetOf(entry))
  return targets.find((target) => target.machine !== undefined || target.ssh !== undefined) ?? {}
}

function workOf(model: FleetModel, item: NeedsYouItem): WorkOffer | undefined {
  return item.kind === 'work-at-risk'
    ? {
        task: item.task,
        target: targetOfWork(model, item.worktrees.map((worktree) => worktree.id)),
        worktrees: item.worktrees.map((worktree) => ({ id: worktree.id, branch: worktree.branch, repository: worktree.repository })),
      }
    : undefined
}

function reasonOf(item: NeedsYouItem): string {
  switch (item.kind) {
    case 'work-at-risk':
      return item.reason
    case 'pr-checks-failed':
      return `${item.failedCheck ?? 'checks'} failed on ${item.repository}#${item.number}`
    case 'agent-blocked':
      return 'agent blocked'
    case 'run-failed':
      return `run ${item.runState}${item.exitCode === undefined ? '' : `, exit code ${item.exitCode}`}`
    case 'pr-needs-you':
      return `${item.repository}#${item.number}: ${item.reason}`
    case 'agent-finished':
      return 'agent finished, work not pushed'
  }
}

function placesOf(item: NeedsYouItem): RowPlace[] {
  // The branch is named only when it says more than the task does (most branches are the task's name).
  if (item.kind === 'work-at-risk')
    return item.worktrees.map((worktree) => ({
      repository: worktree.repository,
      branch: worktree.branch.endsWith(item.task) ? undefined : worktree.branch,
    }))
  if (item.kind === 'agent-blocked' && item.repository !== undefined) return [{ repository: item.repository }]
  return []
}

/** The machine an agent runs on, as a row says it (the name alone for this machine, a chip besides for another). */
function agentMachine(model: FleetModel, item: NeedsYouItem): MachineWords | undefined {
  if (item.kind !== 'agent-blocked' && item.kind !== 'run-failed' && item.kind !== 'agent-finished') return undefined
  // An item names an agent of the document, so the agent is there.
  const machineId = (model.agentById(item.agentId) as Agent).machine_id
  const view = model.machines.find((candidate) => candidate.machine.id === machineId)
  return view && machineWords(view, model.now)
}

/** The rows of Home's "Needs you": the model's first five items, in its order, as a row can show them, then the one row for blocked agents with no task. */
export function needsYouRows(model: FleetModel): NeedsYouRow[] {
  const { withoutTask } = model.needsYou
  const rows = model.needsYou.shown.map((item) => {
    // An item is made from a task of the model, so the task is there.
    const task = model.taskNamed(item.task) as TaskView
    return {
      id: item.id,
      task: item.task,
      state: task.state,
      reason: reasonOf(item),
      places: placesOf(item),
      machine: agentMachine(model, item),
      reportedBy: item.stateSource === 'remote' ? task.reportedBy.map((machine) => machine.name).join(', ') : undefined,
      at: isoOf(item.lastActivityAt),
      age: item.lastActivityAt === undefined ? undefined : formatAge(isoOf(item.lastActivityAt), model.now),
      action: actionOf(item),
      work: workOf(model, item),
    }
  })
  if (withoutTask !== undefined) {
    const words = `${withoutTask.count} blocked ${withoutTask.count === 1 ? 'agent' : 'agents'} with no task`
    rows.push({
      id: 'without-task',
      task: words,
      state: 'blocked',
      reason: '',
      places: [],
      machine: undefined,
      reportedBy: undefined,
      at: undefined,
      age: undefined,
      action: {
        kind: 'route',
        text: 'Open agents',
        label: 'Open the blocked agents with no task',
        link: withoutTask.link,
      },
      work: undefined,
    })
  }
  return rows
}
