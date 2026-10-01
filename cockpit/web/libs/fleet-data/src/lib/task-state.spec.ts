import { Agent, PullRequest, Worktree } from './fleet.types'
import {
  BLOCKED_RUN_EXPIRY_MS,
  NOT_READY_REASONS,
  hasUnpushedWork,
  TASK_STATES,
  TASK_STATE_IDS,
  TaskInputs,
  blockingRuns,
  interimAtRiskWorktrees,
  isBlocked,
  hasLocalEntry,
  isObserved,
  isOpenPullRequest,
  notReadyReasons,
  openObserved,
  startedAt,
  stateSourceOf,
  taskState,
  taskStateInfo,
} from './task-state'
import { OBSERVED, agent, pullRequest, run, worktree } from './test-data'

const NOW = Date.parse('2026-10-01T10:00:00Z')
const HOUR = 3_600_000

function wt(extra: Partial<Worktree> = {}): Worktree {
  return { ...worktree('w', 'r1', 'alpha'), ...extra }
}

function pr(extra: Partial<PullRequest> = {}): PullRequest {
  return pullRequest('p', 'r1', 'w', extra)
}

function inputs(parts: Partial<TaskInputs> = {}): TaskInputs {
  return { worktrees: [], pullRequests: [], agents: [], ...parts }
}

const at = (hoursAgo: number): string => new Date(NOW - hoursAgo * HOUR).toISOString()
const state = (parts: Partial<TaskInputs>): string => taskState(inputs(parts), NOW)

describe('task state: row 1, at risk', () => {
  // cockpit-views#ac:task-state-at-risk
  it('applies the interim rule: local, owner not active, and commits ahead or no upstream', () => {
    const orphanedAhead = wt({ owner_state: 'orphaned', ahead: 2 })
    expect(state({ worktrees: [orphanedAhead] })).toBe('at-risk')
    expect(state({ worktrees: [wt({ owner_state: 'unknown', ahead: 1 })] })).toBe('at-risk')
    expect(state({ worktrees: [wt({ owner_state: 'idle', has_upstream: false })] })).toBe('at-risk')
    // The same worktree with nothing ahead and an upstream is not at risk.
    expect(state({ worktrees: [wt({ owner_state: 'orphaned', ahead: 0, has_upstream: true })] })).not.toBe('at-risk')
    expect(state({ worktrees: [wt({ owner_state: 'orphaned' })] })).not.toBe('at-risk')
    // An active owner is not at risk, and an owner that is not reported is never guessed at.
    expect(state({ worktrees: [wt({ owner_state: 'active', ahead: 3 })] })).not.toBe('at-risk')
    expect(state({ worktrees: [wt({ ahead: 3 })] })).not.toBe('at-risk')
    // Only this machine: an entry cached from another machine is never at risk, whatever it says.
    expect(state({ worktrees: [wt({ route: 'cached', owner_state: 'orphaned', ahead: 2 })] })).not.toBe('at-risk')
    expect(state({ worktrees: [wt({ route: 'live-remote', owner_state: 'orphaned', ahead: 2 })] })).not.toBe('at-risk')
    expect(interimAtRiskWorktrees([orphanedAhead, wt({ id: 'ok' })])).toEqual([orphanedAhead])
  })

  it('beats every row below it', () => {
    expect(state({ worktrees: [wt({ owner_state: 'orphaned', ahead: 1 })], pullRequests: [pr({ checks_failed: 1 })] })).toBe('at-risk')
  })
})

describe('task state: row 2, checks failed', () => {
  // cockpit-views#ac:task-state-checks-failed
  it('beats a blocked agent and a ready pull request', () => {
    const failing = pr({ id: 'p1', checks_failed: 1, checks_passed: 4, checks_green: false })
    const ready = pr({ id: 'p2' })
    const blocked = agent('a', 'r1', 'live', { activity: 'blocked' })
    expect(state({ pullRequests: [failing, ready], agents: [blocked] })).toBe('checks-failed')
  })

  it('considers only observed open pull requests', () => {
    expect(state({ pullRequests: [pr({ checks_failed: 2, state: 'merged' })] })).not.toBe('checks-failed')
    expect(state({ pullRequests: [pr({ checks_failed: 2, checked_at: undefined })] })).not.toBe('checks-failed')
    expect(state({ pullRequests: [pr({ checks_failed: 2, state: 'draft' })] })).toBe('checks-failed')
  })
})

