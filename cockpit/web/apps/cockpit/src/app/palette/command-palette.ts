import { DOCUMENT } from '@angular/common'
import { ChangeDetectionStrategy, Component, ElementRef, Injector, afterNextRender, computed, effect, inject, signal, viewChild } from '@angular/core'
import { Router } from '@angular/router'
import { FleetStore, hrefOf } from '@cockpit/fleet-data'
import { OverlayFocus, focusMainIfLost } from '../shell/overlay-focus'
import { ShellState } from '../shell/shell-state'
import { Icon } from '../ui/icon'
import { PALETTE_KINDS, PaletteGroup, PaletteResult, resolveResult, searchPalette } from './palette-search'
import { PaletteRecents } from './recents'

/** Scrolls `list` the least that shows `item`, both measured from the list's own top. */
export function scrollIntoList(list: Pick<HTMLElement, 'scrollTop' | 'clientHeight'>, item: Pick<HTMLElement, 'offsetTop' | 'offsetHeight'>): void {
  if (item.offsetTop < list.scrollTop) list.scrollTop = item.offsetTop
  else if (item.offsetTop + item.offsetHeight > list.scrollTop + list.clientHeight) list.scrollTop = item.offsetTop + item.offsetHeight - list.clientHeight
}

/** Keeps the selected option of `list`, when there is one, inside the scrolled list. */
export function revealSelected(list: HTMLElement | undefined): void {
  const item = list?.querySelector<HTMLElement>('[aria-selected="true"]')
  if (list && item) scrollIntoList(list, item)
}

interface NumberedResult extends PaletteResult {
  /** Its place among all the results, for the arrow keys. */
  index: number
}

interface NumberedGroup extends Omit<PaletteGroup, 'results'> {
  results: NumberedResult[]
}

/**
 * The command palette (REQ:command-palette): one input over the fleet document.
 * It opens from the shell state (`/` where no list filter applies, Cmd/Ctrl+K),
 * shows the last opened results while the input is empty, and otherwise the
 * matches grouped by kind. It reads the model already in memory: opening it
 * makes no request and no navigation. Actions in the palette are
 * `cockpit-actions`; this one only navigates.
 */
@Component({
  selector: 'app-command-palette',
  imports: [OverlayFocus, Icon],
  templateUrl: './command-palette.html',
  styleUrl: './command-palette.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
})
export class CommandPalette {
  protected readonly shell = inject(ShellState)
  private readonly store = inject(FleetStore)
  private readonly router = inject(Router)
  private readonly recents = inject(PaletteRecents)
  private readonly doc = inject(DOCUMENT)
  private readonly injector = inject(Injector)
  private readonly list = viewChild<ElementRef<HTMLElement>>('list')

  protected readonly query = signal('')
  /** The highlighted result, by id: a poll that reorders the results does not move it to another one. */
  private readonly highlighted = signal<string | undefined>(undefined)

  /** The groups shown: the matches, or the recents for an empty input. */
  protected readonly groups = computed<NumberedGroup[]>(() => {
    const text = this.query()
    const found = text.trim() === '' ? this.recentGroups() : searchPalette(this.store.model(), text, this.store.now())
    let index = 0
    return found.map((group) => ({ ...group, results: group.results.map((result) => ({ ...result, index: index++ })) }))
  })
  private readonly flat = computed(() => this.groups().flatMap((group) => group.results))
  protected readonly count = computed(() => this.flat().length)
  protected readonly active = computed(() => Math.max(0, this.flat().findIndex((result) => result.id === this.highlighted())))
  /** Said to assistive technology as the results change; the listbox itself holds only options. */
  protected readonly status = computed(() => {
    if (!this.searching()) return ''
    return this.count() === 0 ? 'No results' : `${this.count()} result${this.count() === 1 ? '' : 's'}`
  })
  protected readonly searching = computed(() => this.query().trim() !== '')

  constructor() {
    // However it closed (Esc, a click on the backdrop, a result), it opens empty next time.
    effect(() => {
      if (!this.shell.paletteOpen()) {
        this.query.set('')
        this.highlighted.set(undefined)
      }
    })
  }

  private recentGroups(): PaletteGroup[] {
    // What was opened is read again from the model, so its label and detail are current, and what is gone is dropped.
    const model = this.store.model()
    const results = this.recents
      .items()
      .map((item) => resolveResult(model, item.id))
      .filter((result): result is PaletteResult => result !== undefined)
    return results.length === 0 ? [] : [{ kind: 'task', title: 'Recent', icon: 'history', results, more: 0 }]
  }

  protected iconOf(result: PaletteResult) {
    return (PALETTE_KINDS.find((info) => info.kind === result.kind) as (typeof PALETTE_KINDS)[number]).icon
  }

  protected type(event: Event): void {
    this.query.set((event.target as HTMLInputElement).value)
    this.highlighted.set(undefined)
  }

  protected key(event: KeyboardEvent): void {
    // Enter (or an arrow) that picks or moves inside an input method's candidates is not ours.
    if (event.isComposing || event.keyCode === 229) return
    const count = this.count()
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      if (count === 0) return
      this.highlighted.set(this.flat()[(this.active() + (event.key === 'ArrowDown' ? 1 : count - 1)) % count].id)
      this.reveal()
    } else if (event.key === 'Enter') {
      event.preventDefault()
      const result = this.flat()[this.active()]
      if (result) this.open(result)
    }
  }

  protected hover(id: string): void {
    this.highlighted.set(id)
  }

  protected open(result: NumberedResult): void {
    const plain: PaletteResult = { id: result.id, kind: result.kind, label: result.label, detail: result.detail, link: result.link }
    this.shell.closePalette()
    this.recents.add(plain)
    // The focus was on the page the result leaves (the overlay gave it back there): once the new page is drawn, it goes to its main if nothing has it.
    void this.router.navigateByUrl(hrefOf(result.link)).then(() => afterNextRender(() => focusMainIfLost(this.doc), { injector: this.injector }))
  }

  /** After the highlight moved: keep it inside the scrolled list. */
  private reveal(): void {
    queueMicrotask(() => revealSelected(this.list()?.nativeElement))
  }
}
