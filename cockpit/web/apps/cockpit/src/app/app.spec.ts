import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { FleetStore } from '@cockpit/fleet-data'
import { App, PAGE_LINKS } from './app'

describe('App', () => {
  const start = vi.fn()
  const stop = vi.fn()

  beforeEach(() => {
    start.mockClear()
    stop.mockClear()
    TestBed.configureTestingModule({
      providers: [provideRouter([]), { provide: FleetStore, useValue: { start, stop, document: () => ({}), loaded: () => true, warmingUp: () => false, error: () => null } }],
    })
  })

  it('renders the shell with the product name, a link to every page and a router outlet', async () => {
    const fixture = TestBed.createComponent(App)
    await fixture.whenStable()
    const root: HTMLElement = fixture.nativeElement
    expect(root.querySelector('h1')?.textContent).toBe('WB Cockpit')
    const links = [...root.querySelectorAll('nav a')]
    expect(links.map((a) => a.textContent)).toEqual(['Dashboard', 'Repositories', 'Worktrees', 'Agents', 'Machines'])
    expect(links.map((a) => a.getAttribute('href'))).toEqual(PAGE_LINKS.map((link) => link.path))
    expect(root.querySelector('main router-outlet')).not.toBeNull()
    expect(root.querySelector('main app-fleet-banner')).not.toBeNull()
  })

  it('starts the fleet store and stops it when the shell goes', async () => {
    const fixture = TestBed.createComponent(App)
    await fixture.whenStable()
    expect(start).toHaveBeenCalledTimes(1)
    fixture.destroy()
    expect(stop).toHaveBeenCalledTimes(1)
  })
})
