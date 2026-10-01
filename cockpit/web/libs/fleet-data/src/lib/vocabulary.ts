// The filter vocabulary (REQ:filter-vocabulary) as data, the address state of a
// list (REQ:list-quick-filters-sort-and-url-state), and the link builders. A
// link or chart click is built only here, and a builder refuses what the
// vocabulary does not list, so every number is a link that the page can read.

import { AGE_TERMS, AgeTerm, MAX_QUERY_LENGTH, parseQuery } from './matcher'
import { TASK_STATE_IDS } from './task-state'

export type ListPageId = 'tasks' | 'repositories' | 'worktrees' | 'agents' | 'machines'
export type PageId = 'home' | ListPageId

/**
 * What link validation needs of a page: where it lives, the `field:` names, the
 * chip and sort ids and the `state:` values. The rest of the vocabulary (what a
 * bare term searches, the default sort, the chips' labels and hints) is for the
 * list and lives in `filter-vocabulary.ts`, behind `@cockpit/fleet-data/list`.
 */
export interface PageRules {
  /** The page's route. */
  path: string
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
}

export const CODE_INDEX_STATES = ['fresh', 'stale', 'diverged', 'pending', 'failed', 'never'] as const
export const OWNER_STATES = ['active', 'idle', 'orphaned', 'unknown'] as const
export const AGENT_STATES = ['working', 'blocked', 'idle', 'done', 'unknown', 'live', 'parked', 'running', 'completed', 'failed', 'timeout', 'abandoned'] as const
export const MACHINE_STATES = ['live', 'cached', 'stale'] as const

/** The prefix of the dynamic Agents chips, `runtime-<name>`, where the name matches `[a-z0-9-]+`. */
export const RUNTIME_CHIP_PREFIX = 'runtime-'
const RUNTIME_CHIP = /^runtime-[a-z0-9-]+$/

/** The page rules, one per list page (REQ:filter-vocabulary). */
export const PAGE_RULES = {
  tasks: {
    path: '/tasks',
    fields: ['task', 'repo', 'machine'],
    chips: ['needs-you', 'ready', 'working', 'agent', 'pr', 'multirepo', 'idle30'],
    states: TASK_STATE_IDS,
    hasActivity: true,
    sorts: ['task', 'state', 'worktrees', 'activity'],
  },
  repositories: {
    path: '/repositories',
    fields: ['repo', 'machine'],
    chips: ['worktrees', 'agents', 'prs', 'index', 'errors'],
    states: CODE_INDEX_STATES,
    hasActivity: true,
    sorts: ['repository', 'activity', 'worktrees', 'branches'],
  },
  worktrees: {
    path: '/worktrees',
    fields: ['task', 'repo', 'branch', 'machine'],
    chips: ['active', 'orphaned', 'unpushed', 'gone', 'pr', 'idle30', 'safe', 'look'],
    states: OWNER_STATES,
    hasActivity: true,
    sorts: ['worktree', 'state', 'machine', 'activity'],
  },
  agents: {
    path: '/agents',
    fields: ['runtime', 'task', 'repo', 'machine'],
    chips: ['running', 'blocked'],
    states: AGENT_STATES,
    hasActivity: false,
    sorts: ['label', 'activity', 'machine', 'started'],
  },
  machines: {
    path: '/machines',
    fields: ['machine'],
    chips: ['stale', 'outdated'],
    states: MACHINE_STATES,
    hasActivity: false,
    sorts: ['machine', 'state', 'version'],
  },
} as const satisfies Record<ListPageId, PageRules>

export const HOME_PATH = '/'
export const LIST_PAGES = Object.keys(PAGE_RULES) as ListPageId[]

/** The fields a page declares, `state` and `age` included where they apply. */
export function declaredFields(page: ListPageId): ReadonlySet<string> {
  const vocabulary: PageRules = PAGE_RULES[page]
  const fields = new Set([...vocabulary.fields, 'state'])
  if (vocabulary.hasActivity) fields.add('age')
  return fields
}

