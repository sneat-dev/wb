import { FleetDocument } from '@cockpit/fleet-data'
import { agent, fleetDocument, machine, pullRequest, repository, run, worktree } from '@cockpit/fleet-data/testing'
import { NOW } from '../test-harness'

const HOUR = 60 * 60 * 1000
export const hoursAgo = (hours: number) => new Date(NOW - hours * HOUR).toISOString()

/**
 * A fleet of three machines (alpha is this one; beta is cached and reports agents; gamma is cached and reports none)
 * and these agents, in the order of the default sort:
 * - `run-1`: a running dispatched run of claude sonnet-5-5 on task `fix-ci` in `acme/r1`, working, with a pull request;
 * - `s-block`: a live claude session blocked on you, over worktrees of two tasks (`add-search` and `zeta`);
 * - `s-free`: a live claude opus session with no work link, started 2 h ago, activity not reported;
 * - `s-beta`: a live codex session cached from beta, on the worktree `w3` of `fix-ci`;
 * - `run-fail`: a failed run that finished 3 h ago with exit code 1;
 * - `s-parked`: a parked session of a runtime and model unknown, started 3 days ago;
 * - `run-bare`: a completed run with no timestamps and no work.
 */
export function agentsDocument(): FleetDocument {
  const beta = { ...machine('beta', 'cached'), observed_at: hoursAgo(5), transport: 'ssh' as const }
  return fleetDocument({
    machines: [machine('alpha'), beta, { ...machine('gamma', 'cached'), observed_at: hoursAgo(1) }],
    repositories: [repository('r1', 'alpha'), repository('r2', 'beta', { route: 'cached', host: undefined, name: 'acme/r2' })],
    worktrees: [
      { ...worktree('w1', 'r1', 'alpha'), task: 'fix-ci', branch: 'fix-ci', owner_state: 'active', last_activity_at: hoursAgo(0.5), ahead: 2, has_upstream: true },
      { ...worktree('w2', 'r1', 'alpha'), task: 'fix-lint', branch: 'fix-lint', owner_state: 'idle', ahead: 0, has_upstream: true },
      { ...worktree('w3', 'r2', 'beta'), task: 'fix-ci', branch: 'topic/fix-ci', route: 'cached', observed_at: hoursAgo(5) },
      { ...worktree('w4', 'r1', 'alpha'), task: 'add-search', branch: 'add-search', owner_state: 'active', ahead: 0, has_upstream: true },
      { ...worktree('w5', 'r1', 'alpha'), task: 'zeta', branch: 'zeta', owner_state: 'idle', ahead: 0, has_upstream: true },
    ],
    pull_requests: [pullRequest('p1', 'r1', 'w1', { number: 7, checks_total: 4, checks_passed: 3, checks_pending: 1, checks_green: false, mergeable: 'clean' })],
    agents: [
      run('run-1', 'running', { repository: 'r1', model: 'sonnet-5-5', task: 'fix-ci', worktrees: ['w1'], activity: 'working', started_at: hoursAgo(1) }),
      agent('s-block', 'r1', 'live', { runtime: 'claude', model: 'opus', worktrees: ['w4', 'w5'], activity: 'blocked', started_at: hoursAgo(1.5), session_id: 'wbs-3da4ea95-0000-4000-8000-000000000001' }),
      agent('s-free', undefined, 'live', { runtime: 'claude', model: 'opus', started_at: hoursAgo(2), session_id: 'wbs-free' }),
      agent('s-beta', 'r2', 'live', { runtime: 'codex', worktrees: ['w3'], activity: 'idle', started_at: hoursAgo(5), machine: 'beta', machine_id: 'mach-beta', route: 'cached', observed_at: hoursAgo(5) }),
      run('run-fail', 'failed', { repository: 'r1', model: 'opus', worktrees: ['w2'], started_at: hoursAgo(4), finished_at: hoursAgo(3), exit_code: 1 }),
      agent('s-parked', undefined, 'parked', { session_id: 'wbs-parked', started_at: hoursAgo(72), activity: 'idle' }),
      run('run-bare', 'completed', { runtime: undefined, model: undefined }),
    ],
  })
}
