/** Durations and ages for the lazy sections of Home (the first page needs none of them). */

const MINUTE = 60_000
const HOUR = 60 * MINUTE
const DAY = 24 * HOUR

/** How long something has run, in the two largest units that matter: "<1 min", "35 min", "2 h 5 min", "3 d 4 h". */
export function spanText(milliseconds: number): string {
  const span = Math.max(0, milliseconds)
  if (span < MINUTE) return '<1 min'
  if (span < HOUR) return `${Math.floor(span / MINUTE)} min`
  if (span < DAY) {
    const minutes = Math.floor((span % HOUR) / MINUTE)
    return minutes === 0 ? `${Math.floor(span / HOUR)} h` : `${Math.floor(span / HOUR)} h ${minutes} min`
  }
  const hours = Math.floor((span % DAY) / HOUR)
  return hours === 0 ? `${Math.floor(span / DAY)} d` : `${Math.floor(span / DAY)} d ${hours} h`
}

/** Whole minutes between two instants, never negative. */
export function minutesBetween(from: number, to: number): number {
  return Math.max(0, Math.floor((to - from) / MINUTE))
}
