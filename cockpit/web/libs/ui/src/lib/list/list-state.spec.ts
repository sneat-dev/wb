import { emptyListQuery } from '@cockpit/fleet-data'
import {
  ADDRESS_KEYS,
  ListColumn,
  MAX_RENDERED_ROWS,
  addressKey,
  isEmptyCell,
  MAX_COLUMNS,
  addressChange,
  describeFilters,
  effectiveSort,
  scrollToRow,
  sortedBy,
  toggled,
  virtualWindow,
  visibleColumns,
} from './list-state'

describe('addressChange', () => {
  it('names all six parameters, null for each that is not set', () => {
    expect(addressChange(emptyListQuery())).toEqual({ q: null, sort: null, dir: null, machine: null, chips: null, sel: null })
    expect(ADDRESS_KEYS).toEqual(['q', 'sort', 'dir', 'machine', 'chips', 'sel'])
    expect(addressChange({ q: 'fix', machines: [], chips: [] }, 'left')).toEqual({ 'left.q': 'fix', 'left.sort': null, 'left.dir': null, 'left.machine': null, 'left.chips': null, 'left.sel': null })
    expect(addressKey('', 'q')).toBe('q')
    expect(addressKey('a', 'q')).toBe('a.q')
    expect(addressChange({ q: 'fix', sort: 'activity', dir: 'asc', machines: ['a,b'], chips: ['pr', 'gone'], sel: 'w1' })).toEqual({
      q: 'fix',
      sort: 'activity',
      dir: 'asc',
      machine: 'a%2Cb',
      chips: 'pr,gone',
      sel: 'w1',
    })
  })
})

describe('effectiveSort', () => {
  it('is the page default with no sort, and the first direction of a listed column', () => {
    expect(effectiveSort('worktrees', emptyListQuery())).toEqual({ sort: 'activity', dir: 'desc' })
    expect(effectiveSort('worktrees', { ...emptyListQuery(), sort: 'machine' })).toEqual({ sort: 'machine', dir: 'asc' })
    expect(effectiveSort('worktrees', { ...emptyListQuery(), sort: 'activity' })).toEqual({ sort: 'activity', dir: 'desc' })
    expect(effectiveSort('agents', emptyListQuery())).toEqual({ sort: 'running', dir: 'desc' })
  })

  it('takes the direction of the address, and ignores a column the page does not list', () => {
    expect(effectiveSort('worktrees', { ...emptyListQuery(), sort: 'machine', dir: 'desc' })).toEqual({ sort: 'machine', dir: 'desc' })
    expect(effectiveSort('worktrees', { ...emptyListQuery(), sort: 'bogus' })).toEqual({ sort: 'activity', dir: 'desc' })
  })
})

describe('sortedBy', () => {
  it('sorts by a new column in its first direction and reverses the one that sorts already', () => {
    const start = emptyListQuery()
    const machine = sortedBy('worktrees', start, 'machine')
    expect(machine).toMatchObject({ sort: 'machine', dir: 'asc' })
    expect(sortedBy('worktrees', machine, 'machine')).toMatchObject({ sort: 'machine', dir: 'desc' })
    expect(sortedBy('worktrees', sortedBy('worktrees', machine, 'machine'), 'machine')).toMatchObject({ dir: 'asc' })
  })

  it('reverses the default sort on the first click on its column, and keeps the rest of the state', () => {
    const query = { ...emptyListQuery(), q: 'fix', chips: ['pr'] }
    expect(sortedBy('worktrees', query, 'activity')).toEqual({ ...query, sort: 'activity', dir: 'asc' })
    expect(sortedBy('worktrees', query, 'worktree')).toMatchObject({ sort: 'worktree', dir: 'asc' })
  })
})

describe('toggled', () => {
  it('adds what is missing and removes what is there', () => {
    expect(toggled(['a'], 'b')).toEqual(['a', 'b'])
    expect(toggled(['a', 'b'], 'a')).toEqual(['b'])
  })
})