describe('task state: row 3, blocked', () => {
  // cockpit-views#ac:task-state-blocked
  it('is blocked by a blocked agent, or by a failed or timed-out run', () => {
    expect(state({ agents: [agent('a', 'r1', 'live', { activity: 'blocked' })] })).toBe('blocked')
    expect(state({ agents: [run('r', 'timeout', { started_at: at(2) })] })).toBe('blocked')
    expect(state({ agents: [run('r', 'failed', { started_at: at(2) })] })).toBe('blocked')
    expect(state({ agents: [run('r', 'completed', { started_at: at(2) })] })).not.toBe('blocked')
    expect(isBlocked([], NOW)).toBe(false)
  })

  it('stops blocking once a later run or session exists for the task', () => {
    const failed = run('r', 'failed', { started_at: at(3) })
    expect(blockingRuns([failed], NOW)).toEqual([failed])
    expect(state({ agents: [failed, run('r2', 'running', { started_at: at(1) })] })).not.toBe('blocked')
    expect(state({ agents: [failed, agent('s', 'r1', 'parked', { started_at: at(1) })] })).not.toBe('blocked')
    // An agent that started earlier does not.
    expect(state({ agents: [failed, agent('s', 'r1', 'live', { started_at: at(5) })] })).toBe('blocked')
    // Without a start time on the session the order is not guessed: no later agent.
    expect(state({ agents: [failed, agent('s', 'r1', 'live')] })).toBe('blocked')
    // A failed run with no time at all is not reported as blocking.
    expect(state({ agents: [run('r', 'failed'), agent('s', 'r1', 'live')] })).not.toBe('blocked')
  })

  it('stops blocking 24 hours after the run ended, counting from finished_at, else from started_at', () => {
    expect(state({ agents: [run('r', 'failed', { started_at: at(30), finished_at: at(1) })] })).toBe('blocked')
    expect(state({ agents: [run('r', 'failed', { started_at: at(30), finished_at: at(25) })] })).not.toBe('blocked')
    expect(state({ agents: [run('r', 'failed', { started_at: at(25) })] })).not.toBe('blocked')
    expect(state({ agents: [run('r', 'failed', { started_at: at(23) })] })).toBe('blocked')
    // Strictly less than 24 hours: exactly 24 hours is over.
    expect(state({ agents: [run('r', 'failed', { started_at: at(24) })] })).not.toBe('blocked')
    expect(state({ agents: [run('r', 'failed', { finished_at: 'not a time' })] })).not.toBe('blocked')
    expect(BLOCKED_RUN_EXPIRY_MS).toBe(24 * HOUR)
  })

  it('ignores sessions in a terminal-looking state', () => {
    expect(state({ agents: [agent('s', 'r1', 'failed')] })).not.toBe('blocked')
  })
})

