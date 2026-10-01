import {
  OWNER_SESSION_COMMAND,
  canReadContent,
  codeBrowserLink,
  codeIndexView,
  readmeFailureText,
  codeIndexText,
  agentLabel,
  emptyDocument,
  filterAgents,
  filterMachines,
  filterRepositories,
  filterWorktrees,
  formatAge,
  groupBy,
  machineOptions,
  repositoryOptions,
  mostRecentWorktrees,
  repositoryLabel,
  routeLabel,
  worktreeLabel,
} from './fleet-view'
import { agent, fleetDocument, machine, repository, worktree } from './test-data'

const doc = fleetDocument()
const NOW = Date.parse('2026-10-01T10:00:00Z')

describe('emptyDocument', () => {
  it('is well formed and warming up', () => {
    expect(emptyDocument()).toMatchObject({ warming_up: true, machines: [], agents: [] })
  })
})

describe('filters', () => {
  it('keep everything with no filter', () => {
    expect(filterMachines(doc.machines, {}, doc.repositories)).toHaveLength(2)
    expect(filterRepositories(doc.repositories, {})).toHaveLength(2)
    expect(filterWorktrees(doc.worktrees, {})).toHaveLength(3)
    expect(filterAgents(doc.agents, {})).toHaveLength(3)
  })

  it('filter by machine', () => {
    expect(filterMachines(doc.machines, { machine: 'mach-beta' }, doc.repositories).map((m) => m.id)).toEqual(['mach-beta'])
    expect(filterRepositories(doc.repositories, { machine: 'mach-beta' }).map((r) => r.id)).toEqual(['r2'])
    expect(filterWorktrees(doc.worktrees, { machine: 'mach-alpha' }).map((w) => w.id)).toEqual(['w1', 'w2'])
    expect(filterAgents(doc.agents, { machine: 'mach-alpha' })).toHaveLength(3)
  })

  it('tell two machines that share a name apart by id', () => {
    const twin = { ...repository('r3', 'alpha'), machine_id: 'mach-other-login' }
    expect(filterRepositories([...doc.repositories, twin], { machine: 'mach-alpha' }).map((r) => r.id)).toEqual(['r1'])
  })

  it('keep the machines that hold the repository filter', () => {
    expect(filterMachines(doc.machines, { repository: 'r2' }, doc.repositories).map((m) => m.id)).toEqual(['mach-beta'])
    expect(filterMachines(doc.machines, { repository: 'r2', machine: 'mach-alpha' }, doc.repositories)).toEqual([])
    expect(filterMachines(doc.machines, { repository: 'nope' }, doc.repositories)).toEqual([])
  })

  it('filter by repository', () => {
    expect(filterRepositories(doc.repositories, { repository: 'r1' }).map((r) => r.id)).toEqual(['r1'])
    expect(filterWorktrees(doc.worktrees, { repository: 'r1' }).map((w) => w.id)).toEqual(['w1', 'w2'])
    expect(filterAgents(doc.agents, { repository: 'r1' }).map((a) => a.id)).toEqual(['a1', 'a2'])
  })

  it('filter agents by state', () => {
    expect(filterAgents(doc.agents, { state: 'running' }).map((a) => a.id)).toEqual(['a1', 'a3'])
    expect(filterAgents(doc.agents, { repository: 'r1', state: 'running' }).map((a) => a.id)).toEqual(['a1'])
  })
})

describe('options', () => {
  it('label a machine by its name, adding the end of its id when two share one', () => {
    expect(machineOptions(doc.machines)).toEqual([{ id: 'mach-alpha', label: 'alpha' }, { id: 'mach-beta', label: 'beta' }])
    const twins = [machine('alpha'), { ...machine('alpha'), id: 'mach-zzzzzzz' }]
    expect(machineOptions(twins).map((o) => o.label)).toEqual(['alpha (-alpha)', 'alpha (zzzzzz)'])
  })

  it('offer repositories narrowed to the selected machine, labelled with the machine', () => {
    expect(repositoryOptions(doc.repositories, undefined)).toEqual([
      { id: 'r1', label: 'github.com/acme/r1 · alpha' },
      { id: 'r2', label: 'acme/r2 · beta' },
    ])
    expect(repositoryOptions(doc.repositories, 'mach-beta')).toEqual([{ id: 'r2', label: 'acme/r2 · beta' }])
  })
})

