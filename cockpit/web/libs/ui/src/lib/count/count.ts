import { ChangeDetectionStrategy, Component, ElementRef, computed, input, signal, viewChild } from '@angular/core'
import { Params, RouterLink } from '@angular/router'

/** How many entity names a hover card lists before it says "and N more". */
export const CARD_NAME_LIMIT = 8

const CARD_WIDTH = 288
const MARGIN = 8
let nextId = 0

/** Where the hover card sits: below the count, or above it near the screen's foot. */
export interface CardPosition {
  left: number
  top?: number
  bottom?: number
}

/**
 * A number the operator can look into. Hovering or focusing it shows a card
 * that names the entities it counts, or the first of them with the total;
 * clicking it opens the list filtered to exactly those entities. The card has
 * no control in it, and Escape dismisses it.
 *
 * The card is positioned from the count's rectangle, so a table's own
 * scrolling container never clips it.
 */
@Component({
  selector: 'app-count',
  imports: [RouterLink],
  templateUrl: './count.html',
  styleUrl: './count.css',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: {
    '(mouseenter)': 'show()',
    '(focusin)': 'show()',
    '(mouseleave)': 'hide()',
    '(focusout)': 'hide()',
    '(keydown.escape)': 'hide()',
  },
})
export class Count {
  /** The plural noun the number counts, for example "worktrees". */
  readonly label = input.required<string>()
  /**
   * The names of the entities the number counts, in list order. The number is
   * their count, so the number, the hover card and the list the click opens
   * come from one set: the caller builds it with the target page's own filter.
   */
  readonly names = input.required<string[]>()
  /** The list page the click opens, with the filters that select exactly the entities. */
  readonly target = input.required<string>()
  readonly query = input.required<Params>()

  private readonly link = viewChild.required<ElementRef<HTMLElement>>('link')
  protected readonly cardId = `count-card-${nextId++}`
  protected readonly open = signal(false)
  protected readonly position = signal<CardPosition>({ left: MARGIN })
  protected readonly count = computed(() => this.names().length)
  protected readonly shown = computed(() => this.names().slice(0, CARD_NAME_LIMIT))
  protected readonly more = computed(() => this.count() - this.shown().length)

  protected show(): void {
    const rect = this.link().nativeElement.getBoundingClientRect()
    const width = Math.min(CARD_WIDTH, window.innerWidth - 2 * MARGIN)
    const left = Math.max(MARGIN, Math.min(rect.left, window.innerWidth - width - MARGIN))
    this.position.set(
      rect.bottom > window.innerHeight * 0.6
        ? { left, bottom: window.innerHeight - rect.top }
        : { left, top: rect.bottom },
    )
    this.open.set(true)
  }

  protected hide(): void {
    this.open.set(false)
  }
}
