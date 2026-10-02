import { FleetDocument } from './fleet.types'
import { FleetModels } from './fleet-model'
import { REASON_PARTS_SHOWN, seenElsewhereOnly, taskReason } from './task-reason'
import { TaskView } from './view-types'
import { agent, fleetDocument, machine, pullRequest, repository, run, worktree } from './test-data'

const NOW = Date.parse('2026-06-01T12:00:00Z')

const ago = (hours: number) => new Date(NOW - hours * 60 * 60 * 1000).toISOString()

/** The reason of the task `t`, for a document with that task's entries. */
function reasonOf(extra: Partial<FleetDocument>): {
  reason: string
  task: TaskView
} {
  const document = fleetDocument({
    machines: [machine('alpha'), machine('beta', 'cached')],
    repositories: [repository('r1', 'alpha', { name: 'sneat-dev/wb' }), repository('r2', 'alpha', { name: 'sneat-co/sneat-go' }), repository('r3', 'alpha', { name: 'other/wb' })],
    worktrees: [],
    pull_requests: [],
    agents: [],
    ...extra,
  })
  const model = new FleetModels({ now: () => NOW }).forDocument(document, NOW)
  const task = model.taskNamed('t') as TaskView
  return { reason: taskReason(model, 't') as string, task }
}

const wt = (id: string, repositoryId: string, extra: Record<string, unknown> = {}) => ({ ...worktree(id, repositoryId, 'alpha'), task: 't', ...extra })

