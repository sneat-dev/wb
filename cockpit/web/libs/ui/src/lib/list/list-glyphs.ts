import { GlyphPaths } from '../control/glyphs'

// The glyph the list draws that the control surface does not have (search), and the open-page glyph; the rest are the control surface's own.
export const GLYPH_SEARCH: GlyphPaths = ['M4 11a7 7 0 1 0 14 0a7 7 0 1 0 -14 0', 'm20 20-3.5-3.5']
/** "Open the page" is an arrow into the application: the external-link glyph is the host's, and `</>` is the code browser's, so a row never carries three look-alikes. */
export const GLYPH_OPEN: GlyphPaths = ['M5 12h14', 'm13 6 6 6-6 6']
