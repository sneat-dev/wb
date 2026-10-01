import { NgTemplateOutlet } from '@angular/common'
import { ChangeDetectionStrategy, Component, DestroyRef, ElementRef, afterNextRender, computed, contentChild, contentChildren, inject, input, linkedSignal, signal, viewChild } from '@angular/core'
import { toSignal } from '@angular/core/rxjs-interop'
import { ActivatedRoute, Router } from '@angular/router'
import { FleetStore, ListPageId, ListQuery, ListRow, applyListQuery, declaredFields, parseListQuery } from '@cockpit/fleet-data'
import { SidePanel } from '../panel/side-panel'
import { Glyph } from './glyph'
import { ListCell, ListPanelTemplate } from './list-cell'
import { LIST_SHORTCUTS } from './list-host'
import {
  ADDRESS_KEYS,
  ListChip,
  ListColumn,
  ROW_HEIGHT,
  addressChange,
  describeFilters,
  effectiveSort,
  scrollToRow,
  sortedBy,
  toggled,
  virtualWindow,
  visibleColumns,
} from './list-state'

/** Ages in the filter are measured against a clock that moves once a minute, so a poll that changes nothing recomputes nothing. */
const CLOCK_BUCKET_MS = 60_000
/** How many placeholder rows a list shows while the daemon warms up. */
export const SKELETON_ROWS = 12

let nextId = 0

/**
 * The one list every page composes (REQ:list-filter-and-matcher and the
 * requirements after it): a filter box with the wildcard grammar and a hint, a
 * machine chip group, the page's quick-filter chips, sortable headers under a
 * sticky header, a result count, one-line rows rendered by virtual scrolling
 * (a fixed window of rows, whatever the number of rows), a keyboard of `j`, `k`,
 * Enter and Esc, and the master-detail side panel of the selected row. All of
 * its state is in the address (`q`, `sort`, `dir`, `machine`, `chips`, `sel`),
 * so back and forward and a pasted link restore it.
 *
 * A page gives the rows (the view model's `*Rows`), the columns, the chips and,
 * as templates, the cells and the panel; the matching, sorting and link
 * vocabulary come from the fleet-data library.
 */
@Component({
  selector: 'app-list',
  imports: [NgTemplateOutlet, SidePanel, Glyph],
  templateUrl: './list-view.html',
  styleUrl: './list-view.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { '(window:resize)': 'measure()' },
})
export class ListView<T = unknown> {
  readonly page = input.required<ListPageId>()
  /** The plural noun of the rows, for the empty states: "worktrees". */
  readonly noun = input.required<string>()
  readonly rows = input.required<readonly ListRow<T>[]>()
  readonly columns = input.required<readonly ListColumn<T>[]>()
  readonly chips = input.required<readonly ListChip[]>()
  /** What assistive technology calls the panel of a row. */
  readonly panelLabel = input<(item: T) => string>(() => 'Details')
  /** How a `sel` finds its row; by default the row with that id. */
  readonly resolve = input<(rows: readonly ListRow<T>[], sel: string) => ListRow<T> | undefined>((rows, sel) => rows.find((row) => row.id === sel))

  private readonly cells = contentChildren(ListCell)
  protected readonly panelTemplate = contentChild(ListPanelTemplate)
  private readonly viewport = viewChild.required<ElementRef<HTMLElement>>('viewport')
  private readonly filterBox = viewChild.required<ElementRef<HTMLInputElement>>('filterBox')

  protected readonly store = inject(FleetStore)
  private readonly router = inject(Router)
  private readonly route = inject(ActivatedRoute)
  private readonly shortcuts = inject(LIST_SHORTCUTS)
  private readonly params = toSignal(this.route.queryParamMap, { requireSync: true })
  protected readonly id = `list-${nextId++}`

