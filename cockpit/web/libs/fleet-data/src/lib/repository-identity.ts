// Repository identity (REQ:repository-identity): the lower-cased `owner/name`;
// the host is a tie-breaker only when both rows carry one. Rows are merged
// across machines into one MergedRepository.

import { Agent, CodeIndexState, PullRequest, Repository, Route, isRunning } from './fleet.types'

/** A repository's `owner/name` and host as the read model gives them, tolerating a host left in the name. */
export function splitRepositoryName(repository: Pick<Repository, 'name' | 'host'>): { host?: string; slug: string } {
  const segments = repository.name.split('/')
  // A cached entry of an older publisher can still carry `github.com/Owner/name`.
  if (segments.length >= 3 && segments[0].includes('.')) {
    return { host: repository.host ?? segments[0], slug: segments.slice(1).join('/') }
  }
  return { host: repository.host, slug: repository.name }
}

/** The repository's `owner/name` as shown, without the host. */
export function repositorySlug(repository: Pick<Repository, 'name' | 'host'>): string {
  return splitRepositoryName(repository).slug
}

/** Code-index states from worst to best; the merged row shows the worst across machines. */
export const CODE_INDEX_SEVERITY: readonly CodeIndexState[] = ['failed', 'diverged', 'stale', 'never', 'pending', 'fresh']

/** The worst of some states; undefined when there are none. */
export function worstCodeIndex(states: readonly CodeIndexState[]): CodeIndexState | undefined {
  let worst: CodeIndexState | undefined
  for (const state of states) {
    if (worst === undefined || CODE_INDEX_SEVERITY.indexOf(state) < CODE_INDEX_SEVERITY.indexOf(worst)) worst = state
  }
  return worst
}

/** A cached entry older than this is stale (REQ:home-fleet-health). */
export const STALE_AFTER_MS = 24 * 60 * 60 * 1000

/** Whether a cached entry observed at `observedAt` is stale at `now`; an unknown age is never claimed stale. */
export function isStale(route: Route, observedAt: string | undefined, now: number): boolean {
  if (route !== 'cached' || !observedAt) return false
  const observed = Date.parse(observedAt)
  return !Number.isNaN(observed) && now - observed > STALE_AFTER_MS
}

/** One machine's checkout of a merged repository. */
export interface RepositoryCheckout {
  repository: Repository
  machineId: string
  machine: string
  route: Route
  observedAt?: string
  stale: boolean
}

/** One row per repository identity. */
export interface MergedRepository {
  /** The entry id of the preferred checkout (this machine's first): the row's `sel` value. */
  id: string
  /** The lower-cased `owner/name` (`host/owner/name` when two hosts share it): the row's id in `sel`. */
  key: string
  /** `owner/name` as the preferred checkout spells it. */
  slug: string
  host?: string
  checkouts: RepositoryCheckout[]
  /** Whether this machine has a checkout. */
  local: boolean
  worktreeCount: number
  /** Sums over the checkouts that report them; undefined when none does. */
  localBranchCount?: number
  remoteBranchCount?: number
  openPullRequestCount?: number
  activeAgentCount?: number
  /** The worst code-index state across checkouts and indexers; undefined when none is reported. */
  codeIndex?: CodeIndexState
  codeIndexStates: CodeIndexState[]
  /** The newest local-branch activity, in milliseconds. */
  lastActivityAt?: number
  errors: string[]
  webUrl?: string
}

/** The merged row that holds the repository entry `id`, as a `sel` value names it; undefined when none does. */
export function findMergedRepository(rows: readonly MergedRepository[], id: string): MergedRepository | undefined {
  return rows.find((row) => row.checkouts.some((checkout) => checkout.repository.id === id))
}

/** Distinct keys when there are some (a pull request or agent seen by several machines counts once), else the sum of what the entries report. */
function distinctOrSum(keys: string[] | undefined, reported: (number | undefined)[]): number | undefined {
  const distinct = new Set(keys).size
  return distinct > 0 ? distinct : sum(reported)
}

