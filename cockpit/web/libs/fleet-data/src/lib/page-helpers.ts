// Helpers of the list and detail pages that the shell and the first paint of Home do
// not use: the machine and repository filters, labels, the code browser link, the
// code-index text and the README failure sentence. Behind `@cockpit/fleet-data/list`.

import { Agent, CodeIndex, CodeStatistics, Entry, Machine, Repository, Worktree } from './fleet.types'
import { Filters, OWNER_SESSION_COMMAND, onMachine, repositoryLabel } from './fleet-view'

/** The machines that hold the repository filter's repository, as well as the machine filter. */
export function filterMachines(rows: Machine[], filters: Filters, repositories: Repository[]): Machine[] {
  const byMachine = onMachine(rows, filters.machine)
  if (!filters.repository) return byMachine
  const holders = new Set(repositories.filter((row) => row.id === filters.repository).map((row) => row.machine_id))
  return byMachine.filter((row) => holders.has(row.id))
}

export interface RepositoryOption {
  id: string
  label: string
}

/** The repositories to offer, narrowed to the selected machine; the label names the machine too. */
export function repositoryOptions(repositories: Repository[], machine: string | undefined): RepositoryOption[] {
  return onMachine(repositories, machine).map((repository) => ({
    id: repository.id,
    label: `${repositoryLabel(repository)} · ${repository.machine}`,
  }))
}

export function filterRepositories(rows: Repository[], filters: Filters): Repository[] {
  const byMachine = onMachine(rows, filters.machine)
  return filters.repository ? byMachine.filter((row) => row.id === filters.repository) : byMachine
}

export function worktreeLabel(worktree: Worktree): string {
  return `${worktree.task} (${worktree.branch})`
}

export function agentLabel(agent: Agent): string {
  const identity = agent.session_id ?? agent.run_id ?? agent.id
  return agent.runtime ? `${agent.runtime} ${agent.kind} ${identity}` : `${agent.kind} ${identity}`
}

/**
 * The repository's page in the code browser: the base, the host and the
 * repository's slug. A repository whose origin names no forge host has none.
 */
export function codeBrowserLink(base: string | undefined, repository: Repository): string | null {
  if (!base || !repository.host) return null
  const segments = [repository.host, ...repository.name.split('/')]
  if (segments.some((segment) => segment === '' || segment === '.' || segment === '..')) return null
  const path = segments.map(encodeURIComponent).join('/')
  return `${base.replace(/\/+$/, '')}/${path}`
}

/**
 * The text of one code-index state: the state itself, and for a stale index
 * the number of commits it is behind. The state is always text, never colour
 * alone.
 */
export function codeIndexText(index: CodeIndex): string {
  return index.state === 'stale' ? `stale, ${index.behind ?? 0} behind` : index.state
}

/**
 * What a checkout's code-index panel shows. `unknown` is an entry from another
 * machine, whose snapshot carries no code index; `none` is no configured
 * provider; `not-indexed` is a provider that reported no index, or no
 * statistics yet; `failed` carries the provider's short failure code.
 */
export type CodeIndexView =
  | { kind: 'unknown' }
  | { kind: 'none' }
  | { kind: 'not-indexed' }
  | { kind: 'failed'; code: string }
  | { kind: 'indexed'; statistics: CodeStatistics }

/** The panel's view of one entry, from its code index and the document's provider. */
export function codeIndexView(entry: Entry, states: CodeIndex[] | undefined, provider: string | undefined): CodeIndexView {
  if (entry.route === 'cached') return { kind: 'unknown' }
  if (!provider) return { kind: 'none' }
  const statistics = states?.find((state) => state.statistics)?.statistics
  if (!statistics) return { kind: 'not-indexed' }
  if (statistics.error) return { kind: 'failed', code: statistics.error }
  return statistics.indexed ? { kind: 'indexed', statistics } : { kind: 'not-indexed' }
}

/** The sentence for a README read that was refused or failed, from the status and the daemon's short code. */
export function readmeFailureText(status: number, code: string): string {
  switch (code) {
    case 'readme_not_a_regular_file':
      return 'README.md is not a regular file in the repository (a link, a directory or a submodule), so it is not shown.'
    case 'readme_not_found':
      return 'This repository has no README.md at the tip of its default branch.'
    case 'default_branch_unknown':
      return 'The default branch of this repository is not known yet, so its README cannot be read.'
    case 'unknown_repository':
      return 'This repository is not on this machine, so its README cannot be read here.'
    case 'readme_too_large':
      return 'README.md is larger than 1 MiB, so it is not shown.'
    case 'git_too_old':
      return 'The installed Git is too old to read the README safely.'
  }
  return status === 401 || status === 403
    ? `An owner session is needed to read the README. Run \`${OWNER_SESSION_COMMAND}\` to open one.`
    : `The README could not be read (status ${status}).`
}
