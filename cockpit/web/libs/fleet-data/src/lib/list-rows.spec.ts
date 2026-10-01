import { findMergedRepository } from './repository-merge'
import { emptyListQuery } from './list-query'
import { StepCounter } from './match'
import { Agent, FleetDocument, Repository, Worktree } from './fleet.types'
import { FleetModel } from './fleet-model'
import { buildRepositories } from './model-repositories'
import { ListRow, applyListQuery, buildAgentRows, buildMachineRows, buildRepositoryRows, buildTaskRows, buildWorktreeRows } from './list-rows'
import { OBSERVED, agent, fleetDocument, machine, pullRequest, repository, run, worktree } from './test-data'

const NOW = Date.parse(OBSERVED)
const DAY = 86_400_000
const ago = (days: number): string => new Date(NOW - days * DAY).toISOString()

function wt(id: string, task: string, extra: Partial<Worktree> = {}): Worktree {
  return { ...worktree(id, 'r1', 'alpha'), task, ...extra }
}

function modelOf(extra: Partial<FleetDocument>): FleetModel {
  return new FleetModel(fleetDocument({ worktrees: [], agents: [], repositories: [], pull_requests: [], machines: [machine('alpha')], ...extra }), { now: () => NOW })
}

const ids = <T>(rows: readonly ListRow<T>[]): string[] => rows.map((row) => row.id)
const query = (extra: Partial<ReturnType<typeof emptyListQuery>>) => ({ ...emptyListQuery(), ...extra })

