import { MachineRoute } from './fleet.types'
import { buildAgentPanel, buildMachinePanel, buildPullRequestPanel, buildRepositoryPanel, buildTaskPanel, buildWorktreePanel } from './entity-views'
import { FleetModel } from './fleet-model'
import { agent, machine, pullRequest, repository, run, worktree } from './test-data'

const NOW = Date.parse('2026-10-01T10:00:00Z')
const ago = (hours: number): string => new Date(NOW - hours * 3_600_000).toISOString()

const ROUTES: MachineRoute[] = [{ machine_id: 'mach-vm', ssh: { host: 'vm.example', user: 'alex', wb_path: '/usr/local/bin/wb' } }]

function modelOf(routes?: MachineRoute[]): FleetModel {
  const vmWorktree = { ...worktree('w3', 'r3', 'vm'), route: 'cached' as const, task: 'fix-ci', branch: 'task/fix-ci', last_activity_at: ago(5) }
  return new FleetModel(
    {
      schema_version: 2,
      warming_up: false,
      repositories_total: 3,
      repositories_scanned: 3,
      diagnostics: 0,
      machines: [machine('alpha'), { ...machine('vm', 'cached'), wb_version: '0.176.0', boot_time: ago(48) }],
      repositories: [
        repository('r1', 'alpha', { name: 'sneat-dev/wb', remote_url_web: 'https://github.com/sneat-dev/wb' }),
        repository('r3', 'vm', { name: 'sneat-dev/wb', route: 'cached' }),
        repository('r2', 'alpha', { name: 'sneat-co/sneat-go' }),
      ],
      worktrees: [
        { ...worktree('w1', 'r1', 'alpha'), task: 'fix-ci', branch: 'task/fix-ci', owner_state: 'active', ahead: 1, last_activity_at: ago(1) },
        { ...worktree('w2', 'r2', 'alpha'), task: 'other', last_activity_at: ago(2) },
        vmWorktree,
      ],
      pull_requests: [pullRequest('p1', 'r1', 'w1', { number: 12 }), pullRequest('p6', 'r1', undefined, { number: 6, repository: undefined, worktree: undefined }), pullRequest('p2', 'r2', 'w2', { number: 7, checked_at: undefined, state: undefined })],
      agents: [
        run('run-1', 'running', { task: 'fix-ci', worktrees: ['w1', 'missing'], started_at: ago(1), exit_code: 1, finished_at: ago(0.5), repository: 'r1' }),
        agent('s1', 'r1', 'live', { runtime: 'claude', model: 'opus' }),
        { ...agent('s2', undefined, 'live'), route: 'cached' as const, machine: 'vm', machine_id: 'mach-vm', worktrees: ['w3'] },
      ],
    },
    { now: () => NOW, machineRoutes: routes },
  )
}

