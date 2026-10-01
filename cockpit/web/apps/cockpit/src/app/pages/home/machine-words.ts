import { MachineView } from '@cockpit/fleet-data'

/** A machine as a row says it: the name, and for a machine that is not this one one chip with what differs ("19 m · ssh"). */
export interface MachineWords {
  name: string
  /** What differs from this machine, in order: the age of a cached snapshot, "stale", the transport; empty for this machine. */
  chip: string
  stale: boolean
  /** The details in full, for the tooltip; undefined for this machine. */
  title: string | undefined
}

/** The age of a snapshot in the least room: `5 m`, `3 h`, `2 d`; empty when the time is unreadable. */
export function compactAge(observedAt: string | undefined, now: number): string {
  const time = Date.parse(observedAt ?? '')
  if (Number.isNaN(time)) return ''
  const minutes = Math.max(0, Math.floor((now - time) / 60_000))
  if (minutes < 60) return `${minutes} m`
  if (minutes < 24 * 60) return `${Math.floor(minutes / 60)} h`
  return `${Math.floor(minutes / (24 * 60))} d`
}

/**
 * The machine rule of the lists (the machine cell of `@cockpit/ui/list`), for Home's rows: the name
 * alone for this machine; for another one the name and a single chip.
 * TODO(ui): `machine-cell.ts` is in the list barrel, which is too heavy for Home's scripts; when it
 * has its own light entry point, use `app-machine-cell` and drop this.
 */
export function machineWords(view: MachineView, now: number): MachineWords {
  const { machine } = view
  const stale = view.state === 'stale'
  if (machine.route === 'local') return { name: machine.machine, chip: '', stale: false, title: undefined }
  const chip = [machine.route === 'cached' ? compactAge(machine.observed_at, now) : '', stale ? 'stale' : '', machine.transport ?? ''].filter((part) => part !== '').join(' · ')
  return {
    name: machine.machine,
    chip,
    stale,
    title: `${machine.machine}: ${machine.route === 'cached' ? 'cached snapshot' : 'read live'}${machine.transport ? ` over ${machine.transport}` : ''}${stale ? ', older than the freshness window' : ''}`,
  }
}
