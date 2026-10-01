import { AgeTerm, FleetModel, MachineMetrics, MetricsSample, PanelCommand, PullRequest, RegistryAction, Machine, branchCleanup, pullRequestCreate, worktreeList } from '@cockpit/fleet-data'
import { agent, machine, pullRequest, registryAction, repository, run, worktree } from '@cockpit/fleet-data/testing'
import type { BadgeKind } from '@cockpit/ui/control'

// The data of the gallery (the preview build's page that shows every
// control-surface component in every state): fixtures from the fleet-data
// library, so a command shown is a command the library built.

/** Every value of every state vocabulary, with one that is outside it and one that is absent. */
export const BADGE_ROWS: { kind: BadgeKind; title: string; values: (string | undefined)[] }[] = [
  { kind: 'task', title: 'Task state', values: ['at-risk', 'checks-failed', 'blocked', 'ready', 'not-ready', 'working', 'landed', 'idle', 'not-reported'] },
  { kind: 'owner', title: 'Owner state', values: ['active', 'idle', 'orphaned', 'unknown'] },
  { kind: 'agent-activity', title: 'Agent activity', values: ['working', 'blocked', 'idle', 'done', 'unknown', undefined] },
  { kind: 'agent-state', title: 'Agent state', values: ['running', 'live', 'parked', 'completed', 'failed', 'timeout', 'abandoned'] },
  { kind: 'pr-state', title: 'Pull request state', values: ['open', 'draft', 'merged', 'closed', undefined] },
  { kind: 'mergeable', title: 'Merge state', values: ['clean', 'blocked', 'dirty', 'behind', 'unstable', 'draft', 'unknown'] },
  { kind: 'code-index', title: 'Code index', values: ['fresh', 'stale', 'diverged', 'pending', 'failed', 'never'] },
  { kind: 'route', title: 'Machine route and freshness', values: ['local', 'live', 'cached', 'stale', 'none'] },
  { kind: 'load', title: 'Machine load', values: ['free', 'busy', 'not-reported'] },
  { kind: 'checks', title: 'Checks', values: ['passed', 'failed', 'pending', 'unknown'] },
]

/** Sync facts, one set per row of the gallery. */
export const SYNC_ROWS: { title: string; ahead?: number; behind?: number; upstreamGone?: boolean; hasUpstream?: boolean }[] = [
  { title: 'Two commits not pushed', ahead: 2 },
  { title: 'Three commits behind', behind: 3 },
  { title: 'Both', ahead: 1, behind: 4 },
  { title: 'Upstream gone', upstreamGone: true },
  { title: 'No upstream', hasUpstream: false },
  { title: 'Clean, or on another machine: nothing is drawn' },
]

/** Pull requests in the states a row can show. */
export function galleryPullRequests(now: number): { title: string; pullRequest: PullRequest }[] {
  const ago = (minutes: number) => new Date(now - minutes * 60_000).toISOString()
  const make = (title: string, number: number, extra: Partial<PullRequest>) => ({ title, pullRequest: pullRequest(`p${number}`, 'r1', undefined, { number, checked_at: ago(4), ...extra }) })
  return [
    make('Open, checks green', 128, {}),
    make('Open, a check failed', 131, { checks_total: 8, checks_passed: 6, checks_failed: 2, checks_green: false, failed_check: 'build-linux-amd64-integration-tests', checked_at: ago(52) }),
    make('Open, checks pending', 140, { checks_total: 8, checks_passed: 3, checks_pending: 5, checks_green: false, checked_at: ago(1) }),
    make('Draft', 141, { state: 'draft', mergeable: 'draft', checks_total: undefined, checks_passed: undefined, checks_failed: undefined, checks_pending: undefined, checks_green: undefined }),
    make('Merged', 99, { state: 'merged', checked_at: ago(60 * 30) }),
    make('Never observed', 150, { state: undefined, mergeable: undefined, checks_total: undefined, checks_passed: undefined, checks_failed: undefined, checks_pending: undefined, checks_green: undefined, checked_at: undefined, url: undefined }),
  ]
}

/** Machines in the states a chip can show. */
export function galleryMachines(now: number): { title: string; machine: Machine; stale: boolean }[] {
  const ago = (hours: number) => new Date(now - hours * 3_600_000).toISOString()
  const make = (title: string, name: string, extra: Partial<Machine>, stale = false) => ({ title, machine: { ...machine(name), ...extra }, stale })
  return [
    make('This machine', 'alpha', { route: 'local' }),
    make('Cached, three hours old', 'vm', { route: 'cached', observed_at: ago(3) }),
    make('Cached and stale', 'laptop', { route: 'cached', observed_at: ago(52) }, true),
    make('Live over HTTP', 'build-box', { route: 'live-remote', transport: 'http' }),
    make('Live over SSH', 'hetzner', { route: 'live-remote', transport: 'ssh' }),
  ]
}

const OBSERVED_NOW = Date.parse('2026-10-01T10:00:00Z')

