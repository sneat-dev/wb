import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { FleetStore } from '@cockpit/fleet-data'
import { Overlays } from './overlays'
import { ShellState } from './shell-state'

describe('Overlays', () => {
  it('holds the palette and the shortcut sheet, each opened from the shell state', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([])] })
    const fixture = TestBed.createComponent(Overlays)
    await fixture.whenStable()
    const root: HTMLElement = fixture.nativeElement
    expect(root.querySelector('app-command-palette')).not.toBeNull()
    expect(root.querySelector('app-shortcut-sheet')).not.toBeNull()
    expect(root.querySelector('app-owner-popover')).not.toBeNull()
    TestBed.inject(ShellState).openPalette()
    await fixture.whenStable()
    expect(root.querySelector('[role="dialog"][aria-label="Search"]')).not.toBeNull()
  })

  it('explains a schema mismatch, and only then', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([])] })
    const store = TestBed.inject(FleetStore)
    const fixture = TestBed.createComponent(Overlays)
    await fixture.whenStable()
    expect(fixture.nativeElement.querySelector('app-schema-mismatch')).toBeNull()
    store.schemaMismatch.set('daemon-older')
    await fixture.whenStable()
    expect(fixture.nativeElement.querySelector('app-schema-mismatch')?.textContent).toContain('update wb on this machine')
  })
})