describe('taskReason', () => {
  it('says what is only on this machine for an at-risk task, by repository, with the owner state', () => {
    expect(
      reasonOf({
        worktrees: [wt('w1', 'r2', { owner_state: 'idle', ahead: 2, has_upstream: true })],
      }).reason,
    ).toBe('At risk: 2 commits only on this machine in sneat-go (worktree idle)')
    expect(
      reasonOf({
        worktrees: [
          wt('w1', 'r2', {
            owner_state: 'orphaned',
            ahead: 1,
            has_upstream: true,
          }),
        ],
      }).reason,
    ).toBe('At risk: 1 commit only on this machine in sneat-go (worktree orphaned)')
    expect(
      reasonOf({
        worktrees: [wt('w1', 'r2', { owner_state: 'idle', has_upstream: false })],
      }).reason,
    ).toBe('At risk: a branch with no upstream, only on this machine in sneat-go (worktree idle)')
  })

  it('names how many failing checks and which check, per pull request, and says nothing of a check that is not named', () => {
    const base = {
      worktrees: [wt('w1', 'r1', { owner_state: 'idle', ahead: 0, has_upstream: true })],
    }
    expect(
      reasonOf({
        ...base,
        pull_requests: [
          pullRequest('p1', 'r1', 'w1', {
            number: 131,
            checks_failed: 1,
            checks_green: false,
            failed_check: 'build-linux',
          }),
        ],
      }).reason,
    ).toBe('Checks failed: wb#131 has 1 failing check (build-linux)')
    expect(
      reasonOf({
        ...base,
        pull_requests: [
          pullRequest('p1', 'r1', 'w1', {
            number: 9,
            checks_failed: 3,
            checks_green: false,
          }),
        ],
      }).reason,
    ).toBe('Checks failed: wb#9 has 3 failing checks')
  })

  it('names a repository in full when two of the task share the short name, and with no name when the pull request has none', () => {
    const doc = {
      worktrees: [wt('w1', 'r1', { owner_state: 'idle', ahead: 0, has_upstream: true }), wt('w2', 'r3', { owner_state: 'idle', ahead: 0, has_upstream: true })],
      pull_requests: [
        pullRequest('p1', 'r1', 'w1', {
          number: 1,
          checks_failed: 1,
          checks_green: false,
        }),
        {
          ...pullRequest('p2', 'r1', 'w1', {
            number: 2,
            checks_failed: 1,
            checks_green: false,
          }),
          repository: undefined,
        },
      ],
    }
    expect(reasonOf(doc).reason).toBe('Checks failed: sneat-dev/wb#1 has 1 failing check; #2 has 1 failing check')
  })

  it('says an agent waits on you, and a run that failed or timed out with its exit code', () => {
    const base = {
      worktrees: [wt('w1', 'r1', { owner_state: 'idle', ahead: 0, has_upstream: true })],
    }
    expect(
      reasonOf({
        ...base,
        agents: [
          agent('a1', 'r1', 'live', {
            worktrees: ['w1'],
            runtime: 'claude',
            activity: 'blocked',
          }),
        ],
      }).reason,
    ).toBe('Blocked: claude is waiting on you')
    expect(
      reasonOf({
        ...base,
        agents: [
          run('run-1', 'failed', {
            worktrees: ['w1'],
            exit_code: 2,
            finished_at: ago(1),
          }),
        ],
      }).reason,
    ).toBe('Blocked: the claude opus run failed (exit 2)')
    expect(
      reasonOf({
        ...base,
        agents: [run('run-1', 'timeout', { worktrees: ['w1'], finished_at: ago(1) })],
      }).reason,
    ).toBe('Blocked: the claude opus run timed out')
  })

  it('counts the pull requests of a task that is ready to land', () => {
    const base = {
      worktrees: [wt('w1', 'r1', { owner_state: 'idle', ahead: 0, has_upstream: true })],
    }
    expect(
      reasonOf({
        ...base,
        pull_requests: [pullRequest('p1', 'r1', 'w1', { number: 1 }), pullRequest('p2', 'r1', 'w1', { number: 2 })],
      }).reason,
    ).toBe('Ready to land: 2 pull requests green and mergeable')
    expect(
      reasonOf({
        ...base,
        pull_requests: [pullRequest('p1', 'r1', 'w1', { number: 1 })],
      }).reason,
    ).toBe('Ready to land: 1 pull request green and mergeable')
  })

  it("says why each open pull request is not ready, in the library's words, skipping the ones that are", () => {
    const base = {
      worktrees: [wt('w1', 'r1', { owner_state: 'idle', ahead: 0, has_upstream: true })],
    }
    const pending = pullRequest('p1', 'r1', 'w1', {
      number: 131,
      checks_pending: 1,
      checks_green: false,
    })
    const unobserved = {
      ...pullRequest('p2', 'r2', 'w1', { number: 12 }),
      checked_at: undefined,
      state: 'open' as const,
    }
    const ready = pullRequest('p3', 'r1', 'w1', { number: 5 })
    const merged = pullRequest('p4', 'r1', 'w1', {
      number: 6,
      state: 'merged',
    })
    expect(reasonOf({ ...base, pull_requests: [pending, unobserved, ready, merged] }).reason).toBe('Not ready: wb#131 checks pending; sneat-go#12 pull request not yet checked')
  })

  it("says that another machine's ready pull request cannot make a task of this machine ready", () => {
    const remote = {
      ...pullRequest('p1', 'r1', 'w1', { number: 5 }),
      route: 'cached' as const,
    }
    expect(
      reasonOf({
        worktrees: [wt('w1', 'r1', { owner_state: 'idle', ahead: 0, has_upstream: true })],
        pull_requests: [remote],
      }).reason,
    ).toBe('Not ready: no open pull request is on this machine, and only this machine can make the task ready')
  })

  it('names at most three parts, then +n more', () => {
    const many = Array.from({ length: REASON_PARTS_SHOWN + 2 }, (_, index) =>
      pullRequest(`p${index}`, 'r1', 'w1', {
        number: index + 1,
        checks_pending: 1,
        checks_green: false,
      }),
    )
    const { reason } = reasonOf({
      worktrees: [wt('w1', 'r1', { owner_state: 'idle', ahead: 0, has_upstream: true })],
      pull_requests: many,
    })
    expect(reason).toBe('Not ready: wb#1 checks pending; wb#2 checks pending; wb#3 checks pending; +2 more')
  })

  it('says what is working: running agents and active worktrees', () => {
    expect(
      reasonOf({
        worktrees: [
          wt('w1', 'r1', {
            owner_state: 'active',
            ahead: 0,
            has_upstream: true,
          }),
        ],
        agents: [run('run-1', 'running', { worktrees: ['w1'] })],
      }).reason,
    ).toBe('Working: claude opus is running; a worktree in wb is active')
    expect(
      reasonOf({
        worktrees: [wt('w1', 'r1', { owner_state: 'idle', ahead: 0, has_upstream: true })],
        agents: [agent('a1', 'r1', 'live', { worktrees: ['w1'], activity: 'working' })],
      }).reason,
    ).toBe('Working: agent is running')
  })

  it('says landed, idle and not reported plainly', () => {
    const quiet = wt('w1', 'r1', {
      owner_state: 'idle',
      ahead: 0,
      has_upstream: true,
    })
    expect(
      reasonOf({
        worktrees: [quiet],
        pull_requests: [pullRequest('p1', 'r1', 'w1', { state: 'merged' })],
      }).reason,
    ).toBe('Landed: merged, with nothing left unpushed')
    expect(reasonOf({ worktrees: [quiet] }).reason).toBe('Idle: nothing is running and nothing waits on you')
    expect(reasonOf({ worktrees: [wt('w1', 'r1')] }).reason).toContain('State not reported: no owner state')
  })

  it('knows a task seen only from another machine', () => {
    expect(seenElsewhereOnly(reasonOf({ worktrees: [wt('w1', 'r1', { route: 'cached' })] }).task)).toBe(true)
    expect(
      seenElsewhereOnly(
        reasonOf({
          worktrees: [wt('w1', 'r1', { route: 'cached' }), wt('w2', 'r1')],
        }).task,
      ),
    ).toBe(false)
    expect(seenElsewhereOnly({ worktrees: [] } as unknown as TaskView)).toBe(false)
  })

  it('says that a task which only another machine reports cannot be made ready from here, in the words of a task that waits on this machine', () => {
    const remote = { ...pullRequest('p1', 'r1', 'w1', { number: 5 }), route: 'cached' as const, machine: 'beta' }
    const { reason, task } = reasonOf({ worktrees: [wt('w1', 'r1', { route: 'cached', machine: 'beta', owner_state: 'idle', ahead: 0, has_upstream: true })], pull_requests: [remote] })
    expect(task.stateSource).toBe('remote')
    expect(reason).toBe('Ready to land: 1 pull request green and mergeable')
  })

  it('gives none for a task the document does not list', () => {
    const model = new FleetModels({ now: () => NOW }).forDocument(fleetDocument(), NOW)
    expect(taskReason(model, 'nobody')).toBeUndefined()
  })
})