describe('labels', () => {
  it('names a repository with its host when it has one', () => {
    expect(repositoryLabel(repository('r1', 'alpha'))).toBe('github.com/acme/r1')
    expect(repositoryLabel(repository('r2', 'beta', { host: undefined }))).toBe('acme/r2')
  })

  it('names a worktree and an agent', () => {
    expect(worktreeLabel(worktree('w1', 'r1', 'alpha'))).toBe('task-w1 (branch-w1)')
    expect(agentLabel(agent('a1', 'r1', 'running'))).toBe('session s-a1')
    expect(agentLabel(agent('a3', undefined, 'running', { session_id: undefined, run_id: 'run9', runtime: 'claude' }))).toBe('claude session run9')
    expect(agentLabel(agent('a4', undefined, 'idle', { session_id: undefined }))).toBe('session a4')
  })

})

describe('codeBrowserLink', () => {
  const repo = repository('r1', 'alpha', { name: 'specscore/specscore-cli' })

  it('joins the base, host and slug, whether or not the base ends in a slash', () => {
    expect(codeBrowserLink('https://codegrapher.dev/', repo)).toBe('https://codegrapher.dev/github.com/specscore/specscore-cli')
    expect(codeBrowserLink('https://code.example.test', repo)).toBe('https://code.example.test/github.com/specscore/specscore-cli')
  })

  it('encodes each segment', () => {
    expect(codeBrowserLink('https://c.test/', repository('r', 'a', { name: 'a b/c?d' }))).toBe('https://c.test/github.com/a%20b/c%3Fd')
  })

  it('has no link when a segment is empty, a dot or two dots', () => {
    for (const name of ['', 'a/', '/b', 'a//b', '.', 'a/..', '../b']) {
      expect(codeBrowserLink('https://c.test/', repository('r', 'a', { name }))).toBeNull()
    }
    expect(codeBrowserLink('https://c.test/', repository('r', 'a', { host: '..' }))).toBeNull()
  })

  it('has no link without a base or a forge host', () => {
    expect(codeBrowserLink(undefined, repo)).toBeNull()
    expect(codeBrowserLink('', repo)).toBeNull()
    expect(codeBrowserLink('https://c.test/', repository('r', 'a', { host: undefined }))).toBeNull()
  })
})

describe('ages and route labels', () => {
  it.each([
    [NOW - 5_000, 'just now'],
    [NOW + 5_000, 'just now'],
    [NOW + 5 * 60_000, 'clock ahead'],
    [NOW - 5 * 60_000, '5 min ago'],
    [NOW - 3 * 3_600_000, '3 h ago'],
    [NOW - 2 * 86_400_000, '2 d ago'],
  ])('formats %s as %s', (observed, text) => {
    expect(formatAge(new Date(observed).toISOString(), NOW)).toBe(text)
  })

  it('says so when the age is unknown', () => {
    expect(formatAge(undefined, NOW)).toBe('age unknown')
    expect(formatAge('not a time', NOW)).toBe('age unknown')
  })

  it('labels local and cached entries', () => {
    expect(routeLabel(machine('alpha'), NOW)).toBe('local')
    expect(routeLabel(machine('beta', 'cached'), NOW + 7 * 60_000)).toBe('cached, 7 min ago')
  })
})

describe('codeIndexText', () => {
  it('words every state, and gives a stale index its count of commits behind', () => {
    expect(codeIndexText({ indexer: 'x', state: 'fresh' })).toBe('fresh')
    expect(codeIndexText({ indexer: 'x', state: 'stale', behind: 3 })).toBe('stale, 3 behind')
    expect(codeIndexText({ indexer: 'x', state: 'stale' })).toBe('stale, 0 behind')
    expect(codeIndexText({ indexer: 'x', state: 'never' })).toBe('never')
  })
})

describe('groupBy', () => {
  it('groups rows by key and leaves out rows with none', () => {
    const groups = groupBy(doc.agents, (a) => a.repository)
    expect([...groups.keys()]).toEqual(['r1'])
    expect(groups.get('r1')?.map((a) => a.id)).toEqual(['a1', 'a2'])
  })
})

