import { versionKey } from './list-rows'
import { buildRepositories } from './model-repositories'
import { Agent, FleetDocument, MachineMetrics, PullRequest, Worktree } from './fleet.types'
import { Derivation, FleetModel, FleetModels, RESUME_COUNT, agentTitle, compareVersions, machineLoad, parseVersion } from './fleet-model'
import { buildCleanup, buildHealth, buildThroughput, remoteErrorText, remoteFix } from './home-details'
import { buildAgentRows, buildMachineRows, buildRepositoryRows, buildTaskRows, buildWorktreeRows } from './list-rows'
import { NEEDS_YOU_VISIBLE, NeedsYouItem } from './view-types'
import { hrefOf, linkProblems } from './vocabulary'
import { OBSERVED, agent, fleetDocument, machine, pullRequest, repository, run, worktree } from './test-data'

const NOW = Date.parse(OBSERVED)
const HOUR = 3_600_000
const DAY = 24 * HOUR
const ago = (ms: number): string => new Date(NOW - ms).toISOString()

function modelOf(extra: Partial<FleetDocument>, onDerive?: (name: Derivation) => void): FleetModel {
  return new FleetModel(fleetDocument({ worktrees: [], agents: [], repositories: [], pull_requests: [], machines: [machine('alpha')], ...extra }), { now: () => NOW, onDerive })
}

/** A worktree of `task` in repository `r1`, on this machine, with the activity `ageMs` ago. */
function wt(id: string, task: string, extra: Partial<Worktree> = {}): Worktree {
  return { ...worktree(id, 'r1', 'alpha'), task, last_activity_at: ago(HOUR), ...extra }
}

function pr(id: string, worktreeId: string | undefined, extra: Partial<PullRequest> = {}): PullRequest {
  return pullRequest(id, 'r1', worktreeId, { number: Number(id.replace(/\D/g, '')) || 1, ...extra })
}

const REPOS = [repository('r1', 'alpha', { name: 'sneat-dev/wb' }), repository('r2', 'alpha', { name: 'sneat-co/sneat-go' })]

describe('lookups', () => {
  it('names a repository by its id, or by the id when the document does not list it', () => {
    const model = modelOf({ repositories: REPOS })
    expect(model.repositoryName('r1')).toBe('sneat-dev/wb')
    expect(model.repositoryName('gone')).toBe('gone')
  })

  it('finds the task of a pull request through its worktree, or its repository and branch, else none', () => {
    const model = modelOf({
      repositories: REPOS,
      worktrees: [wt('w1', 'fix-ci', { branch: 'task/fix-ci' })],
      pull_requests: [pr('p1', 'w1'), pr('p2', undefined, { branch: 'task/fix-ci' }), pr('p3', 'w-gone', { branch: 'nope' }), pr('p4', undefined, { repository: undefined, branch: undefined })],
    })
    expect(model.document.pull_requests.map((p) => model.taskOfPullRequest(p))).toEqual(['fix-ci', 'fix-ci', undefined, undefined])
  })

  it('finds the tasks of an agent: its own task, or its worktrees\' tasks, never a guess', () => {
    const model = modelOf({ worktrees: [wt('w1', 'a'), wt('w2', 'b'), wt('w3', 'a')] })
    expect(model.tasksOfAgent(agent('x', 'r1', 'live', { task: 'own', worktrees: ['w1'] }))).toEqual(['own'])
    expect(model.tasksOfAgent(agent('x', 'r1', 'live', { worktrees: ['w1', 'w2', 'w3', 'missing'] }))).toEqual(['a', 'b'])
    expect(model.tasksOfAgent(agent('x', 'r1', 'live'))).toEqual([])
  })
})

describe('tasks', () => {
  it('groups worktrees by task name and carries its repositories, machines, activity and state', () => {
    const model = modelOf({
      repositories: REPOS,
      machines: [machine('alpha'), machine('beta', 'cached')],
      worktrees: [
        wt('w1', 'fix-ci', { last_activity_at: ago(2 * HOUR) }),
        { ...worktree('w2', 'r2', 'beta'), route: 'cached', task: 'fix-ci', last_activity_at: ago(HOUR), owner_state: 'idle' },
        wt('w3', 'other', { last_activity_at: undefined }),
      ],
    })
    const [fixCi, other] = model.tasks
    expect(fixCi.name).toBe('fix-ci')
    expect(fixCi.worktrees.map((w) => w.id)).toEqual(['w1', 'w2'])
    expect(fixCi.repositories).toEqual(['sneat-dev/wb', 'sneat-co/sneat-go'])
    expect(fixCi.machines).toEqual([{ id: 'mach-alpha', name: 'alpha' }, { id: 'mach-beta', name: 'beta' }])
    expect(fixCi.lastActivityAt).toBe(NOW - HOUR)
    expect(fixCi.state).toBe('idle')
    expect(fixCi.stateInfo.label).toBe('idle')
    expect(other.lastActivityAt).toBeUndefined()
    expect(other.state).toBe('not-reported')
    expect(model.taskNamed('other')).toBe(other)
    expect(model.taskNamed('nope')).toBeUndefined()
  })

  it('attaches pull requests and agents, a task named only by an agent, and counts the unobserved pull requests', () => {
    const model = modelOf({
      repositories: REPOS,
      worktrees: [wt('w1', 'fix-ci')],
      pull_requests: [pr('p1', 'w1'), pr('p2', 'w1', { checked_at: undefined }), pr('p3', undefined, { worktree: undefined })],
      agents: [agent('a1', 'r1', 'live', { worktrees: ['w1'] }), run('r1', 'failed', { task: 'ghost', started_at: ago(HOUR) })],
    })
    const fixCi = model.taskNamed('fix-ci')
    expect(fixCi?.pullRequests.map((p) => p.id)).toEqual(['p1', 'p2'])
    expect(fixCi?.openPullRequests.map((p) => p.id)).toEqual(['p1'])
    expect(fixCi?.unobservedPullRequests).toBe(1)
    expect(fixCi?.agents.map((a) => a.id)).toEqual(['a1'])
    // An unobserved open pull request keeps the task from being ready.
    expect(fixCi?.state).toBe('not-ready')
    const ghost = model.taskNamed('ghost')
    expect(ghost?.worktrees).toEqual([])
    expect(ghost?.state).toBe('blocked')
    expect(ghost?.lastActivityAt).toBeUndefined()
  })
})

