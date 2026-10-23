import { buildAgentPanel, buildPullRequestPanel, buildRepositoryPanel, buildTaskPanel, buildWorktreePanel } from './entity-views'
import { FleetDocument, PullRequest, Worktree } from './fleet.types'
import { FleetModel } from './fleet-model'
import { buildWorktreeRows } from './list-rows'
import { buildRepositories } from './model-repositories'
import { OBSERVED, agent, fleetDocument, machine, pullRequest, repository, worktree } from './test-data'

const NOW = Date.parse(OBSERVED)

function modelOf(extra: Partial<FleetDocument>): FleetModel {
  return new FleetModel(fleetDocument({ worktrees: [], agents: [], repositories: [repository('r1', 'alpha', { name: 'sneat-dev/wb' })], pull_requests: [], machines: [machine('alpha')], ...extra }), { now: () => NOW })
}

const wt = (id: string, task: string, branch: string, extra: Partial<Worktree> = {}): Worktree => ({ ...worktree(id, 'r1', 'alpha'), task, branch, ...extra })
const pr = (id: string, worktreeId: string | undefined, extra: Partial<PullRequest> = {}): PullRequest => pullRequest(id, 'r1', worktreeId, { number: Number(id.replace(/\D/g, '')) || 1, ...extra })

describe('worktreePullRequests, the one worktree-to-pull-request join', () => {
  it('joins a pull request that names a worktree to that worktree only', () => {
    const model = modelOf({ worktrees: [wt('w1', 'a', 'main'), wt('w2', 'b', 'main')], pull_requests: [pr('p1', 'w1', { branch: 'main' })] })
    expect([...model.worktreePullRequests.keys()]).toEqual(['w1'])
    expect(model.worktreePullRequests.get('w1')?.map((p) => p.id)).toEqual(['p1'])
  })

  it('joins one that names none to the worktrees of its repository and branch, and one that names a missing worktree the same way', () => {
    const model = modelOf({
      worktrees: [wt('w1', 'a', 'task/a'), wt('w2', 'a', 'task/a', { machine_id: 'mach-beta' }), wt('w3', 'b', 'task/b'), wt('w4', 'c', 'task/c')],
      pull_requests: [pr('p1', undefined, { branch: 'task/a' }), pr('p2', 'w-gone', { branch: 'task/b' }), pr('p3', undefined, { branch: 'nope' }), pr('p4', undefined, { repository: undefined, branch: 'task/c' }), pr('p5', undefined, { branch: undefined })],
    })
    expect(Object.fromEntries([...model.worktreePullRequests].map(([id, list]) => [id, list.map((p) => p.id)]))).toEqual({ w1: ['p1'], w2: ['p1'], w3: ['p2'] })
    expect(model.taskOfPullRequest(model.document.pull_requests[0])).toBe('a')
    expect(model.taskOfPullRequest(model.document.pull_requests[1])).toBe('b')
    expect(model.taskOfPullRequest(model.document.pull_requests[2])).toBeUndefined()
  })

  it('lists several pull requests of a worktree in document order', () => {
    const model = modelOf({ worktrees: [wt('w1', 'a', 'task/a')], pull_requests: [pr('p2', 'w1'), pr('p1', undefined, { branch: 'task/a' })] })
    expect(model.worktreePullRequests.get('w1')?.map((p) => p.id)).toEqual(['p2', 'p1'])
  })

  // cockpit-views#ac:worktree-pr-join-is-one
  it('is what the pr chip, the row, the panel and the task and agent panels all read', () => {
    const model = modelOf({
      worktrees: [wt('w1', 'a', 'task/a'), wt('w2', 'b', 'task/b'), wt('w3', 'c', 'task/c')],
      pull_requests: [pr('p1', undefined, { branch: 'task/a' }), pr('p2', 'w2')],
      agents: [agent('s1', 'r1', 'live', { task: 'a', worktrees: ['w1', 'w2'] })],
    })
    const rows = buildWorktreeRows(model)
    for (const row of rows) {
      const panel = buildWorktreePanel(model, row.id)
      expect(row.chips.has('pr'), row.id).toBe((panel?.related.pullRequests.length ?? 0) > 0)
    }
    expect(rows.filter((row) => row.chips.has('pr')).map((row) => row.id)).toEqual(['w1', 'w2'])
    expect(buildWorktreePanel(model, 'w1')?.related.pullRequests.map((p) => p.id)).toEqual(['p1'])
    expect(buildTaskPanel(model, 'a')?.related.pullRequests.map((p) => p.id)).toEqual(['p1'])
    // The agent works on w1 and w2: its panel lists their pull requests, once each.
    expect(buildAgentPanel(model, 's1')?.related.pullRequests.map((p) => p.id)).toEqual(['p1', 'p2'])
  })
})

