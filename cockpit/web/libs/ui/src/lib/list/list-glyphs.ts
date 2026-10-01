import type { GlyphPaths } from '../control/glyphs'

// The two glyphs the list draws that the control surface does not have; the rest are the control surface's own.
export const GLYPH_SEARCH: GlyphPaths = ['M4 11a7 7 0 1 0 14 0a7 7 0 1 0 -14 0', 'm20 20-3.5-3.5']
export const GLYPH_OPEN: GlyphPaths = ['M14 4h6v6', 'M20 4 10 14', 'M18 14v4a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h4']
