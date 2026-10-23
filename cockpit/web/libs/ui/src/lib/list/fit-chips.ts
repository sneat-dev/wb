import { DestroyRef, Directive, ElementRef, afterRenderEffect, inject, input } from '@angular/core'

/** The room "+n" needs, in pixels, when it is not drawn yet. */
const MORE_WIDTH = 36

/**
 * A row of chips that shows as many whole chips as fit and says how many it left out: `<span class="chips"
 * [appFitChips]="key">` holding `.machine` chips and, optionally, the page's own `.more` ("+2"). A chip is never
 * squeezed: the first is always shown (it truncates alone when even it does not fit), the rest are hidden from the
 * end while they do not fit, and a "+n" with the hidden names as its `title` and accessible label takes their place
 * (the page's own `.more` count is folded into it). It measures after each render and when the cell is resized.
 * `key` is anything that changes when the chips do.
 */
@Directive({ selector: '[appFitChips]' })
export class FitChips {
  /** Anything that changes when the chips change, so they are measured again. */
  readonly key = input<unknown>(undefined, { alias: 'appFitChips' })

  private readonly element = inject<ElementRef<HTMLElement>>(ElementRef).nativeElement
  private note: HTMLElement | undefined

  constructor() {
    afterRenderEffect(() => {
      this.key()
      this.fit()
    })
    if (typeof ResizeObserver !== 'undefined') {
      const observer = new ResizeObserver(() => this.fit())
      observer.observe(this.element)
      inject(DestroyRef).onDestroy(() => observer.disconnect())
    }
  }

  private fit(): void {
    const host = this.element
    const chips = [...host.querySelectorAll<HTMLElement>('.machine')]
    const own = host.querySelector<HTMLElement>('.more:not(.fitted)')
    chips.forEach((chip) => chip.removeAttribute('hidden'))
    own?.removeAttribute('hidden')
    const available = host.clientWidth
    const base = Number(own?.textContent?.replace(/\D/g, '') || 0)
    let shown = chips.length
    if (available > 0 && chips.length > 1) {
      const gap = parseFloat(getComputedStyle(host).columnGap) || 0
      const widths = chips.map((chip) => chip.offsetWidth)
      const all = widths.reduce((sum, width) => sum + width + gap, -gap) + (own ? own.offsetWidth + gap : 0)
      if (all > available) {
        shown = 1
        let used = widths[0]
        while (shown < chips.length && used + gap + widths[shown] + gap + MORE_WIDTH <= available) used += gap + widths[shown++]
      }
    }
    const left = chips.slice(shown)
    left.forEach((chip) => chip.setAttribute('hidden', ''))
    if (left.length === 0) {
      this.note?.setAttribute('hidden', '')
      return
    }
    // The page's own "+n" is folded into ours.
    own?.setAttribute('hidden', '')
    const note = (this.note ??= this.make())
    const names = [...left.map((chip) => String(chip.querySelector('.machine-name')?.textContent)), own?.title].filter(Boolean).join(', ')
    note.textContent = `+${left.length + base}`
    note.title = names
    note.setAttribute('aria-label', `${left.length + base} more: ${names}`)
    note.removeAttribute('hidden')
  }

  private make(): HTMLElement {
    const note = this.element.ownerDocument.createElement('span')
    note.className = 'more fitted'
    // Scoped styles of the page that owns the row apply to what it renders: the note carries the same scope attribute.
    for (const name of this.element.getAttributeNames()) if (name.startsWith('_ngcontent')) note.setAttribute(name, '')
    this.element.appendChild(note)
    return note
  }
}
