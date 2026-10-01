// The pure part of the list: the address of a list state, the sort a header
// click makes, which columns show, and which rows a virtual window renders.

import { ListPageId, ListQuery, listQueryParams } from '@cockpit/fleet-data'
import { VOCABULARY, defaultDirection } from '@cockpit/fleet-data/list'

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
/** At most this many columns show by default (REQ:default-columns-are-few): a column with a declared `priority` may go past it, while the list is wide enough. */
export const MAX_COLUMNS = 7
/** The `priority` of a column that is never hidden for lack of room. */
export const ALWAYS = 10
/** The least width a squeezed column keeps while there is room for it. */
const SQUEEZED_FLOOR = 80
/** The width of the row-end "open page" cell, reserved in every row so it never shifts the last column. */
export const OPEN_CELL_WIDTH = 32

/** One column of a list. */
export interface ListColumn<T> {
  id: string
  header: string
  /** The sort column id (REQ:filter-vocabulary) the header sorts by; a header with none does not sort. */
  sort?: string
  /** The most pixels the column takes, or `fill` to share what is left. */
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
  /**
   * What the column is worth when room is short. When the list is narrower than its columns need (a panel
   * beside it, a narrow window), the lowest goes first, the later one first among equals. `ALWAYS` (10) is never
   * hidden: it shrinks instead. A column that declares a `priority` may be one of more than 7: it shows while the
   * list is wide enough for the `min` of every column and goes in this order as it narrows. A column with none
   * (default 5) is held to the default of at most 7, and goes first when more than 7 would show.
   */
  priority?: number
  /** The least pixels the column needs to be worth showing (default: its `width`); below the sum of these the lowest-priority column is hidden. */
  min?: number
  align?: 'end'
  /**
   * A trailing cell of controls (icon links), not a column: it is not counted toward the 7, its header
   * (`header`, still the name assistive technology reads) is visually hidden, and it hides by `priority`
   * like the rest when room is short.
   */
  chrome?: boolean
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
 * remain (the `chrome` cells are not counted) the ones with the lowest `priority`
 * go, the later one first among equals, unless they declare a `priority`: those are
 * left to `fitColumns`, which keeps them while the list is wide enough for all of
 * them. With no rows nothing is hidden, so the header does not change while it waits.
 */
export function visibleColumns<T>(columns: readonly ListColumn<T>[], items: readonly T[], max = MAX_COLUMNS): ListColumn<T>[] {
  const shown = columns.filter((column) => items.length === 0 || !items.every((item) => isEmptyCell(column, item)))
  const counted = shown.filter((column) => column.chrome !== true)
  const dropped = new Set(
    counted
      .map((column, index) => ({ column, index }))
      .sort((a, b) => (a.column.priority ?? 5) - (b.column.priority ?? 5) || b.index - a.index)
      .slice(0, Math.max(0, counted.length - max))
      .map((entry) => entry.column)
      .filter((column) => column.priority === undefined),
  )
  return shown.filter((column) => !dropped.has(column))
}

/** The least width a column needs. */
const needOf = <T>(column: ListColumn<T>): number => column.min ?? (column.width === 'fill' ? 0 : column.width)

/**
 * The columns that fit a list `width` pixels wide, and the grid tracks that lay them out. Columns
 * go lowest `priority` first, the later one first among equals, until the least widths of the rest
 * (and the open-page cell) fit; columns of `ALWAYS` priority are never dropped, and when they alone
 * do not fit the tracks share the width in proportion. A width of 0 (not measured yet) hides nothing.
 * A `fill` column's track is `minmax(min, grow fr)`; a fixed one takes up to its width.
 */
export function fitColumns<T>(columns: readonly ListColumn<T>[], width: number): { columns: ListColumn<T>[]; tracks: string } {
  const kept = [...columns]
  const room = width - OPEN_CELL_WIDTH
  const total = () => kept.reduce((sum, column) => sum + needOf(column), 0)
  while (width > 0 && total() > room) {
    let worst = -1
    kept.forEach((column, index) => {
      if ((column.priority ?? 5) < ALWAYS && (worst < 0 || (column.priority ?? 5) <= (kept[worst].priority ?? 5))) worst = index
    })
    if (worst < 0) break
    kept.splice(worst, 1)
  }
  const squeezed = width > 0 && total() > room
  // When even the columns that stay do not fit, each shrinks toward a floor (40% of its need, at least 80 px), and what is left is shared in proportion; below the floors too, all share the width in proportion.
  const floorOf = (need: number) => Math.min(need, Math.max(SQUEEZED_FLOOR, Math.round(need * 0.4)))
  const floored = squeezed && kept.reduce((sum, column) => sum + floorOf(needOf(column)), 0) <= room
  const track = (column: ListColumn<T>): string => {
    const need = needOf(column)
    if (squeezed) return `minmax(${floored ? `${floorOf(need)}px` : '0'}, ${Math.max(need, 1)}fr)`
    return column.width === 'fill' ? `minmax(${need}px, ${column.grow ?? 1}fr)` : `minmax(${need}px, ${column.width}px)`
  }
  return {
    columns: kept,
    tracks: [...kept.map(track), `${OPEN_CELL_WIDTH}px`].join(' '),
  }
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
