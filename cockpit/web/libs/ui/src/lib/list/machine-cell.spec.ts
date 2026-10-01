import { TestBed } from '@angular/core/testing'
import { FleetStore } from '@cockpit/fleet-data'
import { fleetDocument } from '@cockpit/fleet-data/testing'
import { MachineCell } from './machine-cell'

function render(id: string, now = Date.parse('2026-10-01T10:05:00Z')) {
  TestBed.resetTestingModule()
  const store = TestBed.inject(FleetStore)
  store.document.set(fleetDocument())
  store.now.set(now)
  const fixture = TestBed.createComponent(MachineCell)
  fixture.componentRef.setInput('id', id)
  return fixture.whenStable().then(() => fixture.nativeElement as HTMLElement)
}

describe('MachineCell', () => {
  it('shows the machine chip with how it is reached, and the age of a cached snapshot', async () => {
    const local = await render('mach-alpha')
    expect(local.querySelector('app-machine-chip .name')?.textContent).toContain('alpha')
    expect(local.querySelector('app-relative-time')).toBeNull()
    const cached = await render('mach-beta')
    expect(cached.querySelector('app-machine-chip .name')?.textContent).toContain('beta')
    expect(cached.querySelector('app-relative-time')).not.toBeNull()
  })

  it('marks a machine whose snapshot is stale, and shows nothing for a machine the fleet does not list', async () => {
    const stale = await render('mach-beta', Date.parse('2026-10-03T10:05:00Z'))
    expect(stale.textContent).toContain('stale')
    expect((await render('mach-nope')).textContent?.trim()).toBe('')
  })
})