describe('task state: rows 4 and 5, ready and not ready', () => {
  // cockpit-views#ac:task-state-ready-to-land
  it('is ready when every open pull request is green, not a draft, and mergeable clean or has_hooks', () => {
    expect(state({ pullRequests: [pr({ id: 'a' }), pr({ id: 'b', mergeable: 'has_hooks' })] })).toBe('ready')
    // One that is blocked, and one that is a draft, make the task not ready.
    expect(state({ pullRequests: [pr({ id: 'a' }), pr({ id: 'b', mergeable: 'blocked' })] })).toBe('not-ready')
    expect(state({ pullRequests: [pr({ id: 'a' }), pr({ id: 'b', state: 'draft' })] })).toBe('not-ready')
  })

  // cockpit-views#ac:task-state-not-ready
  it('is not ready for a draft, pending checks, a green pull request that cannot merge, and no verdict', () => {
    expect(state({ pullRequests: [pr({ state: 'draft' })] })).toBe('not-ready')
    expect(state({ pullRequests: [pr({ mergeable: 'dirty' })] })).toBe('not-ready')
    expect(state({ pullRequests: [pr({ mergeable: 'behind' })] })).toBe('not-ready')
    expect(state({ pullRequests: [pr({ checks_pending: 2, checks_green: false })] })).toBe('not-ready')
    expect(state({ pullRequests: [pr({ checks_failed: 0, checks_pending: 0, checks_green: false })] })).toBe('not-ready')
    expect(state({ pullRequests: [pr({ mergeable: undefined })] })).toBe('not-ready')
    expect(state({ pullRequests: [pr({ checks_green: undefined })] })).toBe('not-ready')
  })

  it('reads a count that is not reported as none', () => {
    const bare = pr({ checks_failed: undefined, checks_pending: undefined, checks_total: undefined, checks_passed: undefined })
    expect(state({ pullRequests: [bare] })).toBe('ready')
    expect(notReadyReasons({ ...bare, checks_green: false })).toEqual(['review'])
    expect(state({ pullRequests: [{ ...bare, checks_green: false }] })).toBe('not-ready')
  })

  it('does not count a merged or closed pull request as open', () => {
    expect(state({ pullRequests: [pr({ state: 'merged' })] })).toBe('landed')
    expect(state({ pullRequests: [pr({ state: 'closed' })] })).toBe('idle')
    expect(isOpenPullRequest(pr({ state: 'open' }))).toBe(true)
    expect(isOpenPullRequest(pr({ state: 'draft' }))).toBe(true)
    expect(isOpenPullRequest(pr({ state: undefined }))).toBe(false)
  })

  it('treats a pull request with no checked_at as never observed: it takes no part in row 2', () => {
    const unobserved = pr({ checked_at: undefined })
    expect(isObserved(unobserved)).toBe(false)
    expect(isObserved(pr())).toBe(true)
    expect(openObserved([unobserved, pr({ id: 'q' })]).map((p) => p.id)).toEqual(['q'])
    expect(state({ pullRequests: [pr({ checks_failed: 2, checked_at: undefined })] })).toBe('not-ready')
    // With no state either (never observed at all) it is not an open pull request: the PR input is not reported.
    const nothing = pr({ checked_at: undefined, state: undefined })
    expect(state({ pullRequests: [nothing], worktrees: [wt()] })).toBe('not-reported')
    expect(state({ pullRequests: [pr({ checked_at: undefined, state: 'merged' })], worktrees: [wt()] })).toBe('not-reported')
  })

  // cockpit-views#ac:task-state-ignores-unobserved-pull-requests
  it('is not ready while one open pull request is unobserved, and idle when the only one has no state', () => {
    const unobserved = pr({ id: 'u', checked_at: undefined })
    expect(state({ pullRequests: [pr({ id: 'a' }), unobserved] })).toBe('not-ready')
    expect(notReadyReasons(unobserved)).toEqual(['pull request not yet checked'])
    expect(state({ pullRequests: [pr({ id: 'a' })] })).toBe('ready')
    expect(state({ pullRequests: [pr({ id: 'n', checked_at: undefined, state: undefined })], worktrees: [wt({ owner_state: 'idle' })] })).toBe('idle')
  })

  it('says why a pull request is not ready, only from the list of reasons, and never nothing for a not-ready one', () => {
    expect(notReadyReasons(pr())).toEqual([])
    expect(notReadyReasons(pr({ mergeable: 'has_hooks' }))).toEqual([])
    expect(notReadyReasons(pr({ state: 'draft', mergeable: 'draft' }))).toEqual(['draft'])
    expect(notReadyReasons(pr({ mergeable: 'draft' }))).toEqual(['draft'])
    expect(notReadyReasons(pr({ checks_failed: 1, checks_green: false }))).toEqual(['checks failed'])
    expect(notReadyReasons(pr({ checks_pending: 2, checks_green: false }))).toEqual(['checks pending'])
    // A verdict reported as not green, with nothing failed or pending, is a review.
    expect(notReadyReasons(pr({ checks_green: false }))).toEqual(['review'])
    // An absent verdict is not a review: it is not reported.
    expect(notReadyReasons(pr({ checks_green: undefined }))).toEqual(['checks not reported'])
    expect(notReadyReasons(pr({ mergeable: 'dirty' }))).toEqual(['not mergeable'])
    expect(notReadyReasons(pr({ mergeable: 'behind' }))).toEqual(['behind'])
    expect(notReadyReasons(pr({ mergeable: 'blocked' }))).toEqual(['review'])
    expect(notReadyReasons(pr({ mergeable: 'unstable' }))).toEqual(['unstable'])
    // An unknown or absent merge state is said to be not reported.
    expect(notReadyReasons(pr({ mergeable: 'unknown' }))).toEqual(['merge state not reported'])
    expect(notReadyReasons(pr({ mergeable: undefined }))).toEqual(['merge state not reported'])
    expect(notReadyReasons(pr({ checks_green: false, mergeable: 'blocked' }))).toEqual(['review'])
    for (const mergeable of [undefined, 'unknown', 'blocked', 'dirty', 'behind', 'unstable', 'draft'] as const) {
      const one = pr({ mergeable })
      expect(state({ pullRequests: [one] })).toBe('not-ready')
      expect(notReadyReasons(one).length).toBeGreaterThan(0)
      for (const reason of notReadyReasons(one)) expect(NOT_READY_REASONS as readonly string[]).toContain(reason)
    }
  })
})

