// The pure part of the list: the address of a list state, the sort a header
// click makes, which columns show, and which rows a virtual window renders.

import { ListPageId, ListQuery, VOCABULARY, defaultDirection, listQueryParams } from '@cockpit/fleet-data'

/** The six address parameters of a list (REQ:list-quick-filters-sort-and-url-state). */
export const ADDRESS_KEYS = ['q', 'sort', 'dir', 'machine', 'chips', 'sel'] as const

/** The name of an address parameter of a list with a `prefix` (two lists on one page): `a.q`; with none, `q`. */
export const addressKey = (prefix: string, key: string): string => (prefix === '' ? key : `${prefix}.${key}`)

/** The height of a row, which is also the height of a skeleton row; the list binds `--row-h` from it. */
export const ROW_HEIGHT = 32
/** Rows rendered above and below the visible ones: enough that dragging the scrollbar does not show a gap before the next frame. */
export const OVERSCAN = 12
/** The most rows ever rendered, on however tall a viewport (REQ:bounded-row-elements). */
export const MAX_RENDERED_ROWS = 80
/** At most this many columns show by default (REQ:default-columns-are-few). */
export const MAX_COLUMNS = 7

/** One column of a list. */
export interface ListColumn<T> {
  id: string
  header: string
  /** The sort column id (REQ:filter-vocabulary) the header sorts by; a header with none does not sort. */
  sort?: string
  /** A fixed width in pixels, or `fill` to share what is left. */
  width: number | 'fill'
  /** For a `fill` column, its share of what is left (default 1). */
  grow?: number
  /**
   * The cell's text: its default content, its `title` while it truncates, and what makes it empty. A cell
   * with rich content gives a template as well and keeps this for the title and for the empty rule.
   */
  value?: (item: T) => string | undefined
  /** True for a row whose value is the default, besides an absent or empty `value`; a column that is so for every visible row is hidden. */
  empty?: (item: T) => boolean
  /** What the header's `title` says. */
  hint?: string
  /** When more than 7 columns would show, the lowest `keep` goes first (default 5). */
  keep?: number
  /** Where the column goes first: `narrow` below about 1000 px of list (a panel beside it, a tablet), `phone` below about 640 px. */
  drop?: 'narrow' | 'phone'
  /** For a `fill` column, the least width in pixels it keeps. */
  min?: number
  align?: 'end'
}

/** One quick-filter chip; `id` is a chip of the page's vocabulary. */
export interface ListChip {
  id: string
  label: string
  /** What the chip means, where its label is not enough. */
  hint?: string
}

/** The address change of a list state: every one of the six parameters, null for one that is not set. */
export function addressChange(query: ListQuery, prefix = ''): Record<string, string | null> {
  const params = listQueryParams(query)
  return Object.fromEntries(ADDRESS_KEYS.map((key) => [addressKey(prefix, key), params[key] ?? null]))
}

/** Whether a cell is empty: the column says so, or it has a `value` function and that gives nothing. */
export function isEmptyCell<T>(column: ListColumn<T>, item: T): boolean {
  return (column.empty?.(item) ?? false) || (column.value !== undefined && (column.value(item) ?? '') === '')
}

/** The sort and direction a list shows: the address's, or the page's default. */
export function effectiveSort(page: ListPageId, query: ListQuery): { sort: string; dir: 'asc' | 'desc' } {
  const vocabulary = VOCABULARY[page]
  const requested = query.sort !== undefined && vocabulary.sorts.includes(query.sort) ? query.sort : undefined
  return {
    sort: requested ?? vocabulary.defaultSort.sort,
    dir: query.dir ?? (requested === undefined ? vocabulary.defaultSort.dir : defaultDirection(page, requested)),
  }
}

/** The state after a click on a header: that column in its first direction, or the other one when it already sorts. */
export function sortedBy(page: ListPageId, query: ListQuery, column: string): ListQuery {
  const current = effectiveSort(page, query)
  const dir = current.sort === column ? (current.dir === 'asc' ? 'desc' : 'asc') : defaultDirection(page, column)
  return { ...query, sort: column, dir }
}

/** `items` with `item` removed when it is there, added when it is not. */
export function toggled(items: readonly string[], item: string): string[] {
  return items.includes(item) ? items.filter((candidate) => candidate !== item) : [...items, item]
}

/**
 * The columns to show for `items` (REQ:default-columns-are-few): a column that
 * is empty, or the default, for every row is hidden, and if more than `max`
 * remain the ones with the lowest `keep` go, the later one first among equals.
 * With no rows nothing is hidden, so the header does not change while it waits.
 */
export function visibleColumns<T>(columns: readonly ListColumn<T>[], items: readonly T[], max = MAX_COLUMNS): ListColumn<T>[] {
  const shown = columns.filter((column) => items.length === 0 || !items.every((item) => isEmptyCell(column, item)))
  const dropped = new Set(
    shown
      .map((column, index) => ({ column, index }))
      .sort((a, b) => (a.column.keep ?? 5) - (b.column.keep ?? 5) || b.index - a.index)
      .slice(0, Math.max(0, shown.length - max))
      .map((entry) => entry.column),
  )
  return shown.filter((column) => !dropped.has(column))
}

/** The rows `first` to `last` (exclusive) that a window of `height` pixels scrolled by `scrollTop` renders, with the overscan. */
export function virtualWindow(scrollTop: number, height: number, total: number, rowHeight = ROW_HEIGHT, overscan = OVERSCAN): { first: number; last: number } {
  // A list that shrank can still hold a scroll position past its end until the browser clamps it.
  const top = Math.min(scrollTop, Math.max(0, total * rowHeight - height))
  const first = Math.max(0, Math.floor(top / rowHeight) - overscan)
  const last = Math.min(total, first + MAX_RENDERED_ROWS, Math.ceil((top + height) / rowHeight) + overscan)
  return { first, last }
}

/** Where to scroll so row `index` is fully in view under a header of `rowHeight`, or `scrollTop` when it already is. */
export function scrollToRow(index: number, scrollTop: number, height: number, rowHeight = ROW_HEIGHT): number {
  const top = index * rowHeight
  if (top < scrollTop) return top
  const bottom = top + 2 * rowHeight - height
  return bottom > scrollTop ? bottom : scrollTop
}

/** What the filters are, in words, for the line that says nothing matched. */
export function describeFilters(query: ListQuery, chipLabels: ReadonlyMap<string, string>, machineLabels: ReadonlyMap<string, string>): string {
  const parts: string[] = []
  if (query.q.trim() !== '') parts.push(`the filter “${query.q.trim()}”`)
  if (query.chips.length > 0) parts.push(`the ${query.chips.map((chip) => chipLabels.get(chip) ?? chip).join(', ')} filter${query.chips.length > 1 ? 's' : ''}`)
  if (query.machines.length > 0) parts.push(`machine ${query.machines.map((machine) => machineLabels.get(machine) ?? machine).join(' or ')}`)
  return parts.join(' and ')
}