describe('mostRecentWorktrees', () => {
  it('lists the latest first, those without activity last, up to the limit', () => {
    const at = (id: string, when?: string) => ({ ...worktree(id, 'r1', 'alpha'), last_activity_at: when })
    const rows = [at('none'), at('old', '2026-09-01T00:00:00Z'), at('new', '2026-10-01T00:00:00Z'), at('mid', '2026-09-15T00:00:00Z')]
    expect(mostRecentWorktrees(rows, 3).map((w) => w.id)).toEqual(['new', 'mid', 'old'])
    expect(mostRecentWorktrees(rows, 10).map((w) => w.id)).toEqual(['new', 'mid', 'old', 'none'])
    expect(rows[0].id).toBe('none')
  })
})

describe('canReadContent', () => {
  it('is true only for a session that holds the content capability', () => {
    const session = { principal: 'owner', capabilities: ['fleet.read', 'repo.content.read'], code_browser_url: '' }
    expect(canReadContent(session)).toBe(true)
    expect(canReadContent({ ...session, principal: 'anonymous-local', capabilities: ['fleet.read'] })).toBe(false)
    expect(canReadContent(null)).toBe(false)
  })
})

describe('codeIndexView', () => {
  const local = repository('r1', 'alpha')
  const stats = { indexed: true, files: 12, symbols: 40, edges: 90, kinds: [{ kind: 'function', count: 40 }] }
  const state = (statistics?: typeof stats | { indexed: false; files: 0; symbols: 0; edges: 0; kinds: []; error?: string }) =>
    [{ indexer: 'codegrapher', state: 'fresh' as const, statistics }]

  it('says unknown for another machine, whatever the provider', () => {
    expect(codeIndexView(repository('r2', 'beta', { route: 'cached' }), undefined, 'codegrapher')).toEqual({ kind: 'unknown' })
  })

  it('says no provider when the document names none', () => {
    expect(codeIndexView(local, state(stats), undefined)).toEqual({ kind: 'none' })
    expect(codeIndexView(local, undefined, '')).toEqual({ kind: 'none' })
  })

  it('shows the statistics of an indexed checkout', () => {
    expect(codeIndexView(local, state(stats), 'codegrapher')).toEqual({ kind: 'indexed', statistics: stats })
  })

  it('says not indexed for a checkout with no statistics, or one the provider found no index for', () => {
    expect(codeIndexView(local, undefined, 'codegrapher')).toEqual({ kind: 'not-indexed' })
    expect(codeIndexView(local, state(), 'codegrapher')).toEqual({ kind: 'not-indexed' })
    expect(codeIndexView(local, state({ indexed: false, files: 0, symbols: 0, edges: 0, kinds: [] }), 'codegrapher')).toEqual({ kind: 'not-indexed' })
  })

  it('carries the failure code of a provider that could not answer', () => {
    const failed = { indexed: false as const, files: 0 as const, symbols: 0 as const, edges: 0 as const, kinds: [] as [], error: 'provider_timeout' }
    expect(codeIndexView(local, state(failed), 'codegrapher')).toEqual({ kind: 'failed', code: 'provider_timeout' })
  })
})

describe('readmeFailureText', () => {
  it('names the command that opens an owner session when the read is refused', () => {
    expect(OWNER_SESSION_COMMAND).toBe('wb cockpit')
    for (const status of [401, 403]) expect(readmeFailureText(status, '')).toContain('`wb cockpit`')
  })

  it('has a sentence for each short code the daemon answers', () => {
    expect(readmeFailureText(403, 'readme_not_a_regular_file')).toContain('not a regular file')
    expect(readmeFailureText(404, 'readme_not_found')).toContain('no README.md')
    expect(readmeFailureText(404, 'default_branch_unknown')).toContain('default branch')
    expect(readmeFailureText(404, 'unknown_repository')).toContain('not on this machine')
    expect(readmeFailureText(413, 'readme_too_large')).toContain('1 MiB')
    expect(readmeFailureText(503, 'git_too_old')).toContain('Git is too old')
  })

  it('falls back to the status for anything else', () => {
    expect(readmeFailureText(500, 'read_failed')).toBe('The README could not be read (status 500).')
  })
})
