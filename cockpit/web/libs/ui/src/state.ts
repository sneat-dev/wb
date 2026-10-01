// The first-page part of the control surface: the state badge and the glyphs, as their
// own entry point. A page whose first paint must stay small imports these from here and loads the rest
// (`@cockpit/ui/control`: copy buttons, action slots, chips) lazily; through the barrel every control would
// be statically reachable from the page, and the bundler would ship them all with it.
export * from './lib/control/glyph'
export * from './lib/control/state-vocabulary'
export * from './lib/control/state-badge'
export * from './lib/control/glyphs'