describe('checked web addresses in the views', () => {
  const unsafe = 'javascript:alert(1)'
  const document: Partial<FleetDocument> = {
    repositories: [repository('r1', 'alpha', { name: 'sneat-dev/wb', remote_url_web: unsafe }), repository('r2', 'alpha', { name: 'acme/ok', remote_url_web: 'https://github.com/acme/ok' })],
    worktrees: [wt('w1', 'a', 'task/a', { repository: 'r1', owner_state: 'idle', ahead: 1 }), wt('w2', 'b', 'task/b', { repository: 'r2' })],
    pull_requests: [pr('p1', 'w1', { url: unsafe, checks_failed: 1, checks_green: false, failed_check: 'ci' }), pr('p2', 'w2', { url: 'https://github.com/acme/ok/pull/2', mergeable: 'dirty' })],
  }

  // cockpit-views#ac:web-addresses-are-checked
  it('give a pull request url only when it is an https address with a plain host, in the panels and in Home', () => {
    const model = modelOf(document)
    expect(buildPullRequestPanel(model, 'p1')?.summary.url).toBeUndefined()
    expect(buildPullRequestPanel(model, 'p2')?.summary.url).toBe('https://github.com/acme/ok/pull/2')
    expect(buildWorktreePanel(model, 'w1')?.related.pullRequests.map((p) => p.url)).toEqual([undefined])
    expect(buildTaskPanel(model, 'a')?.related.pullRequests.map((p) => p.url)).toEqual([undefined])
    expect(buildRepositoryPanel(model, 'sneat-dev/wb')?.related.pullRequests.map((p) => p.url)).toEqual([undefined, 'https://github.com/acme/ok/pull/2'])
    const items = Object.fromEntries(model.needsYou.items.map((item) => [item.task, item]))
    // The task has no recorded activity, so its at-risk row is a cleanup matter and the failed checks are its lead.
    expect(items['a']).toMatchObject({ kind: 'pr-checks-failed', url: undefined })
    expect((model.needsYou.items.find((item) => item.kind === 'pr-needs-you') as { url?: string } | undefined)?.url).toBe('https://github.com/acme/ok/pull/2')
  })

  it('keeps the raw entries exactly as the read model sent them, for the Raw data block', () => {
    const model = modelOf(document)
    expect(buildPullRequestPanel(model, 'p1')?.raw).toEqual([expect.objectContaining({ url: unsafe })])
  })

  it('refuses an unsafe pull request url on Home\'s failed-checks row and ready-to-land row, and a repository\'s web address', () => {
    const model = modelOf({
      worktrees: [wt('w1', 'a', 'x', { owner_state: 'active' }), wt('w2', 'b', 'y', { owner_state: 'active' })],
      pull_requests: [pr('p1', 'w1', { url: unsafe, checks_failed: 1, checks_green: false }), pr('p2', 'w2', { url: 'http://plain.example/2' })],
      repositories: [repository('r1', 'alpha', { name: 'sneat-dev/wb', remote_url_web: unsafe })],
    })
    expect(model.needsYou.items[0]).toMatchObject({ kind: 'pr-checks-failed', url: undefined })
    expect(model.readyToLand.ready[0].pullRequests[0].url).toBeUndefined()
    expect(buildRepositories(model)[0].webUrl).toBeUndefined()
    expect(buildRepositories(modelOf(document)).map((row) => row.webUrl)).toEqual([undefined, 'https://github.com/acme/ok'])
  })
})
