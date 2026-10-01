// The filter vocabulary (REQ:filter-vocabulary) as data, the address state of a
// list (REQ:list-quick-filters-sort-and-url-state), and the link builders. A
// link or chart click is built only here, and a builder refuses what the
// vocabulary does not list, so every number is a link that the page can read.

import { AGE_TERMS, AgeTerm, MAX_QUERY_LENGTH, parseQuery } from './matcher'
import { TASK_STATE_IDS } from './task-state'

export type ListPageId = 'tasks' | 'repositories' | 'worktrees' | 'agents' | 'machines'
export type PageId = 'home' | ListPageId

export interface PageVocabulary {
  /** The page's route. */
  path: string
  /** What a bare term searches. */
  bare: readonly string[]
  /** The `field:` names the page declares besides `state` and `age`. */
  fields: readonly string[]
  /** Chip ids; the agents page also has `runtime-<name>`. */
  chips: readonly string[]
  /** The `state:` values. */
  states: readonly string[]
  /** Whether `age:` applies (the page has a last-activity time). */
  hasActivity: boolean
  /** The sort column ids of the page; any other `sort` is ignored. */
  sorts: readonly string[]
  /**
   * The sort of a list with no (valid) `sort` in its address. Agents and
   * Machines default to an internal order (running first, local first) that
   * has no column of its own.
   */
  defaultSort: { sort: string; dir: 'asc' | 'desc' }
}

export const CODE_INDEX_STATES = ['fresh', 'stale', 'diverged', 'pending', 'failed', 'never'] as const
export const OWNER_STATES = ['active', 'idle', 'orphaned', 'unknown'] as const
export const AGENT_STATES = ['working', 'blocked', 'idle', 'done', 'unknown', 'live', 'parked', 'running', 'completed', 'failed', 'timeout', 'abandoned'] as const
export const MACHINE_STATES = ['live', 'cached', 'stale'] as const

/** The prefix of the dynamic Agents chips, `runtime-<name>`, where the name matches `[a-z0-9-]+`. */
export const RUNTIME_CHIP_PREFIX = 'runtime-'
const RUNTIME_CHIP = /^runtime-[a-z0-9-]+$/

/**
 * What `sel=` holds on each page: the task name, a repository entry id (a
 * merged row answers to the id of any of its checkouts), the worktree id, the
 * agent id, the machine id.
 */
export const SEL_KEYS: Readonly<Record<ListPageId, string>> = {
  tasks: 'task name',
  repositories: 'repository entry id',
  worktrees: 'worktree id',
  agents: 'agent id',
  machines: 'machine id',
}
export const VOCABULARY: Readonly<Record<ListPageId, PageVocabulary>> = {
  tasks: {
    path: '/tasks',
    bare: ['task', 'repository'],
    fields: ['task', 'repo', 'machine'],
    chips: ['needs-you', 'ready', 'working', 'agent', 'pr', 'multirepo', 'idle30'],
    states: TASK_STATE_IDS,
    hasActivity: true,
    sorts: ['task', 'state', 'worktrees', 'activity'],
    defaultSort: { sort: 'activity', dir: 'desc' },
  },
  repositories: {
    path: '/repositories',
    bare: ['repository'],
    fields: ['repo', 'machine'],
    chips: ['worktrees', 'agents', 'prs', 'index', 'errors'],
    states: CODE_INDEX_STATES,
    hasActivity: true,
    sorts: ['repository', 'activity', 'worktrees', 'branches'],
    defaultSort: { sort: 'activity', dir: 'desc' },
  },
  worktrees: {
    path: '/worktrees',
    bare: ['task', 'repository', 'branch'],
    fields: ['task', 'repo', 'branch', 'machine'],
    chips: ['active', 'orphaned', 'unpushed', 'gone', 'pr', 'idle30', 'safe', 'look'],
    states: OWNER_STATES,
    hasActivity: true,
    sorts: ['worktree', 'state', 'machine', 'activity'],
    defaultSort: { sort: 'activity', dir: 'desc' },
  },
  agents: {
    path: '/agents',
    bare: ['runtime', 'model', 'task', 'repository'],
    fields: ['runtime', 'task', 'repo', 'machine'],
    chips: ['running', 'blocked'],
    states: AGENT_STATES,
    hasActivity: false,
    sorts: ['label', 'activity', 'machine', 'started'],
    defaultSort: { sort: 'running', dir: 'desc' },
  },
  machines: {
    path: '/machines',
    bare: ['machine'],
    fields: ['machine'],
    chips: ['stale', 'outdated'],
    states: MACHINE_STATES,
    hasActivity: false,
    sorts: ['machine', 'state', 'version'],
    defaultSort: { sort: 'local', dir: 'asc' },
  },
}

