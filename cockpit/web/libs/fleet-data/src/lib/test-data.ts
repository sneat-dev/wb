import { Agent, FleetDocument, Machine, PullRequest, Repository, SCHEMA_VERSION, Worktree } from './fleet.types'

// Shared fixtures for the specs in this library and its consumers; they are
// shaped exactly as the daemon's document is.

export const OBSERVED = '2026-10-01T10:00:00Z'

export function machine(name: string, route: 'local' | 'cached' = 'local'): Machine {
  return {
    id: `mach-${name}`,
    machine: name,
    machine_id: `mach-${name}`,
    route,
    observed_at: OBSERVED,
    repository_count: 1,
    // This machine has one repository, with the fixture's worktrees: two on
    // alpha, one on beta.
    worktree_count: name === 'alpha' ? 2 : 1,
  }
}

export function repository(id: string, machineName: string, extra: Partial<Repository> = {}): Repository {
  return {
    id,
    machine: machineName,
    machine_id: `mach-${machineName}`,
    route: 'local',
    observed_at: OBSERVED,
    host: 'github.com',
    name: `acme/${id}`,
    worktree_count: 2,
    active_agent_count: 1,
    ...extra,
  }
}

export function worktree(id: string, repositoryId: string, machineName: string): Worktree {
  return {
    id,
    machine: machineName,
    machine_id: `mach-${machineName}`,
    route: 'local',
    observed_at: OBSERVED,
    repository: repositoryId,
    task: `task-${id}`,
    branch: `branch-${id}`,
  }
}

/** An observed pull request: it carries `checked_at`, so the task state rules read it. */
export function pullRequest(id: string, repositoryId: string, worktreeId: string | undefined, extra: Partial<PullRequest> = {}): PullRequest {
  return {
    id,
    machine: 'alpha',
    machine_id: 'mach-alpha',
    route: 'local',
    observed_at: OBSERVED,
    repository: repositoryId,
    worktree: worktreeId,
    number: 1,
    url: `https://github.com/acme/${repositoryId}/pull/1`,
    state: 'open',
    mergeable: 'clean',
    checks_total: 5,
    checks_passed: 5,
    checks_failed: 0,
    checks_pending: 0,
    checks_green: true,
    checked_at: OBSERVED,
    ...extra,
  }
}

/** A dispatched run on this machine. */
export function run(id: string, state: string, extra: Partial<Agent> = {}): Agent {
  return agent(id, undefined, state, { kind: 'run', session_id: undefined, run_id: id, runtime: 'claude', model: 'opus', ...extra })
}

export function agent(id: string, repositoryId: string | undefined, state: string, extra: Partial<Agent> = {}): Agent {
  return {
    id,
    machine: 'alpha',
    machine_id: 'mach-alpha',
    route: 'local',
    observed_at: OBSERVED,
    kind: 'session',
    session_id: `s-${id}`,
    state,
    repository: repositoryId,
    ...extra,
  }
}

export function fleetDocument(extra: Partial<FleetDocument> = {}): FleetDocument {
  return {
    schema_version: SCHEMA_VERSION,
    snapshot_at: OBSERVED,
    refresh_interval_seconds: 30,
    warming_up: false,
    repositories_total: 2,
    repositories_scanned: 2,
    diagnostics: 0,
    machines: [machine('alpha'), machine('beta', 'cached')],
    repositories: [
      repository('r1', 'alpha'),
      repository('r2', 'beta', { route: 'cached', host: undefined, name: 'acme/r2', worktree_count: 1, active_agent_count: undefined }),
    ],
    worktrees: [worktree('w1', 'r1', 'alpha'), worktree('w2', 'r1', 'alpha'), worktree('w3', 'r2', 'beta')],
    pull_requests: [],
    agents: [agent('a1', 'r1', 'running'), agent('a2', 'r1', 'idle'), agent('a3', undefined, 'running', { session_id: undefined, run_id: 'run9', runtime: 'claude', kind: 'run' })],
    ...extra,
  }
}