describe('task rows', () => {
  const model = modelOf({
    repositories: [repository('r1', 'alpha', { name: 'sneat-dev/wb' }), repository('r2', 'alpha', { name: 'sneat-co/sneat-go' })],
    machines: [machine('alpha'), machine('beta', 'cached')],
    worktrees: [
      wt('w1', 'fix-ci', { last_activity_at: ago(1) }),
      { ...wt('w2', 'fix-ci', { last_activity_at: ago(2), repository: 'r2' }), machine: 'beta', machine_id: 'mach-beta', route: 'cached' },
      wt('w3', 'old-work', { last_activity_at: ago(60), owner_state: 'idle' }),
      wt('w4', 'risky', { last_activity_at: ago(5), owner_state: 'orphaned', ahead: 1 }),
      wt('w5', 'shipping', { last_activity_at: ago(3), owner_state: 'active' }),
    ],
    pull_requests: [pullRequest('p1', 'r1', 'w5', { number: 3 })],
    agents: [agent('a1', 'r1', 'live', { task: 'fix-ci', activity: 'working' })],
  })
  const rows = buildTaskRows(model)

  // cockpit-views#ac:tasks-list-aggregates-worktrees (the rows the library derives)
  it('has one row per task with its chips: needs-you is the tasks Home lists', () => {
    expect(ids(rows)).toEqual(['fix-ci', 'old-work', 'risky', 'shipping'])
    const chips = (id: string): string[] => [...(rows.find((row) => row.id === id)?.chips ?? [])].sort()
    expect(chips('fix-ci')).toEqual(['agent', 'multirepo', 'working'])
    expect(chips('risky')).toEqual(['needs-you'])
    expect(chips('shipping')).toEqual(['pr', 'ready'])
    expect(chips('old-work')).toEqual([])
    expect(ids(applyListQuery('tasks', rows, query({ chips: ['needs-you'] }), NOW).rows)).toEqual(ids(model.needsYou.items.map((item) => ({ id: item.task }) as ListRow<unknown>)))
  })

  it('filters by chip, machine, text, state and age, and by idle30, in combination', () => {
    const apply = (extra: Partial<ReturnType<typeof emptyListQuery>>) => ids(applyListQuery('tasks', rows, query(extra), NOW).rows)
    expect(apply({ chips: ['multirepo'] })).toEqual(['fix-ci'])
    expect(apply({ chips: ['agent', 'multirepo'] })).toEqual(['fix-ci'])
    expect(apply({ chips: ['agent', 'pr'] })).toEqual([])
    expect(apply({ chips: ['idle30'] })).toEqual(['old-work'])
    expect(apply({ machines: ['mach-beta'] })).toEqual(['fix-ci'])
    expect(apply({ q: 'sneat-co/*' })).toEqual(['fix-ci'])
    expect(apply({ q: 'state:idle' })).toEqual(['old-work'])
    expect(apply({ q: 'state:at-risk' })).toEqual(['risky'])
    expect(apply({ q: 'age:31-90d' })).toEqual(['old-work'])
    expect(apply({ q: 'repo:sneat-dev/wb -task:fix*' })).toEqual(['shipping', 'risky', 'old-work'])
    expect(apply({ q: 'machine:beta' })).toEqual(['fix-ci'])
  })

  it('reports the number before filtering as the total', () => {
    expect(applyListQuery('tasks', rows, query({ q: 'risky' }), NOW)).toMatchObject({ total: 4 })
    expect(applyListQuery('tasks', rows, query({ q: 'risky' }), NOW).rows).toHaveLength(1)
  })

  // cockpit-views#ac:task-state-is-worst-first
  it('sorts by state worst first, one task per state', () => {
    const states = modelOf({
      repositories: [repository('r1', 'alpha')],
      worktrees: [
        wt('w1', 'at-risk', { owner_state: 'orphaned', ahead: 1 }),
        wt('w2', 'failed'),
        wt('w3', 'blocked'),
        wt('w4', 'ready'),
        wt('w5', 'not-ready'),
        wt('w6', 'working', { owner_state: 'active' }),
        wt('w7', 'landed', { lifecycle: 'merged' }),
        wt('w8', 'idle', { owner_state: 'idle' }),
        wt('w9', 'unreported'),
      ],
      pull_requests: [pullRequest('p2', 'r1', 'w2', { checks_failed: 1 }), pullRequest('p4', 'r1', 'w4'), pullRequest('p5', 'r1', 'w5', { state: 'draft' })],
      agents: [agent('a3', 'r1', 'live', { task: 'blocked', activity: 'blocked' })],
    })
    const sorted = applyListQuery('tasks', buildTaskRows(states), query({ sort: 'state' }), NOW).rows.map((row) => row.item.state)
    expect(sorted).toEqual(['at-risk', 'checks-failed', 'blocked', 'ready', 'not-ready', 'working', 'landed', 'idle', 'not-reported'])
    const reversed = applyListQuery('tasks', buildTaskRows(states), query({ sort: 'state', dir: 'desc' }), NOW).rows.map((row) => row.item.state)
    expect(reversed).toEqual([...sorted].reverse())
  })

  it('sorts by last activity newest first by default, and by the other listed columns', () => {
    expect(ids(applyListQuery('tasks', rows, emptyListQuery(), NOW).rows)).toEqual(['fix-ci', 'shipping', 'risky', 'old-work'])
    expect(ids(applyListQuery('tasks', rows, query({ sort: 'activity', dir: 'asc' }), NOW).rows)).toEqual(['old-work', 'risky', 'shipping', 'fix-ci'])
    expect(ids(applyListQuery('tasks', rows, query({ sort: 'task' }), NOW).rows)).toEqual(['fix-ci', 'old-work', 'risky', 'shipping'])
    expect(ids(applyListQuery('tasks', rows, query({ sort: 'worktrees' }), NOW).rows)[0]).toBe('fix-ci')
  })

  it('ignores a sort column the page does not list, and keeps a missing value last in both directions', () => {
    expect(ids(applyListQuery('tasks', rows, query({ sort: 'cpu' }), NOW).rows)).toEqual(ids(applyListQuery('tasks', rows, emptyListQuery(), NOW).rows))
    const blank = buildTaskRows(modelOf({ worktrees: [wt('w1', 'a', { last_activity_at: undefined }), wt('w2', 'b', { last_activity_at: ago(1) }), wt('w3', 'c', { last_activity_at: undefined })] }))
    expect(ids(applyListQuery('tasks', blank, query({ sort: 'activity', dir: 'asc' }), NOW).rows)).toEqual(['b', 'a', 'c'])
    expect(ids(applyListQuery('tasks', blank, query({ sort: 'activity', dir: 'desc' }), NOW).rows)).toEqual(['b', 'a', 'c'])
  })
})

