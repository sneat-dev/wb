import { ChangeDetectionStrategy, Component, ElementRef, computed, inject, signal, viewChild } from '@angular/core'
import { Router } from '@angular/router'
import { FleetStore, hrefOf } from '@cockpit/fleet-data'
import { OverlayFocus } from '../shell/overlay-focus'
import { ShellState } from '../shell/shell-state'
import { Icon } from '../ui/icon'
import { PALETTE_KINDS, PaletteGroup, PaletteResult, searchPalette } from './palette-search'
import { PaletteRecents } from './recents'

/** Scrolls `list` the least that shows `item`, both measured from the list's own top. */
export function scrollIntoList(list: Pick<HTMLElement, 'scrollTop' | 'clientHeight'>, item: Pick<HTMLElement, 'offsetTop' | 'offsetHeight'>): void {
  if (item.offsetTop < list.scrollTop) list.scrollTop = item.offsetTop
  else if (item.offsetTop + item.offsetHeight > list.scrollTop + list.clientHeight) list.scrollTop = item.offsetTop + item.offsetHeight - list.clientHeight
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
  private readonly list = viewChild<ElementRef<HTMLElement>>('list')

  protected readonly query = signal('')
  private readonly highlighted = signal(0)

  /** The groups shown: the matches, or the recents for an empty input. */
  protected readonly groups = computed<NumberedGroup[]>(() => {
    const text = this.query()
    const found = text.trim() === '' ? this.recentGroups() : searchPalette(this.store.model(), text, this.store.now())
    let index = 0
    return found.map((group) => ({ ...group, results: group.results.map((result) => ({ ...result, index: index++ })) }))
  })
  protected readonly count = computed(() => this.groups().reduce((total, group) => total + group.results.length, 0))
  protected readonly active = computed(() => Math.min(this.highlighted(), Math.max(0, this.count() - 1)))
  protected readonly searching = computed(() => this.query().trim() !== '')

  private recentGroups(): PaletteGroup[] {
    const items = this.recents.items()
    if (items.length === 0) return []
    return [{ kind: 'task', title: 'Recent', icon: 'history', results: items, more: 0 }]
  }

  protected iconOf(result: PaletteResult) {
    return (PALETTE_KINDS.find((info) => info.kind === result.kind) as (typeof PALETTE_KINDS)[number]).icon
  }

  protected type(event: Event): void {
    this.query.set((event.target as HTMLInputElement).value)
    this.highlighted.set(0)
  }

  protected key(event: KeyboardEvent): void {
    const count = this.count()
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault()
      if (count === 0) return
      this.highlighted.set((this.active() + (event.key === 'ArrowDown' ? 1 : count - 1)) % count)
      this.reveal()
    } else if (event.key === 'Enter') {
      event.preventDefault()
      const result = this.groups()
        .flatMap((group) => group.results)
        .find((candidate) => candidate.index === this.active())
      if (result) this.open(result)
    }
  }

  protected hover(index: number): void {
    this.highlighted.set(index)
  }

  protected open(result: NumberedResult): void {
    const plain: PaletteResult = { id: result.id, kind: result.kind, label: result.label, detail: result.detail, link: result.link }
    this.shell.closePalette()
    this.query.set('')
    this.highlighted.set(0)
    this.recents.add(plain)
    void this.router.navigateByUrl(hrefOf(result.link))
  }

  /** After the highlight moved: keep it inside the scrolled list. */
  private reveal(): void {
    queueMicrotask(() => {
      const list = this.list()?.nativeElement
      const item = list?.querySelector<HTMLElement>('[aria-selected="true"]')
      if (list && item) scrollIntoList(list, item)
    })
  }
}
