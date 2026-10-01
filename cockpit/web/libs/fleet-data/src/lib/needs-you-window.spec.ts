import { FleetDocument, PullRequest, Worktree } from './fleet.types'
import { BADGE_CAP, FleetModel, NEEDS_YOU_WINDOW_DAYS, badgeLabel } from './fleet-model'
import { buildCleanup } from './home-details'
import { buildTaskRows, buildWorktreeRows } from './list-rows'
import { OBSERVED, agent, fleetDocument, machine, pullRequest, repository, run, worktree } from './test-data'

const NOW = Date.parse(OBSERVED)
const DAY = 86_400_000
const ago = (days: number): string => new Date(NOW - days * DAY).toISOString()

function modelOf(extra: Partial<FleetDocument>): FleetModel {
  return new FleetModel(fleetDocument({ worktrees: [], agents: [], repositories: [repository('r1', 'alpha', { name: 'sneat-dev/wb' })], pull_requests: [], machines: [machine('alpha')], ...extra }), { now: () => NOW })
}

/** A worktree at risk (unpushed work, owner idle) of `task`, last active `days` ago. */
function risky(id: string, task: string, days: number | undefined, extra: Partial<Worktree> = {}): Worktree {
  return { ...worktree(id, 'r1', 'alpha'), task, owner_state: 'idle', ahead: 1, last_activity_at: days === undefined ? undefined : ago(days), ...extra }
}

const pr = (id: string, worktreeId: string, extra: Partial<PullRequest> = {}): PullRequest => pullRequest(id, 'r1', worktreeId, { number: Number(id.replace(/\D/g, '')) || 1, ...extra })

