import { signal } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { Machine } from '@cockpit/fleet-data'
import { machine } from '@cockpit/fleet-data/testing'
import { MachineChip } from './machine-chip'
import { UiClock } from './ui-clock'

const NOW = Date.parse('2026-10-01T13:00:00Z')

async function render(extra: Partial<Machine>, stale = false, name = 'vm') {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [{ provide: UiClock, useValue: { now: signal(NOW) } }] })
  const fixture = TestBed.createComponent(MachineChip)
  fixture.componentRef.setInput('machine', { ...machine(name, 'cached'), ...extra })
  fixture.componentRef.setInput('stale', stale)
  await fixture.whenStable()
  const root: HTMLElement = fixture.nativeElement
  const text = (selector: string) => root.querySelector(selector)?.textContent?.replace(/\s+/g, ' ').trim()
  return { root, text, badges: [...root.querySelectorAll('app-state-badge')].map((badge) => badge.textContent?.replace(/\s+/g, ' ').trim()) }
}

describe('MachineChip', () => {
  it('shows the name, the route and the cached age of a cached machine', async () => {
    const { text, badges } = await render({})
    expect(text('.name')).toBe('vm')
    expect(badges).toEqual(['Route: cached'])
    expect(text('app-relative-time')).toBe('3 h ago')
    expect(text('.transport')).toBeUndefined()
  })

  it('marks a stale snapshot with a word and a glyph', async () => {
    const { badges, root } = await render({}, true)
    expect(badges).toEqual(['Route: cached', 'Route: stale'])
    expect(root.querySelector('.tone-warn svg')).not.toBeNull()
  })

  it('shows this machine as local with no age, and the transport of a live remote', async () => {
    const local = await render({ route: 'local' }, false, 'alpha')
    expect(local.badges).toEqual(['Route: local'])
    expect(local.root.querySelector('app-relative-time')).toBeNull()
    const live = await render({ route: 'live-remote', transport: 'ssh' })
    expect(live.badges).toEqual(['Route: live'])
    expect(live.text('.transport')).toBe('ssh')
    expect(live.root.querySelector('.transport')?.getAttribute('title')).toBe('Read live over ssh')
  })
})
