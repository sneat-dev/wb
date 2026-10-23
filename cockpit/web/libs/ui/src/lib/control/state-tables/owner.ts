import { GLYPH_ACTIVITY, GLYPH_ALERT, GLYPH_HELP, GLYPH_MINUS_CIRCLE } from '../glyphs'
import type { KindTable } from './shared'

/** The `owner` states of the interface: a colour role, a glyph and a word each. */
export const OWNER: KindTable = {
  absent: 'owner not reported',
  entries: {
    active: ['ok', GLYPH_ACTIVITY, 'active'],
    idle: ['idle', GLYPH_MINUS_CIRCLE, 'idle'],
    orphaned: ['bad', GLYPH_ALERT, 'orphaned'],
    unknown: ['idle', GLYPH_HELP, 'unknown'],
  },
}