describe('entity panels', () => {
  it('has a worktree panel: summary, related entities, the three task commands and the raw entry', () => {
    const panel = buildWorktreePanel(modelOf(), 'w1')
    expect(panel?.summary).toMatchObject({ id: 'w1', task: 'fix-ci', repository: 'sneat-dev/wb', branch: 'task/fix-ci', ownerState: 'active', ahead: 1, route: 'local', lastActivityAt: NOW - 3_600_000 })
    expect(panel?.related.task?.name).toBe('fix-ci')
    expect(panel?.related.repository?.key).toBe('sneat-dev/wb')
    expect(panel?.related.pullRequests.map((p) => p.id)).toEqual(['p1'])
    expect(panel?.related.agents.map((a) => a.id)).toEqual(['run-1', 's2'])
    // A pull request of another worktree of the task is not this worktree's.
    expect(buildWorktreePanel(modelOf(), 'w3')?.related.pullRequests).toEqual([])
    expect(panel?.commands.map((c) => [c.title, c.command.ok && c.command.text, c.command.ok && c.command.needsEdit])).toEqual([
      ['List worktrees', "wb worktree list 'fix-ci'", false],
      ['Commit and open pull request', "wb pr create 'fix-ci' --commit-all --message=<<<edit:message>>>", true],
      ['Plan cleanup (dry run)', "wb worktree cleanup 'fix-ci'", false],
    ])
    expect(panel?.raw).toEqual([modelOf().worktreeById('w1')])
    expect(buildWorktreePanel(modelOf(), 'nope')).toBeUndefined()
  })

  it('labels a command for another machine "run on <machine>" for an anonymous reader, and uses the SSH route for an owner', () => {
    const anonymous = buildWorktreePanel(modelOf(), 'w3')
    expect(anonymous?.commands[0].command).toEqual({ ok: true, text: "wb worktree list 'fix-ci'", label: 'run on vm', needsEdit: false })
    expect(anonymous?.related.repository?.key).toBe('sneat-dev/wb')
    const owner = buildWorktreePanel(modelOf(ROUTES), 'w3')
    expect(owner?.commands[0].command).toEqual({ ok: true, text: "ssh alex@vm.example /usr/local/bin/wb worktree list 'fix-ci'", needsEdit: false })
    expect(modelOf(ROUTES).machineRoutes).toBe(ROUTES)
    expect(modelOf().machineRoutes).toBeUndefined()
  })

  it('has a task panel anchored on this machine, with the states reasons and unobserved count', () => {
    const panel = buildTaskPanel(modelOf(), 'fix-ci')
    expect(panel?.summary).toMatchObject({ name: 'fix-ci', state: 'ready', repositories: ['sneat-dev/wb'], unobservedPullRequests: 0, notReadyReasons: [] })
    expect(panel?.related.worktrees.map((w) => w.id)).toEqual(['w1', 'w3'])
    expect(panel?.commands[0].command).toMatchObject({ text: "wb worktree list 'fix-ci'" })
    expect(panel?.commands[0].command.ok && panel.commands[0].command.label).toBeFalsy()
    expect(panel?.raw).toHaveLength(2 + 1 + 2)
    const other = buildTaskPanel(modelOf(), 'other')
    expect(other?.summary).toMatchObject({ unobservedPullRequests: 1, notReadyReasons: [] })
    expect(buildTaskPanel(modelOf(), 'nope')).toBeUndefined()
    // A task with no worktree has no anchor: its commands run here.
    const ghost = buildTaskPanel(new FleetModel({ ...modelOf().document, agents: [run('r', 'failed', { task: 'ghost' })] }, { now: () => NOW }), 'ghost')
    expect(ghost?.commands[0].command).toMatchObject({ ok: true, text: "wb worktree list 'ghost'" })
  })

  it('has a repository panel with its checkouts, related entities and the placeholder commands', () => {
    const panel = buildRepositoryPanel(modelOf(), 'sneat-dev/wb')
    expect(panel?.summary.checkouts.map((c) => c.machine)).toEqual(['alpha', 'vm'])
    expect(panel?.related.worktrees.map((w) => w.id)).toEqual(['w1', 'w3'])
    expect(panel?.related.pullRequests.map((p) => p.id)).toEqual(['p1'])
    expect(panel?.related.agents.map((a) => a.id)).toEqual(['run-1', 's1'])
    expect(panel?.commands.map((c) => c.command.ok && c.command.text)).toEqual([
      "wb worktree create <<<edit:task>>> 'sneat-dev/wb' --model=<<<edit:model>>> --original-prompt-file=<<<edit:file>>>",
      "wb branch list --repo='sneat-dev/wb'",
      "wb fleet status --filter='sneat-dev/wb'",
    ])
    expect(panel?.raw).toHaveLength(2)
    expect(buildRepositoryPanel(modelOf(), 'nope')).toBeUndefined()
  })

  it('has an agent panel: a dispatched run has the agent verbs, a session has none and says it cannot be controlled', () => {
    const model = modelOf()
    const runPanel = buildAgentPanel(model, 'run-1')
    expect(runPanel?.summary).toMatchObject({ kind: 'run', label: 'claude opus', controllable: true, remote: false, task: 'fix-ci', repository: 'sneat-dev/wb', exitCode: 1, finishedAt: NOW - 1_800_000 })
    expect(runPanel?.commands.map((c) => c.command.ok && c.command.text)).toEqual(["wb agent status 'run-1'", "wb agent logs 'run-1'", "wb agent stop 'run-1'"])
    expect(runPanel?.related.worktrees.map((w) => w.id)).toEqual(['w1'])
    expect(runPanel?.related.pullRequests.map((p) => p.id)).toEqual(['p1'])
    expect(runPanel?.related.task?.name).toBe('fix-ci')
    const session = buildAgentPanel(model, 's1')
    expect(session?.summary).toMatchObject({ controllable: false, task: undefined, repository: 'sneat-dev/wb' })
    expect(session?.commands).toEqual([])
    expect(session?.related).toEqual({ task: undefined, worktrees: [], pullRequests: [] })
    const remote = buildAgentPanel(model, 's2')
    expect(remote?.summary).toMatchObject({ remote: true, machine: 'vm', repository: undefined })
    expect(remote?.related.task?.name).toBe('fix-ci')
    expect(buildAgentPanel(model, 'nope')).toBeUndefined()
    // A run with no run_id uses its entry id.
    const bare = new FleetModel({ ...model.document, agents: [run('only', 'running', { run_id: undefined })] }, { now: () => NOW })
    expect(buildAgentPanel(bare, 'only')?.commands[0].command).toMatchObject({ text: "wb agent status 'only'" })
  })

  it('has a machine panel with its running agents and recent worktrees, and no commands', () => {
    const panel = buildMachinePanel(modelOf(), 'mach-alpha')
    expect(panel?.summary.state).toBe('live')
    expect(panel?.related.runningAgents.map((a) => a.id)).toEqual(['run-1', 's1'])
    expect(panel?.related.recentWorktrees.map((w) => w.id)).toEqual(['w1', 'w2'])
    expect(panel?.commands).toEqual([])
    expect(panel?.raw).toEqual([modelOf().machineById('mach-alpha')])
    expect(buildMachinePanel(modelOf(), 'mach-vm')?.summary.uptimeMs).toBe(48 * 3_600_000)
    expect(buildMachinePanel(modelOf(), 'nope')).toBeUndefined()
    const many = new FleetModel({ ...modelOf().document, worktrees: Array.from({ length: 8 }, (_, i) => ({ ...worktree(`x${i}`, 'r1', 'alpha'), last_activity_at: i % 2 ? undefined : ago(i) })) }, { now: () => NOW })
    expect(buildMachinePanel(many, 'mach-alpha')?.related.recentWorktrees).toHaveLength(5)
  })

  it('has a pull request panel with checks, why it is not ready, and the land command', () => {
    const panel = buildPullRequestPanel(modelOf(), 'p1')
    expect(panel?.summary).toMatchObject({ repository: 'sneat-dev/wb', number: 12, observed: true, checksTotal: 5, checksPassed: 5, checksGreen: true, notReadyReasons: [] })
    expect(panel?.commands[0].command).toMatchObject({ text: "wb pr land 'sneat-dev/wb#12'" })
    expect(panel?.related.worktree?.id).toBe('w1')
    expect(panel?.related.task?.name).toBe('fix-ci')
    const unobserved = buildPullRequestPanel(modelOf(), 'p2')
    expect(unobserved?.summary).toMatchObject({ observed: false, notReadyReasons: [], repository: 'sneat-co/sneat-go' })
    expect(unobserved?.related.task?.name).toBe('other')
    expect(unobserved?.related.worktree?.id).toBe('w2')
    const nameless = buildPullRequestPanel(new FleetModel({ ...modelOf().document, pull_requests: [pullRequest('p9', 'r1', undefined, { repository: undefined, state: 'draft', mergeable: 'draft' })] }, { now: () => NOW }), 'p9')
    expect(nameless?.summary.notReadyReasons).toEqual(['draft'])
    expect(nameless?.commands).toEqual([])
    expect(buildPullRequestPanel(modelOf(), 'nope')).toBeUndefined()
  })
})