/** A small fleet whose panels give the "Copy command" lists of the gallery. */
function galleryModel(): FleetModel {
  const onVm = { ...worktree('w3', 'r3', 'vm'), route: 'cached' as const, task: 'fix-ci', branch: 'task/fix-ci' }
  return new FleetModel(
    {
      schema_version: 2,
      warming_up: false,
      repositories_total: 2,
      repositories_scanned: 2,
      diagnostics: 0,
      machines: [machine('alpha'), machine('vm', 'cached')],
      repositories: [repository('r1', 'alpha', { name: 'sneat-dev/wb' }), repository('r3', 'vm', { name: 'sneat-dev/wb', route: 'cached' })],
      worktrees: [{ ...worktree('w1', 'r1', 'alpha'), task: 'fix-ci', branch: 'task/fix-ci' }, onVm],
      pull_requests: [pullRequest('p1', 'r1', 'w1', { number: 12 })],
      agents: [run('run-1', 'running', { task: 'fix-ci', worktrees: ['w1'], repository: 'r1' }), agent('s1', 'r1', 'live', { runtime: 'claude' })],
    },
    { now: () => OBSERVED_NOW },
  )
}

/** The commands of a panel of the gallery's own fleet, which has every entity it asks for. */
const commandsOf = (panel: { commands: PanelCommand[] } | undefined): PanelCommand[] => (panel as { commands: PanelCommand[] }).commands

/** The copy-command lists of the gallery: the entities of the requirement, a refused value and an edit-before-running entry. */
export function galleryCommandLists(): { title: string; entries: PanelCommand[] }[] {
  const model = galleryModel()
  return [
    { title: 'Worktree on this machine', entries: commandsOf(model.worktreeView('w1')) },
    { title: 'Worktree on another machine, no SSH route', entries: commandsOf(model.worktreeView('w3')) },
    { title: 'Pull request', entries: commandsOf(model.pullRequestView('p1')) },
    { title: 'Repository (placeholders to edit)', entries: commandsOf(model.repositoryView('sneat-dev/wb')) },
    { title: 'Dispatched run', entries: commandsOf(model.agentView('run-1')) },
    {
      title: 'Refused values',
      entries: [
        { title: 'Task named with a leading dash', command: worktreeList('-x') },
        { title: 'Task with a line break', command: pullRequestCreate('a\nb') },
        { title: 'Branch named like an option', command: branchCleanup('acme/web', '--upstream') },
        { title: 'Task with a quote (copied, quoted)', command: worktreeList("a'; rm -rf ~; '") },
      ],
    },
  ]
}

/** What a registry returns for a worktree: a guarded action, a destructive one that cannot run, and one the application has never heard of. */
export function galleryRegistry(): RegistryAction[] {
  return [
    registryAction('pr.create', 'Open pull request', { safety: 'guarded', capability: 'pr.create' }),
    registryAction('branch.push', 'Push branch', { applicable: false, reason: 'Nothing to push', capability: 'branch.push' }),
    registryAction('test.extra', 'An extra test action'),
    registryAction('worktree.discard', 'Discard worktree', { safety: 'destructive', applicable: false, reason: '2 commits are not on the remote', capability: 'worktree.discard' }),
    registryAction('branch.delete', 'Delete branch', { safety: 'destructive', capability: 'branch.delete' }),
  ]
}

/** The same registry as an anonymous reader sees it: nothing is permitted. */
export function galleryAnonymousRegistry(): RegistryAction[] {
  return galleryRegistry().map((action) => ({ ...action, permitted: false }))
}

/** An hour of machine metrics, one sample a minute, with a stretch of twelve minutes missing. */
export function galleryMetrics(now: number): MachineMetrics {
  const samples: MetricsSample[] = []
  for (let minute = 59; minute >= 0; minute--) {
    if (minute <= 34 && minute >= 23) continue
    const wave = Math.sin((59 - minute) / 6)
    samples.push({
      cpu_percent: Math.round(38 + 30 * wave + (minute % 5) * 2),
      load1: Math.round((1.6 + 1.2 * wave) * 100) / 100,
      memory_used_bytes: Math.round((11 + 0.04 * (59 - minute)) * 1e9),
      memory_total_bytes: 16e9,
      disk_free_bytes: Math.round((310 - 0.05 * (59 - minute)) * 1e9),
      disk_total_bytes: 500e9,
      sampled_at: new Date(now - minute * 60_000).toISOString(),
    })
  }
  return { machine: 'alpha', route: 'local', fetched_at: new Date(now).toISOString(), samples }
}

/** Landed tasks per day for the last fourteen days. */
export function galleryLandedPerDay(now: number): { label: string; value: number }[] {
  return Array.from({ length: 14 }, (_, index) => {
    const day = new Date(now - (13 - index) * 86_400_000)
    return { label: `${String(day.getMonth() + 1).padStart(2, '0')}-${String(day.getDate()).padStart(2, '0')}`, value: [2, 3, 0, 0, 4, 6, 5, 3, 1, 0, 0, 7, 4, 5][index] }
  })
}

/** Worktree age buckets, each a link into the Worktrees list. */
export const GALLERY_AGE_BUCKETS: { label: string; value: number; term: AgeTerm }[] = [
  { label: 'under 1 day', value: 14, term: '<1d' },
  { label: '1 to 7 days', value: 22, term: '1-7d' },
  { label: '8 to 30 days', value: 9, term: '8-30d' },
  { label: '31 to 90 days', value: 3, term: '31-90d' },
  { label: 'over 90 days', value: 1, term: '>90d' },
]
