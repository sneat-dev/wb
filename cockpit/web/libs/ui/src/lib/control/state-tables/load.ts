import { GLYPH_ACTIVITY, GLYPH_CHECK_CIRCLE, GLYPH_HELP } from '../glyphs'
import type { KindTable } from './shared'

/** The `load` states of the interface: a colour role, a glyph and a word each. */
export const LOAD: KindTable = {
  absent: 'load unknown',
  entries: {
    free: ['ok', GLYPH_CHECK_CIRCLE, 'free'],
    busy: ['warn', GLYPH_ACTIVITY, 'busy'],
    'not-reported': ['idle', GLYPH_HELP, 'load unknown'],
  },
}