/** The sort column ids that begin with the biggest or newest first; the others begin with the smallest or A first. */
export const DESC_FIRST: Readonly<Record<ListPageId, readonly string[]>> = {
  tasks: ['worktrees', 'activity'],
  repositories: ['activity', 'worktrees', 'branches'],
  worktrees: ['activity'],
  agents: ['started'],
  machines: [],
}

/** The direction a sort column starts in when the address names none. */
export function defaultDirection(page: ListPageId, sort: string): 'asc' | 'desc' {
  return DESC_FIRST[page].includes(sort) ? 'desc' : 'asc'
}

export const HOME_PATH = '/'
export const LIST_PAGES = Object.keys(VOCABULARY) as ListPageId[]

/** The fields a page declares, `state` and `age` included where they apply. */
export function declaredFields(page: ListPageId): ReadonlySet<string> {
  const vocabulary = VOCABULARY[page]
  const fields = new Set([...vocabulary.fields, 'state'])
  if (vocabulary.hasActivity) fields.add('age')
  return fields
}

/** Whether `chip` is one of the page's chips; the agents page also takes `runtime-<name>`. */
export function isChip(page: ListPageId, chip: string): boolean {
  return VOCABULARY[page].chips.includes(chip) || (page === 'agents' && RUNTIME_CHIP.test(chip))
}

/** The list state held in the address. */
export interface ListQuery {
  q: string
  sort?: string
  dir?: 'asc' | 'desc'
  /** Machine ids; empty means every machine. */
  machines: string[]
  chips: string[]
  sel?: string
}

export function emptyListQuery(): ListQuery {
  return { q: '', machines: [], chips: [] }
}

/** Reads a list's address parameters; a chip the page does not list is ignored. */
export function parseListQuery(page: ListPageId, params: Readonly<Record<string, string | undefined>>): ListQuery {
  const dir = params['dir']
  const split = (value: string | undefined): string[] => (value ? value.split(',').filter((part) => part !== '') : [])
  return {
    q: params['q'] ?? '',
    sort: params['sort'] || undefined,
    dir: dir === 'asc' || dir === 'desc' ? dir : undefined,
    machines: split(params['machine']),
    chips: [...new Set(split(params['chips']).filter((chip) => isChip(page, chip)))],
    sel: params['sel'] || undefined,
  }
}

/** The address parameters of a list state: `q`, `sort`, `dir`, `machine`, `chips`, `sel`; empty ones are left out. */
export function listQueryParams(query: Partial<ListQuery>): Record<string, string> {
  const params: Record<string, string> = {}
  if (query.q) params['q'] = query.q
  if (query.sort) params['sort'] = query.sort
  if (query.dir) params['dir'] = query.dir
  if (query.machines?.length) params['machine'] = query.machines.join(',')
  if (query.chips?.length) params['chips'] = query.chips.join(',')
  if (query.sel) params['sel'] = query.sel
  return params
}

/** An address inside the application: a path and its query parameters. */
export interface AppLink {
  path: string
  query: Record<string, string>
}

/** The link as one string, with every value percent-encoded. */
export function hrefOf(link: AppLink): string {
  const pairs = Object.entries(link.query).map(([name, value]) => `${encodeURIComponent(name)}=${encodeURIComponent(value)}`)
  return pairs.length ? `${link.path}?${pairs.join('&')}` : link.path
}

/** A link to a list page with the given state. Unknown chips, sorts and terms are refused. */
export function listLink(page: ListPageId, query: Partial<ListQuery> = {}): AppLink {
  const link: AppLink = { path: VOCABULARY[page].path, query: listQueryParams(query) }
  const problems = linkProblems(link)
  if (problems.length > 0) throw new Error(`not in the filter vocabulary: ${problems.join('; ')}`)
  return link
}

