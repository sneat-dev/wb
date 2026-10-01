import { GLYPH_ACTIVITY, GLYPH_BAN, GLYPH_CHECK_CIRCLE, GLYPH_CLOCK, GLYPH_X_CIRCLE } from '../glyphs'
import type { KindTable } from './shared'

/** The `operation` states of the interface: a colour role, a glyph and a word each. */
export const OPERATION: KindTable = {
  absent: 'not reported',
  entries: {
    queued: ['idle', GLYPH_CLOCK, 'queued'],
    running: ['ok', GLYPH_ACTIVITY, 'running'],
    succeeded: ['ok', GLYPH_CHECK_CIRCLE, 'succeeded'],
    failed: ['bad', GLYPH_X_CIRCLE, 'failed'],
    cancelled: ['idle', GLYPH_BAN, 'cancelled'],
  },
}
