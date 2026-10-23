import { LinkResult, agentsBadgeLink, machineAgentsLink, machineRepositoriesLink, machineWorktreesLink, repositoryAgentsLink, repositoryPullRequestsLink, repositoryWorktreesLink, taskWorktreesLink, emptyListQuery, fieldTerm, parseListQuery, sortLink, termLink } from './list-query'
import { DESC_FIRST, SEL_KEYS, VOCABULARY, chipOf, defaultDirection } from './filter-vocabulary'
import { AGE_TERMS } from './matcher'
import { TASK_STATE_IDS } from './task-state'
import { AppLink, encodeListItem, CODE_INDEX_STATES, HOME_PATH, LIST_PAGES, PAGE_RULES, agentDetailLink, ageLink, chipLink, declaredFields, hrefOf, isChip, linkTarget, linkProblems, listLink, listQueryParams, machineDetailLink, machineLink, repositoryDetailLink, selectionLink, stateLink, taskDetailLink, worktreeDetailLink } from './vocabulary'

describe('the vocabulary table', () => {
  // cockpit-views#ac:filter-vocabulary-is-the-only-link-target
  it('gives every chip a label and a hint, so a list can render its chips from the page alone', () => {
    for (const page of LIST_PAGES) {
      expect(VOCABULARY[page].chips.map((chip) => chip.id), page).toEqual([...PAGE_RULES[page].chips])
      for (const chip of VOCABULARY[page].chips) {
        expect(chip.label.trim(), `${page}/${chip.id}`).not.toBe('')
        expect(chip.hint.trim().length, `${page}/${chip.id}`).toBeGreaterThan(10)
        expect(chipOf(page, chip.id)).toBe(chip)
      }
    }
    expect(chipOf('tasks', 'needs-you')).toMatchObject({ label: 'Needs you' })
    expect(chipOf('tasks', 'safe')).toBeUndefined()
  })

  it('knows the dynamic runtime chips of Agents, only there and only for a plain name', () => {
    expect(chipOf('agents', 'runtime-claude')).toEqual({ id: 'runtime-claude', label: 'claude', hint: 'Agents of the claude runtime' })
    expect(chipOf('agents', 'runtime-Claude')).toBeUndefined()
    expect(chipOf('tasks', 'runtime-claude')).toBeUndefined()
  })

  it('lists, per page, what REQ:filter-vocabulary lists', () => {
    expect(VOCABULARY.tasks.chips.map((chip) => chip.id)).toEqual(['needs-you', 'ready', 'working', 'agent', 'pr', 'multirepo', 'idle30', 'older'])
    expect(VOCABULARY.tasks.states).toEqual(TASK_STATE_IDS)
    expect(VOCABULARY.tasks.bare).toEqual(['task', 'repository'])
    expect(VOCABULARY.tasks.sorts).toEqual(['task', 'state', 'worktrees', 'activity'])
    expect(VOCABULARY.repositories.chips.map((chip) => chip.id)).toEqual(['worktrees', 'agents', 'prs', 'index', 'errors'])
    expect(VOCABULARY.repositories.states).toEqual(CODE_INDEX_STATES)
    expect(VOCABULARY.repositories.sorts).toEqual(['repository', 'activity', 'worktrees', 'branches'])
    expect(VOCABULARY.worktrees.chips.map((chip) => chip.id)).toEqual(['active', 'orphaned', 'unpushed', 'gone', 'pr', 'idle30', 'safe', 'look'])
    expect(VOCABULARY.worktrees.states).toEqual(['active', 'idle', 'orphaned', 'unknown'])
    expect(VOCABULARY.worktrees.bare).toEqual(['task', 'repository', 'branch'])
    expect(VOCABULARY.worktrees.sorts).toEqual(['worktree', 'state', 'machine', 'activity'])
    expect(VOCABULARY.agents.chips.map((chip) => chip.id)).toEqual(['running', 'blocked'])
    expect(VOCABULARY.agents.states).toEqual(['working', 'blocked', 'idle', 'done', 'unknown', 'live', 'parked', 'running', 'completed', 'failed', 'timeout', 'abandoned'])
    expect(VOCABULARY.agents.bare).toEqual(['runtime', 'model', 'task', 'repository'])
    expect(VOCABULARY.agents.sorts).toEqual(['label', 'activity', 'machine', 'started'])
    expect(VOCABULARY.machines.chips.map((chip) => chip.id)).toEqual(['stale', 'outdated'])
    expect(VOCABULARY.machines.states).toEqual(['live', 'cached', 'stale'])
    expect(VOCABULARY.machines.sorts).toEqual(['machine', 'state', 'version'])
    expect(AGE_TERMS).toEqual(['<1d', '1-7d', '8-30d', '31-90d', '>90d'])
    expect(LIST_PAGES).toEqual(['tasks', 'repositories', 'worktrees', 'agents', 'machines'])
    expect(Object.values(SEL_KEYS)).toHaveLength(5)
  })

  it('declares the fields of each page, with state and age where they apply and no day', () => {
    expect([...declaredFields('tasks')].sort()).toEqual(['age', 'machine', 'repo', 'state', 'task'])
    expect([...declaredFields('repositories')].sort()).toEqual(['age', 'machine', 'repo', 'state'])
    expect([...declaredFields('worktrees')].sort()).toEqual(['age', 'branch', 'machine', 'repo', 'state', 'task'])
    expect([...declaredFields('agents')].sort()).toEqual(['machine', 'repo', 'runtime', 'state', 'task'])
    expect([...declaredFields('machines')].sort()).toEqual(['machine', 'state'])
    for (const page of LIST_PAGES) expect(declaredFields(page).has('day')).toBe(false)
  })

  it('accepts runtime chips on Agents only, restricted to [a-z0-9-]+', () => {
    expect(isChip('agents', 'runtime-claude')).toBe(true)
    expect(isChip('agents', 'runtime-gpt-5')).toBe(true)
    expect(isChip('agents', 'runtime-')).toBe(false)
    expect(isChip('agents', 'runtime-Claude')).toBe(false)
    expect(isChip('agents', 'runtime-a b')).toBe(false)
    expect(isChip('tasks', 'runtime-claude')).toBe(false)
    expect(isChip('tasks', 'needs-you')).toBe(true)
    expect(isChip('tasks', 'safe')).toBe(false)
  })

  it('starts counts and times newest or biggest first, and names A first', () => {
    expect(DESC_FIRST.agents).toEqual(['started'])
    expect(defaultDirection('repositories', 'branches')).toBe('desc')
    expect(defaultDirection('repositories', 'repository')).toBe('asc')
    expect(defaultDirection('agents', 'activity')).toBe('asc')
    expect(defaultDirection('tasks', 'activity')).toBe('desc')
  })
})