describe('Needs you', () => {
  const item = (model: FleetModel, kind: NeedsYouItem['kind']): NeedsYouItem | undefined => model.needsYou.items.find((i) => i.kind === kind)

  // cockpit-views#ac:needs-you-work-at-risk (the rows the library derives)
  it('has a work-at-risk row with the reason in words and the worktrees concerned', () => {
    const model = modelOf({
      repositories: REPOS,
      worktrees: [wt('w1', 'risky', { owner_state: 'orphaned', ahead: 2, branch: 'task/risky' }), wt('w2', 'risky', { owner_state: 'unknown', has_upstream: false, repository: 'r2' })],
    })
    const row = item(model, 'work-at-risk')
    expect(row).toMatchObject({
      task: 'risky',
      rank: 0,
      reason: '2 unpushed commits and its owner process is gone',
      worktrees: [
        { id: 'w1', branch: 'task/risky', repository: 'sneat-dev/wb' },
        { id: 'w2', branch: 'branch-w2', repository: 'sneat-co/sneat-go' },
      ],
    })
    const noUpstream = modelOf({ worktrees: [wt('w1', 't', { owner_state: 'unknown', has_upstream: false })] })
    expect(item(noUpstream, 'work-at-risk')).toMatchObject({ reason: 'a branch with no upstream and no owner process is recorded' })
    const one = modelOf({ worktrees: [wt('w1', 't', { owner_state: 'idle', ahead: 1 })] })
    expect(item(one, 'work-at-risk')).toMatchObject({ reason: '1 unpushed commit and it has no running owner' })
  })

  // cockpit-views#ac:needs-you-pr-checks-failed
  it('has a checks-failed row naming the task, owner/name#number and the failed check, linking to the pull request', () => {
    const model = modelOf({
      repositories: REPOS,
      worktrees: [wt('w1', 'fix-ci')],
      pull_requests: [pr('p12', 'w1', { number: 12, checks_failed: 1, checks_green: false, failed_check: 'go-ci / test', url: 'https://github.com/sneat-dev/wb/pull/12', checked_at: ago(HOUR) }), pr('p13', 'w1', { number: 13, checks_failed: 2, checked_at: ago(2 * HOUR) })],
    })
    expect(item(model, 'pr-checks-failed')).toMatchObject({
      task: 'fix-ci',
      rank: 1,
      repository: 'sneat-dev/wb',
      number: 13,
      pullRequestId: 'p13',
    })
    const alone = modelOf({ repositories: REPOS, worktrees: [wt('w1', 'fix-ci')], pull_requests: [pr('p12', 'w1', { number: 12, checks_failed: 1, failed_check: 'go-ci / test', url: 'https://x.test/12' })] })
    expect(item(alone, 'pr-checks-failed')).toMatchObject({ number: 12, failedCheck: 'go-ci / test', url: 'https://x.test/12', repository: 'sneat-dev/wb' })
    // A pull request with no repository in the document still has a row, with no name.
    const nameless = modelOf({ worktrees: [wt('w1', 'fix-ci')], pull_requests: [pr('p1', 'w1', { repository: undefined, checks_failed: 1 })] })
    expect(item(nameless, 'pr-checks-failed')).toMatchObject({ repository: '' })
  })

  // cockpit-views#ac:needs-you-agent-blocked
  it('has an agent-blocked row for a blocked agent, never for one whose activity is not reported', () => {
    const model = modelOf({
      repositories: REPOS,
      worktrees: [wt('w1', 'fix-ci')],
      agents: [agent('a1', 'r1', 'live', { activity: 'blocked', task: 'fix-ci', machine: 'mac' }), agent('a2', 'r1', 'live', { task: 'fix-ci' })],
    })
    expect(model.needsYou.items).toHaveLength(1)
    expect(item(model, 'agent-blocked')).toMatchObject({ task: 'fix-ci', rank: 2, agentId: 'a1', machine: 'mac', repository: 'sneat-dev/wb' })
    expect(hrefOf((item(model, 'agent-blocked') as NeedsYouItem).link)).toBe('/agents/a1')
    const unreported = modelOf({ worktrees: [wt('w1', 'fix-ci')], agents: [agent('a2', 'r1', 'live', { task: 'fix-ci' })] })
    expect(unreported.needsYou.items).toEqual([])
    const noRepository = modelOf({ worktrees: [wt('w1', 'fix-ci')], agents: [agent('a1', undefined, 'live', { activity: 'blocked', task: 'fix-ci' })] })
    expect(item(noRepository, 'agent-blocked')).toMatchObject({ repository: undefined })
  })

  // cockpit-views#ac:needs-you-run-failed
  it('has a run-failed row with the state and exit code and no free text, and none while a later agent runs', () => {
    const model = modelOf({
      worktrees: [wt('w1', 'a'), wt('w2', 'b'), wt('w3', 'c')],
      agents: [
        run('ra', 'failed', { task: 'a', exit_code: 2, machine: 'mac', started_at: ago(3 * HOUR) }),
        run('rb', 'timeout', { task: 'b', started_at: ago(3 * HOUR) }),
        run('rc', 'failed', { task: 'c', started_at: ago(3 * HOUR) }),
        agent('s', undefined, 'live', { task: 'c', started_at: ago(HOUR) }),
      ],
    })
    const rows = model.needsYou.items.filter((i) => i.kind === 'run-failed')
    expect(rows.map((row) => row.task)).toEqual(['a', 'b'])
    expect(rows[0]).toMatchObject({ runState: 'failed', exitCode: 2, machine: 'mac', agentId: 'ra' })
    expect(rows[1]).toMatchObject({ runState: 'timeout', exitCode: undefined })
    expect(JSON.stringify(rows)).not.toContain('error')
  })

  it('has a pull-request-needs-you row for a green pull request that cannot merge', () => {
    const model = modelOf({
      repositories: REPOS,
      worktrees: [wt('w1', 'a'), wt('w2', 'b'), wt('w3', 'c'), wt('w4', 'd')],
      pull_requests: [
        pr('p1', 'w1', { mergeable: 'dirty' }),
        pr('p2', 'w2', { mergeable: 'behind' }),
        pr('p3', 'w3', { mergeable: 'blocked', url: 'https://x.test/3' }),
        // Not green, or merely pending: not this kind.
        pr('p4', 'w4', { mergeable: 'blocked', checks_green: false, checks_pending: 1 }),
      ],
    })
    const rows = model.needsYou.items.filter((i) => i.kind === 'pr-needs-you')
    expect(rows.map((row) => [row.task, (row as { reason: string }).reason])).toEqual([
      ['a', 'not mergeable'],
      ['b', 'behind'],
      ['c', 'review'],
    ])
    expect(rows[2]).toMatchObject({ rank: 3, url: 'https://x.test/3', repository: 'sneat-dev/wb' })
    // No reason known: still a row, with a plain one.
    const odd = modelOf({ worktrees: [wt('w1', 'a')], pull_requests: [pr('p1', 'w1', { mergeable: 'dirty', state: 'open' })] })
    expect(odd.needsYou.items[0]).toMatchObject({ kind: 'pr-needs-you' })
  })

  it('has an agent-finished row when an agent is done, work is not pushed, no pull request is open and it has not landed', () => {
    const base = { worktrees: [wt('w1', 'a', { ahead: 1 })], agents: [agent('a1', 'r1', 'live', { activity: 'done', task: 'a', machine: 'mac' })] }
    expect(item(modelOf(base), 'agent-finished')).toMatchObject({ task: 'a', rank: 4, agentId: 'a1', machine: 'mac' })
    expect(item(modelOf({ ...base, agents: [agent('a1', 'r1', 'live', { activity: 'idle', task: 'a' })] }), 'agent-finished')).toBeDefined()
    // No unpushed work, a running mate, an open pull request or a landed task: no row.
    expect(modelOf({ ...base, worktrees: [wt('w1', 'a', { ahead: 0, has_upstream: true })] }).needsYou.items).toEqual([])
    expect(item(modelOf({ ...base, worktrees: [wt('w1', 'a', { has_upstream: false })] }), 'agent-finished')).toBeDefined()
    expect(modelOf({ ...base, pull_requests: [pr('p1', 'w1')] }).needsYou.items.map((i) => i.kind)).not.toContain('agent-finished')
    // Merged and pushed: landed, nothing to do. Merged with work still unpushed is not landed: the finished agent is the lead.
    expect(modelOf({ ...base, worktrees: [wt('w1', 'a', { ahead: 0, has_upstream: true })], pull_requests: [pr('p1', 'w1', { state: 'merged' })] }).needsYou.items).toEqual([])
    expect(item(modelOf({ ...base, pull_requests: [pr('p1', 'w1', { state: 'merged' })] }), 'agent-finished')).toBeDefined()
  })

  // cockpit-views#ac:needs-you-is-capped-and-empty-line
  it('is one row per task, in the order of the kinds then newest activity, capped at five with "+n more" and a badge of all', () => {
    const worktrees: Worktree[] = []
    const agents: Agent[] = []
    for (let index = 0; index < 7; index++) {
      worktrees.push(wt(`w${index}`, `t${index}`, { last_activity_at: ago(index * HOUR) }))
      agents.push(agent(`a${index}`, 'r1', 'live', { activity: 'blocked', task: `t${index}` }))
    }
    // A second kind on one task: its worst kind only.
    agents.push(run('rf', 'failed', { task: 't0', started_at: ago(HOUR) }))
    // And the worse kind first.
    worktrees[3] = wt('w3', 't3', { last_activity_at: ago(3 * HOUR), owner_state: 'orphaned', ahead: 1 })
    const model = modelOf({ worktrees, agents })
    const { items, shown, more, moreLink, withoutTask } = model.needsYou
    expect(items).toHaveLength(7)
    expect(shown).toHaveLength(NEEDS_YOU_VISIBLE)
    expect(more).toBe(2)
    expect(hrefOf(moreLink)).toBe('/tasks?chips=needs-you')
    expect(items.map((i) => i.task)).toEqual(['t3', 't0', 't1', 't2', 't4', 't5', 't6'])
    expect(items.map((i) => i.kind)[0]).toBe('work-at-risk')
    expect(withoutTask).toBeUndefined()
    // When every state has changed, nothing needs the operator.
    const calm = modelOf({ worktrees: worktrees.map((w) => ({ ...w, owner_state: 'idle' as const, ahead: 0 })), agents: [] })
    expect(calm.needsYou).toMatchObject({ items: [], shown: [], more: 0 })
  })

  it('puts blocked agents with no task in one row after the tasks, linking to Agents with chip blocked', () => {
    const model = modelOf({
      worktrees: [wt('w1', 'a')],
      agents: [agent('a1', 'r1', 'live', { activity: 'blocked' }), agent('a2', undefined, 'live', { activity: 'blocked', worktrees: ['missing'] }), agent('a3', 'r1', 'live', { activity: 'blocked', task: 'a' })],
    })
    expect(model.needsYou.items.map((i) => i.task)).toEqual(['a'])
    expect(model.needsYou.withoutTask).toEqual({ count: 2, agentIds: ['a1', 'a2'], link: { path: '/agents', query: { chips: 'blocked' } } })
  })

  it('puts a task with no reported activity after the others of its kind, and orders by name when none is reported', () => {
    const failing = { checks_failed: 1 }
    const model = modelOf({
      repositories: REPOS,
      worktrees: [wt('w1', 'z', { last_activity_at: undefined }), wt('w2', 'y', { last_activity_at: ago(HOUR) }), wt('w3', 'x', { last_activity_at: undefined }), wt('w4', 'w', { last_activity_at: ago(2 * HOUR) })],
      pull_requests: [pr('p1', 'w1', { number: 1, ...failing }), pr('p2', 'w2', { number: 2, ...failing }), pr('p3', 'w3', { number: 3, ...failing }), pr('p4', 'w4', { number: 4, ...failing })],
    })
    expect(model.needsYou.items.map((i) => i.task)).toEqual(['y', 'w', 'x', 'z'])
  })

  it('picks the failed pull request observed longest ago, one with no usable observation last', () => {
    const model = modelOf({
      repositories: REPOS,
      worktrees: [wt('w1', 'a')],
      pull_requests: [pr('p1', 'w1', { number: 1, checks_failed: 1, checked_at: 'junk' }), pr('p2', 'w1', { number: 2, checks_failed: 1, checked_at: ago(HOUR) }), pr('p3', 'w1', { number: 3, checks_failed: 1, checked_at: ago(2 * HOUR) }), pr('p4', 'w1', { number: 4, checks_failed: 1, checked_at: 'junk' })],
    })
    expect(item(model, 'pr-checks-failed')).toMatchObject({ number: 3 })
  })

  it('breaks a tie in kind and activity by task name', () => {
    const model = modelOf({ worktrees: [wt('w1', 'b', { owner_state: 'idle', ahead: 1, last_activity_at: ago(HOUR) }), wt('w2', 'a', { owner_state: 'idle', ahead: 1, last_activity_at: ago(HOUR) })] })
    expect(model.needsYou.items.map((i) => i.task)).toEqual(['a', 'b'])
  })
})

