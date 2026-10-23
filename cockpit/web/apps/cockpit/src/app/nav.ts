/** A signal a tab can carry: the one number that needs the operator's eye. */
export type TabSignal = 'needs-you' | 'running'

export interface PageLink {
  path: string
  label: string
  /** The key after `g` that opens it (REQ:keyboard-shortcuts). */
  key: string
  /** The badge it carries; no tab has one for a static count (REQ:top-bar). */
  signal?: TabSignal
}

/**
 * The tabs of the top bar, in order: Home, Tasks, then the inventory pages.
 * Every route they lead to is registered in app.routes.ts; a page task never
 * edits this list.
 */
export const PAGE_LINKS: readonly PageLink[] = [
  { path: '/', label: 'Home', key: 'h', signal: 'needs-you' },
  { path: '/tasks', label: 'Tasks', key: 't' },
  { path: '/repositories', label: 'Repositories', key: 'r' },
  { path: '/worktrees', label: 'Worktrees', key: 'w' },
  { path: '/agents', label: 'Agents', key: 'a', signal: 'running' },
  { path: '/machines', label: 'Machines', key: 'm' },
]

/** The route of the "New task" form. */
export const NEW_TASK_PATH = '/tasks/new'