describe('the list address', () => {
  it('reads q, sort, dir, machine, chips and sel, and ignores a chip or direction the page does not list', () => {
    const query = parseListQuery('worktrees', { q: 'fix', sort: 'state', dir: 'desc', machine: 'a,b,', chips: 'safe,nope,safe,look', sel: 'wt-1' })
    expect(query).toEqual({ q: 'fix', sort: 'state', dir: 'desc', machines: ['a', 'b'], chips: ['safe', 'look'], sel: 'wt-1' })
    expect(parseListQuery('tasks', { dir: 'sideways' })).toEqual({ q: '', sort: undefined, dir: undefined, machines: [], chips: [], sel: undefined })
    expect(parseListQuery('tasks', { q: undefined, sort: '', sel: '' })).toEqual({ q: '', sort: undefined, dir: undefined, machines: [], chips: [], sel: undefined })
    expect(emptyListQuery()).toEqual({ q: '', machines: [], chips: [] })
  })

  it('writes only the parameters that are set, in the documented names', () => {
    expect(listQueryParams({})).toEqual({})
    expect(listQueryParams({ q: '', machines: [], chips: [] })).toEqual({})
    expect(listQueryParams({ q: 'a b', sort: 'activity', dir: 'asc', machines: ['m1', 'm2'], chips: ['ready'], sel: 'x' })).toEqual({
      q: 'a b',
      sort: 'activity',
      dir: 'asc',
      machine: 'm1,m2',
      chips: 'ready',
      sel: 'x',
    })
  })

  it('round-trips a list state through the address', () => {
    const state = { q: 'sneat-* -wb', sort: 'worktrees', dir: 'desc' as const, machines: ['m1'], chips: ['index'], sel: 'r-1' }
    expect(parseListQuery('repositories', listQueryParams(state))).toEqual(state)
  })
})

