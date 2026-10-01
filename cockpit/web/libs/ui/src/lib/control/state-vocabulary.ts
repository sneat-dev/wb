import { TASK_STATES, taskStateInfo, type TaskStateId } from '@cockpit/fleet-data'
import type { GlyphName } from './glyph'

/**
 * The one table of every state the interface shows (REQ:look-typography-and-state-colour):
 * green for live or fresh, amber for stale or behind, red for orphaned or failed,
 * grey for idle, cached or not reported. Each entry is a colour role, a glyph
 * and a word, so a state is never conveyed by colour alone.
 */
export type Tone = 'ok' | 'warn' | 'bad' | 'idle'

export type BadgeKind = 'task' | 'owner' | 'agent-activity' | 'agent-state' | 'pr-state' | 'mergeable' | 'code-index' | 'route' | 'load' | 'checks'

export interface BadgeSpec {
  tone: Tone
  icon: GlyphName
  /** The word shown. */
  label: string
  /** Whether the value is outside the vocabulary or not reported; the badge is then drawn dashed. */
  unreported: boolean
}

type Entry = [tone: Tone, icon: GlyphName, label: string]

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
}

const TASK: Record<TaskStateId, Entry> = {
  'at-risk': ['bad', 'shield-alert', ''],
  'checks-failed': ['bad', 'x-circle', ''],
  blocked: ['warn', 'ban', ''],
  ready: ['ok', 'check-circle', ''],
  'not-ready': ['warn', 'clock', ''],
  working: ['ok', 'activity', ''],
  landed: ['idle', 'git-merge', ''],
  idle: ['idle', 'minus-circle', ''],
  'not-reported': ['idle', 'help', ''],
}

const TABLES: Record<Exclude<BadgeKind, 'task'>, Record<string, Entry>> = {
  owner: {
    active: ['ok', 'activity', 'active'],
    idle: ['idle', 'minus-circle', 'idle'],
    orphaned: ['bad', 'alert', 'orphaned'],
    unknown: ['idle', 'help', 'unknown'],
  },
  'agent-activity': {
    working: ['ok', 'activity', 'working'],
    blocked: ['warn', 'ban', 'blocked'],
    idle: ['idle', 'minus-circle', 'idle'],
    done: ['idle', 'check-circle', 'done'],
    unknown: ['idle', 'help', 'unknown'],
  },
  'agent-state': {
    running: ['ok', 'activity', 'running'],
    live: ['ok', 'radio', 'live'],
    parked: ['idle', 'minus-circle', 'parked'],
    completed: ['idle', 'check-circle', 'completed'],
    failed: ['bad', 'x-circle', 'failed'],
    timeout: ['bad', 'clock', 'timed out'],
    abandoned: ['warn', 'alert', 'abandoned'],
  },
  'pr-state': {
    open: ['ok', 'git-pull-request', 'open'],
    draft: ['idle', 'pencil', 'draft'],
    merged: ['idle', 'git-merge', 'merged'],
    closed: ['idle', 'x-circle', 'closed'],
  },
  mergeable: {
    clean: ['ok', 'check-circle', 'mergeable'],
    has_hooks: ['ok', 'check-circle', 'mergeable'],
    blocked: ['warn', 'ban', 'blocked'],
    dirty: ['bad', 'x-circle', 'conflicts'],
    behind: ['warn', 'arrow-down', 'behind'],
    unstable: ['warn', 'alert', 'unstable'],
    draft: ['idle', 'pencil', 'draft'],
    unknown: ['idle', 'help', 'merge state unknown'],
  },
  'code-index': {
    fresh: ['ok', 'check-circle', 'fresh'],
    stale: ['warn', 'clock', 'stale'],
    diverged: ['warn', 'alert', 'diverged'],
    pending: ['idle', 'clock', 'pending'],
    failed: ['bad', 'x-circle', 'failed'],
    never: ['idle', 'minus-circle', 'never indexed'],
  },
  route: {
    local: ['ok', 'server', 'local'],
    live: ['ok', 'radio', 'live'],
    'live-remote': ['ok', 'radio', 'live'],
    cached: ['idle', 'archive', 'cached'],
    stale: ['warn', 'clock', 'stale'],
    none: ['idle', 'minus-circle', 'no data'],
  },
  checks: {
    passed: ['ok', 'check-circle', 'passing'],
    failed: ['bad', 'x-circle', 'failing'],
    pending: ['warn', 'clock', 'pending'],
    unknown: ['idle', 'help', 'checks not reported'],
  },
  load: {
    free: ['ok', 'check-circle', 'free'],
    busy: ['warn', 'activity', 'busy'],
    'not-reported': ['idle', 'help', 'load unknown'],
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
}

/** The colour role, glyph and word of a state; an unknown or absent value is a grey, dashed "not reported". */
export function badgeSpec(kind: BadgeKind, value: string | undefined): BadgeSpec {
  if (kind === 'task') {
    const known = value !== undefined && TASK_STATES.some((info) => info.id === value)
    if (!known) return unreported(kind, value)
    const [tone, icon] = TASK[value as TaskStateId]
    return { tone, icon, label: taskStateInfo(value as TaskStateId).label, unreported: value === 'not-reported' }
  }
  const entry = value === undefined ? undefined : TABLES[kind][value]
  if (entry === undefined) return unreported(kind, value)
  return { tone: entry[0], icon: entry[1], label: entry[2], unreported: value === 'unknown' || value === 'not-reported' }
}

function unreported(kind: BadgeKind, value: string | undefined): BadgeSpec {
  return { tone: 'idle', icon: 'help', label: value === undefined || value === '' ? ABSENT[kind] : value, unreported: true }
}
