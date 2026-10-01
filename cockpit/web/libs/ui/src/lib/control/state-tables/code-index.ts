import { GLYPH_ALERT, GLYPH_CHECK_CIRCLE, GLYPH_CLOCK, GLYPH_MINUS_CIRCLE, GLYPH_X_CIRCLE } from '../glyphs'
import type { KindTable } from './shared'

/** The `code-index` states of the interface: a colour role, a glyph and a word each. */
export const CODE_INDEX: KindTable = {
  absent: 'not reported',
  entries: {
    fresh: ['ok', GLYPH_CHECK_CIRCLE, 'fresh'],
    stale: ['warn', GLYPH_CLOCK, 'stale'],
    diverged: ['warn', GLYPH_ALERT, 'diverged'],
    pending: ['idle', GLYPH_CLOCK, 'pending'],
    failed: ['bad', GLYPH_X_CIRCLE, 'failed'],
    never: ['idle', GLYPH_MINUS_CIRCLE, 'never indexed'],
  },
}