describe('link builders', () => {
  it('builds addresses on the page paths, with the query percent-encoded in the string form', () => {
    expect(hrefOf({ path: '/tasks', query: {} })).toBe('/tasks')
    expect(hrefOf(listLink('tasks', { q: 'fix ci', chips: ['pr'] }))).toBe('/tasks?q=fix%20ci&chips=pr')
    expect(HOME_PATH).toBe('/')
    expect(chipLink('tasks', 'needs-you')).toEqual({ path: '/tasks', query: { chips: 'needs-you' } })
    expect(stateLink('tasks', 'at-risk')).toEqual({ path: '/tasks', query: { q: 'state:at-risk' } })
    expect(ageLink('worktrees', '>90d')).toEqual({ path: '/worktrees', query: { q: 'age:>90d' } })
    expect(sortLink('repositories', 'branches')).toEqual({ path: '/repositories', query: { sort: 'branches' } })
    expect(sortLink('repositories', 'worktrees', 'asc')).toEqual({ path: '/repositories', query: { sort: 'worktrees', dir: 'asc' } })
    expect(machineLink('worktrees', 'mach-1')).toEqual({ path: '/worktrees', query: { machine: 'mach-1' } })
    expect(selectionLink('tasks', 'fix ci')).toEqual({ path: '/tasks', query: { sel: 'fix ci' } })
  })

  // cockpit-views#ac:matcher-grammar
  it('always quotes a field value, and refuses one a filter cannot express', () => {
    expect(fieldTerm('task', 'fix-ci')).toEqual({ ok: true, text: 'task:"fix-ci"' })
    expect(fieldTerm('task', 'fix ci')).toEqual({ ok: true, text: 'task:"fix ci"' })
    expect(fieldTerm('task', 'a "b" c')).toMatchObject({ ok: false, reason: expect.stringContaining('double quote') })
    expect(fieldTerm('task', 'x'.repeat(260))).toMatchObject({ ok: false, reason: expect.stringContaining('256') })
    expect(fieldTerm('task', 'x'.repeat(240))).toMatchObject({ ok: true })
    expect(termLink('worktrees', 'task', 'fix ci')).toEqual({ ok: true, link: { path: '/worktrees', query: { q: 'task:"fix ci"' } } })
    expect(termLink('tasks', 'repo', 'sneat-dev/wb', ['pr'])).toEqual({ ok: true, link: { path: '/tasks', query: { q: 'repo:"sneat-dev/wb"', chips: 'pr' } } })
  })

  it('never throws for a name it cannot link: it says why, so the page shows a plain number with a title', () => {
    expect(termLink('worktrees', 'task', 'a"b')).toMatchObject({ ok: false })
    expect(termLink('worktrees', 'task', 'y'.repeat(300))).toMatchObject({ ok: false })
    expect(termLink('agents', 'age', '<1d')).toEqual({ ok: false, reason: 'age: is not a filter field of agents' })
    expect(termLink('tasks', 'repo', 'x', ['nope'])).toMatchObject({ ok: false, reason: expect.stringContaining('chip nope') })
  })

  it('links every count cell to exactly the rows it counted, and none for the branch counts or throughput', () => {
    const link = (result: LinkResult): AppLink => (result.ok ? result.link : { path: '', query: {} })
    expect(link(taskWorktreesLink('fix ci'))).toEqual({ path: '/worktrees', query: { q: 'task:"fix ci"' } })
    expect(link(repositoryWorktreesLink('sneat-dev/wb'))).toEqual({ path: '/worktrees', query: { q: 'repo:"sneat-dev/wb"' } })
    expect(link(repositoryAgentsLink('sneat-dev/wb'))).toEqual({ path: '/agents', query: { q: 'repo:"sneat-dev/wb"' } })
    expect(link(repositoryPullRequestsLink('sneat-dev/wb'))).toEqual({ path: '/tasks', query: { q: 'repo:"sneat-dev/wb"', chips: 'pr' } })
    expect(link(machineRepositoriesLink('m1'))).toEqual({ path: '/repositories', query: { machine: 'm1' } })
    expect(link(machineWorktreesLink('m1'))).toEqual({ path: '/worktrees', query: { machine: 'm1' } })
    expect(link(machineAgentsLink('m1'))).toEqual({ path: '/agents', query: { machine: 'm1' } })
    expect(link(agentsBadgeLink())).toEqual({ path: '/agents', query: { chips: 'running' } })
    expect(taskWorktreesLink('a"b').ok).toBe(false)
  })

  it('encodes a comma in a machine id so it cannot split machine=', () => {
    const params = listQueryParams({ machines: ['a,b', '50%', 'c'] })
    expect(params['machine']).toBe('a%2Cb,50%25,c')
    expect(parseListQuery('tasks', params).machines).toEqual(['a,b', '50%', 'c'])
    expect(encodeListItem('x%,')).toBe('x%25%2C')
  })

  it('builds the detail addresses, encoding the task name', () => {
    expect(hrefOf(taskDetailLink('fix/ci 100%'))).toBe('/tasks/detail?task=fix%2Fci%20100%25')
    expect(hrefOf(taskDetailLink('release.js'))).toBe('/tasks/detail?task=release.js')
    expect(repositoryDetailLink('github.com', 'Sneat-Co/sneat-go').path).toBe('/repositories/github.com/Sneat-Co/sneat-go')
    expect(repositoryDetailLink(undefined, 'a/b').path).toBe('/repositories/-/a/b')
    expect(agentDetailLink('a/1').path).toBe('/agents/a%2F1')
    expect(machineDetailLink('m 1').path).toBe('/machines/m%201')
    expect(worktreeDetailLink('w#1').path).toBe('/worktrees/w%231')
  })

  // cockpit-views#ac:filter-vocabulary-is-the-only-link-target
  it('builds a valid link for every chip, state, age term, sort column and count cell of every page', () => {
    const links: AppLink[] = []
    for (const page of LIST_PAGES) {
      const vocabulary = VOCABULARY[page]
      for (const chip of vocabulary.chips) links.push(chipLink(page, chip.id))
      for (const state of vocabulary.states) links.push(stateLink(page, state))
      for (const sort of vocabulary.sorts) links.push(sortLink(page, sort, defaultDirection(page, sort)))
      if (vocabulary.hasActivity) for (const age of AGE_TERMS) links.push(ageLink(page, age))
      links.push(selectionLink(page, 'any'))
    }
    links.push(chipLink('agents', 'runtime-claude'), machineLink('worktrees', 'mach-1'), machineLink('agents', 'mach-1'), machineLink('repositories', 'mach-1'))
    for (const result of [taskWorktreesLink('fix ci'), repositoryWorktreesLink('sneat-dev/wb'), repositoryAgentsLink('sneat-dev/wb'), repositoryPullRequestsLink('sneat-dev/wb')]) {
      if (result.ok) links.push(result.link)
    }
    for (const link of links) expect(linkProblems(link), hrefOf(link)).toEqual([])
    expect(links.length).toBeGreaterThan(60)
    // The default sort of a page is a sort column of the page, or the internal order of Agents and Machines.
    for (const page of LIST_PAGES) {
      const { sort } = VOCABULARY[page].defaultSort
      expect(VOCABULARY[page].sorts.includes(sort) || ['running', 'local'].includes(sort)).toBe(true)
    }
  })

  it('refuses what the vocabulary does not list', () => {
    expect(() => chipLink('tasks', 'safe')).toThrow(/chip safe on tasks/)
    expect(() => stateLink('tasks', 'checks-pending')).toThrow(/state:checks-pending/)
    expect(() => stateLink('machines', 'fresh')).toThrow(/state:fresh/)
    expect(() => ageLink('worktrees', '30d' as never)).toThrow(/age:30d/)
    expect(() => listLink('tasks', { q: 'day:2026-10-01' })).not.toThrow()
    expect(() => listLink('worktrees', { sort: 'cpu' })).toThrow(/sort cpu/)
  })

  it('reports each problem of an address, and none for Home or a detail page', () => {
    const bad: AppLink = { path: '/agents', query: { chips: 'runtime-X,running', sort: 'nope', dir: 'up', q: 'state:parked age:<1d' } }
    expect(linkProblems(bad)).toEqual(['chip runtime-X on agents', 'sort nope on agents', 'dir up', 'age: not declared on agents'])
    expect(linkProblems({ path: '/repositories', query: { q: `age:<1d state:stale ${'x'.repeat(300)}` } })).toEqual(['filter text over the cap'])
    expect(linkProblems({ path: '/repositories', query: { q: 'age:3d' } })).toEqual(['age:3d'])
    expect(linkProblems({ path: '/machines', query: { q: 'age:<1d' } })).toEqual(['age: not declared on machines'])
    expect(linkProblems({ path: '/', query: {} })).toEqual([])
    expect(linkProblems(taskDetailLink('x'))).toEqual([])
    // A `day:` term is plain text now, never a vocabulary term.
    expect(linkProblems({ path: '/worktrees', query: { q: 'day:2026-10-01' } })).toEqual([])
    expect(linkProblems({ path: '/tasks', query: { q: '-state:landed task:x' } })).toEqual([])
  })
})

