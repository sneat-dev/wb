// `@cockpit/fleet-data/panel`: the per-entity panels of REQ:side-panel, built from
// a model (`buildWorktreePanel(model, id)` and its siblings). Imported lazily by
// the pages that open a panel; the first page does not carry it.
export * from './lib/entity-views'