  /** The address's state. */
  private readonly address = computed(() => {
    const params = this.params()
    return parseListQuery(this.page(), Object.fromEntries(ADDRESS_KEYS.map((key) => [key, params.get(key) ?? undefined])))
  })
  /**
   * The filter text: what is typed now, which the address catches up with within a frame. An address
   * that arrives while this list's own navigations are under way is theirs, not news: the text stays.
   */
  protected readonly text = linkedSignal<string, string>({
    source: () => this.address().q,
    computation: (q, previous) => (this.navigating > 0 ? (previous as { value: string }).value : q),
  })
  protected readonly query = computed<ListQuery>(() => ({ ...this.address(), q: this.text() }))
  private readonly clock = computed(() => Math.floor(this.store.now() / CLOCK_BUCKET_MS) * CLOCK_BUCKET_MS)
  protected readonly result = computed(() => applyListQuery(this.page(), this.rows(), this.query(), this.clock()))
  protected readonly sort = computed(() => effectiveSort(this.page(), this.query()))
  protected readonly shown = computed(() =>
    visibleColumns(
      this.columns(),
      this.result().rows.map((row) => row.item),
    ),
  )

  private readonly cellTemplates = computed(() => new Map(this.cells().map((cell) => [cell.id(), cell.template])))
  protected readonly fields = computed(() => [...declaredFields(this.page())].sort().join(', '))
  protected readonly machines = computed(() => {
    const options = this.store.machineOptions()
    return options.length > 1 || this.query().machines.length > 0 ? options : []
  })
  private readonly chipLabels = computed(() => new Map(this.chips().map((chip) => [chip.id, chip.label])))
  protected readonly nothingMatched = computed(() => describeFilters(this.query(), this.chipLabels(), new Map(this.store.machineOptions().map((machine) => [machine.id, machine.label]))))

  protected readonly selected = computed(() => {
    const sel = this.address().sel
    return sel === undefined ? undefined : this.resolve()(this.rows(), sel)
  })
  /** The row the keyboard is on; it returns to the selected row, or the first, when the result changes. */
  protected readonly focused = linkedSignal(() => {
    const sel = this.query().sel
    const at = sel === undefined ? -1 : this.result().rows.findIndex((row) => row.id === sel)
    return Math.max(at, 0)
  })
  protected readonly focusPanel = signal(false)
  protected readonly hintOpen = signal(false)

  protected readonly scrollTop = signal(0)
  protected readonly height = signal(window.innerHeight)
  protected readonly visible = computed(() => {
    const { first, last } = virtualWindow(this.scrollTop(), this.height(), this.result().rows.length)
    return { first, offset: first * ROW_HEIGHT, rows: this.result().rows.slice(first, last).map((row, index) => ({ row, index: first + index })) }
  })
  protected readonly panelOpen = computed(() => this.panelTemplate() !== undefined && this.selected() !== undefined)
  protected readonly total = computed(() => this.result().rows.length * ROW_HEIGHT)
  /** The placeholder rows: while nothing has loaded, or the first scan has found nothing yet. */
  protected readonly skeleton = computed(() => !this.store.loaded() || (this.store.warmingUp() && this.rows().length === 0))
  protected readonly skeletonRows = Array.from({ length: SKELETON_ROWS }, (_, index) => index)
  protected readonly activeId = computed(() => {
    const focused = this.focused()
    const { first, rows } = this.visible()
    return focused >= first && focused < first + rows.length ? `${this.id}-row-${focused}` : null
  })

  private frame: number | undefined
  /** How many navigations this list has begun and not seen end. */
  private navigating = 0

  constructor() {
    const destroyed = inject(DestroyRef)
    destroyed.onDestroy(this.shortcuts.registerPanel(() => this.closePanel()))
    destroyed.onDestroy(() => cancelAnimationFrame(this.frame as number))
    afterNextRender(() => {
      this.measure()
      const box = this.filterBox().nativeElement
      destroyed.onDestroy(
        this.shortcuts.registerFilter({
          element: box,
          focus: () => box.focus(),
          clear: () => this.clearText(),
          isEmpty: () => box.value === '',
        }),
      )
    })
  }

  protected cellOf(id: string) {
    return this.cellTemplates().get(id)
  }

  /** The flex of a column: a fill column shares what is left, a fixed one shrinks before it would push a fill column below its minimum. */
  protected style(column: ListColumn<T>): string {
    return column.width === 'fill' ? `${column.grow ?? 1} 1 0` : `0 1 ${column.width}px`
  }