describe('Ready to land', () => {
  // cockpit-views#ac:ready-to-land-groups-by-task (the rows the library derives)
  it('lists ready tasks with their pull requests, repositories, checks, the oldest observation and the land command', () => {
    const model = modelOf({
      repositories: REPOS,
      worktrees: [wt('w1', 'fix-ci', { last_activity_at: ago(HOUR) }), wt('w2', 'other', { last_activity_at: ago(5 * HOUR) })],
      pull_requests: [
        pr('p12', 'w1', { number: 12, checked_at: ago(4 * 60_000), url: 'https://x.test/12' }),
        pr('p7', 'w1', { number: 7, repository: 'r2', checked_at: ago(10 * 60_000) }),
        pr('p9', 'w2', { number: 9, repository: undefined }),
      ],
    })
    const { ready, notReady } = model.readyToLand
    expect(ready.map((row) => row.task)).toEqual(['fix-ci', 'other'])
    const [fixCi, other] = ready
    expect(fixCi.repositories).toEqual(['sneat-dev/wb', 'sneat-co/sneat-go'])
    expect(fixCi.pullRequests.map((p) => [p.repository, p.number, p.landCommand])).toEqual([
      ['sneat-dev/wb', 12, "wb pr land 'sneat-dev/wb#12'"],
      ['sneat-co/sneat-go', 7, "wb pr land 'sneat-co/sneat-go#7'"],
    ])
    expect(fixCi.pullRequests[0]).toMatchObject({ id: 'p12', url: 'https://x.test/12', checksPassed: 5, checksTotal: 5 })
    expect([fixCi.checksPassed, fixCi.checksTotal]).toEqual([10, 10])
    expect(fixCi.checkedAt).toBe(NOW - 10 * 60_000)
    expect(fixCi.lastActivityAt).toBe(NOW - HOUR)
    expect(hrefOf(fixCi.link)).toBe('/tasks?sel=fix-ci')
    // A pull request whose repository is unknown has no command to copy.
    expect(other.pullRequests[0]).toMatchObject({ repository: undefined, landCommand: undefined })
    expect(other.repositories).toEqual([])
    expect(notReady).toEqual([])
  })

  it('lists, muted, only the tasks that wait on checks, with how long ago they were read', () => {
    const model = modelOf({
      repositories: REPOS,
      worktrees: [wt('w1', 'pending'), wt('w2', 'draft'), wt('w3', 'conflict'), wt('w4', 'pending2', { last_activity_at: ago(9 * HOUR) })],
      pull_requests: [
        pr('p1', 'w1', { checks_pending: 2, checks_green: false, checked_at: ago(4 * 60_000) }),
        pr('p2', 'w2', { state: 'draft', checks_pending: 1, checks_green: false }),
        pr('p3', 'w3', { mergeable: 'dirty' }),
        pr('p4', 'w4', { checks_pending: 1, checks_green: false, checked_at: ago(2 * 60_000) }),
        pr('p5', 'w4', { checks_pending: 1, checks_green: false, checked_at: ago(8 * 60_000) }),
      ],
    })
    const { ready, notReady } = model.readyToLand
    expect(ready).toEqual([])
    expect(notReady.map((row) => row.task)).toEqual(['pending', 'pending2'])
    expect(notReady[0]).toMatchObject({ checkedAt: NOW - 4 * 60_000, reasons: ['checks pending'] })
    expect(notReady[1].checkedAt).toBe(NOW - 8 * 60_000)
  })

  it('breaks a tie in activity by task name, and shows an unknown observation time as none', () => {
    const model = modelOf({
      worktrees: [wt('w1', 'b'), wt('w2', 'a')],
      pull_requests: [pr('p1', 'w1', { checked_at: 'junk' }), pr('p2', 'w2')],
    })
    expect(model.readyToLand.ready.map((row) => row.task)).toEqual(['a', 'b'])
    expect(model.readyToLand.ready[1].checkedAt).toBeUndefined()
    const pending = modelOf({
      worktrees: [wt('w1', 'b'), wt('w2', 'a')],
      pull_requests: [pr('p1', 'w1', { checks_pending: 1, checks_green: false }), pr('p2', 'w2', { checks_pending: 1, checks_green: false })],
    })
    expect(pending.readyToLand.notReady.map((row) => row.task)).toEqual(['a', 'b'])
  })
})

