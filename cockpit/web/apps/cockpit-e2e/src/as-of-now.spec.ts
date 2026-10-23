import { describe, expect, it } from 'vitest'
import { asOfNow } from './as-of-now'

const SNAPSHOT = Date.parse('2026-10-01T10:00:00Z')
const HOUR = 3_600_000
const DAY = 24 * HOUR

describe('asOfNow', () => {
  const fixture = {
    snapshot_at: '2026-10-01T10:00:00.000Z',
    agents: [{ id: 'run-failed', state: 'failed', finished_at: '2026-10-01T06:00:00Z', exit_code: 2, ok: true, none: null }],
    throughput: { per_day: [{ date: '2026-10-01', finished: 2 }, { date: '2026-09-30', finished: 1 }] },
    names: ['2026', 'v2026-10-01', '2026-10-01T06:00', 'refactor-cache'],
  }

  // The failed run of the Home fixture ended four hours before the snapshot; a task it blocks leaves "Needs you" 24 hours after it ended.
  it('keeps every entry as old as the fixture says, however long after the snapshot it is served', () => {
    for (const later of [0, 20 * HOUR + 123, 3 * DAY, 400 * DAY + 7 * HOUR]) {
      const now = SNAPSHOT + later
      const moved = asOfNow(fixture, SNAPSHOT, now)
      expect(now - Date.parse(moved.snapshot_at), String(later)).toBe(0)
      expect(now - Date.parse(moved.agents[0].finished_at), String(later)).toBe(4 * HOUR)
      // The newest day of the throughput is the day it is served, and the days stay consecutive.
      expect(moved.throughput.per_day.map((day) => day.date), String(later)).toEqual([new Date(now).toISOString().slice(0, 10), new Date(now - DAY).toISOString().slice(0, 10)])
    }
  })

  it('moves only instants and calendar days, and leaves the fixture itself as it was', () => {
    const moved = asOfNow(fixture, SNAPSHOT, SNAPSHOT + 3 * DAY)
    expect(moved.names).toEqual(fixture.names)
    expect(moved.agents[0]).toMatchObject({ id: 'run-failed', state: 'failed', exit_code: 2, ok: true, none: null })
    expect(moved.throughput.per_day.map((day) => day.finished)).toEqual([2, 1])
    expect(fixture.agents[0].finished_at).toBe('2026-10-01T06:00:00Z')
    expect(asOfNow('2026-10-01', SNAPSHOT, SNAPSHOT + DAY)).toBe('2026-10-02')
  })

  it('uses the clock when it is given no time', () => {
    const before = Date.now()
    const moved = Date.parse(asOfNow('2026-10-01T10:00:00Z', SNAPSHOT))
    expect(moved).toBeGreaterThanOrEqual(before)
    expect(moved).toBeLessThanOrEqual(Date.now())
  })
})
