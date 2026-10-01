import { AppLink, FleetModel, MachineView, NeedsYouItem, TaskStateId } from '@cockpit/fleet-data'
import { isoOf } from './home-format'

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
  /** The machine of the agent, only when it is not this one. */
  machine: MachineView | undefined
  /** When the task was last active, as an ISO time. */
  at: string | undefined
  action: RowAction
}

function openTask(item: NeedsYouItem): RowAction {
  return { kind: 'route', text: 'Open task', label: `Open task ${item.task}`, link: item.link }
}

/** What the row's action is: a link for the failures and the agents, the push or the copy for work at risk. */
function actionOf(item: NeedsYouItem): RowAction {
  switch (item.kind) {
    case 'work-at-risk':
      return { kind: 'work', task: item.task, worktrees: item.worktrees.map((worktree) => ({ id: worktree.id, branch: worktree.branch })) }
    case 'pr-checks-failed':
      // A pull request with no address that is safe to open is named and not linked; the row still opens its task.
      return item.url === undefined ? openTask(item) : { kind: 'external', text: 'Open failure', label: `Open failure of ${item.repository}#${item.number}`, href: item.url }
    case 'agent-blocked':
    case 'run-failed':
      return { kind: 'route', text: 'Open agent', label: `Open agent of ${item.task}`, link: item.link }
    case 'pr-needs-you':
      return item.url === undefined ? openTask(item) : { kind: 'external', text: 'Open pull request', label: `Open pull request ${item.repository}#${item.number}`, href: item.url }
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
  if (item.kind === 'work-at-risk') return item.worktrees.map((worktree) => ({ repository: worktree.repository, branch: worktree.branch.endsWith(item.task) ? undefined : worktree.branch }))
  if (item.kind === 'agent-blocked' && item.repository !== undefined) return [{ repository: item.repository }]
  return []
}

/** The machine an agent runs on, when it is another one (a local agent needs no chip). */
function remoteMachine(model: FleetModel, item: NeedsYouItem): MachineView | undefined {
  if (item.kind !== 'agent-blocked' && item.kind !== 'run-failed' && item.kind !== 'agent-finished') return undefined
  const machineId = model.agentById(item.agentId)?.machine_id
  return model.machines.find((view) => view.machine.id === machineId && !view.local)
}

/** The rows of Home's "Needs you": the model's first five items, in its order, as a row can show them. */
export function needsYouRows(model: FleetModel): NeedsYouRow[] {
  return model.needsYou.shown.map((item) => ({
    id: item.id,
    task: item.task,
    state: model.taskNamed(item.task)?.state,
    reason: reasonOf(item),
    places: placesOf(item),
    machine: remoteMachine(model, item),
    at: isoOf(item.lastActivityAt),
    action: actionOf(item),
  }))
}
