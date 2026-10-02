import { FleetDocument, MachineMetrics, Session } from '@cockpit/fleet-data'
import { agent, fleetDocument, machine, repository, run, worktree } from '@cockpit/fleet-data/testing'
import { sample } from '../machines/machines-fixture'

/**
 * Two machines (alpha is this one, beta is cached) and these repositories: `sneat-co/sneat-go` (default branch main) and
 * `sneat-co/bots-go` and `acme/web` (default branch main) on alpha, `acme/tools` (default branch trunk) on beta, and one whose name
 * cannot be picked (`acme/odd name`); the task `fix-ci` already exists with two worktrees; agents report two models.
 */
export function newTaskDocument(): FleetDocument {
  return fleetDocument({
    machines: [machine('alpha'), machine('beta', 'cached')],
    repositories: [
      repository('sneat-go', 'alpha', { name: 'sneat-co/sneat-go', default_branch: 'main' }),
      repository('bots-go', 'alpha', { name: 'sneat-co/bots-go', default_branch: 'main' }),
      repository('web', 'alpha', { name: 'acme/web', default_branch: 'main' }),
      repository('tools', 'beta', { route: 'cached', host: undefined, name: 'acme/tools', default_branch: 'trunk' }),
      repository('odd', 'alpha', { name: 'acme/odd name' }),
    ],
    worktrees: [{ ...worktree('w1', 'sneat-go', 'alpha'), task: 'fix-ci' }, { ...worktree('w2', 'bots-go', 'alpha'), task: 'fix-ci' }, { ...worktree('w3', 'tools', 'beta'), task: 'solo' }],
    pull_requests: [],
    agents: [run('r1', 'running', { model: 'sonnet-5-5' }), run('r2', 'completed', { model: 'sonnet-5-5' }), agent('s1', undefined, 'live', { model: 'gpt-x' }), agent('s2', undefined, 'live', { model: undefined })],
  })
}

/** An owner's session, with an SSH route to beta. */
export const OWNER: Session = {
  principal: 'owner',
  capabilities: ['fleet.read', 'fleet.act'],
  code_browser_url: 'https://codegrapher.dev/',
  machine_routes: [{ machine_id: 'mach-beta', ssh: { host: 'beta.example', user: 'me' } }],
}

/** alpha is busy (90 percent CPU), beta is free. */
export function loadAnswers(): Record<string, MachineMetrics> {
  return {
    'mach-alpha': { machine: 'mach-alpha', route: 'local', samples: [sample(1, 90)] },
    'mach-beta': { machine: 'mach-beta', route: 'cached', samples: [sample(1, 10)] },
  }
}
