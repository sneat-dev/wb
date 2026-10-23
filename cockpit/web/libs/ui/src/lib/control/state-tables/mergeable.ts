import { GLYPH_ALERT, GLYPH_ARROW_DOWN, GLYPH_BAN, GLYPH_CHECK_CIRCLE, GLYPH_HELP, GLYPH_PENCIL, GLYPH_X_CIRCLE } from '../glyphs'
import type { KindTable } from './shared'

/** The `mergeable` states of the interface: a colour role, a glyph and a word each. */
export const MERGEABLE: KindTable = {
  absent: 'merge state not reported',
  entries: {
    clean: ['ok', GLYPH_CHECK_CIRCLE, 'mergeable'],
    has_hooks: ['ok', GLYPH_CHECK_CIRCLE, 'mergeable'],
    blocked: ['warn', GLYPH_BAN, 'blocked'],
    dirty: ['bad', GLYPH_X_CIRCLE, 'conflicts'],
    behind: ['warn', GLYPH_ARROW_DOWN, 'behind'],
    unstable: ['warn', GLYPH_ALERT, 'unstable'],
    draft: ['idle', GLYPH_PENCIL, 'draft'],
    unknown: ['idle', GLYPH_HELP, 'merge state unknown'],
  },
}
