import { Agent, AppLink, FleetModel, NeedsYouItem, TaskStateId, TaskView, formatAge } from '@cockpit/fleet-data'
import { isoOf } from './home-format'
import { MachineWords, machineWords } from './machine-words'

/** The one primary action at the end of a "Needs you" row (REQ:home-needs-you). */
export type RowAction =
  | { kind: 'route'; text: string; label: string; link: AppLink }
  | { kind: 'external'; text: string; label: string; href: string }
  /** Work at risk: the registry's Push per worktree when it offers one, otherwise "Copy command". */
  | { kind: 'work'; task: string; worktrees: { id: string; branch: string }[] }

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
}

function openTask(item: NeedsYouItem): RowAction {
  return {
    kind: 'route',
    text: 'Open task',
    label: `Open task ${item.task}`,
    link: item.link,
  }
}

/** What the row's action is: a link for the failures and the agents, the push or the copy for work at risk. */
function actionOf(item: NeedsYouItem): RowAction {
  switch (item.kind) {
    case 'work-at-risk':
      return {
        kind: 'work',
        task: item.task,
        worktrees: item.worktrees.map((worktree) => ({
          id: worktree.id,
          branch: worktree.branch,
        })),
      }
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
    })
  }
  return rows
}
