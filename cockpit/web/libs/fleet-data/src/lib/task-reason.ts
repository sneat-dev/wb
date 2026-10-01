// Why a task is in its state, in plain words (REQ:task-detail: the summary header), as one function of
// the model, so the Tasks panel, Home's rows and the palette say the same words and cannot disagree
// with the badge. The decision is the library's (`task.state`); this only says it, from the library's
// own predicates and reason words (`interimAtRiskWorktrees`, `failedChecks`, `notReadyReasons`,
// `blockingRuns`). A task that only another machine reports (`stateSource` `remote`) has the state that
// machine reports, and no at-risk rule (the sync facts are known for this machine only); a ready or
// not-ready one says so in the words below, and the page that shows it names the reporting machine.

import type { FleetModel } from './fleet-model'
import { Agent, PullRequest, Worktree, isRunning } from './fleet.types'
import { agentTitle } from './fleet-view'
import { blockingRuns, failedChecks, interimAtRiskWorktrees, isOpenPullRequest, notReadyReasons } from './task-state'
import { TaskView } from './view-types'

/** How many parts of a reason are named before "+n more". */
export const REASON_PARTS_SHOWN = 3

const plural = (count: number, noun: string): string => `${count} ${noun}${count === 1 ? '' : 's'}`

const tail = (slug: string): string => slug.slice(slug.lastIndexOf('/') + 1)

/** A repository as the sentence names it: `wb`, or `owner/wb` when two repositories of the task share that name. */
function repositoryNamer(slugs: readonly string[]): (slug: string) => string {
  return (slug) => (slugs.filter((other) => tail(other) === tail(slug)).length > 1 ? slug : tail(slug))
}

/** The parts of a sentence, which each state has at least one of: the first few and "+n more". */
function sentence(label: string, parts: readonly string[]): string {
  const shown = parts.length > REASON_PARTS_SHOWN ? [...parts.slice(0, REASON_PARTS_SHOWN), `+${parts.length - REASON_PARTS_SHOWN} more`] : parts
  return `${label}: ${shown.join('; ')}`
}

/**
 * The task's state in words: "Ready to land: 2 pull requests green and mergeable". `undefined` for a task
 * the document does not list.
 */
export function taskReason(model: FleetModel, name: string): string | undefined {
  const task = model.taskNamed(name)
  return task === undefined ? undefined : reasonOf(model, task)
}

function reasonOf(model: FleetModel, task: TaskView): string {
  const repositoryName = (id: string): string => model.repositoryName(id)
  const slugs = [...new Set([...task.repositories, ...task.pullRequests.flatMap((pr) => (pr.repository === undefined ? [] : [repositoryName(pr.repository)]))])]
  const name = repositoryNamer(slugs)
  const prLabel = (pr: PullRequest): string => `${pr.repository === undefined ? '' : name(repositoryName(pr.repository))}#${pr.number}`
  switch (task.state) {
    case 'at-risk':
      return sentence(
        'At risk',
        interimAtRiskWorktrees(task.worktrees).map((worktree) => {
          const where = name(repositoryName(worktree.repository))
          const unpushed = (worktree.ahead ?? 0) > 0 ? `${plural(worktree.ahead as number, 'commit')} only on this machine` : 'a branch with no upstream, only on this machine'
          return `${unpushed} in ${where} (worktree ${worktree.owner_state})`
        }),
      )
    case 'checks-failed':
      return sentence(
        'Checks failed',
        task.openPullRequests.filter((pr) => failedChecks(pr) > 0).map((pr) => `${prLabel(pr)} has ${plural(failedChecks(pr), 'failing check')}${pr.failed_check ? ` (${pr.failed_check})` : ''}`),
      )
    case 'blocked':
      return sentence('Blocked', [
        ...task.agents.filter((agent) => agent.activity === 'blocked').map((agent) => `${agentTitle(agent)} is waiting on you`),
        ...blockingRuns(task.agents, model.now).map((run) => `the ${agentTitle(run)} run ${run.state === 'timeout' ? 'timed out' : 'failed'}${run.exit_code === undefined ? '' : ` (exit ${run.exit_code})`}`),
      ])
    case 'ready':
      return `Ready to land: ${plural(task.openPullRequests.length, 'pull request')} green and mergeable`
    case 'not-ready': {
      const parts = task.pullRequests.filter(isOpenPullRequest).flatMap((pr) => {
        const reasons = notReadyReasons(pr)
        return reasons.length === 0 ? [] : [`${prLabel(pr)} ${reasons.join(', ')}`]
      })
      // Every open pull request is ready, but none is on this machine: another machine's report cannot make this machine's task ready (REQ:task-state, trust rule).
      return sentence('Not ready', parts.length > 0 ? parts : ['no open pull request is on this machine, and only this machine can make the task ready'])
    }
    case 'working':
      return sentence('Working', [
        ...task.agents.filter(isWorking).map((agent) => `${agentTitle(agent)} is running`),
        ...task.worktrees.filter((worktree) => worktree.owner_state === 'active').map((worktree) => `a worktree in ${name(repositoryName(worktree.repository))} is active`),
      ])
    case 'landed':
      return 'Landed: merged, with nothing left unpushed'
    case 'idle':
      return 'Idle: nothing is running and nothing waits on you'
    default:
      return 'State not reported: no owner state, pull request state or agent activity was reported for this task'
  }
}

function isWorking(agent: Agent): boolean {
  return agent.activity === 'working' || (agent.kind === 'run' && isRunning(agent))
}

/** Whether none of the task's worktrees was read on this machine: its sync facts and the at-risk rule are not known here. */
export function seenElsewhereOnly(task: TaskView): boolean {
  return task.worktrees.length > 0 && task.worktrees.every((worktree) => worktree.route !== 'local')
}
