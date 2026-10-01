import { GLYPH_ARCHIVE, GLYPH_CLOCK, GLYPH_MINUS_CIRCLE, GLYPH_RADIO, GLYPH_SERVER } from '../glyphs'
import type { KindTable } from './shared'

/** The `route` states of the interface: a colour role, a glyph and a word each. */
export const ROUTE: KindTable = {
  absent: 'not reported',
  entries: {
    local: ['ok', GLYPH_SERVER, 'local'],
    live: ['ok', GLYPH_RADIO, 'live'],
    'live-remote': ['ok', GLYPH_RADIO, 'live'],
    cached: ['idle', GLYPH_ARCHIVE, 'cached'],
    stale: ['warn', GLYPH_CLOCK, 'stale'],
    none: ['idle', GLYPH_MINUS_CIRCLE, 'no data'],
  },
}
