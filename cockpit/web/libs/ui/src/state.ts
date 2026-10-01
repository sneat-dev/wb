// The first-page part of the control surface: the glyph, the glyphs of the task states and the task state badge, as their
// own entry point. A page whose first paint must stay small imports these from here and loads the rest
// (`@cockpit/ui/control`: the badge of every kind, every glyph, copy buttons, action slots, chips) lazily; through the barrel every
// control would be statically reachable from the page, and the bundler would ship them all with it.
export * from './lib/control/glyph'
export * from './lib/control/glyphs-state'
export * from './lib/control/task-state-badge'