describe('In flight', () => {
  // cockpit-views#ac:in-flight-lists-agents-on-every-machine (the rows the library derives)
  it('lists running agents everywhere: sessions that are live and runs that are running, remote ones with no action', () => {
    const model = modelOf({
      repositories: REPOS,
      worktrees: [wt('w1', 'fix-ci')],
      machines: [machine('alpha'), machine('vm', 'cached')],
      agents: [
        run('r1', 'running', { task: 'fix-ci', started_at: ago(HOUR), activity: 'working', runtime: 'claude', model: 'opus' }),
        agent('s1', 'r1', 'live', { started_at: ago(3 * HOUR), runtime: 'codex', model: undefined }),
        { ...agent('s2', undefined, 'live', { started_at: ago(2 * HOUR), runtime: undefined, model: undefined }), route: 'cached', machine: 'vm', machine_id: 'mach-vm' },
        agent('s3', undefined, 'parked'),
        run('r2', 'completed'),
        { ...run('r3', 'running', { started_at: ago(HOUR) }), route: 'live-remote', machine: 'vm', machine_id: 'mach-vm' },
      ],
    })
    const rows = model.inFlight
    expect(rows.map((row) => row.agent.id)).toEqual(['r1', 's1', 'r3', 's2'])
    expect(rows[0]).toMatchObject({ label: 'claude opus', task: 'fix-ci', machine: 'alpha', remote: false, controllable: true, activity: 'working', startedAt: NOW - HOUR })
    expect(rows[1]).toMatchObject({ label: 'codex', repository: 'sneat-dev/wb', controllable: false, activity: undefined, remote: false })
    expect(rows[2]).toMatchObject({ remote: true, controllable: false, machineId: 'mach-vm' })
    expect(rows[3]).toMatchObject({ label: 'agent', remote: true, observedAt: OBSERVED, controllable: false })
  })

  it('lists a ready task whose checks counts are not reported as 0 of 0', () => {
    const model = modelOf({ worktrees: [wt('w1', 'a')], pull_requests: [pr('p1', 'w1', { checks_passed: undefined, checks_total: undefined })] })
    expect(model.readyToLand.ready[0].checksPassed).toBeUndefined()
    expect(model.readyToLand.ready[0].checksTotal).toBeUndefined()
    expect(model.readyToLand.ready[0].pullRequests[0].checksTotal).toBeUndefined()
  })

  it('breaks ties by id and puts an unknown start last', () => {
    const model = modelOf({ agents: [agent('b', undefined, 'live'), agent('a', undefined, 'live'), agent('c', undefined, 'live', { started_at: ago(HOUR) })] })
    expect(model.inFlight.map((row) => row.agent.id)).toEqual(['c', 'a', 'b'])
  })

  it('titles an agent by runtime and model', () => {
    expect(agentTitle(agent('a', undefined, 'live', { runtime: 'claude', model: 'opus' }))).toBe('claude opus')
    expect(agentTitle(agent('a', undefined, 'live', { runtime: undefined, model: 'opus' }))).toBe('opus')
    expect(agentTitle(agent('a', undefined, 'live', { runtime: undefined, model: undefined }))).toBe('agent')
  })
})