export function chipLink(page: ListPageId, chip: string): AppLink {
  return listLink(page, { chips: [chip] })
}

export function stateLink(page: ListPageId, state: string): AppLink {
  return listLink(page, { q: `state:${state}` })
}

/** `field:value`, the value quoted when it holds whitespace (a quote inside it is dropped). */
export function fieldTerm(field: string, value: string): string {
  const clean = value.replace(/"/g, '')
  return /\s/.test(clean) ? `${field}:"${clean}"` : `${field}:${clean}`
}

/** A link to a list filtered by one `field:value` term, and chips. The field must be declared by the page. */
export function termLink(page: ListPageId, field: string, value: string, chips: string[] = []): AppLink {
  if (!declaredFields(page).has(field)) throw new Error(`not in the filter vocabulary: ${field}: on ${page}`)
  return listLink(page, { q: fieldTerm(field, value), chips })
}

export function ageLink(page: ListPageId, age: AgeTerm): AppLink {
  return listLink(page, { q: `age:${age}` })
}

export function sortLink(page: ListPageId, sort: string, dir?: 'asc' | 'desc'): AppLink {
  return listLink(page, { sort, dir })
}

export function machineLink(page: ListPageId, machineId: string): AppLink {
  return listLink(page, { machines: [machineId] })
}

export function selectionLink(page: ListPageId, id: string): AppLink {
  return listLink(page, { sel: id })
}

/** `/tasks/detail?task=<name>`, the name percent-encoded. */
export function taskDetailLink(task: string): AppLink {
  return { path: '/tasks/detail', query: { task } }
}

/** `/repositories/:host/:owner/:name`; a repository with no host uses `-` for it. */
export function repositoryDetailLink(host: string | undefined, name: string): AppLink {
  const segments = [host ?? '-', ...name.split('/')].map(encodeURIComponent)
  return { path: `/repositories/${segments.join('/')}`, query: {} }
}

export function agentDetailLink(id: string): AppLink {
  return { path: `/agents/${encodeURIComponent(id)}`, query: {} }
}

export function machineDetailLink(id: string): AppLink {
  return { path: `/machines/${encodeURIComponent(id)}`, query: {} }
}

export function worktreeDetailLink(id: string): AppLink {
  return { path: `/worktrees/${encodeURIComponent(id)}`, query: {} }
}

/**
 * What is wrong with an address that targets a list page, in words; none when
 * every chip, `state:` and `age:` term and sort id is in the vocabulary.
 * An address that is not on a list page (Home, a detail page) has no problems.
 */
export function linkProblems(link: AppLink): string[] {
  const page = LIST_PAGES.find((candidate) => VOCABULARY[candidate].path === link.path)
  if (page === undefined) return []
  const vocabulary = VOCABULARY[page]
  const problems: string[] = []
  for (const chip of (link.query['chips'] ?? '').split(',').filter((part) => part !== '')) {
    if (!isChip(page, chip)) problems.push(`chip ${chip} on ${page}`)
  }
  const sort = link.query['sort']
  if (sort !== undefined && !vocabulary.sorts.includes(sort)) problems.push(`sort ${sort} on ${page}`)
  const dir = link.query['dir']
  if (dir !== undefined && dir !== 'asc' && dir !== 'desc') problems.push(`dir ${dir}`)
  const text = link.query['q'] ?? ''
  if (text.length > MAX_QUERY_LENGTH) problems.push('filter text over the cap')
  const declared = declaredFields(page)
  for (const term of parseQuery(text)) {
    const field = term.field
    if (field !== 'state' && field !== 'age') continue
    if (!declared.has(field)) problems.push(`${field}: not declared on ${page}`)
    else if (field === 'state' && !vocabulary.states.includes(term.value)) problems.push(`state:${term.value} on ${page}`)
    else if (field === 'age' && !(AGE_TERMS as readonly string[]).includes(term.value)) problems.push(`age:${term.value}`)
  }
  return problems
}
