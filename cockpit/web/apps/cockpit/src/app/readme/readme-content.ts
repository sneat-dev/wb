import { ChangeDetectionStrategy, Component, ElementRef, ViewEncapsulation, computed, effect, inject, input, signal, viewChild } from '@angular/core'
import { DOCUMENT } from '@angular/common'
import { MAX_PARSED_CHARACTERS } from './readme-limit'

/**
 * Untrusted Markdown, shown as DOM built node by node (see markdown-dom.ts): no
 * innerHTML, no sanitizer, nothing from the source becomes an element or an
 * attribute the allow-list does not name. A link to a heading of the same README
 * scrolls to it instead of navigating the application.
 *
 * The parser and the renderer (`markdown-dom.ts`, with `marked`, most of the size of this feature) are fetched when a
 * README is first shown: this component is small and the first page of the application does not carry the rest.
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

  constructor() {
    effect((onCleanup) => {
      const source = this.source()
      const root = this.root().nativeElement
      const plain = this.plain()
      // A source replaced while the renderer was on its way is dropped.
      let stale = false
      onCleanup(() => (stale = true))
      void import('./markdown-dom').then(({ renderMarkdown, renderPlain }) => {
        if (stale) return
        try {
          root.replaceChildren(plain ? renderPlain(source, this.document) : renderMarkdown(source, this.document))
          this.failed.set(false)
        } catch {
          // A document the parser cannot take (it is untrusted, and may be built to exhaust it) shows nothing of itself.
          root.replaceChildren()
          this.failed.set(true)
        }
      })
    })
  }

  protected jump(event: MouseEvent): void {
    const link = (event.target as Element).closest('a.jump')
    if (!link) return
    event.preventDefault()
    const id = (link.getAttribute('href') as string).slice(1)
    this.root().nativeElement.querySelector(`[id="${id}"]`)?.scrollIntoView()
  }
}
