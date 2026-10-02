import { fleet, modelOf, only } from './home-testing'
import { needsYouRows } from './needs-you-rows'

describe('needsYouRows', () => {
  it('lists one row per task, worst first, with the reason in words and its one action', () => {
    const rows = needsYouRows(modelOf(fleet()))
    expect(rows.map((row) => [row.task, row.state, row.reason])).toEqual([
      ['refactor-cache', 'at-risk', '3 unpushed commits and its owner process is gone'],
      ['add-search', 'at-risk', '2 unpushed commits and it has no running owner'],
      ['fix-ci-race', 'checks-failed', 'build-linux failed on sneat-dev/wb#131'],
      ['migrate-auth', 'blocked', 'agent blocked'],
      ['bump-deps', 'blocked', 'run failed, exit code 2'],
      ['3 blocked agents with no task', 'blocked', ''],
    ])
    expect(rows.map((row) => row.age)).toEqual(['1 h ago', '8 h ago', '40 min ago', '50 min ago', '5 h ago', undefined])
    expect(rows[0].at).toBe('2026-10-01T09:00:00.000Z')
  })

  it('names the repositories of work at risk, and a branch only when it says more than the task', () => {
    const document = fleet()
    document.worktrees.find((worktree) => worktree.task === 'add-search')!.branch = 'codex/split-cache'
    const rows = needsYouRows(modelOf(document))
    expect(rows[0].places).toEqual([
      { repository: 'sneat-dev/wb', branch: undefined },
      { repository: 'sneat-co/sneat-go', branch: undefined },
    ])
    expect(rows[1].places).toEqual([{ repository: 'sneat-co/sneat-go', branch: 'codex/split-cache' }])
  })

  it('gives work at risk "Open task" as its action and the push or copy for each worktree beside it, a failure a link out, an agent a link in', () => {
    const rows = needsYouRows(modelOf(fleet()))
    expect(rows[0].action).toEqual({ kind: 'route', text: 'Open task', label: 'Open task refactor-cache', link: { path: '/tasks', query: { sel: 'refactor-cache' } } })
    expect(rows[2].work).toBeUndefined()
    expect(rows[5].work).toBeUndefined()
    expect(rows[0].work).toEqual({
      task: 'refactor-cache',
      worktrees: [
        { id: 'wt-1', branch: 'task/refactor-cache', repository: 'sneat-dev/wb' },
        { id: 'wt-2', branch: 'task/refactor-cache', repository: 'sneat-co/sneat-go' },
      ],
    })
    expect(rows[2].action).toEqual({ kind: 'external', text: 'Open failure', label: 'Open failure of sneat-dev/wb#131', href: 'https://github.com/example/r-wb/pull/131' })
    expect(rows[3].action).toMatchObject({ kind: 'route', text: 'Open agent', link: { path: '/agents/vm-blocked', query: {} } })
    expect(rows[4].action).toMatchObject({ kind: 'route', text: 'Open agent' })
    expect(rows[5].action).toMatchObject({ kind: 'route', text: 'Open agents', link: { path: '/agents', query: { chips: 'blocked' } } })
  })

  it('says a task another machine reported, with that machine named', () => {
    const rows = needsYouRows(modelOf(fleet()))
    expect(rows[3]).toMatchObject({ reportedBy: 'vm', machine: { name: 'vm', chip: 'ssh', stale: false, title: 'vm: read live over ssh' } })
    expect(rows[0].reportedBy).toBeUndefined()
    expect(rows[0].machine).toBeUndefined()
    // A local agent's machine is its name alone.
    expect(rows[4].machine).toEqual({ name: 'mac', chip: '', stale: false, title: undefined })
  })

  it('opens the task when a pull request has no address that is safe to link', () => {
    const document = fleet()
    for (const pullRequest of document.pull_requests) if (pullRequest.number === 131 || pullRequest.number === 128) pullRequest.url = 'javascript:alert(1)'
    const rows = needsYouRows(modelOf(document))
    expect(rows[2].action).toMatchObject({ kind: 'route', text: 'Open task', link: { path: '/tasks', query: { sel: 'fix-ci-race' } } })
    const unlinkable = only('drop-legacy')
    unlinkable.pull_requests[0].url = 'javascript:alert(1)'
    const dirty = needsYouRows(modelOf(unlinkable))[0]
    expect(dirty).toMatchObject({ task: 'drop-legacy', state: 'not-ready', action: { kind: 'route', text: 'Open task' } })
    expect(dirty.reason).toContain('sneat-dev/wb#128')
  })

  it('links a green pull request that needs a merge, and a finished agent opens its task', () => {
    const dirty = needsYouRows(modelOf(only('drop-legacy')))[0]
    expect(dirty.action.kind).toBe('external')
    expect(dirty.action).toMatchObject({ text: 'Open pull request', href: 'https://github.com/example/r-wb/pull/128' })
    const document = only('add-search')
    document.worktrees[0].owner_state = 'active'
    const finished = needsYouRows(modelOf(document))[0]
    expect(finished).toMatchObject({ task: 'add-search', reason: 'agent finished, work not pushed', machine: { name: 'mac', chip: '' }, action: { kind: 'route', text: 'Open task' } })
  })

  it('names the repository of a blocked agent, says a failed run that had no exit code, and a failure without a check name', () => {
    const document = fleet()
    document.agents.find((agent) => agent.id === 'vm-blocked')!.repository = 'r-go'
    Object.assign(
      document.agents.find((agent) => agent.id === 'run-failed')!,
      { state: 'timeout', exit_code: undefined },
    )
    delete document.pull_requests.find((pullRequest) => pullRequest.number === 131)!.failed_check
    const rows = needsYouRows(modelOf(document))
    expect(rows[3].places).toEqual([{ repository: 'sneat-co/sneat-go' }])
    expect(rows[4].reason).toBe('run timeout')
    expect(rows[2].reason).toBe('checks failed on sneat-dev/wb#131')
  })

  it('has no age for a task whose activity was never recorded', () => {
    const document = fleet()
    document.worktrees.find((worktree) => worktree.task === 'bump-deps')!.last_activity_at = undefined
    const row = needsYouRows(modelOf(document)).find((candidate) => candidate.task === 'bump-deps')!
    expect(row.at).toBeUndefined()
    expect(row.age).toBeUndefined()
  })

  it('writes one blocked agent without a plural', () => {
    const document = fleet()
    document.agents = document.agents.filter((agent) => !agent.id.startsWith('orphan-') || agent.id === 'orphan-1')
    const last = needsYouRows(modelOf(document)).at(-1)
    expect(last?.task).toBe('1 blocked agent with no task')
  })

  it('has no rows for a calm fleet', () => {
    expect(needsYouRows(modelOf(fleet('healthy')))).toEqual([])
  })
})