/** Whether `chip` is one of the page's chips; the agents page also takes `runtime-<name>`. */
export function isChip(page: ListPageId, chip: string): boolean {
  return (PAGE_RULES[page].chips as readonly string[]).includes(chip) || (page === 'agents' && RUNTIME_CHIP.test(chip))
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

/** One list item with `%` and `,` escaped, so a comma inside an id cannot split `machine=`. */
export function encodeListItem(item: string): string {
  return item.replace(/%/g, '%25').replace(/,/g, '%2C')
}

/** The address parameters of a list state: `q`, `sort`, `dir`, `machine`, `chips`, `sel`; empty ones are left out. */
export function listQueryParams(query: Partial<ListQuery>): Record<string, string> {
  const params: Record<string, string> = {}
  if (query.q) params['q'] = query.q
  if (query.sort) params['sort'] = query.sort
  if (query.dir) params['dir'] = query.dir
  if (query.machines?.length) params['machine'] = query.machines.map(encodeListItem).join(',')
  if (query.chips?.length) params['chips'] = query.chips.join(',')
  if (query.sel) params['sel'] = query.sel
  return params
}

/** An address inside the application: a path and its query parameters. */
export interface AppLink {
  /** The address as a string, every segment percent-encoded once: for `href`, `navigateByUrl` and `hrefOf`. */
  path: string
  query: Record<string, string>
  /**
   * The same path as router commands, with each segment as the raw value (`['/agents', 'a/1']`), for a detail
   * address whose id is a path segment. A string given to `[routerLink]` is encoded a second time by the router
   * (`a%252F1`), so a template binds `linkTarget(link)`, which is these commands when there are any, else the path.
   */
  commands?: readonly string[]
}

/** What `[routerLink]` is given for a link: its commands when it has them (an id with a `/` or a `%` survives), else its path. */
export function linkTarget(link: AppLink): string | readonly string[] {
  return link.commands ?? link.path
}

/** The link as one string, with every value percent-encoded. */
export function hrefOf(link: AppLink): string {
  const pairs = Object.entries(link.query).map(([name, value]) => `${encodeURIComponent(name)}=${encodeURIComponent(value)}`)
  return pairs.length ? `${link.path}?${pairs.join('&')}` : link.path
}

/** A link to a list page with the given state. Unknown chips, sorts and terms are refused. */
export function listLink(page: ListPageId, query: Partial<ListQuery> = {}): AppLink {
  const link: AppLink = {
    path: PAGE_RULES[page].path,
    query: listQueryParams(query),
  }
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

export function ageLink(page: ListPageId, age: AgeTerm): AppLink {
  return listLink(page, { q: `age:${age}` })
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

/** A detail address whose id is one path segment: `/<root>/<id>`, encoded once for the string and raw in the commands. */
function idLink(root: string, id: string): AppLink {
  return {
    path: `${root}/${encodeURIComponent(id)}`,
    query: {},
    commands: [root, id],
  }
}

/**
 * `/repositories/:host/:owner/:name` (a repository with no host uses `-` for it). A name that is not exactly
 * `owner/name` (GitLab `group/sub/project`) has no such address, so with its entry `id` it is `/repositories/<id>`.
 */
export function repositoryDetailLink(host: string | undefined, name: string, id?: string): AppLink {
  const parts = name.split('/')
  if (parts.length !== 2 && id !== undefined) return idLink('/repositories', id)
  const segments = [host ?? '-', ...parts]
  return {
    path: `/repositories/${segments.map(encodeURIComponent).join('/')}`,
    query: {},
    commands: ['/repositories', ...segments],
  }
}

export function agentDetailLink(id: string): AppLink {
  return idLink('/agents', id)
}

export function machineDetailLink(id: string): AppLink {
  return idLink('/machines', id)
}

export function worktreeDetailLink(id: string): AppLink {
  return idLink('/worktrees', id)
}

/**
 * What is wrong with an address that targets a list page, in words; none when
 * every chip, `state:` and `age:` term and sort id is in the vocabulary.
 * An address that is not on a list page (Home, a detail page) has no problems.
 */
export function linkProblems(link: AppLink): string[] {
  const page = LIST_PAGES.find((candidate) => PAGE_RULES[candidate].path === link.path)
  if (page === undefined) return []
  const vocabulary: PageRules = PAGE_RULES[page]
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
