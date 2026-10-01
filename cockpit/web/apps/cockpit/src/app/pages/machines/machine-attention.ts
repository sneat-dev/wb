import { FleetModel, HealthItem, PanelCommand } from '@cockpit/fleet-data'
import { buildHealth } from '@cockpit/fleet-data/home-details'

/** One thing wrong with a machine, in words, and why there is nothing to copy when there is not. */
export interface AttentionNote {
  id: string
  text: string
  /** Why no command is offered; absent when there is one. */
  reason?: string
}

/** What a machine needs: the notes, and the commands the library wrote for them (with their "run on <machine>" labels). */
export interface Attention {
  notes: AttentionNote[]
  commands: PanelCommand[]
}

/** The title of the copy entry for each kind of problem. */
const TITLES = { stale: 'Publish its snapshot', older: 'Update wb', remote: 'Fix the remote read', export: 'Try the export' } as const

/**
 * The Fleet health lines of one machine (REQ:remote-error-is-visible and the stale, older WB and export lines of
 * REQ:home-health), from the library's `buildHealth`, so Home and this page say and copy the same thing.
 */
export function attentionOf(model: FleetModel, machineId: string): Attention {
  const health = buildHealth(model)
  const groups: [keyof typeof TITLES, HealthItem[]][] = [
    ['stale', health.staleMachines],
    ['older', health.olderWb],
    ['remote', health.remoteErrors],
    ['export', health.exportDropped],
  ]
  const notes: AttentionNote[] = []
  const commands: PanelCommand[] = []
  for (const [kind, items] of groups) {
    for (const item of items.filter((candidate) => candidate.machineId === machineId)) {
      const id = `${kind}:${machineId}`
      if ('text' in item.command) {
        notes.push({ id, text: item.text })
        commands.push({ title: TITLES[kind], command: { ok: true, text: item.command.text, label: item.command.label, needsEdit: item.command.needsEdit } })
      } else {
        notes.push({ id, text: item.text, reason: item.command.reason })
      }
    }
  }
  return { notes, commands }
}
