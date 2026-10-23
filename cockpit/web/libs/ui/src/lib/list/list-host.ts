import { InjectionToken } from '@angular/core'

/** What a list hands the shell so `/` and Esc reach its filter box (the shell's `FilterTarget`). */
export interface ListFilterTarget {
  element: HTMLElement
  focus(): void
  clear(): void
  /** Whether the box holds no text; an empty focused filter lets Esc through to the side panel. */
  isEmpty(): boolean
}

/**
 * The part of the shell's keyboard a list uses: the shell's `Shortcuts` service
 * satisfies it, and the application provides it once, so this library never
 * imports the application.
 */
export interface ListShortcuts {
  registerFilter(target: ListFilterTarget): () => void
  /** The closer returns whether it closed a panel. */
  registerPanel(close: () => boolean): () => void
}

/** Provided once, by the application (`{ provide: LIST_SHORTCUTS, useExisting: Shortcuts }`): a list cannot be created without it. */
export const LIST_SHORTCUTS = new InjectionToken<ListShortcuts>('list shortcuts')
