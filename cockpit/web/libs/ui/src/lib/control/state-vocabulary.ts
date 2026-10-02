// The one vocabulary of every state the interface shows (REQ:look-typography-and-state-colour),
// see state-tables/shared.ts. This module is the whole of it: `badgeSpec(kind, value)`
// carries every kind's table. A page that shows one kind of badge imports that kind's
// function (`ownerBadgeSpec`, ...) from here and, the tables being separate modules, carries
// only that table.

import { AGENT_ACTIVITY } from './state-tables/agent-activity'
import { AGENT_STATE } from './state-tables/agent-state'
import { CHECKS } from './state-tables/checks'
import { CODE_INDEX } from './state-tables/code-index'
import { LOAD } from './state-tables/load'
import { MERGEABLE } from './state-tables/mergeable'
import { OPERATION } from './state-tables/operation'
import { OWNER } from './state-tables/owner'
import { PR_STATE } from './state-tables/pr-state'
import { ROUTE } from './state-tables/route'
import { KIND_NAME, specFrom } from './state-tables/kinds'
import { BadgeKind, BadgeSpec, KindTable } from './state-tables/shared'
import { taskSpec } from './state-tables/task'

export { KIND_NAME, specFrom }
export { RAW_VALUE_LIMIT, sanitisedValue, unreportedSpec } from './state-tables/shared'
export type { BadgeKind, BadgeSpec, Entry, KindTable, Tone } from './state-tables/shared'
export { taskSpec as taskBadgeSpec } from './state-tables/task'

export const ownerBadgeSpec = (value: string | undefined): BadgeSpec => specFrom(OWNER, value)
export const agentActivityBadgeSpec = (value: string | undefined): BadgeSpec => specFrom(AGENT_ACTIVITY, value)
export const agentStateBadgeSpec = (value: string | undefined): BadgeSpec => specFrom(AGENT_STATE, value)
export const prStateBadgeSpec = (value: string | undefined): BadgeSpec => specFrom(PR_STATE, value)
export const mergeableBadgeSpec = (value: string | undefined): BadgeSpec => specFrom(MERGEABLE, value)
export const codeIndexBadgeSpec = (value: string | undefined): BadgeSpec => specFrom(CODE_INDEX, value)
export const routeBadgeSpec = (value: string | undefined): BadgeSpec => specFrom(ROUTE, value)
export const checksBadgeSpec = (value: string | undefined): BadgeSpec => specFrom(CHECKS, value)
export const operationBadgeSpec = (value: string | undefined): BadgeSpec => specFrom(OPERATION, value)
export const loadBadgeSpec = (value: string | undefined): BadgeSpec => specFrom(LOAD, value)

const TABLES: Record<Exclude<BadgeKind, 'task'>, KindTable> = {
  owner: OWNER,
  'agent-activity': AGENT_ACTIVITY,
  'agent-state': AGENT_STATE,
  'pr-state': PR_STATE,
  mergeable: MERGEABLE,
  'code-index': CODE_INDEX,
  route: ROUTE,
  load: LOAD,
  checks: CHECKS,
  operation: OPERATION,
}

/**
 * The colour role, glyph and word of a state of any kind. An absent value is a grey dashed
 * "not reported"; a value outside the vocabulary shows its sanitised raw value, grey and
 * dashed, and is "not recognised" to assistive technology.
 */
export function badgeSpec(kind: BadgeKind, value: string | undefined): BadgeSpec {
  return kind === 'task' ? taskSpec(value) : specFrom(TABLES[kind], value)
}
