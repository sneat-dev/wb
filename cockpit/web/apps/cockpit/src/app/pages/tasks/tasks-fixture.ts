import { FleetDocument } from '@cockpit/fleet-data'
import { agent, fleetDocument, machine, pullRequest, repository, run, worktree } from '@cockpit/fleet-data/testing'
import { NOW } from '../test-harness'

const DAY = 24 * 60 * 60 * 1000
export const ago = (days: number) => new Date(NOW - days * DAY).toISOString()

/**
 * A fleet of two machines (alpha is this one, beta is cached and stale) and these tasks:
 * - `fix-ci`: three repositories on two machines, a running agent and a pull request with 3 of 4 checks passed (not ready);
 * - `add-search`: ready to land, with two green pull requests;
 * - `zeta`: at risk, two commits only on this machine;
 * - `broken`: a pull request with a failing check;
 * - `far`: only on beta, and no pull request;
 * - `old`: idle for 31 days, with a parked agent;
 * - `mystery`: nothing reported;
 * - `fix/ci 100%` and `release.js`: names that need encoding.
 */
export function tasksDocument(): FleetDocument {
  const repositories = [repository('r1', 'alpha'), repository('r2', 'beta', { route: 'cached', host: undefined, name: 'acme/r2' }), repository('r3', 'alpha'), repository('r4', 'alpha', { name: 'other/r4' })]
  return fleetDocument({
    machines: [machine('alpha'), { ...machine('beta', 'cached'), observed_at: ago(2), transport: 'ssh' }],
    repositories,
    worktrees: [
      { ...worktree('w1', 'r1', 'alpha'), task: 'fix-ci', branch: 'fix-ci', owner_state: 'active', last_activity_at: ago(0.01), ahead: 0, has_upstream: true },
      { ...worktree('w2', 'r3', 'alpha'), task: 'fix-ci', branch: 'fix-ci', owner_state: 'active', last_activity_at: ago(0.02), ahead: 0, has_upstream: true },
      { ...worktree('w3', 'r2', 'beta'), task: 'fix-ci', branch: 'topic/fix-ci', route: 'cached', observed_at: ago(2), owner_state: 'idle', last_activity_at: ago(0.5) },
      { ...worktree('w4', 'r1', 'alpha'), task: 'add-search', branch: 'add-search', owner_state: 'idle', last_activity_at: ago(1), ahead: 0, has_upstream: true },
      { ...worktree('w5', 'r3', 'alpha'), task: 'zeta', branch: 'zeta', owner_state: 'idle', last_activity_at: ago(3), ahead: 2, has_upstream: true },
      { ...worktree('w6', 'r4', 'alpha'), task: 'broken', branch: 'broken', owner_state: 'idle', last_activity_at: ago(4), ahead: 0, has_upstream: true },
      { ...worktree('w7', 'r2', 'beta'), task: 'far', branch: 'far', route: 'cached', observed_at: ago(2), owner_state: 'idle', last_activity_at: ago(5) },
      { ...worktree('w8', 'r1', 'alpha'), task: 'old', branch: 'old', owner_state: 'idle', last_activity_at: ago(31), ahead: 0, has_upstream: true },
      { ...worktree('w9', 'r1', 'alpha'), task: 'mystery', branch: 'mystery', last_activity_at: ago(6) },
      { ...worktree('w10', 'r1', 'alpha'), task: 'fix/ci 100%', branch: 'fix/ci-100', owner_state: 'idle', last_activity_at: ago(7), ahead: 0, has_upstream: true },
      { ...worktree('w11', 'r1', 'alpha'), task: 'release.js', branch: 'release.js', owner_state: 'idle', last_activity_at: ago(8), ahead: 0, has_upstream: true },
    ],
    pull_requests: [
      pullRequest('p1', 'r1', 'w1', { number: 7, checks_total: 4, checks_passed: 3, checks_pending: 1, checks_green: false, mergeable: 'clean' }),
      pullRequest('p2', 'r1', 'w4', { number: 8 }),
      pullRequest('p3', 'r1', 'w4', { number: 9 }),
      pullRequest('p4', 'r4', 'w6', { number: 131, checks_total: 5, checks_passed: 4, checks_failed: 1, checks_green: false, failed_check: 'build-linux' }),
    ],
    agents: [run('run-1', 'running', { worktrees: ['w1'], runtime: 'claude', model: 'opus' }), agent('a-old', 'r1', 'parked', { worktrees: ['w8'], activity: 'idle' })],
  })
}
