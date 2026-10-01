import { TestBed } from '@angular/core/testing'
import { FleetStore } from '@cockpit/fleet-data'
import { fleetDocument } from '@cockpit/fleet-data/testing'
import { MachineCell, compactAge } from './machine-cell'

function render(id: string, now = Date.parse('2026-10-01T10:05:00Z'), transport?: 'http' | 'ssh', route?: 'live') {
  TestBed.resetTestingModule()
  const store = TestBed.inject(FleetStore)
  const doc = fleetDocument()
  store.document.set({ ...doc, machines: doc.machines.map((machine) => (machine.machine_id === 'mach-beta' ? { ...machine, ...(transport ? { transport } : {}), ...(route ? { route } : {}) } : machine)) })
  store.now.set(now)
  const fixture = TestBed.createComponent(MachineCell)
  fixture.componentRef.setInput('id', id)
  return fixture.whenStable().then(() => fixture.nativeElement as HTMLElement)
}

describe('MachineCell', () => {
  it('shows the name alone for this machine, and a chip with the age for a cached one', async () => {
    const local = await render('mach-alpha')
    expect(local.querySelector('.name')?.textContent).toBe('alpha')
    expect(local.querySelector('.chip')).toBeNull()
    expect(local.querySelector('.unit')?.getAttribute('title')).toBeNull()
    const cached = await render('mach-beta')
    expect(cached.querySelector('.name')?.textContent).toBe('beta')
    expect(cached.querySelector('.chip')?.textContent).toMatch(/^\d+ [mhd]$/)
    expect(cached.querySelector('.unit')?.getAttribute('title')).toContain('Cached snapshot')
  })

  it('puts the age, stale and the transport in one chip, and marks a stale snapshot', async () => {
    const stale = await render('mach-beta', Date.parse('2026-10-03T10:05:00Z'))
    expect(stale.querySelector('.chip')?.textContent).toMatch(/^\d+ [hd] · stale$/)
    expect(stale.querySelector('.chip')?.classList.contains('stale')).toBe(true)
    expect(stale.querySelector('.unit')?.getAttribute('title')).toContain('older than the freshness window')
    const ssh = await render('mach-beta', Date.parse('2026-10-01T10:05:00Z'), 'ssh')
    expect(ssh.querySelector('.chip')?.textContent).toMatch(/ · ssh$/)
    expect(ssh.querySelector('.unit')?.getAttribute('title')).toContain('over ssh')
    const live = await render('mach-beta', Date.parse('2026-10-01T10:05:00Z'), 'http', 'live')
    expect(live.querySelector('.chip')?.textContent).toBe('http')
    expect(live.querySelector('.unit')?.getAttribute('title')).toContain('Read live over http')
  })

  it('shows nothing for a machine the fleet does not list', async () => {
    expect((await render('mach-nope')).textContent?.trim()).toBe('')
  })
})

describe('compactAge', () => {
  it('says minutes, hours and days in the least room, and nothing for an unreadable time', () => {
    const now = Date.parse('2026-10-01T10:00:00Z')
    expect(compactAge('2026-10-01T09:41:00Z', now)).toBe('19 m')
    expect(compactAge('2026-10-01T10:30:00Z', now)).toBe('0 m')
    expect(compactAge('2026-10-01T07:00:00Z', now)).toBe('3 h')
    expect(compactAge('2026-09-29T10:00:00Z', now)).toBe('2 d')
    expect(compactAge(undefined, now)).toBe('')
  })
})
