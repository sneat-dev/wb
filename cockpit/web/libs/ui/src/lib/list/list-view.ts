import { NgTemplateOutlet } from '@angular/common'
import {
  ChangeDetectionStrategy,
  Component,
  DestroyRef,
  ElementRef,
  afterNextRender,
  afterRenderEffect,
  computed,
  contentChild,
  contentChildren,
  effect,
  inject,
  input,
  linkedSignal,
  signal,
  viewChild,
} from '@angular/core'
import { toSignal } from '@angular/core/rxjs-interop'
import { ActivatedRoute, Router, RouterLink } from '@angular/router'
import { AppLink, FleetStore, ListPageId, ListQuery, declaredFields, hrefOf, linkTarget } from '@cockpit/fleet-data'
import { ListRow, VOCABULARY, applyListQuery, parseListQuery } from '@cockpit/fleet-data/list'
import { ClipboardWriter } from '../control/clipboard'
import { Glyph } from '../control/glyph'
import { GLYPH_ARROW_DOWN, GLYPH_ARROW_UP } from '../control/glyphs'
import { SidePanel } from '../panel/side-panel'
import { SheetMode } from '../panel/sheet-mode'
import { ListAnnouncer } from './list-announcer'
import { GLYPH_OPEN } from './list-glyphs'
import { ListCell, ListPanelTemplate } from './list-cell'
import { LIST_SHORTCUTS } from './list-host'
import { ListToolbar } from './list-toolbar'
import { PAGE_NOUN, chipsOf, copyOf, detailOf, nameOf, rowsOf } from './page-defaults'
import {
  ADDRESS_KEYS,
  ListChip,
  ListColumn,
  ROW_HEIGHT,
  addressChange,
  addressKey,
  describeFilters,
  effectiveSort,
  fitColumns,
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
/** A filter that reads the clock: an `age:` term, or the `idle30` chip. */
const AGE_TERM = /(^|\s)-?age:/i

let nextId = 0

/**
 * The one list every page composes (REQ:list-filter-and-matcher and the
 * requirements after it): a filter box with the wildcard grammar and a hint, a
 * machine chip group, the page's quick-filter chips, sortable headers under a
 * sticky header, a result count, one-line rows rendered by virtual scrolling (a
 * bounded window of rows, whatever the number of rows), a keyboard of `j`, `k`,
 * PageUp, PageDown, Home, End, Enter, `o` (the entity's page), `c` (its name)
 * and Esc, and the master-detail side panel of the selected row. All of its state
 * is in the address (`q`, `sort`, `dir`, `machine`, `chips`, `sel`; with a
 * `prefix`, `<prefix>.q` and so on), so back and forward and a pasted link
 * restore it. The list is one tab stop: the links and buttons in its rows are out
 * of the tab order.
 *
 * Under the rows, `[listFooter]` projects the page's notes ("showing the first 200"); it takes no space while empty.
 *
 * A page gives the `page` and the columns, and templates for the cells that are
 * more than text and for the panel; the rows, the noun, the chips and their
 * words, the entity's address and name come from the page id, and the
 * matching, sorting and link vocabulary from the fleet-data library.
 */
@Component({
  selector: 'app-list',
  imports: [NgTemplateOutlet, RouterLink, SidePanel, ListToolbar, Glyph],
  providers: [ListAnnouncer],
  templateUrl: './list-view.html',
  styleUrl: './list-view.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { '[style.--row-h]': "rowHeight + 'px'" },
})
export class ListView<T = unknown> {
  readonly page = input.required<ListPageId>()
  readonly columns = input.required<readonly ListColumn<T>[]>()
  /** The rows to list; by default the view model's rows for the page. */
  readonly rows = input<readonly ListRow<T>[]>()
  /** Chips the vocabulary cannot list in advance (the Agents page's `runtime-<name>`). */
  readonly extraChips = input<readonly ListChip[]>([])
  /** Names this list's address parameters (`<prefix>.q`), so two lists on one page do not collide. */
  readonly prefix = input('')
  /** What assistive technology calls the panel of a row. */
  readonly panelLabel = input<(item: T) => string>(() => 'Details')
  /** What `c` copies for the focused row; by default its name, and for an agent its run or session id (`copyOf`). */
  readonly copyValue = input<(item: T) => string>()
  /** How a `sel` finds its row; by default the row with that id. */
  readonly resolve = input<(rows: readonly ListRow<T>[], sel: string) => ListRow<T> | undefined>((rows, sel) => rows.find((row) => row.id === sel))

  private readonly cells = contentChildren(ListCell)
  protected readonly panelTemplate = contentChild(ListPanelTemplate)
  private readonly viewport = viewChild.required<ElementRef<HTMLElement>>('viewport')
  private readonly toolbar = viewChild.required(ListToolbar)
  private readonly panelHost = viewChild(SidePanel)

  protected readonly store = inject(FleetStore)
  private readonly router = inject(Router)
  private readonly route = inject(ActivatedRoute)
  private readonly shortcuts = inject(LIST_SHORTCUTS)
  private readonly clipboard = inject(ClipboardWriter)
  protected readonly announcer = inject(ListAnnouncer)
  protected readonly sheet = inject(SheetMode).active
  private readonly params = toSignal(this.route.queryParamMap, { requireSync: true })
  protected readonly id = `list-${nextId++}`
  protected readonly rowHeight = ROW_HEIGHT
  protected readonly open = GLYPH_OPEN
  protected readonly target = linkTarget
  protected readonly up = GLYPH_ARROW_UP
  protected readonly down = GLYPH_ARROW_DOWN

  protected readonly noun = computed(() => PAGE_NOUN[this.page()])
  protected readonly items = computed(() => (this.rows() ?? rowsOf(this.store.model(), this.page())) as readonly ListRow<T>[])
  protected readonly chips = computed(() => [...chipsOf(this.page()), ...this.extraChips()])

  /** The address's state; a machine the fleet does not list is dropped (once the fleet is known). */
  private readonly address = computed(() => {
    const params = this.params()
    const prefix = this.prefix()
    const query = parseListQuery(this.page(), Object.fromEntries(ADDRESS_KEYS.map((key) => [key, params.get(addressKey(prefix, key)) ?? undefined])))
    const known = this.store.machineOptions().map((machine) => machine.id)
    return known.length === 0 ? query : { ...query, machines: query.machines.filter((machine) => known.includes(machine)) }
  })

  /** The filter texts this list's navigations are writing; an address that carries one of them is not news. */
  private readonly writing: string[] = []
  /**
   * The filter text: what is typed now, which the address catches up with within a frame. An address
   * that carries what this list is itself writing is theirs, not news: the text stays. Any other
   * address (back, forward, a pasted link) sets it.
   */
  protected readonly text = linkedSignal<string, string>({
    source: () => this.address().q,
    computation: (q, previous) => (this.writing.includes(q) ? (previous as { value: string }).value : q),
  })
  protected readonly query = computed<ListQuery>(() => ({ ...this.address(), q: this.text() }))
  private readonly clock = computed(() => (AGE_TERM.test(this.query().q) || this.query().chips.includes('idle30') ? Math.floor(this.store.now() / CLOCK_BUCKET_MS) * CLOCK_BUCKET_MS : 0))
  protected readonly result = computed(() => applyListQuery(this.page(), this.items(), this.query(), this.clock()))
  protected readonly sort = computed(() => effectiveSort(this.page(), this.query()))
  protected readonly width = signal(0)
  private readonly fitted = computed(() =>
    fitColumns(
      visibleColumns(
        this.columns(),
        this.result().rows.map((row) => row.item),
      ),
      this.width(),
    ),
  )
  protected readonly shown = computed(() => this.fitted().columns)
  protected readonly tracks = computed(() => this.fitted().tracks)

  private readonly cellTemplates = computed(() => new Map(this.cells().map((cell) => [cell.id(), cell.template])))
  protected readonly fields = computed(() => [...declaredFields(this.page())].sort().join(', '))
  protected readonly machines = computed(() => {
    const options = this.store.machineOptions()
    return options.length > 1 || this.query().machines.length > 0 ? options : []
  })
  protected readonly nothingMatched = computed(() =>
    describeFilters(this.query(), new Map(this.chips().map((chip) => [chip.id, chip.label])), new Map(this.store.machineOptions().map((machine) => [machine.id, machine.label]))),
  )

  protected readonly selected = computed(() => {
    const sel = this.address().sel
    return sel === undefined ? undefined : this.resolve()(this.items(), sel)
  })
  /** The `sel` of the last row that was found, so a selection that was there and is gone can be told from one that never was. */
  private readonly seen = signal<string | undefined>(undefined)
  protected readonly vanished = computed(() => this.selected() === undefined && this.address().sel !== undefined && this.address().sel === this.seen())
  protected readonly panelOpen = computed(() => this.panelTemplate() !== undefined && (this.selected() !== undefined || this.vanished()))

  /** The id of the row the keyboard is on, and the index it had, to land near it when the row goes. */
  private readonly focusedId = signal<string | undefined>(undefined)
  private readonly focusedAt = signal(0)
  /** The row the keyboard is on: the row with the held id, else the selected row, else the nearest index to where it was. */
  protected readonly focused = computed(() => {
    const rows = this.result().rows
    const held = this.focusedId() ?? this.selected()?.id
    const at = held === undefined ? -1 : rows.findIndex((row) => row.id === held)
    return at >= 0 ? at : Math.min(this.focusedAt(), Math.max(0, rows.length - 1))
  })

  protected readonly scrollTop = signal(0)
  protected readonly height = signal(window.innerHeight)
  protected readonly visible = computed(() => {
    const { first, last } = virtualWindow(this.scrollTop(), this.height(), this.result().rows.length)
    return { first, offset: first * ROW_HEIGHT, rows: this.result().rows.slice(first, last).map((row, index) => ({ row, index: first + index })) }
  })
  protected readonly total = computed(() => this.result().rows.length * ROW_HEIGHT)
  /** The placeholder rows: while nothing has loaded, or the first scan has found nothing yet. */
  protected readonly skeleton = computed(() => !this.store.loaded() || (this.store.warmingUp() && this.items().length === 0))
  protected readonly skeletonRows = Array.from({ length: SKELETON_ROWS }, (_, index) => index)
  protected readonly activeId = computed(() => {
    const focused = this.focused()
    const { first, rows } = this.visible()
    return focused >= first && focused < first + rows.length ? `${this.id}-row-${focused}` : null
  })

  private frame: number | undefined
  private readonly links = new WeakMap<object, AppLink>()
  private scrolledTo: string | undefined

  constructor() {
    const destroyed = inject(DestroyRef)
    destroyed.onDestroy(this.shortcuts.registerPanel(() => this.closePanel()))
    destroyed.onDestroy(() => this.cancelFrame())
    // A selection that is there is where the keyboard goes next, and a deep link or back from a page shows it.
    effect(() => {
      const row = this.selected()
      if (row === undefined) return
      this.seen.set(this.address().sel)
      this.focusedId.set(row.id)
    })
    afterRenderEffect(() => {
      const id = this.selected()?.id
      if (id === this.scrolledTo) return
      this.scrolledTo = id
      const index = this.result().rows.findIndex((row) => row.id === id)
      if (index >= 0) this.scrollTo(scrollToRow(index, this.viewport().nativeElement.scrollTop, this.viewport().nativeElement.clientHeight || this.height()))
    })
    // The one tab stop is the list: the links and buttons its rows hold are reached with the mouse or the list's own keys.
    afterRenderEffect(() => {
      this.visible()
      for (const control of this.viewport().nativeElement.querySelectorAll<HTMLElement>('.row a, .row button')) control.tabIndex = -1
    })
    afterNextRender(() => {
      this.measure()
      // The viewport grows when data replaces the placeholders and when the window is resized: measure each time.
      if (typeof ResizeObserver !== 'undefined') {
        const observer = new ResizeObserver(() => this.measure())
        observer.observe(this.viewport().nativeElement)
        destroyed.onDestroy(() => observer.disconnect())
      }
      const toolbar = this.toolbar()
      destroyed.onDestroy(
        this.shortcuts.registerFilter({
          element: toolbar.element,
          focus: () => toolbar.focus(),
          clear: () => toolbar.clear(),
          isEmpty: () => toolbar.isEmpty(),
        }),
      )
    })
  }

  protected cellOf(id: string) {
    return this.cellTemplates().get(id)
  }

  /** The `aria-sort` of a header: which way the list is sorted by that column, or none. */
  protected ariaSort(column: ListColumn<T>): string | null {
    if (column.sort === undefined) return null
    return this.sort().sort === column.sort ? (this.sort().dir === 'asc' ? 'ascending' : 'descending') : 'none'
  }

  /** The address of a row's own page, made once per row. */
  protected detail(item: T): AppLink {
    const found = this.links.get(item as object)
    if (found !== undefined) return found
    const link = detailOf(this.page(), item)
    this.links.set(item as object, link)
    return link
  }

  protected name(item: T): string {
    return nameOf(this.page(), item)
  }

  /** What `c` copies for a row: the page's `copyValue`, else the default for the page. */
  protected copied(item: T): string {
    return this.copyValue()?.(item) ?? copyOf(this.page(), item)
  }

  protected measure(): void {
    this.height.set(this.viewport().nativeElement.clientHeight || window.innerHeight)
    this.width.set(this.viewport().nativeElement.clientWidth)
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

  protected toggleChip(chip: string): void {
    this.commit({ ...this.query(), chips: toggled(this.query().chips, chip) })
  }

  protected toggleMachine(machine: string): void {
    this.commit({ ...this.query(), machines: toggled(this.query().machines, machine) })
  }

  protected sortBy(column: string): void {
    this.commit(sortedBy(this.page(), this.query(), column))
  }

  protected clearFilters(): void {
    this.toolbar().clear()
    this.commit({ ...this.query(), q: '', chips: [], machines: [], sel: undefined })
  }

  /** A change of the filters, which carries the text too: a queued write of the text is no longer needed. */
  private commit(query: ListQuery): void {
    this.cancelFrame()
    this.toTop()
    void this.go(query)
  }

  /**
   * Selects a row and opens its panel, or with no row closes it. Going from one row to another replaces the
   * history entry, so back leaves the panel's rows at once; opening and closing are entries of their own.
   */
  protected select(id: string | undefined): void {
    void this.go({ ...this.query(), sel: id }, id !== undefined && this.address().sel !== undefined)
  }

  /** Closes the panel and says whether one was open; the focus returns to the list, on the row that was open. */
  protected closePanel(): boolean {
    if (!this.panelOpen()) return false
    this.select(undefined)
    this.viewport().nativeElement.focus()
    return true
  }

  /** A click on a row selects it; one on a link or button in a cell, or on the header, is that control's own. */
  protected clicked(event: MouseEvent): void {
    const target = event.target as Element
    const row = target.closest<HTMLElement>('[data-index]')
    if (row === null || target.closest('a, button') !== null) return
    this.focusRow(Number(row.dataset['index']))
    this.select(this.result().rows[this.focused()].id)
    this.viewport().nativeElement.focus()
  }

  protected keydown(event: KeyboardEvent): void {
    // Keys typed in a link or button inside a row belong to it.
    if (event.target !== event.currentTarget || event.ctrlKey || event.metaKey || event.altKey || event.isComposing) return
    const rows = this.result().rows
    const page = Math.max(1, Math.floor(this.height() / ROW_HEIGHT) - 1)
    switch (event.key) {
      case 'j':
      case 'ArrowDown':
        this.move(this.focused() + 1)
        break
      case 'k':
      case 'ArrowUp':
        this.move(this.focused() - 1)
        break
      case 'PageDown':
        this.move(this.focused() + page)
        break
      case 'PageUp':
        this.move(this.focused() - page)
        break
      case 'Home':
        this.move(0)
        break
      case 'End':
        this.move(rows.length - 1)
        break
      case 'Enter':
        this.enter()
        break
      case 'o':
        if (rows.length > 0) void this.router.navigateByUrl(hrefOf(this.detail(rows[this.focused()].item)))
        break
      case 'c':
        if (rows.length > 0) void this.copyName(this.copied(rows[this.focused()].item))
        break
      case 's':
        this.sortNext()
        break
      case 'S':
        this.sortAnnounced(this.sort().sort)
        break
      default:
        return
    }
    event.preventDefault()
  }

  /** `s`: the next sortable column, the way the header buttons (outside the one Tab stop of the grid) sort with a pointer. */
  private sortNext(): void {
    const sortable = this.shown().flatMap((column) => (column.sort === undefined ? [] : [column.sort]))
    if (sortable.length > 0) this.sortAnnounced(sortable[(sortable.indexOf(this.sort().sort) + 1) % sortable.length])
  }

  /** Sorts by a column and says so, for the keyboard, which has no header to look at. */
  private sortAnnounced(column: string): void {
    const query = sortedBy(this.page(), this.query(), column)
    this.commit(query)
    const now = effectiveSort(this.page(), query)
    this.announcer.say(`Sorted by ${now.sort}, ${now.dir === 'asc' ? 'ascending' : 'descending'}`)
  }

  /** Enter selects the focused row; on the row that is already open it moves the focus into the panel. */
  private enter(): void {
    const row = this.result().rows[this.focused()]
    if (row === undefined) return
    const panel = this.panelHost()
    if (panel !== undefined && this.selected()?.id === row.id) panel.focus()
    else this.select(row.id)
  }

  private async copyName(name: string): Promise<void> {
    this.announcer.say((await this.clipboard.copy(name)) ? `Copied ${name}` : 'Copy failed')
  }

  /** Moves the keyboard to a row, which the panel follows while it is open. */
  private move(to: number): void {
    const last = this.result().rows.length - 1
    if (last < 0) return
    const index = Math.min(last, Math.max(0, to))
    this.focusRow(index)
    const element = this.viewport().nativeElement
    this.scrollTo(scrollToRow(index, element.scrollTop, element.clientHeight || this.height()))
    if (this.panelOpen()) this.select(this.result().rows[index].id)
  }

  private focusRow(index: number): void {
    this.focusedId.set(this.result().rows[index].id)
    this.focusedAt.set(index)
  }

  private scrollTo(top: number): void {
    this.viewport().nativeElement.scrollTop = top
    this.scrollTop.set(top)
  }

  private toTop(): void {
    this.scrollTo(0)
  }

  private cancelFrame(): void {
    if (this.frame !== undefined) cancelAnimationFrame(this.frame)
    this.frame = undefined
  }

  private async go(query: ListQuery, replaceUrl = false): Promise<void> {
    this.writing.push(query.q)
    try {
      await this.router.navigate([], { relativeTo: this.route, queryParams: addressChange(query, this.prefix()), queryParamsHandling: 'merge', replaceUrl })
    } finally {
      this.writing.splice(this.writing.indexOf(query.q), 1)
    }
  }
}