describe('task state: rows 6 to 9', () => {
  // cockpit-views#ac:task-state-working
  it('is working for a working agent, a running dispatched run, or an active owner', () => {
    expect(state({ agents: [agent('a', 'r1', 'live', { activity: 'working' })] })).toBe('working')
    expect(state({ agents: [run('r', 'running')] })).toBe('working')
    expect(state({ worktrees: [wt({ owner_state: 'active' })] })).toBe('working')
    // A live session with no activity says nothing about work.
    expect(state({ agents: [agent('a', 'r1', 'live')] })).toBe('not-reported')
  })

  // cockpit-views#ac:task-state-landed
  it('is landed for a merged pull request, or every worktree merged, and not with an open one', () => {
    expect(state({ pullRequests: [pr({ state: 'merged' })] })).toBe('landed')
    expect(state({ worktrees: [wt({ lifecycle: 'merged' }), wt({ id: 'w2', lifecycle: 'merged' })] })).toBe('landed')
    expect(state({ worktrees: [wt({ lifecycle: 'merged' }), wt({ id: 'w2', lifecycle: 'in_progress' })] })).not.toBe('landed')
    expect(state({ pullRequests: [pr({ id: 'a', state: 'merged' }), pr({ id: 'b' })] })).not.toBe('landed')
    expect(state({})).not.toBe('landed')
  })

  it('is not landed while a worktree of the task holds unpushed work', () => {
    const merged = wt({ lifecycle: 'merged' })
    expect(state({ worktrees: [merged, wt({ id: 'w2', lifecycle: 'merged', ahead: 1 })] })).not.toBe('landed')
    expect(state({ worktrees: [merged, wt({ id: 'w3', lifecycle: 'merged', has_upstream: false })] })).not.toBe('landed')
    expect(state({ pullRequests: [pr({ state: 'merged' })], worktrees: [wt({ ahead: 2 })] })).not.toBe('landed')
    expect(state({ pullRequests: [pr({ state: 'merged' })], worktrees: [wt({ ahead: 0, has_upstream: true })] })).toBe('landed')
    expect(hasUnpushedWork(wt({ ahead: 0 }))).toBe(false)
  })

  // cockpit-views#ac:task-state-idle
  it('is idle when something is reported and nothing else applies', () => {
    expect(state({ worktrees: [wt({ owner_state: 'idle' })] })).toBe('idle')
    expect(state({ agents: [agent('a', 'r1', 'live', { activity: 'done' })] })).toBe('idle')
    expect(state({ pullRequests: [pr({ state: 'closed' })] })).toBe('idle')
  })

  // cockpit-views#ac:task-state-not-reported
  it('is not reported when no owner_state, pull request state or agent activity is reported', () => {
    expect(state({ worktrees: [wt()] })).toBe('not-reported')
    expect(state({ worktrees: [wt()], pullRequests: [pr({ state: undefined, checked_at: undefined })], agents: [agent('a', 'r1', 'live')] })).toBe('not-reported')
    expect(taskStateInfo('not-reported').label).toBe('state not reported')
  })
})

describe('the table', () => {
  // cockpit-views#ac:task-state-is-worst-first
  it('orders the nine states worst first by rank, and each has its label', () => {
    expect(TASK_STATES.map((info) => info.label)).toEqual([
      'at risk',
      'checks failed',
      'blocked',
      'ready to land',
      'not ready',
      'working',
      'landed',
      'idle',
      'state not reported',
    ])
    expect(TASK_STATES.map((info) => info.rank)).toEqual([0, 1, 2, 3, 4, 5, 6, 7, 8])
    expect(TASK_STATE_IDS).toEqual(['at-risk', 'checks-failed', 'blocked', 'ready', 'not-ready', 'working', 'landed', 'idle', 'not-reported'])
    const sorted = [...TASK_STATES].reverse().sort((a, b) => a.rank - b.rank)
    expect(sorted.map((info) => info.id)).toEqual(TASK_STATE_IDS)
  })

  it('reads start times, and none when they are absent or invalid', () => {
    expect(startedAt({ started_at: OBSERVED } as Agent)).toBe(Date.parse(OBSERVED))
    expect(startedAt({} as Agent)).toBeUndefined()
    expect(startedAt({ started_at: 'x' } as Agent)).toBeUndefined()
  })
})


