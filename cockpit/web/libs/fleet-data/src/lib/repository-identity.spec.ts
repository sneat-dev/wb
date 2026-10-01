import { CodeIndex, Repository } from './fleet.types'
import {
  CODE_INDEX_SEVERITY,
  STALE_AFTER_MS,
  isStale,
  mergeRepositories,
  repositorySlug,
  splitRepositoryName,
  worstCodeIndex,
} from './repository-identity'
import { OBSERVED, repository } from './test-data'

const NOW = Date.parse('2026-10-01T10:00:00Z')

function repo(id: string, machine: string, name: string, extra: Partial<Repository> = {}): Repository {
  return { ...repository(id, machine, { name, host: 'github.com', worktree_count: 1 }), ...extra }
}

describe('splitRepositoryName', () => {
  it('keeps owner/name and the separate host, and splits a host left in the name', () => {
    expect(splitRepositoryName({ name: 'sneat-co/sneat-go', host: 'github.com' })).toEqual({ host: 'github.com', slug: 'sneat-co/sneat-go' })
    expect(splitRepositoryName({ name: 'sneat-co/sneat-go' })).toEqual({ host: undefined, slug: 'sneat-co/sneat-go' })
    expect(splitRepositoryName({ name: 'github.com/Sneat-Co/sneat-go' })).toEqual({ host: 'github.com', slug: 'Sneat-Co/sneat-go' })
    expect(splitRepositoryName({ name: 'gitlab.com/a/b', host: 'github.com' }).host).toBe('github.com')
    // Three segments whose first has no dot are a nested path, not a host.
    expect(splitRepositoryName({ name: 'org/group/name' })).toEqual({ host: undefined, slug: 'org/group/name' })
    expect(repositorySlug({ name: 'github.com/a/b' })).toBe('a/b')
  })
})

