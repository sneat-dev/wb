import { TestBed } from '@angular/core/testing'
import { FleetStore } from '@cockpit/fleet-data'
import { fleetDocument } from '@cockpit/fleet-data/testing'
import { FleetBanner } from './fleet-banner'

async function render(setup: (store: FleetStore) => void): Promise<HTMLElement> {
  const store = TestBed.inject(FleetStore)
  setup(store)
  const fixture = TestBed.createComponent(FleetBanner)
  await fixture.whenStable()
  return fixture.nativeElement
}

const text = (root: HTMLElement) => (root.textContent ?? '').replace(/\s+/g, ' ').trim()

describe('FleetBanner', () => {
  it('says the fleet is loading until the first read is answered', async () => {
    expect(text(await render(() => undefined))).toBe('Loading the fleet…')
  })

  it('shows the scan progress while warming up, without hiding what is listed', async () => {
    const root = await render((store) => {
      store.loaded.set(true)
      store.document.set(fleetDocument({ warming_up: true, repositories_scanned: 3, repositories_total: 12 }))
    })
    expect(text(root)).toContain('Scanning repositories: 3 of 12')
    const progress = root.querySelector('progress') as HTMLProgressElement
    expect([progress.value, progress.max]).toEqual([3, 12])
  })

  it('is silent for a complete document', async () => {
    expect(text(await render((store) => { store.loaded.set(true); store.document.set(fleetDocument()) }))).toBe('')
  })

  it('shows the diagnostics, singular and plural', async () => {
    const one = await render((store) => { store.loaded.set(true); store.document.set(fleetDocument({ diagnostics: 1 })) })
    expect(text(one)).toBe('1 pull request could not be matched to a single repository.')
  })

  it('shows the plural, a truncated agent list and an unreadable repository list', async () => {
    const root = await render((store) => {
      store.loaded.set(true)
      store.document.set(fleetDocument({ diagnostics: 2, agents_truncated: true, error: 'repositories_unreadable' }))
    })
    expect(text(root)).toContain('2 pull requests could not be matched')
    expect(text(root)).toContain('The agents list is capped')
    expect(root.querySelector('[role=alert]')?.textContent).toContain('could not be listed')
  })

  it('explains an old Git, and gives any other document error a generic notice', async () => {
    const old = await render((store) => { store.loaded.set(true); store.document.set(fleetDocument({ error: 'git_too_old' })) })
    expect(text(old)).toContain('Git on this machine is older')
    TestBed.resetTestingModule()
    const other = await render((store) => { store.loaded.set(true); store.document.set(fleetDocument({ error: 'something_else' })) })
    expect(text(other)).toContain('(something_else)')
    expect(text(other)).not.toContain('could not be listed')
  })

  it('shows a failed read as an alert', async () => {
    const root = await render((store) => { store.loaded.set(true); store.error.set('down') })
    expect(root.querySelector('[role=alert]')?.textContent).toBe('down')
  })
})
