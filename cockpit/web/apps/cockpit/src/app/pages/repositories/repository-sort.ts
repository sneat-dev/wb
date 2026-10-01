/** One of the one-click orders of the Repositories page (REQ:repositories-list): a sort column, newest or largest first. */
export interface SortPreset {
  /** The sort column id of REQ:filter-vocabulary: what `sort=` holds. */
  id: 'activity' | 'worktrees' | 'branches'
  label: string
  hint: string
}

export const SORT_PRESETS: readonly SortPreset[] = [
  { id: 'activity', label: 'Recent', hint: 'Most recent activity first' },
  { id: 'worktrees', label: 'Most worktrees', hint: 'The most worktrees first' },
  { id: 'branches', label: 'Most branches', hint: 'The most branches (local and remote) first' },
]

/** The preset the list is sorted by (a header click that arrives at the same order counts); none for any other order. */
export function activePreset(sort: { sort: string; dir: 'asc' | 'desc' }): SortPreset['id'] | undefined {
  return sort.dir === 'desc' ? SORT_PRESETS.find((preset) => preset.id === sort.sort)?.id : undefined
}
