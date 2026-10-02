import { FleetHealth } from '@cockpit/fleet-data'
import type { AppLink } from '@cockpit/fleet-data'

/** One problem of "Fleet health" with the command that fixes it. */
export interface HealthRow {
  id: string
  /** What is wrong, in words. */
  text: string
  /** Where the words link, when they do. */
  link: AppLink | undefined
  /** Where the command runs ("run on vm", "run here"); absent when it runs anywhere. */
  where: string | undefined
  /** The text to copy, and whether it holds a part to edit; or why there is no command. */
  command: { text: string; needsEdit: boolean; quoteTwice?: boolean } | { reason: string } | undefined
}

/** The rows of "Fleet health": one per problem, stale machines first. Empty when nothing is wrong. */
export function healthRows(health: FleetHealth, dropped: number): HealthRow[] {
  const machine =
    (kind: string) =>
    (item: FleetHealth['staleMachines'][number]): HealthRow => ({
      id: `${kind}:${item.machineId}`,
      text: item.text,
      link: item.link,
      where: 'text' in item.command ? item.command.label : undefined,
      command: 'text' in item.command ? { text: item.command.text, needsEdit: item.command.needsEdit, ...(item.command.quoteTwice ? { quoteTwice: true } : {}) } : item.command,
    })
  const rows = [
    ...health.staleMachines.map(machine('stale')),
    ...health.olderWb.map(machine('older')),
    ...health.remoteErrors.map(machine('remote')),
    ...health.exportDropped.map(machine('export')),
    ...health.publishErrors.map(machine('publish')),
  ]
  for (const error of health.scanErrors) {
    rows.push({
      id: `scan:${error.repository}`,
      text: `Scan error in ${error.repository}`,
      link: error.link,
      // `wb fleet status` reads this machine's own scan.
      where: 'text' in error.command ? 'run here' : undefined,
      command: 'text' in error.command ? { text: error.command.text, needsEdit: false } : error.command,
    })
  }
  if (dropped > 0) {
    rows.push({
      id: 'dropped',
      text: `${dropped} ${dropped === 1 ? 'entry' : 'entries'} of the fleet document ${dropped === 1 ? 'was' : 'were'} invalid and left out`,
      link: undefined,
      where: undefined,
      command: undefined,
    })
  }
  return rows
}