describe('repository rows', () => {
  const repos: Repository[] = [
    repository('l1', 'alpha', { name: 'sneat-co/sneat-go', last_activity_at: ago(1), local_branch_count: 5, remote_branch_count: 2, worktree_count: 3, active_agent_count: 1, open_pull_request_count: 2, code_index: [{ indexer: 'cg', state: 'stale' }] }),
    { ...repository('c1', 'beta', { name: 'sneat-co/sneat-go', route: 'cached', worktree_count: 1, active_agent_count: undefined }), machine_id: 'mach-beta', code_index: undefined },
    repository('l2', 'alpha', { name: 'strongo/dalgo', last_activity_at: ago(30), worktree_count: 0, active_agent_count: undefined, error: 'scan_failed', code_index: [{ indexer: 'cg', state: 'fresh' }] }),
    repository('l3', 'alpha', { name: 'a/none', last_activity_at: undefined, worktree_count: 0, active_agent_count: undefined }),
    repository('l4', 'alpha', { name: 'a/local-only', last_activity_at: ago(40), worktree_count: 0, active_agent_count: undefined, local_branch_count: 2 }),
    repository('l5', 'alpha', { name: 'a/remote-only', last_activity_at: ago(50), worktree_count: 0, active_agent_count: undefined, remote_branch_count: 9 }),
  ]
  const model = modelOf({ repositories: repos, machines: [machine('alpha'), machine('beta', 'cached')] })
  const rows = buildRepositoryRows(model)

  it('has one row per identity, selected by the entry id of its preferred checkout, with its chips', () => {
    expect(ids(rows)).toEqual(['l1', 'l2', 'l3', 'l4', 'l5'])
    expect(rows[0].machineIds).toEqual(['mach-alpha', 'mach-beta'])
    expect([...rows[0].chips].sort()).toEqual(['agents', 'index', 'prs', 'worktrees'])
    expect([...rows[1].chips].sort()).toEqual(['errors'])
    expect(findMergedRepository(buildRepositories(model), 'c1')?.key).toBe('sneat-co/sneat-go')
    expect(findMergedRepository(buildRepositories(model), 'l1')?.key).toBe('sneat-co/sneat-go')
    expect(findMergedRepository(buildRepositories(model), 'nope')).toBeUndefined()
  })

  // cockpit-views#ac:repositories-quick-filters (the filters the library derives)
  it('filters by chip, state and machine', () => {
    const apply = (extra: Partial<ReturnType<typeof emptyListQuery>>) => ids(applyListQuery('repositories', rows, query(extra), NOW).rows)
    expect(apply({ chips: ['index'] })).toEqual(['l1'])
    expect(apply({ chips: ['errors'] })).toEqual(['l2'])
    expect(apply({ q: 'state:stale' })).toEqual(['l1'])
    expect(apply({ q: 'state:fresh' })).toEqual(['l2'])
    expect(apply({ machines: ['mach-beta'] })).toEqual(['l1'])
    expect(apply({ q: 'machine:beta' })).toEqual(['l1'])
    expect(apply({ q: 'sneat-*/*-go' })).toEqual(['l1'])
  })

  // cockpit-views#ac:repositories-sort-presets (the presets the library derives)
  it('sorts by the presets: activity, worktrees and branches, biggest or newest first, an unknown value last', () => {
    expect(ids(applyListQuery('repositories', rows, emptyListQuery(), NOW).rows)).toEqual(['l1', 'l2', 'l4', 'l5', 'l3'])
    expect(ids(applyListQuery('repositories', rows, query({ sort: 'activity' }), NOW).rows)).toEqual(['l1', 'l2', 'l4', 'l5', 'l3'])
    expect(ids(applyListQuery('repositories', rows, query({ sort: 'worktrees' }), NOW).rows)).toEqual(['l1', 'l2', 'l3', 'l4', 'l5'])
    // Branches: 7 on l1, 9 on l5 (remote only), 2 on l4 (local only), none known elsewhere.
    expect(ids(applyListQuery('repositories', rows, query({ sort: 'branches' }), NOW).rows)).toEqual(['l5', 'l1', 'l4', 'l2', 'l3'])
    expect(ids(applyListQuery('repositories', rows, query({ sort: 'repository' }), NOW).rows)).toEqual(['l4', 'l3', 'l5', 'l1', 'l2'])
    expect(ids(applyListQuery('repositories', rows, query({ sort: 'branches', dir: 'asc' }), NOW).rows)).toEqual(['l4', 'l1', 'l5', 'l2', 'l3'])
  })
})

