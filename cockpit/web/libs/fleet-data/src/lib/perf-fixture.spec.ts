import { PERF_COUNTS, PERF_NOW, performanceFixture, repeatRows, seeded } from './perf-fixture'
import { FleetModel } from './fleet-model'
import { buildRepositories } from './model-repositories'
import { isFleetDocument } from './fleet-client'
import { buildCleanup } from './home-details'

describe('the performance fixture', () => {
  const fixture = performanceFixture()

  // cockpit-views#ac:fleet-document-fits-the-budget (the shape the budget is measured on)
  it('has 500 repository entries, 600 worktrees, 4,000 branches and 3 machines', () => {
    const { document, branches } = fixture
    expect(document.repositories).toHaveLength(PERF_COUNTS.repositories)
    expect(document.worktrees).toHaveLength(PERF_COUNTS.worktrees)
    expect(document.machines).toHaveLength(PERF_COUNTS.machines)
    expect([...branches.values()].reduce((total, list) => total + list.length, 0)).toBe(PERF_COUNTS.branches)
    expect(isFleetDocument(document)).toBe(true)
    expect(Object.keys(document)).not.toContain('branches')
  })

  it('has realistic names, and code_index entries that carry statistics', () => {
    const { document } = fixture
    for (const repository of document.repositories) expect(repository.name).toMatch(/^[a-z-]+\/[a-z0-9-]+$/)
    const withStatistics = document.repositories.filter((repository) => repository.code_index?.[0].statistics?.indexed)
    expect(withStatistics.length).toBeGreaterThan(100)
    const [first] = withStatistics
    expect(first.code_index?.[0].statistics?.kinds.length).toBeGreaterThan(3)
    expect(document.repositories.some((repository) => repository.code_index?.[0].state === 'never' && repository.code_index[0].statistics?.kinds.length === 0)).toBe(true)
    expect(new Set(document.worktrees.map((worktree) => worktree.task)).size).toBe(455)
    // Cached machines carry no sync facts and no code index.
    const cached = document.worktrees.filter((worktree) => worktree.route === 'cached')
    expect(cached.length).toBe(100)
    expect(cached.every((worktree) => worktree.ahead === undefined && worktree.code_index === undefined)).toBe(true)
  })

  it('merges to about the size of the founder fleet, and every worktree and branch points at a real repository', () => {
    const model = new FleetModel(fixture.document, { now: () => PERF_NOW })
    expect(buildRepositories(model)).toHaveLength(445)
    expect(model.tasks).toHaveLength(455)
    const ids = new Set(fixture.document.repositories.map((repository) => repository.id))
    expect(fixture.document.worktrees.every((worktree) => ids.has(worktree.repository))).toBe(true)
    expect([...fixture.branches.keys()].every((id) => ids.has(id))).toBe(true)
  })

  // cockpit-views#ac:needs-you-lists-recent-work-at-risk-only
  it('looks like a real fleet: a handful of tasks need you, most at-risk work is old, and the badge is not a debt counter', () => {
    const model = new FleetModel(fixture.document, { now: () => PERF_NOW })
    const atRisk = model.tasks.filter((task) => task.state === 'at-risk')
    expect(atRisk.length).toBeGreaterThan(100)
    expect(model.homeBadge).toBeGreaterThanOrEqual(3)
    expect(model.homeBadge).toBeLessThanOrEqual(15)
    expect(model.homeBadgeLabel).toBe(String(model.homeBadge))
    const recent = atRisk.filter((task) => task.lastActivityAt !== undefined && PERF_NOW - task.lastActivityAt <= 14 * 86_400_000)
    expect(recent.length).toBeLessThan(atRisk.length / 10)
    expect(model.needsYou.items.filter((item) => item.kind === 'work-at-risk')).toHaveLength(recent.length)
    // The old at-risk work is the Cleanup line's.
    expect(buildCleanup(model).lookCount).toBeGreaterThan(atRisk.length - recent.length)
  })

  // cockpit-views#ac:worktree-pr-join-is-one
  it('names most branches after the task and about a third otherwise, as agent/, codex/ or fix/ branches', () => {
    const { worktrees } = fixture.document
    const named = worktrees.filter((worktree) => worktree.branch === worktree.task)
    expect(named.length / worktrees.length).toBeGreaterThan(0.55)
    expect(named.length / worktrees.length).toBeLessThan(0.75)
    const others = worktrees.filter((worktree) => worktree.branch !== worktree.task)
    expect(others.every((worktree) => /^(agent|codex|fix)\/[a-z-]+-[a-z]+-\d+$/.test(worktree.branch))).toBe(true)
    expect(new Set(others.map((worktree) => worktree.branch.split('/')[0]))).toEqual(new Set(['agent', 'codex', 'fix']))
    expect(worktrees.some((worktree) => worktree.branch.startsWith('task/'))).toBe(false)
    // A pull request carries the branch of its worktree.
    for (const pullRequest of fixture.document.pull_requests) expect(worktrees.find((worktree) => worktree.id === pullRequest.worktree)?.branch).toBe(pullRequest.branch)
  })

  it('serves metrics in each route', () => {
    const { metrics } = fixture
    expect(metrics.get('mach-mac')).toMatchObject({ route: 'local' })
    expect(metrics.get('mach-mac')?.samples).toHaveLength(360)
    expect(metrics.get('mach-vm')).toMatchObject({ route: 'live-remote' })
    expect(metrics.get('mach-old')?.samples).toHaveLength(1)
  })

  it('is the same data every time from the same seed, and other data from another', () => {
    expect(JSON.stringify(performanceFixture().document)).toBe(JSON.stringify(fixture.document))
    expect(JSON.stringify(performanceFixture(7).document)).not.toBe(JSON.stringify(fixture.document))
    const random = seeded(1)
    const values = Array.from({ length: 1000 }, random)
    expect(values.every((value) => value >= 0 && value < 1)).toBe(true)
    expect(new Set(values).size).toBe(1000)
  })

  it('repeats rows under new ids', () => {
    const rows = repeatRows([{ id: 'a' }, { id: 'b' }], 5)
    expect(rows.map((row) => row.id)).toEqual(['a#0', 'b#1', 'a#2', 'b#3', 'a#4'])
  })
})
