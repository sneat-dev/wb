import { GLYPH_ACTIVITY, GLYPH_ALERT, GLYPH_CHECK_CIRCLE, GLYPH_CLOCK, GLYPH_MINUS_CIRCLE, GLYPH_RADIO, GLYPH_X_CIRCLE } from '../glyphs'
import type { KindTable } from './shared'

/** The `agent-state` states of the interface: a colour role, a glyph and a word each. */
export const AGENT_STATE: KindTable = {
  absent: 'not reported',
  entries: {
    running: ['ok', GLYPH_ACTIVITY, 'running'],
    live: ['ok', GLYPH_RADIO, 'live'],
    parked: ['idle', GLYPH_MINUS_CIRCLE, 'parked'],
    completed: ['idle', GLYPH_CHECK_CIRCLE, 'completed'],
    failed: ['bad', GLYPH_X_CIRCLE, 'failed'],
    timeout: ['bad', GLYPH_CLOCK, 'timed out'],
    abandoned: ['warn', GLYPH_ALERT, 'abandoned'],
  },
}
