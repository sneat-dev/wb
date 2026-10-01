import { TASK_STATES, taskStateInfo, type TaskStateId } from '@cockpit/fleet-data'
import * as G from './glyphs'
import type { GlyphPaths } from './glyphs'

/**
 * The one table of every state the interface shows (REQ:look-typography-and-state-colour):
 * green for live or fresh, amber for stale or behind, red for orphaned or failed,
 * grey for idle, cached or not reported. Each entry is a colour role, a glyph
 * and a word, so a state is never conveyed by colour alone.
 */
export type Tone = 'ok' | 'warn' | 'bad' | 'idle'

export type BadgeKind = 'task' | 'owner' | 'agent-activity' | 'agent-state' | 'pr-state' | 'mergeable' | 'code-index' | 'route' | 'load' | 'checks' | 'operation'

export interface BadgeSpec {
  tone: Tone
  icon: GlyphPaths
  /** The word shown. */
  label: string
  /** Whether the value is outside the vocabulary or not reported; the badge is then drawn dashed. */
  unreported: boolean
  /** The value is not in the vocabulary: `label` is the sanitised raw value, and the accessible name says "not recognised". */
  unrecognised: boolean
}

type Entry = [tone: Tone, icon: GlyphPaths, label: string]

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

const TASK: Record<TaskStateId, Entry> = {
  'at-risk': ['bad', G.GLYPH_SHIELD_ALERT, ''],
  'checks-failed': ['bad', G.GLYPH_X_CIRCLE, ''],
  blocked: ['warn', G.GLYPH_BAN, ''],
  ready: ['ok', G.GLYPH_CHECK_CIRCLE, ''],
  'not-ready': ['warn', G.GLYPH_CLOCK, ''],
  working: ['ok', G.GLYPH_ACTIVITY, ''],
  landed: ['idle', G.GLYPH_GIT_MERGE, ''],
  idle: ['idle', G.GLYPH_MINUS_CIRCLE, ''],
  'not-reported': ['idle', G.GLYPH_HELP, ''],
}

const TABLES: Record<Exclude<BadgeKind, 'task'>, Record<string, Entry>> = {
  owner: {
    active: ['ok', G.GLYPH_ACTIVITY, 'active'],
    idle: ['idle', G.GLYPH_MINUS_CIRCLE, 'idle'],
    orphaned: ['bad', G.GLYPH_ALERT, 'orphaned'],
    unknown: ['idle', G.GLYPH_HELP, 'unknown'],
  },
  'agent-activity': {
    working: ['ok', G.GLYPH_ACTIVITY, 'working'],
    blocked: ['warn', G.GLYPH_BAN, 'blocked'],
    idle: ['idle', G.GLYPH_MINUS_CIRCLE, 'idle'],
    done: ['idle', G.GLYPH_CHECK_CIRCLE, 'done'],
    unknown: ['idle', G.GLYPH_HELP, 'unknown'],
  },
  'agent-state': {
    running: ['ok', G.GLYPH_ACTIVITY, 'running'],
    live: ['ok', G.GLYPH_RADIO, 'live'],
    parked: ['idle', G.GLYPH_MINUS_CIRCLE, 'parked'],
    completed: ['idle', G.GLYPH_CHECK_CIRCLE, 'completed'],
    failed: ['bad', G.GLYPH_X_CIRCLE, 'failed'],
    timeout: ['bad', G.GLYPH_CLOCK, 'timed out'],
    abandoned: ['warn', G.GLYPH_ALERT, 'abandoned'],
  },
  'pr-state': {
    open: ['ok', G.GLYPH_GIT_PULL_REQUEST, 'open'],
    draft: ['idle', G.GLYPH_PENCIL, 'draft'],
    merged: ['idle', G.GLYPH_GIT_MERGE, 'merged'],
    closed: ['idle', G.GLYPH_X_CIRCLE, 'closed'],
  },
  mergeable: {
    clean: ['ok', G.GLYPH_CHECK_CIRCLE, 'mergeable'],
    has_hooks: ['ok', G.GLYPH_CHECK_CIRCLE, 'mergeable'],
    blocked: ['warn', G.GLYPH_BAN, 'blocked'],
    dirty: ['bad', G.GLYPH_X_CIRCLE, 'conflicts'],
    behind: ['warn', G.GLYPH_ARROW_DOWN, 'behind'],
    unstable: ['warn', G.GLYPH_ALERT, 'unstable'],
    draft: ['idle', G.GLYPH_PENCIL, 'draft'],
    unknown: ['idle', G.GLYPH_HELP, 'merge state unknown'],
  },
  'code-index': {
    fresh: ['ok', G.GLYPH_CHECK_CIRCLE, 'fresh'],
    stale: ['warn', G.GLYPH_CLOCK, 'stale'],
    diverged: ['warn', G.GLYPH_ALERT, 'diverged'],
    pending: ['idle', G.GLYPH_CLOCK, 'pending'],
    failed: ['bad', G.GLYPH_X_CIRCLE, 'failed'],
    never: ['idle', G.GLYPH_MINUS_CIRCLE, 'never indexed'],
  },
  route: {
    local: ['ok', G.GLYPH_SERVER, 'local'],
    live: ['ok', G.GLYPH_RADIO, 'live'],
    'live-remote': ['ok', G.GLYPH_RADIO, 'live'],
    cached: ['idle', G.GLYPH_ARCHIVE, 'cached'],
    stale: ['warn', G.GLYPH_CLOCK, 'stale'],
    none: ['idle', G.GLYPH_MINUS_CIRCLE, 'no data'],
  },
  checks: {
    passed: ['ok', G.GLYPH_CHECK_CIRCLE, 'passing'],
    failed: ['bad', G.GLYPH_X_CIRCLE, 'failing'],
    pending: ['warn', G.GLYPH_CLOCK, 'pending'],
    unknown: ['idle', G.GLYPH_HELP, 'checks not reported'],
  },
  operation: {
    queued: ['idle', G.GLYPH_CLOCK, 'queued'],
    running: ['ok', G.GLYPH_ACTIVITY, 'running'],
    succeeded: ['ok', G.GLYPH_CHECK_CIRCLE, 'succeeded'],
    failed: ['bad', G.GLYPH_X_CIRCLE, 'failed'],
    cancelled: ['idle', G.GLYPH_BAN, 'cancelled'],
  },
  load: {
    free: ['ok', G.GLYPH_CHECK_CIRCLE, 'free'],
    busy: ['warn', G.GLYPH_ACTIVITY, 'busy'],
    'not-reported': ['idle', G.GLYPH_HELP, 'load unknown'],
  },
}

