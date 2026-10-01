import { By } from '@angular/platform-browser'
import { Router, provideRouter } from '@angular/router'
import { TestBed } from '@angular/core/testing'
import { agent, fleetDocument } from '@cockpit/fleet-data/testing'
import { FilterBar } from '@cockpit/ui'
import { AgentsPage } from './agents-page'
import { bodyRows, openPage } from '../test-harness'

describe('AgentsPage', () => {
  it('lists every agent, naming its repository or a dash', async () => {
    const { root } = await openPage('/agents', AgentsPage)
    expect(bodyRows(root).map((row) => row.slice(0, 5))).toEqual([
      ['session s-a1', 'running', 'github.com/acme/r1', 'alpha', 'local'],
      ['session s-a2', 'idle', 'github.com/acme/r1', 'alpha', 'local'],
      ['claude run run9', 'running', '—', 'alpha', 'local'],
    ])
    expect(bodyRows(root)[2].slice(5)).toEqual(['claude', '—'])
  })

  it('names a repository by its id when the document does not list it, and shows the model', async () => {
    const doc = fleetDocument({ agents: [agent('a1', 'r-gone', 'idle', { model: 'm1' })] })
    const { root } = await openPage('/agents', AgentsPage, doc)
    expect(bodyRows(root)[0][2]).toBe('r-gone')
    expect(bodyRows(root)[0][6]).toBe('m1')
  })

  it('opens exactly the running agents of a repository from the URL query', async () => {
    const { root, component } = await openPage('/agents?repository=r1&state=running', AgentsPage)
    expect(bodyRows(root).map((row) => row[0])).toEqual(['session s-a1'])
    expect(component.state()).toBe('running')
    expect(root.querySelector('.state-filter')?.textContent).toContain('running')
  })

  it('filters by machine, and says when nothing matches or nothing exists', async () => {
    const none = await openPage('/agents?machine=mach-beta', AgentsPage)
    expect(bodyRows(none.root)).toEqual([['No agents match the filters.']])
    expect(none.root.querySelector('.state-filter')).toBeNull()
    const empty = await openPage('/agents', AgentsPage, fleetDocument({ agents: [] }))
    expect(bodyRows(empty.root)).toEqual([['No agents yet.']])
  })

  it('writes a changed filter into the URL query', async () => {
    const { harness } = await openPage('/agents', AgentsPage)
    harness.fixture.debugElement.query(By.directive(FilterBar)).componentInstance.changed.emit({ key: 'machine', value: 'mach-alpha' })
    await harness.fixture.whenStable()
    expect(TestBed.inject(Router).url).toBe('/agents?machine=mach-alpha')
  })

  it('renders on its own over an empty, warming-up fleet', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([])] })
    const fixture = TestBed.createComponent(AgentsPage)
    await fixture.whenStable()
    expect(fixture.nativeElement.querySelector('.page-title')).toBeNull()
    expect(fixture.nativeElement.querySelectorAll('tbody tr').length).toBeGreaterThan(0)
  })
})