describe('machine load', () => {
  const sample = (cpu: number, used: number, total = 100) => ({ cpu_percent: cpu, load1: 1, memory_used_bytes: used, memory_total_bytes: total, disk_free_bytes: 1, disk_total_bytes: 2, sampled_at: ago(60_000) })
  const metrics = (route: MachineMetrics['route'], ...samples: ReturnType<typeof sample>[]): MachineMetrics => ({ machine: 'm', route, samples })

  // cockpit-views#ac:in-flight-machine-load-indicator (the verdict the library derives)
  it('says free below 70 percent CPU and 80 percent memory, busy otherwise, with the sample route and age', () => {
    expect(machineLoad(metrics('local', sample(40, 50)))).toEqual({ state: 'free', route: 'local', sampledAt: NOW - 60_000, cpuPercent: 40, memoryPercent: 50 })
    expect(machineLoad(metrics('live-remote', sample(85, 50)))).toMatchObject({ state: 'busy', route: 'live-remote' })
    expect(machineLoad(metrics('local', sample(40, 80)))).toMatchObject({ state: 'busy' })
    expect(machineLoad(metrics('local', sample(70, 10)))).toMatchObject({ state: 'busy' })
    expect(machineLoad(metrics('local', sample(69.9, 79.9)))).toMatchObject({ state: 'free' })
    // The latest sample counts, which is the last.
    expect(machineLoad(metrics('local', sample(99, 99), sample(10, 10)))).toMatchObject({ state: 'free' })
    expect(machineLoad(metrics('cached', sample(10, 10)))).toMatchObject({ route: 'cached', state: 'free' })
  })

  it('says not reported for no metrics, no sample or no memory total, and never invents a load', () => {
    expect(machineLoad(undefined)).toEqual({ state: 'not-reported', route: 'none' })
    expect(machineLoad({ machine: 'm', route: 'none', samples: [], reason: 'unsupported' })).toEqual({ state: 'not-reported', route: 'none' })
    expect(machineLoad(metrics('local', sample(10, 10, 0)))).toEqual({ state: 'not-reported', route: 'local' })
  })
})

describe('Resume and Cleanup', () => {
  // cockpit-views#ac:resume-lists-five-recent-tasks (the list the library derives)
  it('lists the five tasks with the newest activity, newest first, ties by name', () => {
    const worktrees = Array.from({ length: 8 }, (_, index) => wt(`w${index}`, `t${index}`, { last_activity_at: ago((index + 1) * HOUR) }))
    worktrees.push(wt('wn', 'none', { last_activity_at: undefined }), wt('wa', 'aaa', { last_activity_at: ago(HOUR) }))
    const model = modelOf({ worktrees })
    expect(RESUME_COUNT).toBe(5)
    expect(model.resume.map((task) => task.name)).toEqual(['aaa', 't0', 't1', 't2', 't3'])
    expect(model.resume[0].stateInfo.label).toBeDefined()
  })

  // cockpit-views#ac:cleanup-line-counts-and-chart (the counts the library derives)
  it('counts safe and look worktrees, with the sets behind the chips and the age bars linking to Worktrees', () => {
    const worktrees: Worktree[] = []
    for (let index = 0; index < 12; index++) worktrees.push(wt(`s${index}`, `landed${index}`, { lifecycle: 'merged', ahead: 0, owner_state: 'idle', last_activity_at: ago(2 * DAY) }))
    for (let index = 0; index < 3; index++) worktrees.push(wt(`o${index}`, `orph${index}`, { owner_state: 'orphaned', last_activity_at: ago(HOUR) }))
    for (let index = 0; index < 2; index++) worktrees.push(wt(`u${index}`, `unk${index}`, { owner_state: 'unknown', last_activity_at: ago(100 * DAY) }))
    for (let index = 0; index < 4; index++) worktrees.push(wt(`i${index}`, `idle${index}`, { owner_state: 'idle', ahead: 0, last_activity_at: ago(40 * DAY) }))
    const model = modelOf({ worktrees })
    const cleanup = buildCleanup(model)
    expect([cleanup.safeCount, cleanup.lookCount]).toEqual([12, 9])
    expect(cleanup.indicative).toBe(true)
    expect(cleanup.safeIds.has('s0')).toBe(true)
    expect(cleanup.lookIds.has('i0')).toBe(true)
    expect(cleanup.lookIds.has('s0')).toBe(false)
    expect(hrefOf(cleanup.reviewLink)).toBe('/worktrees?chips=safe')
    expect(cleanup.bars.map((bar) => [bar.label, bar.count, hrefOf(bar.link)])).toEqual([
      ['today', 3, '/worktrees?q=age%3A%3C1d'],
      ['1 to 7 days', 12, '/worktrees?q=age%3A1-7d'],
      ['8 to 30 days', 0, '/worktrees?q=age%3A8-30d'],
      ['31 to 90 days', 4, '/worktrees?q=age%3A31-90d'],
      ['over 90 days', 2, '/worktrees?q=age%3A%3E90d'],
    ])
    expect(cleanup.unknownAge).toBe(0)
  })

  it('never calls a worktree safe unless ahead is present and 0, and not when its owner is active', () => {
    const model = modelOf({
      worktrees: [
        wt('a', 'landed', { lifecycle: 'merged', ahead: 0, owner_state: 'idle' }),
        wt('b', 'landed2', { lifecycle: 'merged', owner_state: 'idle' }),
        wt('c', 'landed3', { lifecycle: 'merged', ahead: 1, owner_state: 'idle' }),
        wt('d', 'landed4', { lifecycle: 'merged', ahead: 0, owner_state: 'active' }),
        wt('e', 'landed5', { lifecycle: 'merged', ahead: 0 }),
        wt('f', 'young', { last_activity_at: undefined }),
      ],
    })
    expect([...buildCleanup(model).safeIds].sort()).toEqual(['a', 'e'])
    expect(buildCleanup(model).lookCount).toBe(0)
    expect(buildCleanup(model).unknownAge).toBe(1)
  })
})

