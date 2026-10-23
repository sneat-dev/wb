import { ChangeDetectionStrategy, Component, ElementRef, ViewEncapsulation, computed, effect, inject, input, signal, viewChild } from '@angular/core'
import { DOCUMENT } from '@angular/common'
import type { MarkdownRenderer } from './markdown-dom'
import { MAX_PARSED_CHARACTERS } from './readme-limit'

/** Where a README drawn in parts stands: how many parts there are, how many are on the page, and whether the rest is being drawn. */
interface Parts {
  total: number
  drawn: number
  running: boolean
}

/** Gives the browser a turn: a part is drawn after the page has painted and answered whatever was waiting. */
const pause = (): Promise<void> => new Promise((resolve) => setTimeout(resolve))

/**
 * Untrusted Markdown, shown as DOM built node by node (see markdown-dom.ts): no
 * innerHTML, no sanitizer, nothing from the source becomes an element or an
 * attribute the allow-list does not name. A link to a heading of the same README
 * scrolls to it instead of navigating the application.
 *
 * The parser and the renderer (`markdown-dom.ts`, with `marked`, most of the size of this feature) are fetched when a
 * README is first shown: this component is small and the first page of the application does not carry the rest.
 *
 * A README is lexed in parts of about 64 Ki characters. A short one is one part. A longer one shows its first part at
 * once and a "Show the rest" button, which draws the other parts one at a time with a pause between them, so one lex of
 * the whole text never holds the page. Beyond 256 Ki characters it is shown as plain text.
 *
 * Its styles are not encapsulated because the nodes are not created by a
 * template; every rule is scoped by the `readme-content` class.
 */
@Component({
  selector: 'app-readme-content',
  templateUrl: './readme-content.html',
  styleUrl: './readme-content.css',
  encapsulation: ViewEncapsulation.None,
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { '(click)': 'jump($event)' },
})
export class ReadmeContent {
  readonly source = input.required<string>()

  private readonly document = inject(DOCUMENT)
  private readonly root = viewChild.required<ElementRef<HTMLElement>>('root')
  protected readonly failed = signal(false)
  /** A source over the parse budget is shown as one block of text, unparsed. */
  protected readonly plain = computed(() => this.source().length > MAX_PARSED_CHARACTERS)
  /** Set while parts are still to be drawn, or being drawn. */
  protected readonly parts = signal<Parts | undefined>(undefined)
  /** What draws the rest of the parts, for the source on show. */
  private rest: (() => Promise<void>) | undefined

  constructor() {
    effect((onCleanup) => {
      const source = this.source()
      const root = this.root().nativeElement
      const plain = this.plain()
      // A source replaced while the renderer was on its way, or between two parts, is dropped.
      let stale = false
      onCleanup(() => (stale = true))
      this.rest = undefined
      this.parts.set(undefined)
      void import('./markdown-dom').then(({ MarkdownRenderer, renderPlain, splitSource }) => {
        if (stale) return
        try {
          if (plain) {
            root.replaceChildren(renderPlain(source, this.document))
          } else {
            const parts = splitSource(source)
            const renderer: MarkdownRenderer = new MarkdownRenderer(this.document)
            root.replaceChildren(renderer.render(parts[0]))
            if (parts.length > 1) {
              this.parts.set({ total: parts.length, drawn: 1, running: false })
              this.rest = () => this.drawRest(renderer, parts, root, () => stale)
            }
          }
          this.failed.set(false)
        } catch {
          // A document the parser cannot take (it is untrusted, and may be built to exhaust it) shows nothing of itself.
          root.replaceChildren()
          this.parts.set(undefined)
          this.failed.set(true)
        }
      })
    })
  }

  /** Draws the parts not yet on the page, one at a time with a pause before each. */
  protected async showRest(): Promise<void> {
    // The button is there only while there is a rest to draw.
    await (this.rest as () => Promise<void>)()
  }

  private async drawRest(renderer: MarkdownRenderer, parts: readonly string[], root: HTMLElement, stale: () => boolean): Promise<void> {
    // There are parts to draw while there is a rest; a second call while the first is drawing leaves it to the first.
    const state = this.parts() as Parts
    if (state.running) return
    this.parts.set({ ...state, running: true })
    for (let index = state.drawn; index < parts.length; index++) {
      await pause()
      if (stale()) return
      try {
        root.appendChild(renderer.render(parts[index]))
      } catch {
        // A part the parser cannot take stops the drawing there: what is drawn stays, and the notice says the rest is not.
        this.parts.set(undefined)
        this.failed.set(true)
        return
      }
      this.parts.set({ total: parts.length, drawn: index + 1, running: true })
    }
    this.parts.set(undefined)
    this.rest = undefined
  }

  protected jump(event: MouseEvent): void {
    const link = (event.target as Element).closest('a.jump')
    if (!link) return
    event.preventDefault()
    const id = (link.getAttribute('href') as string).slice(1)
    this.root().nativeElement.querySelector(`[id="${id}"]`)?.scrollIntoView()
  }
}
