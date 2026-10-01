import { GLYPH_ACTIVITY, GLYPH_BAN, GLYPH_CHECK_CIRCLE, GLYPH_HELP, GLYPH_MINUS_CIRCLE } from '../glyphs'
import type { KindTable } from './shared'

/** The `agent-activity` states of the interface: a colour role, a glyph and a word each. */
export const AGENT_ACTIVITY: KindTable = {
  absent: 'not reported',
  entries: {
    working: ['ok', GLYPH_ACTIVITY, 'working'],
    blocked: ['warn', GLYPH_BAN, 'blocked'],
    idle: ['idle', GLYPH_MINUS_CIRCLE, 'idle'],
    done: ['idle', GLYPH_CHECK_CIRCLE, 'done'],
    unknown: ['idle', GLYPH_HELP, 'unknown'],
  },
}
