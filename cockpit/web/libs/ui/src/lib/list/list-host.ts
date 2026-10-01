import { InjectionToken, Provider, Type } from '@angular/core'

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
 * satisfies it, and each page provides it (`provideListShortcuts`), so this
 * library never imports the application. With none provided a list still works
 * and the shell's keys just do not reach it.
 */
export interface ListShortcuts {
  registerFilter(target: ListFilterTarget): () => void
  /** The closer returns whether it closed a panel. */
  registerPanel(close: () => boolean): () => void
}

export const LIST_SHORTCUTS = new InjectionToken<ListShortcuts>('list shortcuts', {
  providedIn: 'root',
  factory: () => ({ registerFilter: () => () => undefined, registerPanel: () => () => undefined }),
})

/** What a list page lists in its `providers`: `provideListShortcuts(Shortcuts)`, with the shell's service. */
export function provideListShortcuts(shortcuts: Type<ListShortcuts>): Provider {
  return { provide: LIST_SHORTCUTS, useExisting: shortcuts }
}
