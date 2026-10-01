import { DOCUMENT } from '@angular/common'
import { DestroyRef, Directive, ElementRef, afterNextRender, inject } from '@angular/core'

const FOCUSABLE = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])'

/** Gives the focus back to `element`, when there is an element that can take it. */
export function restoreFocus(element: Element | null): void {
  if (element instanceof HTMLElement) element.focus()
}

/**
 * Keeps the keyboard inside an overlay: on creation the element marked
 * `data-autofocus` (or the overlay itself) takes the focus, Tab and Shift+Tab
 * wrap inside it, and when the overlay goes the focus returns to what had it
 * before. Put it on the element with `role="dialog"`.
 */
@Directive({
  selector: '[appOverlayFocus]',
  host: { tabindex: '-1', '(keydown)': 'trap($event)' },
})
export class OverlayFocus {
  private readonly host = inject<ElementRef<HTMLElement>>(ElementRef).nativeElement
  private readonly previous = inject(DOCUMENT).activeElement

  constructor() {
    afterNextRender(() => (this.host.querySelector<HTMLElement>('[data-autofocus]') ?? this.host).focus())
    inject(DestroyRef).onDestroy(() => restoreFocus(this.previous))
  }

  protected trap(event: KeyboardEvent): void {
    if (event.key !== 'Tab') return
    const items = [...this.host.querySelectorAll<HTMLElement>(FOCUSABLE)]
    if (items.length === 0) {
      event.preventDefault()
      return
    }
    const active = this.host.ownerDocument.activeElement
    const edge = event.shiftKey ? items[0] : items[items.length - 1]
    if (active === edge || active === this.host) {
      event.preventDefault()
      ;(event.shiftKey ? items[items.length - 1] : items[0]).focus()
    }
  }
}
