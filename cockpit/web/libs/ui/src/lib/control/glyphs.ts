// One export per glyph: a page that draws only a few pulls only those into its chunk.
// Circles are written out as paths. 24 px outlines drawn with a 1.75 px stroke, in the style of the shell's icons (apps/cockpit/src/app/ui/icon.ts).

/** The paths of one glyph. */
export type GlyphPaths = readonly string[]

export const GLYPH_CHECK: GlyphPaths = ['m5 12.5 4.5 4.5L19 7.5']

export const GLYPH_CHECK_CIRCLE: GlyphPaths = ['M3 12a9 9 0 1 0 18 0a9 9 0 1 0 -18 0', 'm8 12.5 2.8 2.8L16 9.5']

export const GLYPH_X_CIRCLE: GlyphPaths = ['M3 12a9 9 0 1 0 18 0a9 9 0 1 0 -18 0', 'm9 9 6 6m0-6-6 6']

export const GLYPH_MINUS_CIRCLE: GlyphPaths = ['M3 12a9 9 0 1 0 18 0a9 9 0 1 0 -18 0', 'M8.5 12h7']

export const GLYPH_HELP: GlyphPaths = ['M3 12a9 9 0 1 0 18 0a9 9 0 1 0 -18 0', 'M9.6 9.7a2.5 2.5 0 1 1 3.6 2.2c-.8.4-1.2.9-1.2 1.8', 'M12 16.8v.1']

export const GLYPH_BAN: GlyphPaths = ['M3 12a9 9 0 1 0 18 0a9 9 0 1 0 -18 0', 'm5.7 5.7 12.6 12.6']

export const GLYPH_ALERT: GlyphPaths = ['M12 4 3 19.5h18L12 4Z', 'M12 10v4.5M12 17.2v.1']

export const GLYPH_SHIELD_ALERT: GlyphPaths = ['M12 3 5 6v5.5c0 4.3 3 7.6 7 9.5 4-1.9 7-5.2 7-9.5V6l-7-3Z', 'M12 8.5v4M12 15.6v.1']

export const GLYPH_CLOCK: GlyphPaths = ['M3 12a9 9 0 1 0 18 0a9 9 0 1 0 -18 0', 'M12 7v5l3 2']

export const GLYPH_ACTIVITY: GlyphPaths = ['M3 12h4l3-8 4 16 3-8h4']

export const GLYPH_RADIO: GlyphPaths = ['M10 12a2 2 0 1 0 4 0a2 2 0 1 0 -4 0', 'M7.8 7.8a6 6 0 0 0 0 8.4M16.2 7.8a6 6 0 0 1 0 8.4']

export const GLYPH_ARCHIVE: GlyphPaths = ['M3 5h18v4H3V5Z', 'M5 9v9a1 1 0 0 0 1 1h12a1 1 0 0 0 1-1V9', 'M10 13h4']

export const GLYPH_SERVER: GlyphPaths = ['M4 5.5A1.5 1.5 0 0 1 5.5 4h13A1.5 1.5 0 0 1 20 5.5v4a1.5 1.5 0 0 1-1.5 1.5h-13A1.5 1.5 0 0 1 4 9.5v-4Z', 'M4 14.5A1.5 1.5 0 0 1 5.5 13h13a1.5 1.5 0 0 1 1.5 1.5v4a1.5 1.5 0 0 1-1.5 1.5h-13A1.5 1.5 0 0 1 4 18.5v-4Z', 'M8 7.5h.1M8 16.5h.1']

export const GLYPH_GIT_PULL_REQUEST: GlyphPaths = ['M4 5.5a2 2 0 1 0 4 0a2 2 0 1 0 -4 0', 'M4 18.5a2 2 0 1 0 4 0a2 2 0 1 0 -4 0', 'M16 18.5a2 2 0 1 0 4 0a2 2 0 1 0 -4 0', 'M6 7.5v9M18 16.5V9.5a3 3 0 0 0-3-3h-3', 'm14.5 3.5-2.5 3 2.5 3']

export const GLYPH_GIT_MERGE: GlyphPaths = ['M4 5.5a2 2 0 1 0 4 0a2 2 0 1 0 -4 0', 'M4 18.5a2 2 0 1 0 4 0a2 2 0 1 0 -4 0', 'M16 12.5a2 2 0 1 0 4 0a2 2 0 1 0 -4 0', 'M6 7.5v9', 'M8 6c6 0 10 2.5 10 4.5']

export const GLYPH_PENCIL: GlyphPaths = ['M4 20h4L19 9l-4-4L4 16v4Z', 'm13.5 6.5 4 4']

export const GLYPH_COPY: GlyphPaths = ['M8 8.5A1.5 1.5 0 0 1 9.5 7h9A1.5 1.5 0 0 1 20 8.5v10a1.5 1.5 0 0 1-1.5 1.5h-9A1.5 1.5 0 0 1 8 18.5v-10Z', 'M16 7V5.5A1.5 1.5 0 0 0 14.5 4h-9A1.5 1.5 0 0 0 4 5.5v10A1.5 1.5 0 0 0 5.5 17H8']

export const GLYPH_LOCK: GlyphPaths = ['M6 11.5A1.5 1.5 0 0 1 7.5 10h9a1.5 1.5 0 0 1 1.5 1.5v7a1.5 1.5 0 0 1-1.5 1.5h-9A1.5 1.5 0 0 1 6 18.5v-7Z', 'M8.5 10V7.5a3.5 3.5 0 0 1 7 0V10']

export const GLYPH_TERMINAL: GlyphPaths = ['m5 8 4 4-4 4', 'M12 17h7']

export const GLYPH_MORE: GlyphPaths = ['M5 12h.1M12 12h.1M19 12h.1']

export const GLYPH_CHEVRON_DOWN: GlyphPaths = ['m6 9 6 6 6-6']

export const GLYPH_ARROW_UP: GlyphPaths = ['M12 19V5', 'm6 11 6-6 6 6']

export const GLYPH_ARROW_DOWN: GlyphPaths = ['M12 5v14', 'm6 13 6 6 6-6']

export const GLYPH_X: GlyphPaths = ['M6 6l12 12M18 6 6 18']
