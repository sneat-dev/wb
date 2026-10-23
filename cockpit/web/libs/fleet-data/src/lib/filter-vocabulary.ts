// The full filter vocabulary (REQ:filter-vocabulary), for the lists: what a bare
// term searches, the default sort and, for every chip, the label and the hint a
// list renders from the page alone. The ids, `state:` values, fields and sorts
// that link validation needs are in `vocabulary.ts` (PAGE_RULES), which this
// builds on, so the two cannot disagree. Entry point: `@cockpit/fleet-data/list`.

import { ListPageId, PAGE_RULES, PageRules, RUNTIME_CHIP_PREFIX } from './vocabulary'

/** One quick-filter chip as a list shows it. */
export interface Chip {
  id: string
  /** The words on the chip. */
  label: string
  /** One sentence for the tooltip: which rows the chip keeps. */
  hint: string
}

export interface PageVocabulary extends Omit<PageRules, 'chips'> {
  /** What a bare term searches. */
  bare: readonly string[]
  /** The page's chips, in the order they are shown; the agents page also has `runtime-<name>` (see `chipOf`). */
  chips: readonly Chip[]
  /**
   * The sort of a list with no (valid) `sort` in its address. Agents and
   * Machines default to an internal order (running first, local first) that
   * has no column of its own.
   */
  defaultSort: { sort: string; dir: 'asc' | 'desc' }
}

type ChipText = readonly [label: string, hint: string]
type ChipsOf<P extends ListPageId> = (typeof PAGE_RULES)[P]['chips'][number]

// Typed by the rules, so a chip with no text, or text for a chip that is not listed, does not compile.
const CHIP_TEXT: { [P in ListPageId]: Record<ChipsOf<P>, ChipText> } = {
  tasks: {
    'needs-you': ['Needs you', 'The tasks Home lists under Needs you: a recent at-risk task, failed checks, a blocked agent, a pull request that needs you'],
    ready: ['Ready to land', 'Every open pull request of the task is ready to merge'],
    working: ['Working', 'An agent is working, a run is running or a worktree is in use'],
    agent: ['Has agent', 'The task has an agent or a run'],
    pr: ['Has pull request', 'The task has a pull request'],
    multirepo: ['Several repositories', 'The task spans more than one repository'],
    idle30: ['Idle 30 days', 'No activity for more than 30 days'],
    older: ['Older at-risk', 'Also list the at-risk tasks idle for more than 14 days, which this list leaves out as Home does (their worktrees are Home\'s Cleanup "need a look")'],
  },
  repositories: {
    worktrees: ['Has worktrees', 'At least one worktree'],
    agents: ['Has agents', 'At least one running agent'],
    prs: ['Open pull requests', 'At least one open pull request'],
    index: ['Index needs a look', 'The code index is stale, diverged or failed'],
    errors: ['Scan errors', 'The last scan of a checkout reported an error'],
  },
  worktrees: {
    active: ['Active', 'An agent or process owns it now'],
    orphaned: ['Orphaned', 'Its owner process is gone'],
    unpushed: ['Unpushed', 'It has commits that are not pushed (ahead of its upstream). Known for this machine only: a worktree read from another machine is never counted'],
    gone: ['Upstream gone', 'The upstream branch it tracked was deleted. Known for this machine only: a worktree read from another machine is never counted'],
    pr: ['Has pull request', 'A pull request is attached to its branch'],
    idle30: ['Idle 30 days', 'No activity for more than 30 days'],
    safe: ['Safe to remove', 'Counted as safe to remove by the Cleanup line of Home (indicative)'],
    look: ['Needs a look', 'Counted as needing a look by the Cleanup line of Home (indicative)'],
  },
  agents: {
    running: ['Running', 'A live session or a running run'],
    blocked: ['Blocked', 'The agent is waiting on you'],
  },
  machines: {
    stale: ['Stale', 'Has not published for over 24 hours'],
    outdated: ['Older WB', 'Runs an older WB than the newest in the fleet'],
  },
}

const BARE: Readonly<Record<ListPageId, readonly string[]>> = {
  tasks: ['task', 'repository'],
  repositories: ['repository'],
  worktrees: ['task', 'repository', 'branch'],
  agents: ['runtime', 'model', 'task', 'repository'],
  machines: ['machine'],
}

const DEFAULT_SORT: Readonly<Record<ListPageId, PageVocabulary['defaultSort']>> = {
  // By state precedence (worst first, REQ:task-state), then last activity newest first.
  tasks: { sort: 'state', dir: 'asc' },
  repositories: { sort: 'activity', dir: 'desc' },
  worktrees: { sort: 'activity', dir: 'desc' },
  agents: { sort: 'running', dir: 'desc' },
  machines: { sort: 'local', dir: 'asc' },
}

function page(id: ListPageId): PageVocabulary {
  const rules: PageRules = PAGE_RULES[id]
  const text: Readonly<Record<string, ChipText>> = CHIP_TEXT[id]
  return {
    ...rules,
    bare: BARE[id],
    chips: rules.chips.map((chip): Chip => ({
      id: chip,
      label: text[chip][0],
      hint: text[chip][1],
    })),
    defaultSort: DEFAULT_SORT[id],
  }
}

export const VOCABULARY: Readonly<Record<ListPageId, PageVocabulary>> = {
  tasks: page('tasks'),
  repositories: page('repositories'),
  worktrees: page('worktrees'),
  agents: page('agents'),
  machines: page('machines'),
}

/** The chip with this id on the page, or undefined when the page does not list it; the agents page also takes `runtime-<name>`. */
export function chipOf(pageId: ListPageId, id: string): Chip | undefined {
  const listed = VOCABULARY[pageId].chips.find((chip) => chip.id === id)
  if (listed !== undefined) return listed
  if (pageId === 'agents' && /^runtime-[a-z0-9-]+$/.test(id)) {
    const runtime = id.slice(RUNTIME_CHIP_PREFIX.length)
    return { id, label: runtime, hint: `Agents of the ${runtime} runtime` }
  }
  return undefined
}

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
