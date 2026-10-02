import { GLYPH_GIT_MERGE, GLYPH_GIT_PULL_REQUEST, GLYPH_PENCIL, GLYPH_X_CIRCLE } from '../glyphs'
import type { KindTable } from './shared'

/** The `pr-state` states of the interface: a colour role, a glyph and a word each. */
export const PR_STATE: KindTable = {
  absent: 'not reported',
  entries: {
    open: ['ok', GLYPH_GIT_PULL_REQUEST, 'open'],
    draft: ['idle', GLYPH_PENCIL, 'draft'],
    merged: ['idle', GLYPH_GIT_MERGE, 'merged'],
    closed: ['idle', GLYPH_X_CIRCLE, 'closed'],
  },
}