describe('machines and Fleet health', () => {

  it('says live, cached or stale, the age, the version mark, the running agents and the uptime', () => {
    const model = modelOf({
      machines: [
        m('alpha', { wb_version: '0.176.0', boot_time: ago(3 * DAY) }),
        m('vm', { route: 'live-remote', wb_version: 'v0.176.0', observed_at: ago(2 * HOUR) }),
        m('beta', { route: 'cached', wb_version: '0.170.1', observed_at: ago(20 * 60_000) }),
        m('old', { route: 'cached', wb_version: 'dev', observed_at: ago(25 * HOUR) }),
        m('none', { route: 'cached', observed_at: undefined, boot_time: 'junk' }),
      ],
      agents: [agent('a1', undefined, 'live'), agent('a2', undefined, 'parked'), { ...agent('a3', undefined, 'live'), machine_id: 'mach-beta', machine: 'beta' }],
    })
    const views = model.machines
    expect(views.map((v) => [v.machine.machine, v.state, v.local, v.outdated, v.runningAgents])).toEqual([
      ['alpha', 'live', true, false, 1],
      ['vm', 'live', false, false, 0],
      ['beta', 'cached', false, true, 1],
      ['old', 'stale', false, false, 0],
      ['none', 'cached', false, false, 0],
    ])
    expect(views[0].uptimeMs).toBe(3 * DAY)
    expect(views[0].ageMs).toBe(0)
    expect(views[2].ageMs).toBe(20 * 60_000)
    expect(views[4].ageMs).toBeUndefined()
    expect(views[4].uptimeMs).toBeUndefined()
  })

  // cockpit-views#ac:fleet-health-only-when-not-ok (the lines the library derives)
  it('is ok for a fleet in which every machine is live and current', () => {
    const model = modelOf({ machines: [m('alpha', { wb_version: '0.176.0' }), m('beta', { route: 'cached', wb_version: '0.176.0', observed_at: ago(HOUR) })], repositories: [repository('r1', 'alpha')] })
    expect(buildHealth(model)).toEqual({ ok: true, staleMachines: [], olderWb: [], remoteErrors: [], scanErrors: [] })
  })

  it('shows a stale machine, an older WB and a scan error, each with a command to copy', () => {
    const model = modelOf({
      machines: [m('alpha', { wb_version: '0.176.0' }), m('vm', { route: 'cached', wb_version: '0.170.0', observed_at: ago(25 * HOUR) })],
      repositories: [repository('r1', 'alpha', { name: 'sneat-dev/wb', error: 'scan_failed' })],
    })
    const { ok, staleMachines, olderWb, scanErrors } = buildHealth(model)
    expect(ok).toBe(false)
    expect(staleMachines).toEqual([
      { machine: 'vm', machineId: 'mach-vm', text: 'vm has not published for over 24 hours', command: { text: 'wb remote publish', label: 'run on vm', needsEdit: false }, link: { path: '/machines', query: { chips: 'stale' } } },
    ])
    expect(olderWb).toEqual([
      { machine: 'vm', machineId: 'mach-vm', text: 'vm runs an older WB (0.170.0)', command: { text: 'wb self-update', label: 'run on vm', needsEdit: false }, link: { path: '/machines', query: { chips: 'outdated' } } },
    ])
    expect(scanErrors).toEqual([{ repository: 'sneat-dev/wb', command: { text: "wb fleet status --filter='sneat-dev/wb'" }, link: { path: '/repositories', query: { chips: 'errors' } } }])
  })

  it('refuses a fix command for a repository name it cannot quote', () => {
    const model = modelOf({ repositories: [repository('r1', 'alpha', { name: '-x/y', error: 'scan_failed' })] })
    expect(buildHealth(model).scanErrors[0].command).toMatchObject({ reason: expect.stringContaining('starts with') })
  })

  // cockpit-views#ac:failed-export-falls-back-and-shows-a-typed-error (the lines the library derives)
  it('shows each remote_error with its fix command, through ssh when the machine has an SSH route', () => {
    const codes = ['http_unavailable', 'http_auth_failed', 'ssh_unavailable', 'auth_failed', 'timeout', 'wb_missing', 'wb_too_old', 'daemon_not_running', 'export_refused', 'bad_payload', 'brand_new_code']
    const machines = codes.map((code, index) => m(`vm${index}`, { route: 'cached', remote_error: code, observed_at: ago(HOUR) }))
    const model = new FleetModel(fleetDocument({ machines, repositories: [], worktrees: [], agents: [], pull_requests: [] }), {
      now: () => NOW,
      sshRoute: (machine) => (machine.machine === 'vm2' ? { host: 'vm.example', user: 'alex', wbPath: '/usr/local/bin/wb' } : undefined),
    })
    const byCode = Object.fromEntries(buildHealth(model).remoteErrors.map((item, index) => [codes[index], item]))
    expect(buildHealth(model).ok).toBe(false)
    expect(byCode['http_auth_failed'].command).toEqual({ text: 'wb remote enroll --url=<<<edit:hub-url>>> --token-stdin', label: 'run here', needsEdit: true })
    expect(byCode['daemon_not_running'].command).toEqual({ text: 'wb daemon start', label: 'run on vm7', needsEdit: false })
    expect(byCode['wb_too_old'].command).toEqual({ text: 'wb self-update', label: 'run on vm6', needsEdit: false })
    expect(byCode['ssh_unavailable'].command).toEqual({ text: "ssh alex@vm.example /usr/local/bin/wb cockpit export --format='json'", label: 'run here', needsEdit: false })
    for (const code of ['http_unavailable', 'auth_failed', 'timeout', 'wb_missing', 'export_refused', 'bad_payload']) {
      expect(byCode[code].command).toMatchObject({ text: "wb cockpit export --format='json'" })
    }
    expect(byCode['brand_new_code'].text).toBe('vm10: unknown error')
    expect(byCode['timeout'].text).toBe('vm4: its export timed out')
    expect(byCode['timeout'].link).toEqual({ path: '/machines', query: { machine: 'mach-vm4' } })
  })

  it('shows the reason instead of a command when the SSH route of a machine is hostile', () => {
    const model = new FleetModel(fleetDocument({ machines: [m('vm', { route: 'cached', remote_error: 'timeout', observed_at: ago(HOUR) })], repositories: [], worktrees: [], agents: [], pull_requests: [] }), {
      now: () => NOW,
      sshRoute: () => ({ host: '-oProxyCommand=x', user: 'alex' }),
    })
    expect(buildHealth(model).remoteErrors[0].command).toEqual({ reason: expect.stringContaining('starts with') })
  })

  it('says each remote_error in words, and an unknown code as an unknown error', () => {
    for (const code of ['http_unavailable', 'http_auth_failed', 'ssh_unavailable', 'auth_failed', 'timeout', 'wb_missing', 'wb_too_old', 'daemon_not_running', 'export_refused', 'bad_payload']) {
      expect(remoteErrorText(code)).not.toBe('unknown error')
    }
    expect(remoteErrorText('???')).toBe('unknown error')
    expect(remoteFix('timeout', undefined)).toMatchObject({ ok: true })
  })

  it('compares WB versions as dotted numbers and ignores what is not one', () => {
    expect(parseVersion('0.176.0')).toEqual([0, 176, 0])
    expect(parseVersion('v1.2')).toEqual([1, 2])
    expect(parseVersion('0.176.0-rc1')).toEqual([0, 176, 0])
    expect(parseVersion('dev')).toBeUndefined()
    expect(parseVersion(undefined)).toBeUndefined()
    expect(compareVersions([0, 176, 0], [0, 170, 9])).toBeGreaterThan(0)
    expect(compareVersions([1], [1, 0, 0])).toBe(0)
    expect(compareVersions([0, 9], [0, 10])).toBeLessThan(0)
    expect(compareVersions([1], [1, 2])).toBeLessThan(0)
    expect(compareVersions([1, 2], [1])).toBeGreaterThan(0)
    // A version key sorts as a string the way the version sorts as numbers.
    expect(versionKey('0.9.0')! < versionKey('0.10.0')!).toBe(true)
    expect(versionKey('dev')).toBeUndefined()
  })
})

