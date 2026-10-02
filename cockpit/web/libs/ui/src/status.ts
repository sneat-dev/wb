// The shared status region of the copy buttons, as its own entry point: the shell renders it on the first page, and
// through the control barrel (`@cockpit/ui/control`) every control would be statically reachable from it.
export * from './lib/control/status-announcer'
