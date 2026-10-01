// The rest of the list's address state and links (REQ:list-quick-filters-sort-and-url-state,
// REQ:every-number-is-a-link): reading a list's address back into a query, `field:"value"`
// terms and the count-cell links. Only the lists need them, so they are apart from
// `vocabulary.ts` (what Home and the shell need to build links) and behind
// `@cockpit/fleet-data/list`.

import { ListPageId, ListQuery, PAGE_RULES, AppLink, chipLink, declaredFields, isChip, linkProblems, listLink, listQueryParams, machineLink } from './vocabulary'
import { MAX_QUERY_LENGTH } from './matcher'

export function emptyListQuery(): ListQuery {
  return { q: '', machines: [], chips: [] }
}


function decodeListItem(item: string): string {
  return item.replace(/%2C/g, ',').replace(/%25/g, '%')
}

/** Reads a list's address parameters; a chip the page does not list is ignored. */
export function parseListQuery(page: ListPageId, params: Readonly<Record<string, string | undefined>>): ListQuery {
  const dir = params['dir']
  const split = (value: string | undefined): string[] => (value ? value.split(',').filter((part) => part !== '').map(decodeListItem) : [])
  return {
    q: params['q'] ?? '',
    sort: params['sort'] || undefined,
    dir: dir === 'asc' || dir === 'desc' ? dir : undefined,
    machines: split(params['machine']),
    chips: [...new Set(split(params['chips']).filter((chip) => isChip(page, chip)))],
    sel: params['sel'] || undefined,
  }
}


/** A link, or why none can be made: the page then shows the count as plain text with the reason as its title. */
export type LinkResult = { ok: true; link: AppLink } | { ok: false; reason: string }

/** A `field:"value"` term, or why the value cannot be expressed. */
export type TermResult = { ok: true; text: string } | { ok: false; reason: string }

/**
 * `field:"value"`: always quoted, so it matches exactly that value (a quoted
 * value is a whole-value match and its `*` and `?` are literal) and a count cell
 * opens exactly the rows it counted. A value that holds a double quote cannot be
 * quoted, and one that does not fit the filter cap cannot be typed: neither can
 * be a link.
 */
export function fieldTerm(field: string, value: string): TermResult {
  if (value.includes('"')) return { ok: false, reason: 'the name contains a double quote, which a filter cannot express' }
  const text = `${field}:"${value}"`
  if (text.length > MAX_QUERY_LENGTH) return { ok: false, reason: `the filter for this name would be over ${MAX_QUERY_LENGTH} characters` }
  return { ok: true, text }
}

/** A link to a list filtered by one exact `field:"value"` term, and chips. The field must be declared by the page. */
export function termLink(page: ListPageId, field: string, value: string, chips: string[] = []): LinkResult {
  if (!declaredFields(page).has(field)) return { ok: false, reason: `${field}: is not a filter field of ${page}` }
  const term = fieldTerm(field, value)
  if (!term.ok) return term
  const link: AppLink = { path: PAGE_RULES[page].path, query: listQueryParams({ q: term.text, chips }) }
  const problems = linkProblems(link)
  return problems.length > 0 ? { ok: false, reason: `not in the filter vocabulary: ${problems.join('; ')}` } : { ok: true, link }
}


export function sortLink(page: ListPageId, sort: string, dir?: 'asc' | 'desc'): AppLink {
  return listLink(page, { sort, dir })
}


// ---- count-cell links (REQ:every-number-is-a-link) ----
// Each is an exact link to the rows the count counted, or a reason when the name
// cannot be expressed in a filter: the page then shows the count as plain text
// with the reason as its title. Branch counts and the throughput numbers never link.

const linked = (link: AppLink): LinkResult => ({ ok: true, link })

/** Tasks: the worktree count opens Worktrees with `task:"<name>"`. */
export function taskWorktreesLink(task: string): LinkResult {
  return termLink('worktrees', 'task', task)
}

/** Repositories: the worktree count opens Worktrees with `repo:"<owner/name>"`. */
export function repositoryWorktreesLink(slug: string): LinkResult {
  return termLink('worktrees', 'repo', slug)
}

/** Repositories: the agent count opens Agents with `repo:"<owner/name>"`. */
export function repositoryAgentsLink(slug: string): LinkResult {
  return termLink('agents', 'repo', slug)
}

/** Repositories: the pull request count opens Tasks with chip `pr` and `repo:"<owner/name>"`. */
export function repositoryPullRequestsLink(slug: string): LinkResult {
  return termLink('tasks', 'repo', slug, ['pr'])
}

/** Machines: the repository count opens Repositories on that machine. */
export function machineRepositoriesLink(machineId: string): LinkResult {
  return linked(machineLink('repositories', machineId))
}

/** Machines: the worktree count opens Worktrees on that machine. */
export function machineWorktreesLink(machineId: string): LinkResult {
  return linked(machineLink('worktrees', machineId))
}

/** Machines: the agent count opens Agents on that machine. */
export function machineAgentsLink(machineId: string): LinkResult {
  return linked(machineLink('agents', machineId))
}

/** The Agents tab badge opens Agents with chip `running`. */
export function agentsBadgeLink(): LinkResult {
  return linked(chipLink('agents', 'running'))
}
