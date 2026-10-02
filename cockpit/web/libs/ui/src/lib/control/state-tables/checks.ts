import { GLYPH_CHECK_CIRCLE, GLYPH_CLOCK, GLYPH_HELP, GLYPH_X_CIRCLE } from '../glyphs'
import type { KindTable } from './shared'

/** The `checks` states of the interface: a colour role, a glyph and a word each. */
export const CHECKS: KindTable = {
  absent: 'checks not reported',
  entries: {
    passed: ['ok', GLYPH_CHECK_CIRCLE, 'passing'],
    failed: ['bad', GLYPH_X_CIRCLE, 'failing'],
    pending: ['warn', GLYPH_CLOCK, 'pending'],
    unknown: ['idle', GLYPH_HELP, 'checks not reported'],
  },
}