describe('"Needs you" is a signal, not a debt counter', () => {
  // cockpit-views#ac:needs-you-lists-recent-work-at-risk-only
  it('lists an at-risk task only when its last activity is within 14 days', () => {
    expect(NEEDS_YOU_WINDOW_DAYS).toBe(14)
    const model = modelOf({
      worktrees: [risky('w1', 'today', 0), risky('w2', 'edge', 14), risky('w3', 'old', 15), risky('w4', 'ancient', 200), risky('w5', 'unknown', undefined)],
    })
    expect(model.tasks.every((task) => task.state === 'at-risk')).toBe(true)
    expect(model.needsYou.items.map((item) => item.task).sort()).toEqual(['edge', 'today'])
    expect(model.homeBadge).toBe(2)
    expect(model.needsYou.more).toBe(0)
  })

  it('counts the newest activity of the task, not of one worktree', () => {
    const model = modelOf({ worktrees: [risky('w1', 'mixed', 90), risky('w2', 'mixed', 2, { owner_state: 'orphaned' })] })
    expect(model.needsYou.items.map((item) => item.task)).toEqual(['mixed'])
  })

  // cockpit-views#ac:cleanup-counts-older-at-risk-work
  it('counts the worktrees of older at-risk tasks as needing a look, and not the recent ones', () => {
    const model = modelOf({
      worktrees: [risky('w1', 'recent', 3), risky('w2', 'old', 20), risky('w3', 'old', 40, { owner_state: 'orphaned' }), risky('w4', 'older-but-pushed', 60, { ahead: 0, has_upstream: true })],
    })
    const cleanup = buildCleanup(model)
    // w4 is not at risk and idle for over 30 days, so it needs a look for that reason; w1 is in Needs you.
    expect([...cleanup.lookIds].sort()).toEqual(['w2', 'w3', 'w4'])
    expect(cleanup.lookCount).toBe(3)
    // An older at-risk worktree that is idle but not yet 30 days old is counted only because of the rule.
    const only = buildCleanup(modelOf({ worktrees: [risky('w1', 'old', 20)] }))
    expect([...only.lookIds]).toEqual(['w1'])
    expect(buildCleanup(modelOf({ worktrees: [risky('w1', 'recent', 10)] })).lookCount).toBe(0)
  })

  it('does not age-limit failed checks, blocked agents, a pull request that needs you, or the older at-risk task that also has one', () => {
    const model = modelOf({
      worktrees: [
        risky('w1', 'failing', 90),
        risky('w2', 'stuck', 90),
        risky('w3', 'blocked', 90),
        risky('w4', 'failed-run', 90),
        risky('w5', 'quiet', 90),
        { ...worktree('w6', 'r1', 'alpha'), task: 'plain-failing', owner_state: 'idle', ahead: 0, has_upstream: true, last_activity_at: ago(300) },
      ],
      pull_requests: [pr('p1', 'w1', { checks_failed: 1, checks_green: false }), pr('p2', 'w2', { mergeable: 'dirty' }), pr('p6', 'w6', { checks_failed: 1, checks_green: false })],
      agents: [agent('a3', 'r1', 'live', { activity: 'blocked', task: 'blocked' }), run('r4', 'failed', { task: 'failed-run', started_at: ago(0.5) })],
    })
    const kinds = Object.fromEntries(model.needsYou.items.map((item) => [item.task, item.kind]))
    expect(kinds).toEqual({ failing: 'pr-checks-failed', stuck: 'pr-needs-you', blocked: 'agent-blocked', 'failed-run': 'run-failed', 'plain-failing': 'pr-checks-failed' })
    expect(model.homeBadge).toBe(5)
    // The older at-risk task that has a row is still counted by Cleanup for its unpushed work.
    expect(buildCleanup(model).lookIds.has('w1')).toBe(true)
    // The one with nothing else is only a cleanup matter.
    expect(model.needsYou.items.some((item) => item.task === 'quiet')).toBe(false)
    expect(buildCleanup(model).lookIds.has('w5')).toBe(true)
  })

  it('applies the same window to "Agent finished", and to nothing else of the soft kinds', () => {
    const finished = (days: number | undefined): Partial<FleetDocument> => ({
      worktrees: [{ ...worktree('w1', 'r1', 'alpha'), task: 'a', owner_state: 'active', ahead: 1, last_activity_at: days === undefined ? undefined : ago(days) }],
      agents: [agent('a1', 'r1', 'live', { activity: 'done', task: 'a' })],
    })
    expect(modelOf(finished(1)).needsYou.items.map((item) => item.kind)).toEqual(['agent-finished'])
    expect(modelOf(finished(14)).needsYou.items.map((item) => item.kind)).toEqual(['agent-finished'])
    expect(modelOf(finished(15)).needsYou.items).toEqual([])
    expect(modelOf(finished(undefined)).needsYou.items).toEqual([])
  })

  // cockpit-views#ac:needs-you-chip-is-the-home-set
  it('is the same set as the Tasks chip needs-you, so the badge and the chip agree', () => {
    const model = modelOf({ worktrees: [risky('w1', 'recent', 2), risky('w2', 'old', 30), { ...worktree('w3', 'r1', 'alpha'), task: 'calm', owner_state: 'active', last_activity_at: ago(1) }] })
    const chipped = buildTaskRows(model).filter((row) => row.chips.has('needs-you')).map((row) => row.id)
    expect(chipped).toEqual(model.needsYou.items.map((item) => item.task))
    expect(chipped).toEqual(['recent'])
    expect(buildWorktreeRows(model).filter((row) => row.chips.has('look')).map((row) => row.id)).toEqual(['w2'])
  })
})

describe('the Home badge as shown', () => {
  // cockpit-views#ac:home-badge-is-capped
  it('shows the number up to 99 and "99+" above, and exposes both', () => {
    expect(BADGE_CAP).toBe(99)
    expect([0, 1, 98, 99, 100, 294].map(badgeLabel)).toEqual(['0', '1', '98', '99', '99+', '99+'])
    const many = (count: number): FleetModel => modelOf({ worktrees: Array.from({ length: count }, (_, index) => risky(`w${index}`, `t${index}`, 1)) })
    expect([many(99).homeBadge, many(99).homeBadgeLabel]).toEqual([99, '99'])
    expect([many(100).homeBadge, many(100).homeBadgeLabel]).toEqual([100, '99+'])
    expect(modelOf({}).homeBadgeLabel).toBe('0')
  })
})
