// The glyphs of the task states (the first page of Home draws these and no others, so they are a module of their own:
// a bundler splits chunks by module, and a page that shows a task badge must not carry every glyph of the surface).
// Circles are written out as paths. 24 px outlines drawn with a 1.75 px stroke, in the style of the shell's icons (apps/cockpit/src/app/ui/icon.ts).

/** The paths of one glyph. */
export type GlyphPaths = readonly string[]

export const GLYPH_CHECK_CIRCLE: GlyphPaths = ['M3 12a9 9 0 1 0 18 0a9 9 0 1 0 -18 0', 'm8 12.5 2.8 2.8L16 9.5']

export const GLYPH_X_CIRCLE: GlyphPaths = ['M3 12a9 9 0 1 0 18 0a9 9 0 1 0 -18 0', 'm9 9 6 6m0-6-6 6']

export const GLYPH_MINUS_CIRCLE: GlyphPaths = ['M3 12a9 9 0 1 0 18 0a9 9 0 1 0 -18 0', 'M8.5 12h7']

export const GLYPH_HELP: GlyphPaths = ['M3 12a9 9 0 1 0 18 0a9 9 0 1 0 -18 0', 'M9.6 9.7a2.5 2.5 0 1 1 3.6 2.2c-.8.4-1.2.9-1.2 1.8', 'M12 16.8v.1']

export const GLYPH_BAN: GlyphPaths = ['M3 12a9 9 0 1 0 18 0a9 9 0 1 0 -18 0', 'm5.7 5.7 12.6 12.6']

export const GLYPH_SHIELD_ALERT: GlyphPaths = ['M12 3 5 6v5.5c0 4.3 3 7.6 7 9.5 4-1.9 7-5.2 7-9.5V6l-7-3Z', 'M12 8.5v4M12 15.6v.1']

export const GLYPH_CLOCK: GlyphPaths = ['M3 12a9 9 0 1 0 18 0a9 9 0 1 0 -18 0', 'M12 7v5l3 2']

export const GLYPH_ACTIVITY: GlyphPaths = ['M3 12h4l3-8 4 16 3-8h4']

export const GLYPH_GIT_MERGE: GlyphPaths = ['M4 5.5a2 2 0 1 0 4 0a2 2 0 1 0 -4 0', 'M4 18.5a2 2 0 1 0 4 0a2 2 0 1 0 -4 0', 'M16 12.5a2 2 0 1 0 4 0a2 2 0 1 0 -4 0', 'M6 7.5v9', 'M8 6c6 0 10 2.5 10 4.5']
