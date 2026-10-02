import {
  Agent,
  CAPABILITY_REPO_CONTENT_READ,
  Entry,
  FleetDocument,
  Machine,
  MachineRoute,
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
export function onMachine<T extends Entry>(rows: T[], machine: string | undefined): T[] {
  return machine ? rows.filter((row) => row.machine_id === machine) : rows
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

/** The `limit` worktrees with the latest activity, latest first; one with none comes last. */
export function mostRecentWorktrees(rows: Worktree[], limit: number): Worktree[] {
  const activity = (row: Worktree): number => Date.parse(row.last_activity_at ?? '') || 0
  return [...rows].sort((a, b) => activity(b) - activity(a)).slice(0, limit)
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

/** Groups rows by a key; a row whose key is undefined belongs to no group. */
export function groupBy<T>(rows: T[], key: (row: T) => string | undefined): Map<string, T[]> {
  const groups = new Map<string, T[]>()
  for (const row of rows) {
    const name = key(row)
    if (name !== undefined) groups.set(name, [...(groups.get(name) ?? []), row])
  }
  return groups
}

/** The repository's name as shown: host and slug, or the slug alone for a cached one. */
export function repositoryLabel(repository: Repository): string {
  return repository.host ? `${repository.host}/${repository.name}` : repository.name
}

/** The agent's runtime and model, or "agent" for neither. */
export function agentTitle(agent: Agent): string {
  return [agent.runtime, agent.model].filter((part) => part).join(' ') || 'agent'
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

/** A worktree's owner state in words for a panel: never the raw value, and a value outside the vocabulary is "not reported". */
export function ownerStateText(state: string | undefined): string {
  switch (state) {
    case 'active':
      return 'active'
    case 'idle':
      return 'idle, no running owner'
    case 'orphaned':
      return 'orphaned, its owner process is gone'
    case 'unknown':
      return 'owner not recorded'
  }
  return 'owner not reported'
}

/** A worktree's lifecycle in words; none for a value outside the vocabulary (it is "not reported", and says nothing). */
export function lifecycleText(lifecycle: string | undefined): string | undefined {
  switch (lifecycle) {
    case 'working':
      return 'in progress'
    case 'review':
      return 'in review'
    case 'merged':
      return 'merged'
    case 'superseded':
      return 'superseded'
  }
  return undefined
}

/** The route label every row carries, in the one vocabulary of routes (`local`, `live`, `cached`); a cached row adds how old it is. */
export function routeLabel(entry: Entry, now: number): string {
  if (entry.route === 'cached') return `cached, ${formatAge(entry.observed_at, now)}`
  return entry.route === 'live-remote' ? 'live' : 'local'
}

/** The command that opens an owner session. */
export const OWNER_SESSION_COMMAND = 'wb cockpit'

/**
 * The SSH routes of the machines, only for an owner session: they name hosts and users, which the metadata set does
 * not (cockpit#req:anonymous-local-reads-metadata-only). A response that carried them for another principal is not
 * believed, so no copied command ever holds a host of an anonymous reader.
 */
export function ownerRoutes(session: Session | null): MachineRoute[] | undefined {
  return session?.principal === 'owner' ? session.machine_routes : undefined
}

/** Whether the session lets the caller read repository content. No session, no content. */
export function canReadContent(session: Session | null): boolean {
  return session?.capabilities.includes(CAPABILITY_REPO_CONTENT_READ) ?? false
}