describe('throughput', () => {
  // cockpit-views#ac:home-charts-from-throughput (the series the library derives)
  it('fills the window day by day, oldest first, and keeps the slowest five, slowest first', () => {
    const model = modelOf({
      throughput: {
        window_days: 5,
        per_day: [
          { date: '2026-10-01', landed: 2 },
          { date: '2026-09-29', landed: 1 },
          { date: '2026-08-01', landed: 9 },
        ],
        slowest: [1, 2, 3, 4, 5, 6].map((n) => ({ task: `t${n}`, duration_seconds: n * 100, landed_at: OBSERVED })),
      },
    })
    const series = buildThroughput(model)
    expect(series?.perDay).toEqual([
      { date: '2026-09-27', landed: 0 },
      { date: '2026-09-28', landed: 0 },
      { date: '2026-09-29', landed: 1 },
      { date: '2026-09-30', landed: 0 },
      { date: '2026-10-01', landed: 2 },
    ])
    expect([series?.windowDays, series?.totalLanded, series?.maxLanded]).toEqual([5, 3, 2])
    expect(series?.slowest.map((entry) => [entry.task, entry.durationSeconds])).toEqual([['t6', 600], ['t5', 500], ['t4', 400], ['t3', 300], ['t2', 200]])
    expect(Object.keys(series?.slowest[0] ?? {})).not.toContain('link')
  })

  it('has no series without the block, and a window of no landings has a maximum of 0', () => {
    expect(buildThroughput(modelOf({}))).toBeUndefined()
    expect(buildThroughput(modelOf({ throughput: { window_days: 2, per_day: [], slowest: [] } }))).toMatchObject({ totalLanded: 0, maxLanded: 0 })
    expect(buildThroughput(modelOf({ throughput: { window_days: 0, per_day: [], slowest: [] } }))?.perDay).toEqual([])
  })
})

describe('the model as a memo', () => {
  // cockpit-views#ac:derived-collections-computed-once
  it('derives each collection once per document, however often it is read', () => {
    const derived: string[] = []
    const model = modelOf({ worktrees: [wt('w1', 'a')], repositories: REPOS }, (name) => derived.push(name))
    for (let render = 0; render < 5; render++) {
      void [buildRepositories(model), model.tasks, model.needsYou, model.readyToLand, buildCleanup(model), model.inFlight, model.resume, model.machines, buildHealth(model), buildThroughput(model)]
      void [buildTaskRows(model), buildRepositoryRows(model), buildWorktreeRows(model), buildAgentRows(model), buildMachineRows(model)]
    }
    for (const name of ['repositories', 'tasks', 'needsYou', 'readyToLand', 'cleanup', 'inFlight', 'resume', 'machines', 'health', 'throughput', 'rows:tasks', 'rows:repositories', 'rows:worktrees', 'rows:agents', 'rows:machines'])
      expect(derived.filter((entry) => entry === name), name).toHaveLength(1)
    expect(derived.filter((entry) => entry === 'index')).toHaveLength(1)
  })

  it('gives the same model for the same document and a new one for another, which derives again', () => {
    const derived: string[] = []
    const models = new FleetModels({ now: () => NOW, onDerive: (name) => derived.push(name) })
    const first = fleetDocument()
    expect(models.forDocument(first)).toBe(models.forDocument(first))
    void models.forDocument(first).tasks
    void models.forDocument(first).tasks
    expect(derived.filter((name) => name === 'tasks')).toHaveLength(1)
    const second = fleetDocument()
    expect(models.forDocument(second)).not.toBe(models.forDocument(first))
    void models.forDocument(second).tasks
    expect(derived.filter((name) => name === 'tasks')).toHaveLength(2)
    expect(new FleetModels().forDocument(first).now).toBeGreaterThan(0)
  })

  // cockpit-views#ac:filter-vocabulary-is-the-only-link-target
  it('builds every link it carries from the vocabulary', () => {
    const model = new FleetModel(
      fleetDocument({
        repositories: REPOS,
        machines: [{ ...machine('alpha'), wb_version: '0.176.0', remote_error: 'timeout' }, { ...machine('beta', 'cached'), wb_version: '0.1.0', observed_at: ago(30 * HOUR) }],
        worktrees: [wt('w1', 'fix ci', { owner_state: 'orphaned', ahead: 1 }), wt('w2', 'b', { lifecycle: 'merged', ahead: 0 })],
        pull_requests: [pr('p1', 'w2', { checks_failed: 1 })],
        agents: [agent('a1', undefined, 'live', { activity: 'blocked' })],
        throughput: { window_days: 3, per_day: [], slowest: [] },
      }),
      { now: () => NOW },
    )
    const links = [
      model.needsYou.moreLink,
      model.needsYou.withoutTask?.link,
      ...model.needsYou.items.map((i) => i.link),
      ...model.readyToLand.ready.map((r) => r.link),
      ...model.readyToLand.notReady.map((r) => r.link),
      buildCleanup(model).reviewLink,
      ...buildCleanup(model).bars.map((bar) => bar.link),
      ...[...buildHealth(model).staleMachines, ...buildHealth(model).olderWb, ...buildHealth(model).remoteErrors].map((i) => i.link),
      ...buildHealth(model).scanErrors.map((i) => i.link),
    ]
    expect(links.filter((link) => link !== undefined).length).toBeGreaterThan(8)
    for (const link of links) expect(linkProblems(link as NonNullable<typeof link>)).toEqual([])
  })
})

