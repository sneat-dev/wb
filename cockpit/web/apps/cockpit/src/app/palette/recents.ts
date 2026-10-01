import { DOCUMENT } from '@angular/common'
import { Injectable, InjectionToken, inject, signal } from '@angular/core'
import { PALETTE_KINDS, PaletteKind, PaletteResult } from './palette-search'

/** The palette remembers this many opened results. */
export const RECENTS_LIMIT = 6

export const RECENTS_KEY = 'wb-cockpit.palette-recents'

/** The browser's local storage, or null where it is not available (a private window, blocked site data). */
export function defaultStorage(win: Window | null): Storage | null {
  try {
    return win?.localStorage ?? null
  } catch {
    return null
  }
}

export const RECENTS_STORAGE = new InjectionToken<Storage | null>('palette recents storage', {
  providedIn: 'root',
  factory: () => defaultStorage(inject(DOCUMENT).defaultView),
})

const KINDS: ReadonlySet<string> = new Set(PALETTE_KINDS.map((info) => info.kind))
/** A path inside the application: no scheme, no host, no dot segments. */
const SAFE_PATH = /^\/(?:(?!\.{1,2}(?:\/|$))[\w~%.-]+(?:\/(?!\.{1,2}(?:\/|$))[\w~%.-]+)*)?$/

const text = (value: unknown, limit: number): value is string => typeof value === 'string' && value.length <= limit

/** What was stored may have been changed by anything that shares the origin: only a well-formed result is kept. */
export function parseRecents(raw: string | null): PaletteResult[] {
  let stored: unknown
  try {
    stored = JSON.parse(raw ?? '[]')
  } catch {
    return []
  }
  if (!Array.isArray(stored)) return []
  const results: PaletteResult[] = []
  for (const item of stored) {
    if (typeof item !== 'object' || item === null) continue
    const { id, kind, label, detail, link } = item as Record<string, unknown>
    if (!text(id, 300) || typeof kind !== 'string' || !KINDS.has(kind) || !text(label, 300) || !text(detail, 300)) continue
    if (typeof link !== 'object' || link === null) continue
    const { path, query } = link as Record<string, unknown>
    if (!text(path, 500) || !SAFE_PATH.test(path) || typeof query !== 'object' || query === null || Array.isArray(query)) continue
    const entries = Object.entries(query)
    if (entries.length > 8 || !entries.every(([name, value]) => text(name, 40) && text(value, 300))) continue
    results.push({ id, kind: kind as PaletteKind, label, detail, link: { path, query: query as Record<string, string> } })
  }
  return results.slice(0, RECENTS_LIMIT)
}

/** The results the operator opened last, newest first, kept in the browser (REQ:command-palette). */
@Injectable({ providedIn: 'root' })
export class PaletteRecents {
  private readonly storage = inject(RECENTS_STORAGE)
  readonly items = signal<PaletteResult[]>(parseRecents(this.read()))

  /** Moves `result` to the front. */
  add(result: PaletteResult): void {
    const next = [result, ...this.items().filter((item) => item.id !== result.id)].slice(0, RECENTS_LIMIT)
    this.items.set(next)
    try {
      this.storage?.setItem(RECENTS_KEY, JSON.stringify(next))
    } catch {
      // A full or blocked store only means the recents are not kept.
    }
  }

  private read(): string | null {
    try {
      return this.storage?.getItem(RECENTS_KEY) ?? null
    } catch {
      return null
    }
  }
}