/** What a value that is absent says, by kind: "not reported" is a state, never a guess. */
const ABSENT: Record<BadgeKind, string> = {
  task: 'state not reported',
  owner: 'owner not reported',
  'agent-activity': 'not reported',
  'agent-state': 'not reported',
  'pr-state': 'not reported',
  mergeable: 'merge state not reported',
  'code-index': 'not reported',
  route: 'not reported',
  load: 'load unknown',
  checks: 'checks not reported',
  operation: 'not reported',
}

// Control, invisible and bidirectional characters, which a value from another machine must not smuggle into text.
// eslint-disable-next-line no-control-regex
const INVISIBLE = /[\u0000-\u001f\u007f-\u009f\u061c\u200b-\u200f\u2028\u2029\u202a-\u202e\u2066-\u2069\ufeff]/g

/** The longest raw value shown in a badge. */
export const RAW_VALUE_LIMIT = 32

/** A value from outside the vocabulary as text that is safe to show: no control or invisible characters, one line, short. */
export function sanitisedValue(value: string): string {
  const clean = value.replace(INVISIBLE, ' ').replace(/\s+/g, ' ').trim()
  if (clean === '') return 'unknown'
  return clean.length > RAW_VALUE_LIMIT ? `${clean.slice(0, RAW_VALUE_LIMIT - 1)}…` : clean
}

/**
 * The colour role, glyph and word of a state. An absent value is a grey dashed
 * "not reported". A value outside the vocabulary (including an inherited key such as
 * `constructor`, which is looked up as an own property only) shows its sanitised
 * raw value, grey and dashed, and is "not recognised" to assistive technology.
 */
export function badgeSpec(kind: BadgeKind, value: string | undefined): BadgeSpec {
  if (kind === 'task') {
    if (value === undefined || !TASK_STATES.some((info) => info.id === value)) return unreported(kind, value)
    const [tone, icon] = TASK[value as TaskStateId]
    return { tone, icon, label: taskStateInfo(value as TaskStateId).label, unreported: value === 'not-reported', unrecognised: false }
  }
  const table = TABLES[kind]
  const entry = value !== undefined && Object.prototype.hasOwnProperty.call(table, value) ? table[value] : undefined
  if (entry === undefined) return unreported(kind, value)
  return { tone: entry[0], icon: entry[1], label: entry[2], unreported: value === 'unknown' || value === 'not-reported', unrecognised: false }
}

function unreported(kind: BadgeKind, value: string | undefined): BadgeSpec {
  if (value === undefined || value === '') return { tone: 'idle', icon: G.GLYPH_HELP, label: ABSENT[kind], unreported: true, unrecognised: false }
  return { tone: 'idle', icon: G.GLYPH_HELP, label: sanitisedValue(value), unreported: true, unrecognised: true }
}
