// Merging repository entries across machines into one row per identity
// (REQ:repository-identity). Not needed by the shell or the first paint of Home,
// so apart from `repository-identity.ts` (names and staleness) and behind
// `@cockpit/fleet-data/list`.

import { Agent, CodeIndexState, PullRequest, Repository, isRunning } from './fleet.types'
import { MergedRepository, isStale, repositorySlug, splitRepositoryName } from './repository-identity'
import { webAddress } from './web-address'

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

/** The merged row that holds the repository entry `id`, as a `sel` value names it; undefined when none does. */
export function findMergedRepository(rows: readonly MergedRepository[], id: string): MergedRepository | undefined {
  return rows.find((row) => row.checkouts.some((checkout) => checkout.repository.id === id))
}

/** Distinct keys when there are some (a pull request or agent seen by several machines counts once), else the sum of what the entries report. */
/**
 * The distinct things listed for the repository; else what its checkouts report; else, when the lists are known to
 * be complete for a checkout of this machine (`knownZero`), a real zero. Absent data stays absent: "not reported".
 */
function distinctOrSum(keys: string[] | undefined, reported: (number | undefined)[], knownZero = false): number | undefined {
  const distinct = new Set(keys).size
  if (distinct > 0) return distinct
  return sum(reported) ?? (keys !== undefined && knownZero ? 0 : undefined)
}

function sum(values: (number | undefined)[]): number | undefined {
  const known = values.filter((value): value is number => value !== undefined)
  return known.length === 0 ? undefined : known.reduce((total, value) => total + value, 0)
}

/** The pull requests and agents of the document, to count what several machines report once. */
export interface MergeLists {
  pullRequests: readonly PullRequest[]
  agents: readonly Agent[]
  /** The document's agent list is whole (not cut at the daemon's bound): this machine's repositories then have no agent when none is listed. */
  agentsComplete?: boolean
  /** The pull request list is whole (the daemon did not throttle it). */
  pullRequestsComplete?: boolean
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
      lists?.pullRequestsComplete === true && ordered.some((row) => row.route === 'local'),
    ),
    activeAgentCount: distinctOrSum(
      lists?.agents.filter((agent) => ids.has(agent.repository ?? '') && isRunning(agent)).map((agent) => agent.id),
      ordered.map((row) => row.active_agent_count),
      lists?.agentsComplete === true && ordered.some((row) => row.route === 'local'),
    ),
    codeIndex: worstCodeIndex(states),
    codeIndexStates: [...new Set(states)],
    lastActivityAt: activity.length === 0 ? undefined : Math.max(...activity),
    errors: ordered.flatMap((row) => (row.error ? [row.error] : [])),
    // Only a local entry's address is trusted: another machine's snapshot is not.
    webUrl: ordered.filter((row) => row.route === 'local').map((row) => webAddress(row.remote_url_web)).find((address) => address !== undefined),
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
