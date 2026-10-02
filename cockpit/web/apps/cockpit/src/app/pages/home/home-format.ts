/** Small formatting helpers shared by Home's sections. */

/** A time in milliseconds as the ISO string `app-relative-time` takes; undefined stays undefined. */
export function isoOf(time: number | undefined): string | undefined {
  return time === undefined ? undefined : new Date(time).toISOString()
}

/** "1 repository", "2 repositories". */
export function counted(count: number, singular: string, plural = `${singular}s`): string {
  return `${count} ${count === 1 ? singular : plural}`
}
