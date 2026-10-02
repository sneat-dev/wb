import { fleet, modelOf } from './home-testing'
import { compactAge, machineWords } from './machine-words'

describe('machine-words', () => {
  it('writes an age in the least room, and nothing for a time it cannot read', () => {
    const now = Date.parse('2026-10-01T10:00:00Z')
    expect(compactAge('2026-10-01T09:55:00Z', now)).toBe('5 m')
    expect(compactAge('2026-10-01T07:00:00Z', now)).toBe('3 h')
    expect(compactAge('2026-09-29T10:00:00Z', now)).toBe('2 d')
    expect(compactAge('2026-10-01T10:05:00Z', now)).toBe('0 m')
    expect(compactAge(undefined, now)).toBe('')
    expect(compactAge('later', now)).toBe('')
  })

  it('names this machine alone, and another with one chip of what differs', () => {
    const model = modelOf(fleet())
    const view = (name: string) => model.machines.find((candidate) => candidate.machine.machine === name)!
    expect(machineWords(view('mac'), model.now)).toEqual({ name: 'mac', chip: '', stale: false, title: undefined })
    expect(machineWords(view('vm'), model.now)).toEqual({ name: 'vm', chip: 'ssh', stale: false, title: 'vm: read live over ssh' })
    expect(machineWords(view('old'), model.now)).toEqual({ name: 'old', chip: '1 d · stale', stale: true, title: 'old: cached snapshot, older than the freshness window' })
  })

  it('shows a cached machine whose snapshot has no time as stale, with no age: an unknown time is never fresh', () => {
    const document = fleet()
    const old = document.machines.find((machine) => machine.machine === 'old')!
    old.observed_at = undefined
    const model = modelOf(document)
    const words = machineWords(
      model.machines.find((candidate) => candidate.machine.machine === 'old')!,
      model.now,
    )
    expect(words.chip).toBe('stale')
    expect(words.title).toBe('old: cached snapshot, older than the freshness window')
  })
})
