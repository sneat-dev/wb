import { GLYPH_EXTERNAL_LINK, GlyphPaths } from '../control/glyphs'

// The glyph the list draws that the control surface does not have (search), and the open-page glyph; the rest are the control surface's own.
export const GLYPH_SEARCH: GlyphPaths = ['M4 11a7 7 0 1 0 14 0a7 7 0 1 0 -14 0', 'm20 20-3.5-3.5']
/** The control surface's external-link glyph, under the name the list has always used. */
export const GLYPH_OPEN: GlyphPaths = GLYPH_EXTERNAL_LINK
