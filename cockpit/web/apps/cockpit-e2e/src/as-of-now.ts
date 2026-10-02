// Moves a dated fixture to the present. It has no dependency, so it is unit-tested (as-of-now.spec.ts).

const DAY_MS = 86_400_000
const INSTANT = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z$/
const CALENDAR_DAY = /^\d{4}-\d{2}-\d{2}$/

/**
 * A fixture of Home's states as it would be read now. The fixtures are dated at one fixed instant (`snapshot`),
 * and the page judges them against the real clock: a failed run stops blocking its task after 24 hours, at-risk work
 * leaves "Needs you" after 14 days, a machine is stale after a day. Served as they are, the same fleet shows other
 * rows and counts as the days pass. So every instant in the document is moved by the time that has passed since
 * the snapshot, and every calendar day by the whole days, when it is served: each entry is as old as the fixture
 * says, whenever the test runs.
 */
export function asOfNow<T>(value: T, snapshot: number, now = Date.now()): T {
  const days = Math.floor(now / DAY_MS) - Math.floor(snapshot / DAY_MS)
  const move = (item: unknown): unknown => {
    if (typeof item === 'string') {
      if (INSTANT.test(item)) return new Date(Date.parse(item) + (now - snapshot)).toISOString()
      if (CALENDAR_DAY.test(item)) return new Date(Date.parse(`${item}T00:00:00Z`) + days * DAY_MS).toISOString().slice(0, 10)
      return item
    }
    if (Array.isArray(item)) return item.map(move)
    if (typeof item === 'object' && item !== null) return Object.fromEntries(Object.entries(item).map(([name, entry]) => [name, move(entry)]))
    return item
  }
  return move(value) as T
}
