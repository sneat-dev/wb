import { type BadgeKind, type BadgeSpec, type KindTable, unreportedSpec } from './shared'

// Kept out of shared.ts so that a page which shows one kind of badge (Home's first page shows the task kind) does not carry the
// name of every kind and the lookup of a table: a bundler splits chunks by module.

/** What each kind of state is called, said ahead of the value for assistive technology. */
export const KIND_NAME: Record<BadgeKind, string> = {
  task: 'Task state',
  owner: 'Owner state',
  'agent-activity': 'Agent activity',
  'agent-state': 'Agent state',
  'pr-state': 'Pull request state',
  mergeable: 'Merge state',
  'code-index': 'Code index',
  route: 'Route',
  load: 'Load',
  checks: 'Checks',
  operation: 'Operation',
}

/**
 * The colour role, glyph and word of a state from one kind's table. An absent
 * value is a grey dashed "not reported". A value outside the vocabulary (including
 * an inherited key such as `constructor`, which is looked up as an own property
 * only) shows its sanitised raw value, grey and dashed, and is "not recognised" to
 * assistive technology.
 */
export function specFrom(table: KindTable, value: string | undefined): BadgeSpec {
  const entry = value !== undefined && Object.prototype.hasOwnProperty.call(table.entries, value) ? table.entries[value] : undefined
  if (entry === undefined) return unreportedSpec(table.absent, value)
  return {
    tone: entry[0],
    icon: entry[1],
    label: entry[2],
    unreported: value === 'unknown' || value === 'not-reported',
    unrecognised: false,
  }
}