describe('worktree rows', () => {
  const doc: Partial<FleetDocument> = {
    repositories: [repository('r1', 'alpha', { name: 'sneat-dev/wb' })],
    worktrees: [
      wt('w1', 'fix-ci', { branch: 'topic', owner_state: 'active', last_activity_at: ago(1) }),
      wt('w2', 'cleanup', { lifecycle: 'merged', ahead: 0, owner_state: 'idle', last_activity_at: ago(40), upstream_gone: true }),
      wt('w3', 'risky', { owner_state: 'orphaned', ahead: 2, last_activity_at: ago(3) }),
      wt('w4', 'bare', { last_activity_at: undefined }),
      wt('w5', 'by-branch', { branch: 'task/by-branch', last_activity_at: ago(2), repository: 'r-missing' }),
    ],
    pull_requests: [pullRequest('p1', 'r1', 'w1'), pullRequest('p2', 'r-missing', undefined, { branch: 'task/by-branch', worktree: undefined })],
  }
  const model = modelOf(doc)
  const rows = buildWorktreeRows(model)

  // cockpit-views#ac:worktrees-quick-filters (the chips the library derives)
  it('has the chips active, orphaned, unpushed, gone, pr, safe and look, and idle30 by the clock', () => {
    const chips = (id: string): string[] => [...(rows.find((row) => row.id === id)?.chips ?? [])].sort()
    expect(chips('w1')).toEqual(['active', 'pr'])
    expect(chips('w2')).toEqual(['gone', 'safe'])
    expect(chips('w3')).toEqual(['look', 'orphaned', 'unpushed'])
    expect(chips('w4')).toEqual([])
    expect(chips('w5')).toEqual(['pr'])
    const apply = (extra: Partial<ReturnType<typeof emptyListQuery>>) => ids(applyListQuery('worktrees', rows, query(extra), NOW).rows)
    expect(apply({ chips: ['idle30'] })).toEqual(['w2'])
    expect(apply({ chips: ['safe'] })).toEqual(['w2'])
    expect(apply({ chips: ['look'] })).toEqual(['w3'])
  })

  // cockpit-views#ac:matcher-limits-and-bare-fields (on the Worktrees page)
  it('searches the task, repository without its host and branch for a bare term', () => {
    const apply = (q: string) => ids(applyListQuery('worktrees', rows, query({ q }), NOW).rows)
    expect(apply('topic')).toEqual(['w1'])
    expect(apply('github')).toEqual([])
    expect(apply('sneat-dev/wb')).toEqual(['w1', 'w3', 'w2', 'w4'].filter((id) => ['w1', 'w3', 'w2', 'w4'].includes(id)))
    expect(apply('state:orphaned')).toEqual(['w3'])
    expect(apply('state:orph')).toEqual([])
    expect(apply('branch:task/*')).toEqual(['w5'])
    expect(apply('age:<1d')).toEqual([])
    expect(apply('age:1-7d')).toEqual(['w1', 'w5', 'w3'])
    // The 20-term filter applies only its first 16 terms: the failing last ones are ignored.
    expect(apply(`${Array.from({ length: 16 }, () => 'topic').join(' ')} ${Array.from({ length: 4 }, () => 'zzz').join(' ')}`)).toEqual(['w1'])
    expect(apply(`${'topic '.repeat(43)}zzz`)).toEqual(['w1'])
  })

  it('sorts by worktree, state, machine and activity; an owner that is not reported comes last', () => {
    const apply = (extra: Partial<ReturnType<typeof emptyListQuery>>) => ids(applyListQuery('worktrees', rows, query(extra), NOW).rows)
    expect(apply({ sort: 'state' })).toEqual(['w1', 'w2', 'w3', 'w4', 'w5'])
    expect(apply({ sort: 'worktree' })).toEqual(['w4', 'w5', 'w2', 'w1', 'w3'])
    expect(apply({ sort: 'machine' })).toEqual(['w1', 'w2', 'w3', 'w4', 'w5'])
  })
})

