import { ChangeDetectionStrategy, Component, ElementRef, ViewEncapsulation, computed, effect, inject, input, signal, viewChild } from '@angular/core'
import { DOCUMENT } from '@angular/common'
import { MAX_PARSED_CHARACTERS, renderMarkdown, renderPlain } from './markdown-dom'

/**
 * Untrusted Markdown, shown as DOM built node by node (see markdown-dom.ts): no
 * innerHTML, no sanitizer, nothing from the source becomes an element or an
 * attribute the allow-list does not name. A link to a heading of the same README
 * scrolls to it instead of navigating the application.
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
    effect(() => {
      const source = this.source()
      const root = this.root().nativeElement
      try {
        root.replaceChildren(this.plain() ? renderPlain(source, this.document) : renderMarkdown(source, this.document))
        this.failed.set(false)
      } catch {
        // A document the parser cannot take (it is untrusted, and may be built to exhaust it) shows nothing of itself.
        root.replaceChildren()
        this.failed.set(true)
      }
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