describe('task state: remote entries never decide a local task\'s good states (REQ:task-state trust rule)', () => {
  // cockpit-views#ac:task-state-ready-to-land, cockpit-views#ac:task-state-landed
  const cached = { route: 'cached' as const, machine: 'vm', machine_id: 'mach-vm' }
  const liveRemote = { route: 'live-remote' as const, machine: 'vm', machine_id: 'mach-vm' }
  const local = (): Worktree => wt({ id: 'mine' })

  it('names where the state is decided: any entry of this machine makes it local', () => {
    expect(stateSourceOf(inputs({ worktrees: [local()] }))).toBe('local')
    expect(stateSourceOf(inputs({ pullRequests: [pr()] }))).toBe('local')
    expect(stateSourceOf(inputs({ agents: [agent('a', 'r1', 'live')] }))).toBe('local')
    expect(stateSourceOf(inputs({ worktrees: [wt(cached)], pullRequests: [pr(liveRemote)] }))).toBe('remote')
    expect(hasLocalEntry(inputs({ worktrees: [wt(cached)], agents: [agent('a', 'r1', 'live', cached), agent('b', 'r1', 'live')] }))).toBe(true)
    expect(hasLocalEntry(inputs())).toBe(false)
  })

  it('row 4: a ready remote pull request alone does not make a local task ready', () => {
    for (const remote of [cached, liveRemote]) {
      expect(state({ worktrees: [local()], pullRequests: [pr(remote)] })).toBe('not-ready')
      // With a ready local one beside it, the task is ready.
      expect(state({ worktrees: [local()], pullRequests: [pr({ id: 'l' }), pr(remote)] })).toBe('ready')
      // A remote open pull request that is not ready still blocks a local ready one.
      expect(state({ worktrees: [local()], pullRequests: [pr({ id: 'l' }), pr({ ...remote, id: 'r', mergeable: 'blocked' })] })).toBe('not-ready')
    }
  })

  it('row 4: a local entry that is not a pull request still trusts only local pull requests', () => {
    expect(state({ agents: [agent('a', 'r1', 'live')], pullRequests: [pr(cached)] })).toBe('not-ready')
  })

  it('row 7: a merged remote pull request or merged remote worktrees do not land a local task', () => {
    expect(state({ worktrees: [local()], pullRequests: [pr({ ...cached, state: 'merged' })] })).toBe('idle')
    expect(state({ worktrees: [local(), wt({ ...cached, id: 'r', lifecycle: 'merged' })] })).toBe('not-reported')
    // The same evidence on this machine lands it.
    expect(state({ worktrees: [local()], pullRequests: [pr({ state: 'merged' })] })).toBe('landed')
    expect(state({ worktrees: [wt({ lifecycle: 'merged' }), wt({ ...cached, id: 'r', lifecycle: 'working' })] })).toBe('landed')
    // The unpushed veto reads local worktrees only: a remote one does not veto, a local one does.
    expect(state({ worktrees: [wt({ lifecycle: 'merged' }), wt({ ...cached, id: 'r', ahead: 4 })] })).toBe('landed')
    expect(state({ worktrees: [wt({ lifecycle: 'merged', ahead: 1 })], pullRequests: [pr({ state: 'merged' })] })).toBe('idle')
  })

  it('rows 2, 3, 5 and 6: a remote entry may still worsen the state or add working', () => {
    expect(state({ worktrees: [local()], pullRequests: [pr({ id: 'l' }), pr({ ...cached, id: 'r', checks_failed: 1, checks_green: false })] })).toBe('checks-failed')
    expect(state({ worktrees: [local()], pullRequests: [pr({ ...cached, mergeable: 'dirty' }), pr({ id: 'l' })] })).toBe('not-ready')
    expect(state({ worktrees: [local()], agents: [agent('a', 'r1', 'live', { ...cached, activity: 'blocked' })] })).toBe('blocked')
    expect(state({ worktrees: [local()], agents: [agent('a', 'r1', 'live', { ...cached, activity: 'working' })] })).toBe('working')
    expect(state({ worktrees: [local(), wt({ ...cached, id: 'r', owner_state: 'active' })] })).toBe('working')
  })

  it('a task with no local entry is computed from its remote entries, as before', () => {
    expect(state({ worktrees: [wt(cached)], pullRequests: [pr(cached)] })).toBe('ready')
    expect(state({ worktrees: [wt(cached)], pullRequests: [pr({ ...cached, state: 'merged' })] })).toBe('landed')
    expect(state({ worktrees: [wt({ ...cached, lifecycle: 'merged' })] })).toBe('landed')
    expect(state({ worktrees: [wt(cached)], pullRequests: [pr({ ...cached, checks_failed: 1 })] })).toBe('checks-failed')
    expect(state({ worktrees: [wt({ ...cached, owner_state: 'active' })] })).toBe('working')
  })
})