describe('agent rows', () => {
  const agents: Agent[] = [
    run('r1', 'running', { task: 'fix-ci', started_at: ago(0.5), activity: 'working', runtime: 'claude', model: 'opus' }),
    agent('s1', 'r1', 'live', { runtime: 'codex', model: 'gpt-5', started_at: ago(0.2), activity: 'blocked', task: 'fix-ci' }),
    { ...agent('s2', undefined, 'parked', { runtime: undefined, model: undefined }), machine: 'vm', machine_id: 'mach-vm', route: 'cached' },
    run('r2', 'failed', { runtime: 'claude', task: 'x', started_at: ago(3) }),
  ]
  const model = modelOf({ repositories: [repository('r1', 'alpha', { name: 'sneat-dev/wb' })], agents, worktrees: [wt('w1', 'fix-ci')], machines: [machine('alpha'), machine('vm', 'cached')] })
  const rows = buildAgentRows(model)

  it('has the chips running, blocked and runtime-<name>', () => {
    const chips = (id: string): string[] => [...(rows.find((row) => row.id === id)?.chips ?? [])].sort()
    expect(chips('r1')).toEqual(['running', 'runtime-claude'])
    expect(chips('s1')).toEqual(['blocked', 'running', 'runtime-codex'])
    expect(chips('s2')).toEqual([])
    const apply = (extra: Partial<ReturnType<typeof emptyListQuery>>) => ids(applyListQuery('agents', rows, query(extra), NOW).rows)
    expect(apply({ chips: ['running'] })).toEqual(['s1', 'r1'])
    expect(apply({ chips: ['runtime-claude'] })).toEqual(['r1', 'r2'])
    expect(apply({ chips: ['runtime-claude', 'running'] })).toEqual(['r1'])
    expect(apply({ chips: ['blocked'] })).toEqual(['s1'])
  })

  it('searches runtime, model, task and repository, and the state of either the activity or the run or session', () => {
    const apply = (q: string) => ids(applyListQuery('agents', rows, query({ q }), NOW).rows)
    expect(apply('opus')).toEqual(['r1', 'r2'])
    expect(apply('runtime:codex')).toEqual(['s1'])
    expect(apply('sneat-dev/wb')).toEqual(['s1'])
    expect(apply('task:fix-ci')).toEqual(['s1', 'r1'])
    expect(apply('state:blocked')).toEqual(['s1'])
    expect(apply('state:failed')).toEqual(['r2'])
    expect(apply('state:parked')).toEqual(['s2'])
    expect(apply('state:live')).toEqual(['s1'])
    expect(apply('machine:vm')).toEqual(['s2'])
    // Agents declare no `age`: it is plain text there.
    expect(apply('age:<1d')).toEqual([])
  })

  it('has running agents first, then the newest, by default, and sorts by label, activity, machine and start', () => {
    const apply = (extra: Partial<ReturnType<typeof emptyListQuery>>) => ids(applyListQuery('agents', rows, query(extra), NOW).rows)
    expect(apply({})).toEqual(['s1', 'r1', 'r2', 's2'])
    expect(apply({ sort: 'started' })).toEqual(['s1', 'r1', 'r2', 's2'])
    expect(apply({ sort: 'label' })).toEqual(['s2', 'r1', 'r2', 's1'])
    expect(apply({ sort: 'activity' })).toEqual(['s1', 'r1', 's2', 'r2'])
    expect(apply({ sort: 'machine' })).toEqual(['r1', 's1', 'r2', 's2'])
  })
})

describe('machine rows', () => {
  const model = modelOf({
    machines: [
      { ...machine('mac'), id: 'mach-mac', machine_id: 'mach-mac', wb_version: '0.176.0' },
      { ...machine('vm', 'cached'), wb_version: '0.170.0', observed_at: new Date(NOW - 30 * 3_600_000).toISOString() },
      { ...machine('old', 'cached'), wb_version: '0.176.0' },
    ],
  })
  const rows = buildMachineRows(model)

  // cockpit-views#ac:machines-filter-and-stale-chip (the rows the library derives)
  it('has the chips stale and outdated, and the states live, cached and stale', () => {
    const apply = (extra: Partial<ReturnType<typeof emptyListQuery>>) => ids(applyListQuery('machines', rows, query(extra), NOW).rows)
    expect(apply({ chips: ['stale'] })).toEqual(['mach-vm'])
    expect(apply({ chips: ['outdated'] })).toEqual(['mach-vm'])
    expect(apply({ q: 'state:live' })).toEqual(['mach-mac'])
    expect(apply({ q: 'state:cached' })).toEqual(['mach-old'])
    expect(apply({ q: 'vm' })).toEqual(['mach-vm'])
    expect(apply({ machines: ['mach-old'] })).toEqual(['mach-old'])
  })

  // cockpit-views#ac:default-sorts
  it('has this machine first, then by name, and sorts by machine, state and version', () => {
    const apply = (extra: Partial<ReturnType<typeof emptyListQuery>>) => ids(applyListQuery('machines', rows, query(extra), NOW).rows)
    expect(apply({})).toEqual(['mach-mac', 'mach-old', 'mach-vm'])
    expect(apply({ sort: 'machine', dir: 'desc' })).toEqual(['mach-vm', 'mach-old', 'mach-mac'])
    expect(apply({ sort: 'state' })).toEqual(['mach-old', 'mach-mac', 'mach-vm'])
    expect(apply({ sort: 'version' })).toEqual(['mach-vm', 'mach-mac', 'mach-old'])
  })
})

describe('the step counter', () => {
  it('counts the glob steps of a whole filter', () => {
    const model = modelOf({ repositories: [repository('r1', 'alpha', { name: 'sneat-dev/wb' })], worktrees: [wt('w1', 'a')] })
    const counter: StepCounter = { steps: 0 }
    applyListQuery('worktrees', buildWorktreeRows(model), query({ q: 'sneat-*/w?' }), NOW, counter)
    expect(counter.steps).toBeGreaterThan(0)
  })
})
