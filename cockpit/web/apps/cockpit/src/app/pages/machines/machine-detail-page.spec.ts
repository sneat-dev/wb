import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { fleetDocument } from '@cockpit/fleet-data/testing'
import { MachineDetailPage } from './machine-detail-page'
import { openMachines, machinesDocument } from './machines-fixture'

const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()

describe('MachineDetailPage', () => {
  // cockpit-views#ac:detail-routes-render-the-same-panel
  it('is the machine panel on a page, under a link back to Machines', async () => {
    const { root } = await openMachines('/machines/mach-vm', MachineDetailPage)
    expect(root.querySelector('.back a')?.getAttribute('href')).toBe('/machines')
    expect(text(root.querySelector('app-machine-panel h2'))).toBe('vm')
  })

  it('says it is waiting for the snapshot, and that the machine is not there once the snapshot is complete', async () => {
    const warming = await openMachines('/machines/x', MachineDetailPage, { document: fleetDocument({ warming_up: true, machines: [] }), answers: 'never' })
    expect(text(warming.root)).toContain('Waiting for the fleet snapshot')
    const gone = await openMachines('/machines/nope', MachineDetailPage, { document: machinesDocument(), answers: 'never' })
    expect(text(gone.root)).toContain('This machine is not in the fleet document')
    expect(gone.root.querySelector('app-machine-panel')).toBeNull()
    const unread = await openMachines('/machines/nope', MachineDetailPage, { answers: 'never' })
    unread.store.loaded.set(false)
    unread.harness.detectChanges()
    expect(text(unread.root)).toContain('Waiting for the fleet snapshot')
  })

  it('renders on its own over an empty, warming-up fleet', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([])] })
    const fixture = TestBed.createComponent(MachineDetailPage)
    fixture.componentRef.setInput('id', 'x')
    await fixture.whenStable()
    expect(fixture.nativeElement.textContent).toContain('Waiting for the fleet snapshot')
  })
})
