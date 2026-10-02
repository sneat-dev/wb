import { GLYPH_HELP } from '../glyphs-state'
import type { GlyphPaths } from '../glyphs-state'

/**
 * The one vocabulary of every state the interface shows (REQ:look-typography-and-state-colour):
 * green for live or fresh, amber for stale or behind, red for orphaned or failed,
 * grey for idle, cached or not reported. Each entry is a colour role, a glyph
 * and a word, so a state is never conveyed by colour alone.
 *
 * Each kind's table is its own module (`state-tables/<kind>.ts`), so a page that
 * shows one kind of badge, through `ownerBadgeSpec` and its siblings, carries that
 * table and not the others. `badgeSpec(kind, value)` (state-vocabulary.ts) carries all.
 */
export type Tone = 'ok' | 'warn' | 'bad' | 'idle'

export type BadgeKind = 'task' | 'owner' | 'agent-activity' | 'agent-state' | 'pr-state' | 'mergeable' | 'code-index' | 'route' | 'load' | 'checks' | 'operation'

export interface BadgeSpec {
  tone: Tone
  icon: GlyphPaths
  /** The word shown. */
  label: string
  /** Whether the value is outside the vocabulary or not reported; the badge is then drawn dashed. */
  unreported: boolean
  /** The value is not in the vocabulary: `label` is the sanitised raw value, and the accessible name says "not recognised". */
  unrecognised: boolean
}

export type Entry = [tone: Tone, icon: GlyphPaths, label: string]

/** One kind's table: its states, and what an absent value says ("not reported" is a state, never a guess). */
export interface KindTable {
  absent: string
  entries: Record<string, Entry>
}

// Control, invisible and bidirectional characters, which a value from another machine must not smuggle into text.
// eslint-disable-next-line no-control-regex
const INVISIBLE = /[\u0000-\u001f\u007f-\u009f\u061c\u200b-\u200f\u2028\u2029\u202a-\u202e\u2066-\u2069\ufeff]/g

/** The longest raw value shown in a badge. */
export const RAW_VALUE_LIMIT = 32

/** A value from outside the vocabulary as text that is safe to show: no control or invisible characters, one line, short. */
export function sanitisedValue(value: string): string {
  const clean = value.replace(INVISIBLE, ' ').replace(/\s+/g, ' ').trim()
  if (clean === '') return 'unknown'
  return clean.length > RAW_VALUE_LIMIT ? `${clean.slice(0, RAW_VALUE_LIMIT - 1)}…` : clean
}

/** The badge of a value that is absent or outside the vocabulary: grey, dashed, and for a raw value "not recognised". */
export function unreportedSpec(absent: string, value: string | undefined): BadgeSpec {
  if (value === undefined || value === '')
    return {
      tone: 'idle',
      icon: GLYPH_HELP,
      label: absent,
      unreported: true,
      unrecognised: false,
    }
  return {
    tone: 'idle',
    icon: GLYPH_HELP,
    label: sanitisedValue(value),
    unreported: true,
    unrecognised: true,
  }
}
