import {
  Agent,
  Entry,
  FleetDocument,
  Machine,
  Repository,
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
    schema_version: 1,
    warming_up: true,
    repositories_total: 0,
    repositories_scanned: 0,
    diagnostics: 0,
    machines: [],
    repositories: [],
    worktrees: [],
    branches: [],
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
