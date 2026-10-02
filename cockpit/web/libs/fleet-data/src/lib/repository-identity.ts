// Repository identity (REQ:repository-identity): the lower-cased `owner/name`;
// the host is a tie-breaker only when both rows carry one. Rows are merged
// across machines into one MergedRepository (`repository-merge.ts`, which only the
// lists and panels need, so the first page does not carry it).

import { CodeIndexState, Repository, Route } from './fleet.types'

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
  /** The repository's page on the forge, only when it is a checked web address (`webAddress`). */
  webUrl?: string
}
