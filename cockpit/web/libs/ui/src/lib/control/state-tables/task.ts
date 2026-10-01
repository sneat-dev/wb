import { TASK_STATES, taskStateInfo, type TaskStateId } from '@cockpit/fleet-data'
import { GLYPH_ACTIVITY, GLYPH_BAN, GLYPH_CHECK_CIRCLE, GLYPH_CLOCK, GLYPH_GIT_MERGE, GLYPH_HELP, GLYPH_MINUS_CIRCLE, GLYPH_SHIELD_ALERT, GLYPH_X_CIRCLE } from '../glyphs-state'
import { type BadgeSpec, type Entry, unreportedSpec } from './shared'

/** The words of the task states are the library's (`TASK_STATES`), so only the colour role and glyph are here. */
const TASK: Record<TaskStateId, Entry> = {
  'at-risk': ['bad', GLYPH_SHIELD_ALERT, ''],
  'checks-failed': ['bad', GLYPH_X_CIRCLE, ''],
  blocked: ['warn', GLYPH_BAN, ''],
  ready: ['ok', GLYPH_CHECK_CIRCLE, ''],
  'not-ready': ['warn', GLYPH_CLOCK, ''],
  working: ['ok', GLYPH_ACTIVITY, ''],
  landed: ['idle', GLYPH_GIT_MERGE, ''],
  idle: ['idle', GLYPH_MINUS_CIRCLE, ''],
  'not-reported': ['idle', GLYPH_HELP, ''],
}

export const TASK_ABSENT = 'state not reported'

export function taskSpec(value: string | undefined): BadgeSpec {
  if (value === undefined || !TASK_STATES.some((info) => info.id === value)) return unreportedSpec(TASK_ABSENT, value)
  const [tone, icon] = TASK[value as TaskStateId]
  return {
    tone,
    icon,
    label: taskStateInfo(value as TaskStateId).label,
    unreported: value === 'not-reported',
    unrecognised: false,
  }
}