describe('review fixes', () => {
  // A task is listed as waiting on checks only for a real, non-empty reason that is pending checks.
  it('never lists a not-ready task as waiting on checks without a reason, or when anything else is wrong with it', () => {
    const model = modelOf({
      repositories: REPOS,
      worktrees: [wt('w1', 'pending'), wt('w2', 'unobserved'), wt('w3', 'nomerge')],
      pull_requests: [
        pr('p1', 'w1', { checks_pending: 1, checks_green: false }),
        pr('p2', 'w2', { checks_pending: 1, checks_green: false }),
        pr('p3', 'w2', { checked_at: undefined }),
        pr('p4', 'w3', { checks_pending: 1, checks_green: false, mergeable: 'unknown' }),
      ],
    })
    expect(model.readyToLand.notReady.map((row) => row.task)).toEqual(['pending'])
    // A not-ready task with no reasons at all (no open pull request in the list) is not shown either.
    const tasks = model.tasks
    expect(tasks.find((task) => task.name === 'unobserved')?.state).toBe('not-ready')
  })

  it('says the owner of an at-risk worktree in words, and "owner state not reported" for a value it does not know', () => {
    const reasonOf = (owner: string) => {
      const model = modelOf({ worktrees: [wt('w1', 't', { owner_state: owner as never, ahead: 1 })] })
      return (model.needsYou.items[0] as { reason: string }).reason
    }
    expect(reasonOf('orphaned')).toBe('1 unpushed commit and its owner process is gone')
    expect(reasonOf('idle')).toBe('1 unpushed commit and it has no running owner')
    expect(reasonOf('unknown')).toBe('1 unpushed commit and no owner process is recorded')
    expect(reasonOf('zombie')).toBe('1 unpushed commit and owner state not reported')
  })

  it('keeps an unobserved count on the ready row, and labels a land command for a pull request on another machine', () => {
    const model = modelOf({
      repositories: REPOS,
      worktrees: [wt('w1', 'a')],
      pull_requests: [{ ...pr('p1', 'w1'), route: 'cached', machine: 'vm', machine_id: 'mach-vm' }, pr('p2', 'w1', { checked_at: undefined, state: undefined })],
    })
    const row = model.readyToLand.ready[0]
    expect(row.unobservedPullRequests).toBe(1)
    expect(row.pullRequests[0]).toMatchObject({ landCommand: "wb pr land 'sneat-dev/wb#1'", landLabel: 'run on vm' })
    expect(modelOf({ worktrees: [wt('w1', 'a')], pull_requests: [pr('p1', 'w1')] }).readyToLand.ready[0].pullRequests[0].landLabel).toBeUndefined()
  })

  it('is never safe to clean up a task while a sibling worktree has unpushed work', () => {
    const model = modelOf({ worktrees: [wt('a', 'done', { lifecycle: 'merged', ahead: 0 }), wt('b', 'done', { lifecycle: 'merged', ahead: 3 }), wt('c', 'other', { lifecycle: 'merged', ahead: 0 })] })
    expect([...buildCleanup(model).safeIds]).toEqual(['c'])
  })

  it('reads the clock itself when made without a time or options', () => {
    expect(new FleetModel(fleetDocument()).now).toBeGreaterThan(0)
  })

  it('gives the Home badge, the running-agent count and tasks by name from a map', () => {
    const model = modelOf({ worktrees: [wt('w1', 'a', { owner_state: 'idle', ahead: 1 })], agents: [agent('s', undefined, 'live'), agent('p', undefined, 'parked')] })
    expect(model.homeBadge).toBe(1)
    expect(model.runningAgentCount).toBe(1)
    expect(model.taskNamed('a')).toBe(model.taskMap.get('a'))
  })

  // cockpit-views#ac:unchanged-snapshot-does-nothing
  it('keys time-dependent derivations on the document and a 60 second clock bucket, and on the session routes', () => {
    const derived: string[] = []
    const models = new FleetModels({ onDerive: (name) => derived.push(name) })
    const document = fleetDocument()
    const t0 = Date.parse('2026-10-01T10:00:10Z')
    const first = models.forDocument(document, t0)
    void first.tasks
    expect(models.forDocument(document, t0 + 30_000)).toBe(first)
    void models.forDocument(document, t0 + 30_000).tasks
    expect(derived.filter((name) => name === 'tasks')).toHaveLength(1)
    // The next minute derives again, with a model whose clock is the one asked for.
    const later = models.forDocument(document, t0 + 61_000)
    expect(later).not.toBe(first)
    expect(later.now).toBe(t0 + 61_000)
    void later.tasks
    expect(derived.filter((name) => name === 'tasks')).toHaveLength(2)
    // New routes (the session arrived) give a model that carries them; the same array does not.
    const routes = [{ machine_id: 'mach-beta', ssh: { host: 'h' } }]
    const withRoutes = models.forDocument(document, t0 + 61_000, routes)
    expect(withRoutes).not.toBe(later)
    expect(withRoutes.machineRoutes).toBe(routes)
    expect(models.forDocument(document, t0 + 62_000, routes)).toBe(withRoutes)
  })

  it('does not rethrow a throwing derivation on every render: it shows its empty value and reports once', () => {
    const errors: string[] = []
    const derived: string[] = []
    const broken = { ...fleetDocument(), agents: null } as unknown as FleetDocument
    const model = new FleetModel(broken, { now: () => NOW, onDerive: (name) => derived.push(name), onError: (name) => errors.push(name) })
    for (let render = 0; render < 3; render++) {
      expect(model.inFlight).toEqual([])
      expect(model.tasks).toEqual([])
      expect(model.needsYou.items).toEqual([])
      expect(buildCleanup(model).safeCount).toBe(0)
      expect(buildHealth(model).ok).toBe(true)
      expect(model.readyToLand).toEqual({ ready: [], notReady: [] })
      expect(buildThroughput(model)).toBeUndefined()
    }
    expect(model.failedDerivations).toContain('inFlight')
    expect(derived.filter((name) => name === 'inFlight')).toHaveLength(1)
    expect(errors.filter((name) => name === 'inFlight')).toHaveLength(1)
    // Another model of a healthy document has no failures.
    expect(new FleetModel(fleetDocument(), { now: () => NOW }).failedDerivations).toEqual([])
    // Without an error hook the failure is still contained.
    const quiet = new FleetModel(broken, { now: () => NOW })
    expect(quiet.inFlight).toEqual([])
  })

  it('labels a health command "run here" for the ssh form and enrolling, and "run on <machine>" otherwise, using the session routes', () => {
    const machines = [
      m('alpha', { wb_version: '0.176.0' }),
      m('vm', { route: 'cached', wb_version: '0.170.0', observed_at: ago(25 * HOUR) }),
      m('old', { route: 'cached', wb_version: '0.170.0', observed_at: ago(25 * HOUR), remote_error: 'wb_too_old' }),
    ]
    const routes = [{ machine_id: 'mach-vm', ssh: { host: 'vm.example', user: '', wb_path: '' } }]
    const model = new FleetModel(fleetDocument({ machines, repositories: [], worktrees: [], agents: [], pull_requests: [] }), { now: () => NOW, machineRoutes: routes })
    const stale = buildHealth(model).staleMachines
    expect(stale.find((item) => item.machine === 'vm')?.command).toEqual({ text: 'ssh vm.example wb remote publish', label: 'run here', needsEdit: false })
    expect(stale.find((item) => item.machine === 'old')?.command).toEqual({ text: 'wb remote publish', label: 'run on old', needsEdit: false })
    expect(buildHealth(model).olderWb.find((item) => item.machine === 'vm')?.command).toMatchObject({ text: 'ssh vm.example wb self-update', label: 'run here' })
    expect(buildHealth(model).remoteErrors[0].command).toEqual({ text: 'wb self-update', label: 'run on old', needsEdit: false })
  })
})

function m(name: string, extra: Record<string, unknown> = {}) {
  return { ...machine(name), ...extra }
}
