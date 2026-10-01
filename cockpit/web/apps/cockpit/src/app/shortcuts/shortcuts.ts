import { DOCUMENT } from '@angular/common'
import { Injectable, inject } from '@angular/core'
import { Router } from '@angular/router'
import { PAGE_LINKS } from '../nav'
import { ShellState } from '../shell/shell-state'

/** How long the `g` of a `g x` sequence waits for its second key, in milliseconds. */
export const SEQUENCE_TIMEOUT_MS = 1500

/** What a list page hands over so `/` and Esc reach its filter box (REQ:keyboard-shortcuts). */
export interface FilterTarget {
  element: HTMLElement
  focus(): void
  clear(): void
  /** Whether the box holds no text; an empty focused filter lets Esc through to the side panel. */
  isEmpty?(): boolean
}

/** Closes the page's side panel when it is open and says whether it did. */
export type PanelCloser = () => boolean

const EDITABLE = 'input, textarea, select, [contenteditable=""], [contenteditable="true"], [role="textbox"], [role="combobox"], [role="searchbox"]'

/** Whether keys typed at `target` belong to the element: an input, a text area or editable content. */
export function isEditable(target: EventTarget | null): boolean {
  return target instanceof Element && (target.closest(EDITABLE) !== null || (target as HTMLElement).isContentEditable === true)
}

/**
 * The keyboard of the application, one listener on the document:
 * `g h|t|r|w|a|m` switch tabs, `/` focuses the page's filter or opens the
 * palette, Cmd/Ctrl+K opens the palette anywhere, `?` shows the sheet and Esc
 * closes the palette, the sheet, clears the focused filter or closes the side
 * panel, in that order. A shortcut never fires while the focus is in an input,
 * a text area or editable content.
 *
 * A list page registers its filter box and its side panel here; with none
 * registered `/` opens the palette and Esc has nothing more to close.
 */
@Injectable({ providedIn: 'root' })
export class Shortcuts {
  private readonly router = inject(Router)
  private readonly shell = inject(ShellState)
  private readonly doc = inject(DOCUMENT)
  private filter: FilterTarget | null = null
  private readonly panels: PanelCloser[] = []
  private waiting = false
  private timer: ReturnType<typeof setTimeout> | undefined

  /** Starts listening; the returned function stops. */
  attach(): () => void {
    const listener = (event: KeyboardEvent) => this.handle(event)
    this.doc.addEventListener('keydown', listener)
    return () => {
      this.doc.removeEventListener('keydown', listener)
      this.endSequence()
    }
  }

  /** The page's filter box; the returned function unregisters it. */
  registerFilter(target: FilterTarget): () => void {
    this.filter = target
    return () => {
      if (this.filter === target) this.filter = null
    }
  }

  /** The page's side panel; the returned function unregisters it. The newest panel closes first. */
  registerPanel(close: PanelCloser): () => void {
    this.panels.push(close)
    return () => {
      const index = this.panels.indexOf(close)
      if (index >= 0) this.panels.splice(index, 1)
    }
  }

  handle(event: KeyboardEvent): void {
    // Composing text with an input method: its keys, Esc and Enter included, belong to the composition.
    if (event.isComposing || event.keyCode === 229) return
    const modifier = event.metaKey || event.ctrlKey
    if (modifier && !event.altKey && event.key.toLowerCase() === 'k') {
      event.preventDefault()
      this.shell.openPalette()
      return
    }
    if (event.key === 'Escape') {
      if (!event.defaultPrevented && this.escape()) event.preventDefault()
      return
    }
    if (modifier || event.altKey || isEditable(event.target) || this.shell.modalOpen()) return
    if (this.waiting) {
      this.endSequence()
      const link = PAGE_LINKS.find((candidate) => candidate.key === event.key)
      if (link) {
        event.preventDefault()
        void this.router.navigateByUrl(link.path)
        return
      }
    }
    if (event.key === 'g') {
      this.waiting = true
      this.timer = setTimeout(() => this.endSequence(), SEQUENCE_TIMEOUT_MS)
    } else if (event.key === '/') {
      event.preventDefault()
      if (this.filter) this.filter.focus()
      else this.shell.openPalette()
    } else if (event.key === '?') {
      event.preventDefault()
      this.shell.openSheet()
    }
  }

  /** Esc: the palette, then the sheet, then the focused filter, then the side panel. */
  private escape(): boolean {
    if (this.shell.modalOpen()) {
      this.shell.closePalette()
      this.shell.closeSheet()
      // The overlay leaves the page on the next render; until then its input would still be
      // the target of the next key, which is then typed into it instead of being a shortcut.
      ;(this.doc.activeElement as HTMLElement | null)?.blur()
      return true
    }
    // A focused filter with text in it is cleared first; an empty one leaves Esc to the panel.
    if (this.filter && this.doc.activeElement === this.filter.element && this.filter.isEmpty?.() !== true) {
      this.filter.clear()
      return true
    }
    return [...this.panels].reverse().some((close) => close())
  }

  private endSequence(): void {
    this.waiting = false
    clearTimeout(this.timer)
  }
}
