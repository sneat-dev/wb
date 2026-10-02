import { TestBed } from '@angular/core/testing'
import { FleetStore } from '@cockpit/fleet-data'
import { fleetDocument, machine } from '@cockpit/fleet-data/testing'
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
  it('is silent for a complete document', async () => {
    expect(text(await render((store) => { store.loaded.set(true); store.document.set(fleetDocument()) }))).toBe('')
  })

  it('shows the diagnostics, singular and plural', async () => {
    const one = await render((store) => { store.loaded.set(true); store.document.set(fleetDocument({ diagnostics: 1 })) })
    expect(text(one)).toBe('1 pull request could not be matched to a single repository.')
  })

  it('shows the plural, a truncated agent list (the local machine\'s flag or another machine\'s) and an unreadable repository list', async () => {
    const root = await render((store) => {
      store.loaded.set(true)
      store.document.set(fleetDocument({ diagnostics: 2, agents_truncated: true, error: 'repositories_unreadable' }))
    })
    expect(text(root)).toContain('2 pull requests could not be matched')
    expect(text(root)).toContain('The agents list is capped')
    expect(text(root)).toContain('the counts of agents are at least')
    expect(root.querySelector('[role=alert]')?.textContent).toContain('could not be listed')
    // Another machine's flag, on its own entry, says it too; a document with neither says nothing.
    TestBed.resetTestingModule()
    const remote = await render((store) => {
      store.loaded.set(true)
      store.document.set(fleetDocument({ machines: [machine('alpha'), { ...machine('beta', 'cached'), agents_truncated: true }] }))
    })
    expect(text(remote)).toContain('The agents list is capped')
    TestBed.resetTestingModule()
    expect(text(await render((store) => { store.loaded.set(true); store.document.set(fleetDocument()) }))).not.toContain('capped')
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

  it('counts the entries the client dropped as unreadable, singular and plural', async () => {
    const one = await render((store) => {
      store.loaded.set(true)
      store.droppedEntries.set(1)
    })
    expect(text(one)).toBe('1 entry in the fleet document could not be read and is left out.')
    TestBed.resetTestingModule()
    const many = await render((store) => {
      store.loaded.set(true)
      store.droppedEntries.set(3)
    })
    expect(text(many)).toBe('3 entries in the fleet document could not be read and are left out.')
  })

  it('says nothing of a warming-up scan, which the freshness chip and the skeleton rows show', async () => {
    const root = await render((store) => {
      store.document.set(fleetDocument({ warming_up: true, repositories_scanned: 3, repositories_total: 12 }))
    })
    expect(text(root)).toBe('')
  })

  it('leaves a schema mismatch to its own state instead of repeating its message', async () => {
    const root = await render((store) => {
      store.loaded.set(true)
      store.error.set('reload')
      store.schemaMismatch.set('page-older')
    })
    expect(root.querySelector('[role=alert]')).toBeNull()
  })
})