  /** The `aria-sort` of a header: which way the list is sorted by that column, or none. */
  protected ariaSort(column: ListColumn<T>): string | null {
    if (column.sort === undefined) return null
    return this.sort().sort === column.sort ? (this.sort().dir === 'asc' ? 'ascending' : 'descending') : 'none'
  }

  protected measure(): void {
    this.height.set(this.viewport().nativeElement.clientHeight || window.innerHeight)
  }

  protected scrolled(): void {
    this.scrollTop.set(this.viewport().nativeElement.scrollTop)
    this.measure()
  }

  /** Filtering reacts at once; the address follows within the next frame, without a history entry for each key. */
  protected setText(value: string): void {
    this.text.set(value)
    this.toTop()
    if (this.frame === undefined) {
      this.frame = requestAnimationFrame(() => {
        this.frame = undefined
        void this.go(this.query(), true)
      })
    }
  }

  /** Empties the filter box itself as well as the text: a key typed in the frame before has not been rendered into it yet. */
  protected clearText(): void {
    this.filterBox().nativeElement.value = ''
    this.setText('')
  }

  protected toggleChip(chip: string): void {
    this.toTop()
    void this.go({ ...this.query(), chips: toggled(this.query().chips, chip) })
  }

  protected toggleMachine(machine: string): void {
    this.toTop()
    void this.go({ ...this.query(), machines: toggled(this.query().machines, machine) })
  }

  protected sortBy(column: string): void {
    this.toTop()
    void this.go(sortedBy(this.page(), this.query(), column))
  }

  protected clearFilters(): void {
    this.text.set('')
    this.toTop()
    void this.go({ ...this.query(), q: '', chips: [], machines: [], sel: undefined })
  }

  /** Selects a row and opens its panel, and the focus goes into it; with no row it closes the panel. A pasted address opens a panel without that. */
  protected select(id: string | undefined): void {
    this.focusPanel.set(id !== undefined)
    void this.go({ ...this.query(), sel: id })
  }

  /** Closes the panel and says whether one was open; the focus returns to the list. */
  protected closePanel(): boolean {
    if (this.selected() === undefined) return false
    this.select(undefined)
    this.viewport().nativeElement.focus()
    return true
  }

  /** A click on a row selects it; one on a link or button in a cell, or on the header, is that control's own. */
  protected clicked(event: MouseEvent): void {
    const target = event.target as Element
    const row = target.closest<HTMLElement>('[data-index]')
    if (row === null || target.closest('a, button') !== null) return
    const index = Number(row.dataset['index'])
    this.focused.set(index)
    this.select(this.result().rows[index].id)
    this.viewport().nativeElement.focus()
  }

  protected keydown(event: KeyboardEvent): void {
    // Keys typed in a link or button inside a row belong to it.
    if (event.target !== event.currentTarget || event.ctrlKey || event.metaKey || event.altKey || event.isComposing) return
    const rows = this.result().rows
    switch (event.key) {
      case 'j':
      case 'ArrowDown':
        this.move(1)
        break
      case 'k':
      case 'ArrowUp':
        this.move(-1)
        break
      case 'Enter':
        if (rows.length > 0) this.select(rows[this.focused()].id)
        break
      default:
        return
    }
    event.preventDefault()
  }

  private move(delta: number): void {
    const last = this.result().rows.length - 1
    if (last < 0) return
    const index = Math.min(last, Math.max(0, this.focused() + delta))
    this.focused.set(index)
    const element = this.viewport().nativeElement
    const top = scrollToRow(index, element.scrollTop, element.clientHeight || this.height())
    element.scrollTop = top
    this.scrollTop.set(top)
  }

  private toTop(): void {
    this.viewport().nativeElement.scrollTop = 0
    this.scrollTop.set(0)
  }

  private async go(query: ListQuery, replaceUrl = false): Promise<void> {
    this.navigating++
    try {
      await this.router.navigate([], { relativeTo: this.route, queryParams: addressChange(query), queryParamsHandling: 'merge', replaceUrl })
    } finally {
      this.navigating--
    }
  }
}