describe('detail addresses', () => {
  // The router encodes the segments of router commands itself: a string path given to routerLink would be encoded a second time.
  const IDS = ['plain', 'a/b', 'a b', '100%', 'é/ü ß', 'x?y#z;w', '%2F']

  it('carries, for an id in the path, the encoded path once and the same address as router commands, whatever the id', () => {
    for (const id of IDS) {
      for (const [make, root] of [
        [agentDetailLink, '/agents'],
        [machineDetailLink, '/machines'],
        [worktreeDetailLink, '/worktrees'],
      ] as const) {
        const link = make(id)
        expect(link.path, id).toBe(`${root}/${encodeURIComponent(id)}`)
        expect(link.commands, id).toEqual([root, id])
        expect(linkTarget(link), id).toEqual([root, id])
        // Decoded once, the path is the id again.
        expect(decodeURIComponent(link.path.slice(root.length + 1)), id).toBe(id)
      }
    }
    expect(linkTarget({ path: '/tasks', query: {} })).toBe('/tasks')
    expect(linkTarget(taskDetailLink('fix/ci 100%'))).toBe('/tasks/detail')
  })

  it('addresses a repository by host, owner and name, and one that is not owner/name by its entry id', () => {
    expect(repositoryDetailLink('github.com', 'a/b')).toEqual({ path: '/repositories/github.com/a/b', query: {}, commands: ['/repositories', 'github.com', 'a', 'b'] })
    expect(repositoryDetailLink(undefined, 'a/b').commands).toEqual(['/repositories', '-', 'a', 'b'])
    expect(repositoryDetailLink('github.com', 'a/b', 'r 1').path).toBe('/repositories/github.com/a/b')
    const gitlab = repositoryDetailLink('gitlab.com', 'group/sub/project', 'r/1 é')
    expect(gitlab.path).toBe('/repositories/r%2F1%20%C3%A9')
    expect(gitlab.commands).toEqual(['/repositories', 'r/1 é'])
    expect(repositoryDetailLink('gitlab.com', 'solo', 'solo-1').path).toBe('/repositories/solo-1')
    // Without an id there is nothing better than the host path.
    expect(repositoryDetailLink('gitlab.com', 'group/sub/project').path).toBe('/repositories/gitlab.com/group/sub/project')
  })

  // cockpit-views#ac:worktrees-columns-and-badges
  it('says, for the chips that only this machine can know, that they cover this machine only', () => {
    for (const id of ['unpushed', 'gone']) {
      expect(chipOf('worktrees', id)?.hint, id).toContain('this machine only')
    }
  })
})
