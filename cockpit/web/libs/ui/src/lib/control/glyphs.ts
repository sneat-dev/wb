// One export per glyph: a page that draws only a few pulls only those into its chunk.
// Circles are written out as paths. 24 px outlines drawn with a 1.75 px stroke, in the style of the shell's icons (apps/cockpit/src/app/ui/icon.ts).

import type { GlyphPaths } from './glyphs-state'

export type { GlyphPaths } from './glyphs-state'
export * from './glyphs-state'

export const GLYPH_CHECK: GlyphPaths = ['m5 12.5 4.5 4.5L19 7.5']

export const GLYPH_ALERT: GlyphPaths = ['M12 4 3 19.5h18L12 4Z', 'M12 10v4.5M12 17.2v.1']

export const GLYPH_RADIO: GlyphPaths = ['M10 12a2 2 0 1 0 4 0a2 2 0 1 0 -4 0', 'M7.8 7.8a6 6 0 0 0 0 8.4M16.2 7.8a6 6 0 0 1 0 8.4']

export const GLYPH_ARCHIVE: GlyphPaths = ['M3 5h18v4H3V5Z', 'M5 9v9a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V9', 'M10 13h4']

export const GLYPH_SERVER: GlyphPaths = ['M4 5.5A1.5 1.5 0 0 1 5.5 4h13A1.5 1.5 0 0 1 20 5.5v4a1.5 1.5 0 0 1-1.5 1.5h-13A1.5 1.5 0 0 1 4 9.5v-4Z', 'M4 14.5A1.5 1.5 0 0 1 5.5 13h13a1.5 1.5 0 0 1 1.5 1.5v4a1.5 1.5 0 0 1-1.5 1.5h-13A1.5 1.5 0 0 1 4 18.5v-4Z', 'M8 7.5h.1M8 16.5h.1']

export const GLYPH_GIT_PULL_REQUEST: GlyphPaths = ['M4 5.5a2 2 0 1 0 4 0a2 2 0 1 0 -4 0', 'M4 18.5a2 2 0 1 0 4 0a2 2 0 1 0 -4 0', 'M16 18.5a2 2 0 1 0 4 0a2 2 0 1 0 -4 0', 'M6 7.5v9M18 16.5V9.5a3 3 0 0 0-3-3h-3', 'm14.5 3.5-2.5 3 2.5 3']

export const GLYPH_PENCIL: GlyphPaths = ['M4 20h4L19 9l-4-4L4 16v4Z', 'm13.5 6.5 4 4']

export const GLYPH_COPY: GlyphPaths = ['M8 8.5A1.5 1.5 0 0 1 9.5 7h9A1.5 1.5 0 0 1 20 8.5v10a1.5 1.5 0 0 1-1.5 1.5h-9A1.5 1.5 0 0 1 8 18.5v-10Z', 'M16 7V5.5A1.5 1.5 0 0 0 14.5 4h-9A1.5 1.5 0 0 0 4 5.5v10A1.5 1.5 0 0 0 5.5 17H8']

export const GLYPH_LOCK: GlyphPaths = ['M6 11.5A1.5 1.5 0 0 1 7.5 10h9a1.5 1.5 0 0 1 1.5 1.5v7a1.5 1.5 0 0 1-1.5 1.5h-9A1.5 1.5 0 0 1 6 18.5v-7Z', 'M8.5 10V7.5a3.5 3.5 0 0 1 7 0V10']

export const GLYPH_TERMINAL: GlyphPaths = ['m5 8 4 4-4 4', 'M12 17h7']

export const GLYPH_MORE: GlyphPaths = ['M5 12h.1M12 12h.1M19 12h.1']

export const GLYPH_CHEVRON_DOWN: GlyphPaths = ['m6 9 6 6 6-6']

export const GLYPH_ARROW_UP: GlyphPaths = ['M12 19V5', 'm6 11 6-6 6 6']

export const GLYPH_ARROW_DOWN: GlyphPaths = ['M12 5v14', 'm6 13 6 6 6-6']

export const GLYPH_X: GlyphPaths = ['M6 6l12 12M18 6 6 18']

export const GLYPH_CODE: GlyphPaths = ['m8 8-4 4 4 4', 'm16 8 4 4-4 4', 'm13.5 5-3 14']

export const GLYPH_EXTERNAL_LINK: GlyphPaths = ['M14 4h6v6', 'M20 4 10 14', 'M18 14v4a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h4']