function sum(values: (number | undefined)[]): number | undefined {
  const known = values.filter((value): value is number => value !== undefined)
  return known.length === 0 ? undefined : known.reduce((total, value) => total + value, 0)
}

/** The pull requests and agents of the document, to count what several machines report once. */
export interface MergeLists {
  pullRequests: readonly PullRequest[]
  agents: readonly Agent[]
}

function mergeGroup(key: string, rows: Repository[], now: number, lists?: MergeLists): MergedRepository {
  // A local checkout first: its spelling and host are the best known.
  const ordered = [...rows].sort((a, b) => Number(b.route === 'local') - Number(a.route === 'local'))
  const states = ordered.flatMap((row) => (row.code_index ?? []).map((index) => index.state))
  const activity = ordered.map((row) => (row.last_activity_at ? Date.parse(row.last_activity_at) : Number.NaN)).filter((time) => !Number.isNaN(time))
  const first = splitRepositoryName(ordered[0])
  const ids = new Set(ordered.map((row) => row.id))
  return {
    id: ordered[0].id,
    key,
    slug: first.slug,
    host: ordered.map((row) => splitRepositoryName(row).host).find((host) => host !== undefined),
    checkouts: ordered.map((row) => ({
      repository: row,
      machineId: row.machine_id,
      machine: row.machine,
      route: row.route,
      observedAt: row.observed_at,
      stale: isStale(row.route, row.observed_at, now),
    })),
    local: ordered.some((row) => row.route === 'local'),
    worktreeCount: ordered.reduce((total, row) => total + row.worktree_count, 0),
    localBranchCount: sum(ordered.map((row) => row.local_branch_count)),
    remoteBranchCount: sum(ordered.map((row) => row.remote_branch_count)),
    openPullRequestCount: distinctOrSum(
      lists?.pullRequests.filter((pullRequest) => ids.has(pullRequest.repository ?? '') && (pullRequest.state === 'open' || pullRequest.state === 'draft')).map((pullRequest) => `${key}#${pullRequest.number}`),
      ordered.map((row) => row.open_pull_request_count),
    ),
    activeAgentCount: distinctOrSum(
      lists?.agents.filter((agent) => ids.has(agent.repository ?? '') && isRunning(agent)).map((agent) => agent.id),
      ordered.map((row) => row.active_agent_count),
    ),
    codeIndex: worstCodeIndex(states),
    codeIndexStates: [...new Set(states)],
    lastActivityAt: activity.length === 0 ? undefined : Math.max(...activity),
    errors: ordered.flatMap((row) => (row.error ? [row.error] : [])),
    // Only a local entry's address is trusted: another machine's snapshot is not.
    webUrl: ordered.find((row) => row.route === 'local' && row.remote_url_web)?.remote_url_web,
  }
}

/**
 * Merges the repository entries by lower-cased `owner/name`, across machines.
 * Two entries that both carry a host and differ in it stay apart; an entry
 * with no host joins the first host group of its identity.
 */
export function mergeRepositories(rows: readonly Repository[], now: number, lists?: MergeLists): MergedRepository[] {
  const byIdentity = new Map<string, Repository[]>()
  for (const row of rows) {
    const identity = repositorySlug(row).toLowerCase()
    byIdentity.set(identity, [...(byIdentity.get(identity) ?? []), row])
  }
  const merged: MergedRepository[] = []
  for (const [identity, group] of byIdentity) {
    const byHost = new Map<string, Repository[]>()
    for (const row of group) {
      const host = splitRepositoryName(row).host?.toLowerCase()
      if (host !== undefined) byHost.set(host, [...(byHost.get(host) ?? []), row])
    }
    if (byHost.size <= 1) {
      merged.push(mergeGroup(identity, group, now, lists))
      continue
    }
    const hostless = group.filter((row) => splitRepositoryName(row).host === undefined)
    let first = true
    for (const [host, hostRows] of byHost) {
      merged.push(mergeGroup(`${host}/${identity}`, first ? [...hostRows, ...hostless] : hostRows, now, lists))
      first = false
    }
  }
  return merged
}
