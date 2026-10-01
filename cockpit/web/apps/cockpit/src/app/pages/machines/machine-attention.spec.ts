import { FleetModels } from '@cockpit/fleet-data'
import { NOW } from '../test-harness'
import { attentionOf } from './machine-attention'
import { machinesDocument } from './machines-fixture'

const model = new FleetModels().forDocument(machinesDocument(), NOW)

describe('attentionOf', () => {
  it('lists, for a stale machine on an older WB with a remote error, each problem with the command the library writes and where it runs', () => {
    const { notes, commands } = attentionOf(model, 'mach-oldmac')
    expect(notes).toEqual([
      { id: 'stale:mach-oldmac', text: 'oldmac has not published for over 24 hours' },
      { id: 'older:mach-oldmac', text: 'oldmac runs an older WB (1.0.0)' },
      { id: 'remote:mach-oldmac', text: 'oldmac: its daemon is not running' },
    ])
    expect(commands.map((entry) => entry.title)).toEqual(['Publish its snapshot', 'Update wb', 'Fix the remote read'])
    expect(commands.map((entry) => (entry.command.ok ? [entry.command.text, entry.command.label, entry.command.needsEdit] : entry.command))).toEqual([
      ['wb remote publish', 'run on oldmac', false],
      ['wb self-update', 'run on oldmac', false],
      ['wb daemon start', 'run on oldmac', false],
    ])
  })

  it('gives the reason, and no command, for a remote error that clears itself', () => {
    const { notes, commands } = attentionOf(model, 'mach-nas')
    expect(notes.find((note) => note.id === 'remote:mach-nas')).toMatchObject({ text: 'nas: is still warming up and has no export yet', reason: 'nothing to run: it clears when the machine finishes its first scan' })
    expect(commands.map((entry) => entry.title)).toEqual(['Publish its snapshot'])
  })

  it('names the entries a live export left out, with the export to try', () => {
    const { notes, commands } = attentionOf(model, 'mach-vm')
    expect(notes).toEqual([{ id: 'export:mach-vm', text: '3 entries left out of vm\'s export' }])
    expect(commands[0].command).toMatchObject({ ok: true, text: "wb cockpit export --format='json'" })
  })

  it('has nothing for a machine with nothing wrong', () => {
    expect(attentionOf(model, 'mach-macbook')).toEqual({ notes: [], commands: [] })
  })
})
