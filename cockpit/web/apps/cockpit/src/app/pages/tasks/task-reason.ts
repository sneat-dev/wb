import { Agent, PullRequest, TaskView, agentTitle, blockingRuns, failedChecks, interimAtRiskWorktrees, isOpenPullRequest, isRunning, notReadyReasons } from '@cockpit/fleet-data'

// Why a task is in its state, in plain words (REQ:task-detail: the summary header). The decision is the
// library's (`task.state`); this only says it, from the library's own predicates and reason words
// (`interimAtRiskWorktrees`, `failedChecks`, `notReadyReasons`, `blockingRuns`), so that the sentence
// cannot disagree with the badge.
// TODO(fleet-data): a `taskReason(model, name)` next to `buildTaskPanel` would let Home's rows and the
// palette say the same words; until it exists the Tasks page composes them here.

/** How many parts of a reason are named before "+n more". */
export const REASON_PARTS_SHOWN = 3

export interface ReasonContext {
  /** `owner/name` of a repository entry id. */
  repositoryName: (id: string) => string
  /** Epoch milliseconds: a failed run blocks for 24 hours. */
  now: number
}

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

/** The task's state in words: "Ready to land: 2 pull requests green and mergeable". */
export function taskReason(task: TaskView, context: ReasonContext): string {
  const slugs = [...new Set([...task.repositories, ...task.pullRequests.flatMap((pr) => (pr.repository === undefined ? [] : [context.repositoryName(pr.repository)]))])]
  const name = repositoryNamer(slugs)
  const prLabel = (pr: PullRequest): string => `${pr.repository === undefined ? '' : name(context.repositoryName(pr.repository))}#${pr.number}`
  switch (task.state) {
    case 'at-risk':
      return sentence(
        'At risk',
        interimAtRiskWorktrees(task.worktrees).map((worktree) => {
          const where = name(context.repositoryName(worktree.repository))
          const unpushed = (worktree.ahead ?? 0) > 0 ? `${plural(worktree.ahead as number, 'commit')} only on this machine` : 'a branch with no upstream, only on this machine'
          return `${unpushed} in ${where} (worktree ${worktree.owner_state})`
        }),
      )
    case 'checks-failed':
      return sentence(
        'Checks failed',
        task.openPullRequests
          .filter((pr) => failedChecks(pr) > 0)
          .map((pr) => `${prLabel(pr)} has ${plural(failedChecks(pr), 'failing check')}${pr.failed_check ? ` (${pr.failed_check})` : ''}`),
      )
    case 'blocked':
      return sentence('Blocked', [
        ...task.agents.filter((agent) => agent.activity === 'blocked').map((agent) => `${agentTitle(agent)} is waiting on you`),
        ...blockingRuns(task.agents, context.now).map((run) => `the ${agentTitle(run)} run ${run.state === 'timeout' ? 'timed out' : 'failed'}${run.exit_code === undefined ? '' : ` (exit ${run.exit_code})`}`),
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
        ...task.worktrees.filter((worktree) => worktree.owner_state === 'active').map((worktree) => `a worktree in ${name(context.repositoryName(worktree.repository))} is active`),
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
