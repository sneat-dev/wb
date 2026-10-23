import { MachineView, RepositoryCheckout, routeLabel } from '@cockpit/fleet-data'
import { compactAge } from '@cockpit/ui/list'

/** How many machine chips a row shows before "+n"; the rest are in the panel. */
export const MAX_CHIPS = 3

/** One machine's checkout of a repository, as a chip of the Machines column. */
export interface MachineChipView {
  /** The machine entry id: the panel scrolls to the section of this machine. */
  id: string
  name: string
  /** What differs from this machine: the age of a cached snapshot, "stale", the transport of a live one; empty for this machine. */
  detail: string
  stale: boolean
  /** Everything the chip says, in words. */
  title: string
}

/**
 * The chips of a merged repository: this machine by its name alone (REQ:repositories-list), a
 * cached checkout with the age of its snapshot and a stale mark, a live remote with its transport.
 */
export function machineChips(checkouts: readonly RepositoryCheckout[], machines: readonly MachineView[], now: number): MachineChipView[] {
  return checkouts.map((checkout) => {
    const transport = machines.find((machine) => machine.machine.id === checkout.machineId)?.machine.transport
    const name = checkout.machine
    if (checkout.route === 'local') return { id: checkout.machineId, name, detail: '', stale: false, title: `${name}: this machine` }
    if (checkout.route === 'cached') {
      const detail = [compactAge(checkout.observedAt, now), checkout.stale ? 'stale' : ''].filter((part) => part !== '').join(' · ')
      const title = `${name}: ${routeLabel(checkout.repository, now)}${checkout.stale ? '; older than the freshness window' : ''}`
      return { id: checkout.machineId, name, detail, stale: checkout.stale, title }
    }
    return { id: checkout.machineId, name, detail: transport ?? '', stale: false, title: `${name}: read live${transport ? ` over ${transport}` : ''}` }
  })
}