describe('visibleColumns', () => {
  const column = (id: string, extra: Partial<ListColumn<number>> = {}): ListColumn<number> => ({ id, header: id, width: 10, ...extra })

  it('keeps every column when none can be uniform', () => {
    expect(visibleColumns([column('a'), column('b')], [1, 2]).map((c) => c.id)).toEqual(['a', 'b'])
  })

  it('hides a column that is empty for every row, and keeps one that is empty for some', () => {
    const columns = [column('a'), column('b', { empty: (n) => n > 0 }), column('c', { empty: (n) => n > 1 })]
    expect(visibleColumns(columns, [1, 2]).map((c) => c.id)).toEqual(['a', 'c'])
    // A column with a value that is empty for every row is hidden too, without an `empty` rule.
    const valued = [column('v', { value: (n) => (n > 5 ? 'x' : undefined) }), column('w', { value: () => '' }), column('u', { value: () => 'x' })]
    expect(visibleColumns(valued, [1, 2]).map((c) => c.id)).toEqual(['u'])
    expect(visibleColumns(valued, [1, 9]).map((c) => c.id)).toEqual(['v', 'u'])
    expect(isEmptyCell(column('z', { empty: () => true, value: () => 'x' }), 1)).toBe(true)
    expect(isEmptyCell(column('z'), 1)).toBe(false)
    expect(visibleColumns(columns, [0, 2]).map((c) => c.id)).toEqual(['a', 'b', 'c'])
  })

  it('hides nothing while there are no rows, so the header does not change while it waits', () => {
    expect(visibleColumns([column('a', { empty: () => true })], []).map((c) => c.id)).toEqual(['a'])
  })

  it('shows at most seven, dropping the lowest keep first and the later one among equals', () => {
    expect(MAX_COLUMNS).toBe(7)
    const columns = ['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i'].map((id) => column(id))
    expect(visibleColumns(columns, [1]).map((c) => c.id)).toEqual(['a', 'b', 'c', 'd', 'e', 'f', 'g'])
    const kept = [column('a'), column('b', { keep: 1 }), column('c', { keep: 1 }), column('d'), column('e')]
    expect(visibleColumns(kept, [1], 3).map((c) => c.id)).toEqual(['a', 'd', 'e'])
    expect(visibleColumns(kept, [1], 4).map((c) => c.id)).toEqual(['a', 'b', 'd', 'e'])
    expect(visibleColumns([column('a', { keep: 9 }), column('b')], [1], 1).map((c) => c.id)).toEqual(['a'])
  })
})

describe('virtualWindow', () => {
  it('renders the rows in view with the overscan on both sides, and never more than the total', () => {
    expect(virtualWindow(0, 320, 600, 32, 8)).toEqual({ first: 0, last: 18 })
    expect(virtualWindow(3200, 320, 600, 32, 8)).toEqual({ first: 92, last: 118 })
    expect(virtualWindow(0, 320, 5, 32, 8)).toEqual({ first: 0, last: 5 })
    expect(virtualWindow(0, 320, 0)).toEqual({ first: 0, last: 0 })
  })

  it('never renders more than the cap, however tall the viewport', () => {
    expect(MAX_RENDERED_ROWS).toBe(80)
    const { first, last } = virtualWindow(0, 4000, 600)
    expect(last - first).toBe(MAX_RENDERED_ROWS)
  })

  it('clamps a scroll position that is past the end of a list that shrank', () => {
    expect(virtualWindow(100000, 320, 600, 32, 8)).toEqual({ first: 582, last: 600 })
  })
})

describe('scrollToRow', () => {
  it('scrolls up to a row above the window, down to one below it, and stays for one in view', () => {
    expect(scrollToRow(1, 200, 320, 32)).toBe(32)
    expect(scrollToRow(12, 0, 320, 32)).toBe(12 * 32 + 64 - 320)
    expect(scrollToRow(5, 100, 320, 32)).toBe(100)
  })
})

describe('describeFilters', () => {
  const chips = new Map([['pr', 'Pull request']])
  const machines = new Map([['m1', 'mac']])

  it('says each active filter in words', () => {
    expect(describeFilters({ ...emptyListQuery(), q: ' zzz ' }, chips, machines)).toBe('the filter “zzz”')
    expect(describeFilters({ ...emptyListQuery(), chips: ['pr'] }, chips, machines)).toBe('the Pull request filter')
    expect(describeFilters({ ...emptyListQuery(), chips: ['pr', 'other'], machines: ['m1', 'm2'] }, chips, machines)).toBe('the Pull request, other filters and machine mac or m2')
    expect(describeFilters({ q: 'a', chips: ['pr'], machines: ['m1'] }, chips, machines)).toBe('the filter “a” and the Pull request filter and machine mac')
    expect(describeFilters(emptyListQuery(), chips, machines)).toBe('')
  })
})