describe('mergeRepositories', () => {
  // cockpit-views#ac:repository-identity-merges-local-and-cached
  it('merges a local and a cached entry that share the lower-cased owner/name', () => {
    const local = repo('l1', 'alpha', 'sneat-co/sneat-go', { last_activity_at: '2026-09-30T10:00:00Z' })
    const cached = repo('c1', 'beta', 'github.com/Sneat-Co/sneat-go', { route: 'cached', host: undefined })
    const merged = mergeRepositories([cached, local], NOW)
    expect(merged).toHaveLength(1)
    const [row] = merged
    expect(row.key).toBe('sneat-co/sneat-go')
    // The row is selected by the entry id of its preferred (local) checkout.
    expect(row.id).toBe('l1')
    // The local checkout's spelling and host win, and it comes first.
    expect(row.slug).toBe('sneat-co/sneat-go')
    expect(row.host).toBe('github.com')
    expect(row.checkouts.map((checkout) => [checkout.machine, checkout.route])).toEqual([['alpha', 'local'], ['beta', 'cached']])
    expect(row.local).toBe(true)
    expect(row.lastActivityAt).toBe(Date.parse('2026-09-30T10:00:00Z'))
  })

  it('merges a second pair that differs only in that one carries no host', () => {
    const merged = mergeRepositories([repo('a', 'alpha', 'acme/x'), repo('b', 'beta', 'acme/X', { host: undefined, route: 'cached' })], NOW)
    expect(merged.map((row) => row.key)).toEqual(['acme/x'])
    expect(merged[0].checkouts).toHaveLength(2)
  })

  it('uses the host as a tie-breaker only when both rows carry one', () => {
    const merged = mergeRepositories(
      [repo('a', 'alpha', 'acme/x'), repo('b', 'beta', 'acme/x', { host: 'gitlab.com', route: 'cached' }), repo('c', 'gamma', 'acme/x', { host: undefined, route: 'cached' })],
      NOW,
    )
    expect(merged.map((row) => [row.key, row.checkouts.map((checkout) => checkout.machine)])).toEqual([
      ['github.com/acme/x', ['alpha', 'gamma']],
      ['gitlab.com/acme/x', ['beta']],
    ])
  })

  it('shows a checkout stale when cached and observed more than 24 hours ago', () => {
    const old = new Date(NOW - STALE_AFTER_MS - 1000).toISOString()
    const [row] = mergeRepositories([repo('a', 'alpha', 'acme/x'), repo('b', 'beta', 'acme/x', { route: 'cached', observed_at: old })], NOW)
    expect(row.checkouts.map((checkout) => checkout.stale)).toEqual([false, true])
    expect(row.checkouts[1].observedAt).toBe(old)
  })

  // cockpit-views#ac:repositories-merge-across-machines
  it('sums counts over the checkouts that report them, and leaves a count nobody reports undefined', () => {
    const [row] = mergeRepositories(
      [
        repo('a', 'alpha', 'acme/x', { worktree_count: 2, local_branch_count: 3, remote_branch_count: 1, open_pull_request_count: 1, active_agent_count: 2 }),
        repo('b', 'beta', 'acme/x', { worktree_count: 1, local_branch_count: 4, route: 'cached', active_agent_count: undefined }),
        repo('c', 'gamma', 'acme/x', { worktree_count: 1, route: 'cached', active_agent_count: undefined }),
      ],
      NOW,
    )
    expect(row.worktreeCount).toBe(4)
    expect(row.localBranchCount).toBe(7)
    expect(row.remoteBranchCount).toBe(1)
    expect(row.openPullRequestCount).toBe(1)
    expect(row.activeAgentCount).toBe(2)
    const [bare] = mergeRepositories([repo('a', 'alpha', 'acme/y', { active_agent_count: undefined })], NOW)
    expect(bare.localBranchCount).toBeUndefined()
    expect(bare.activeAgentCount).toBeUndefined()
    expect(bare.lastActivityAt).toBeUndefined()
    expect(bare.codeIndex).toBeUndefined()
    expect(bare.webUrl).toBeUndefined()
  })

  it('shows the worst code-index state, the errors and the web address of the checkouts', () => {
    const index = (state: CodeIndex['state']): CodeIndex[] => [{ indexer: 'cg', state }]
    const [row] = mergeRepositories(
      [
        repo('a', 'alpha', 'acme/x', { code_index: index('fresh'), remote_url_web: 'https://github.com/acme/x' }),
        repo('b', 'beta', 'acme/x', { code_index: index('stale'), error: 'scan_failed', route: 'cached' }),
        repo('c', 'gamma', 'acme/x', { code_index: index('stale'), route: 'cached' }),
      ],
      NOW,
    )
    expect(row.codeIndex).toBe('stale')
    expect(row.codeIndexStates).toEqual(['fresh', 'stale'])
    expect(row.errors).toEqual(['scan_failed'])
    expect(row.webUrl).toBe('https://github.com/acme/x')
  })

  it('takes the newest activity among the checkouts', () => {
    const [row] = mergeRepositories(
      [repo('a', 'alpha', 'acme/x', { last_activity_at: '2026-09-01T00:00:00Z' }), repo('b', 'beta', 'acme/x', { last_activity_at: '2026-09-20T00:00:00Z' }), repo('c', 'gamma', 'acme/x', { last_activity_at: 'junk' })],
      NOW,
    )
    expect(row.lastActivityAt).toBe(Date.parse('2026-09-20T00:00:00Z'))
  })

  it('merges only cached entries too, none of them local', () => {
    const [row] = mergeRepositories([repo('b', 'beta', 'acme/x', { route: 'cached' })], NOW)
    expect(row.local).toBe(false)
  })
})

describe('worstCodeIndex', () => {
  it('orders failed, diverged, stale, never, pending, fresh and has none for none', () => {
    expect(CODE_INDEX_SEVERITY).toEqual(['failed', 'diverged', 'stale', 'never', 'pending', 'fresh'])
    expect(worstCodeIndex(['fresh', 'pending'])).toBe('pending')
    expect(worstCodeIndex(['fresh', 'failed', 'stale'])).toBe('failed')
    expect(worstCodeIndex(['stale', 'fresh'])).toBe('stale')
    expect(worstCodeIndex([])).toBeUndefined()
  })
})

describe('isStale', () => {
  it('is for cached entries older than 24 hours only, and never claims it for an unknown age', () => {
    expect(isStale('cached', new Date(NOW - STALE_AFTER_MS - 1).toISOString(), NOW)).toBe(true)
    expect(isStale('cached', new Date(NOW - STALE_AFTER_MS).toISOString(), NOW)).toBe(false)
    expect(isStale('local', new Date(NOW - 99 * STALE_AFTER_MS).toISOString(), NOW)).toBe(false)
    expect(isStale('cached', undefined, NOW)).toBe(false)
    expect(isStale('cached', 'junk', NOW)).toBe(false)
    expect(OBSERVED).toBeTruthy()
  })
})
