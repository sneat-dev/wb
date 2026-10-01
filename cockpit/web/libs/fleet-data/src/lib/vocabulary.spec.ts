import { AGE_TERMS } from './matcher'
import { TASK_STATE_IDS } from './task-state'
import {
  AppLink,
  CODE_INDEX_STATES,
  DESC_FIRST,
  HOME_PATH,
  LIST_PAGES,
  SEL_KEYS,
  VOCABULARY,
  agentDetailLink,
  ageLink,
  chipLink,
  declaredFields,
  defaultDirection,
  emptyListQuery,
  fieldTerm,
  hrefOf,
  isChip,
  linkProblems,
  listLink,
  listQueryParams,
  machineDetailLink,
  machineLink,
  parseListQuery,
  repositoryDetailLink,
  selectionLink,
  sortLink,
  stateLink,
  taskDetailLink,
  termLink,
  worktreeDetailLink,
} from './vocabulary'

describe('the vocabulary table', () => {
  it('lists, per page, what REQ:filter-vocabulary lists', () => {
    expect(VOCABULARY.tasks.chips).toEqual(['needs-you', 'ready', 'working', 'agent', 'pr', 'multirepo', 'idle30'])
    expect(VOCABULARY.tasks.states).toEqual(TASK_STATE_IDS)
    expect(VOCABULARY.tasks.bare).toEqual(['task', 'repository'])
    expect(VOCABULARY.tasks.sorts).toEqual(['task', 'state', 'worktrees', 'activity'])
    expect(VOCABULARY.repositories.chips).toEqual(['worktrees', 'agents', 'prs', 'index', 'errors'])
    expect(VOCABULARY.repositories.states).toEqual(CODE_INDEX_STATES)
    expect(VOCABULARY.repositories.sorts).toEqual(['repository', 'activity', 'worktrees', 'branches'])
    expect(VOCABULARY.worktrees.chips).toEqual(['active', 'orphaned', 'unpushed', 'gone', 'pr', 'idle30', 'safe', 'look'])
    expect(VOCABULARY.worktrees.states).toEqual(['active', 'idle', 'orphaned', 'unknown'])
    expect(VOCABULARY.worktrees.bare).toEqual(['task', 'repository', 'branch'])
    expect(VOCABULARY.worktrees.sorts).toEqual(['worktree', 'state', 'machine', 'activity'])
    expect(VOCABULARY.agents.chips).toEqual(['running', 'blocked'])
    expect(VOCABULARY.agents.states).toEqual(['working', 'blocked', 'idle', 'done', 'unknown', 'live', 'parked', 'running', 'completed', 'failed', 'timeout', 'abandoned'])
    expect(VOCABULARY.agents.bare).toEqual(['runtime', 'model', 'task', 'repository'])
    expect(VOCABULARY.agents.sorts).toEqual(['label', 'activity', 'machine', 'started'])
    expect(VOCABULARY.machines.chips).toEqual(['stale', 'outdated'])
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

  it('quotes a field value with whitespace and drops quotes inside it', () => {
    expect(fieldTerm('task', 'fix-ci')).toBe('task:fix-ci')
    expect(fieldTerm('task', 'fix ci')).toBe('task:"fix ci"')
    expect(fieldTerm('task', 'a "b" c')).toBe('task:"a b c"')
    expect(termLink('worktrees', 'task', 'fix ci')).toEqual({ path: '/worktrees', query: { q: 'task:"fix ci"' } })
    expect(termLink('tasks', 'repo', 'sneat-dev/wb', ['pr'])).toEqual({ path: '/tasks', query: { q: 'repo:sneat-dev/wb', chips: 'pr' } })
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
      for (const chip of vocabulary.chips) links.push(chipLink(page, chip))
      for (const state of vocabulary.states) links.push(stateLink(page, state))
      for (const sort of vocabulary.sorts) links.push(sortLink(page, sort, defaultDirection(page, sort)))
      if (vocabulary.hasActivity) for (const age of AGE_TERMS) links.push(ageLink(page, age))
      links.push(selectionLink(page, 'any'))
    }
    links.push(chipLink('agents', 'runtime-claude'), termLink('worktrees', 'task', 'fix ci'), termLink('worktrees', 'repo', 'sneat-dev/wb'), termLink('agents', 'repo', 'sneat-dev/wb'))
    links.push(termLink('tasks', 'repo', 'sneat-dev/wb', ['pr']), machineLink('worktrees', 'mach-1'), machineLink('agents', 'mach-1'), machineLink('repositories', 'mach-1'))
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
    expect(() => termLink('agents', 'age' as never, '<1d')).toThrow(/age: on agents/)
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
