import {
  Agent,
  CAPABILITY_REPO_CONTENT_READ,
  CodeIndex,
  CodeStatistics,
  Entry,
  FleetDocument,
  Machine,
  Repository,
  SCHEMA_VERSION,
  Session,
  Worktree,
} from './fleet.types'

/**
 * The list filters, held in the page's URL query so a drill-down is a plain
 * link. `machine` is a machine name, `repository` a repository id, `state` an
 * agent state.
 */
export interface Filters {
  machine?: string
  repository?: string
  state?: string
}

/** The query parameters that carry the filters. */
export type FilterQuery = Record<string, string>

export function emptyDocument(): FleetDocument {
  return {
    schema_version: SCHEMA_VERSION,
    warming_up: true,
    repositories_total: 0,
    repositories_scanned: 0,
    diagnostics: 0,
    machines: [],
    repositories: [],
    worktrees: [],
    pull_requests: [],
    agents: [],
  }
}

/** Keeps the entries of the machine with id `machine`; no machine keeps all of them. */
function onMachine<T extends Entry>(rows: T[], machine: string | undefined): T[] {
  return machine ? rows.filter((row) => row.machine_id === machine) : rows
}

/** The machines that hold the repository filter's repository, as well as the machine filter. */
export function filterMachines(rows: Machine[], filters: Filters, repositories: Repository[]): Machine[] {
  const byMachine = onMachine(rows, filters.machine)
  if (!filters.repository) return byMachine
  const holders = new Set(repositories.filter((row) => row.id === filters.repository).map((row) => row.machine_id))
  return byMachine.filter((row) => holders.has(row.id))
}

export interface MachineOption {
  id: string
  label: string
}

/** One option per machine; a name two machines share carries the end of its id. */
export function machineOptions(machines: Machine[]): MachineOption[] {
  const shared = [...groupBy(machines, (machine) => machine.machine)].filter(([, group]) => group.length > 1).map(([name]) => name)
  return machines.map((machine) => ({
    id: machine.id,
    label: shared.includes(machine.machine) ? `${machine.machine} (${machine.id.slice(-6)})` : machine.machine,
  }))
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

export function filterWorktrees(rows: Worktree[], filters: Filters): Worktree[] {
  const byMachine = onMachine(rows, filters.machine)
  return filters.repository
    ? byMachine.filter((row) => row.repository === filters.repository)
    : byMachine
}

export function filterAgents(rows: Agent[], filters: Filters): Agent[] {
  const byMachine = onMachine(rows, filters.machine)
  const byRepository = filters.repository
    ? byMachine.filter((row) => row.repository === filters.repository)
    : byMachine
  return filters.state ? byRepository.filter((row) => row.state === filters.state) : byRepository
}

/** Groups rows by a key; a row whose key is undefined belongs to no group. */
export function groupBy<T>(rows: T[], key: (row: T) => string | undefined): Map<string, T[]> {
  const groups = new Map<string, T[]>()
  for (const row of rows) {
    const name = key(row)
    if (name !== undefined) groups.set(name, [...(groups.get(name) ?? []), row])
  }
  return groups
}

/** The `limit` worktrees with the latest activity, latest first; one with none comes last. */
export function mostRecentWorktrees(rows: Worktree[], limit: number): Worktree[] {
  const activity = (row: Worktree): number => Date.parse(row.last_activity_at ?? '') || 0
  return [...rows].sort((a, b) => activity(b) - activity(a)).slice(0, limit)
}

/** The repository's name as shown: host and slug, or the slug alone for a cached one. */
export function repositoryLabel(repository: Repository): string {
  return repository.host ? `${repository.host}/${repository.name}` : repository.name
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

const MINUTE = 60_000
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

/** How long ago `observedAt` was, in the largest whole unit. */
export function formatAge(observedAt: string | undefined, now: number): string {
  const observed = observedAt ? Date.parse(observedAt) : Number.NaN
  if (Number.isNaN(observed)) return 'age unknown'
  const elapsed = now - observed
  // A little skew between two clocks is normal; more than a minute is not.
  if (elapsed < -MINUTE) return 'clock ahead'
  if (elapsed < MINUTE) return 'just now'
  if (elapsed < HOUR) return `${Math.floor(elapsed / MINUTE)} min ago`
  if (elapsed < DAY) return `${Math.floor(elapsed / HOUR)} h ago`
  return `${Math.floor(elapsed / DAY)} d ago`
}

/** The route label every row carries; a cached row adds how old it is. */
export function routeLabel(entry: Entry, now: number): string {
  return entry.route === 'cached' ? `cached, ${formatAge(entry.observed_at, now)}` : 'local'
}

/**
 * The text of one code-index state: the state itself, and for a stale index
 * the number of commits it is behind. The state is always text, never colour
 * alone.
 */
export function codeIndexText(index: CodeIndex): string {
  return index.state === 'stale' ? `stale, ${index.behind ?? 0} behind` : index.state
}

/** The command that opens an owner session. */
export const OWNER_SESSION_COMMAND = 'wb cockpit'

/** Whether the session lets the caller read repository content. No session, no content. */
export function canReadContent(session: Session | null): boolean {
  return session?.capabilities.includes(CAPABILITY_REPO_CONTENT_READ) ?? false
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
