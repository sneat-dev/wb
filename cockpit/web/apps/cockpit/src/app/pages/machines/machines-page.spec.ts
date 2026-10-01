import { By } from '@angular/platform-browser'
import { Router, provideRouter } from '@angular/router'
import { TestBed } from '@angular/core/testing'
import { fleetDocument, machine } from '@cockpit/fleet-data/testing'
import { FilterBar } from '@cockpit/ui'
import { MachinesPage } from './machines-page'
import { bodyRows, openPage } from '../test-harness'

describe('MachinesPage', () => {
  it('lists every machine with its route and age, and counts that open their entities', async () => {
    const { root } = await openPage('/machines', MachinesPage)
    expect(bodyRows(root)).toEqual([
      ['alpha', 'local', '1', '2', '—'],
      ['beta', 'cached, 5 min ago', '1', '1', '—'],
    ])
    const counts = [...root.querySelectorAll<HTMLElement>('app-count')]
    expect(counts[0].querySelector('a')?.getAttribute('href')).toBe('/repositories?machine=mach-alpha')
    expect([...counts[0].querySelectorAll('li')].map((li) => li.textContent)).toEqual(['github.com/acme/r1'])
    expect(counts[1].querySelector('a')?.getAttribute('href')).toBe('/worktrees?machine=mach-alpha')
    expect(counts[1].querySelectorAll('li')).toHaveLength(2)
    // A cached machine's card names exactly the rows its click opens.
    expect([...counts[2].querySelectorAll('li')].map((li) => li.textContent)).toEqual(['acme/r2'])
    expect([...counts[3].querySelectorAll('li')].map((li) => li.textContent)).toEqual(['task-w3 (branch-w3)'])
    expect(counts[3].querySelector('strong')?.textContent).toBe('1 worktrees')
  })

  it('names nothing behind the counts of a machine that has no entries in the document', async () => {
    const doc = fleetDocument({ machines: [machine('lonely')], repositories: [], worktrees: [] })
    const { root } = await openPage('/machines', MachinesPage, doc)
    expect(root.querySelectorAll('li')).toHaveLength(0)
  })

  it('shows the WB version when the machine reports one', async () => {
    const doc = fleetDocument({ machines: [{ ...machine('alpha'), wb_version: '1.2.3' }] })
    expect(bodyRows((await openPage('/machines', MachinesPage, doc)).root)[0][4]).toBe('1.2.3')
  })

  it('keeps only the machine named in the URL query, and says when nothing matches or exists', async () => {
    const one = await openPage('/machines?machine=mach-beta', MachinesPage)
    expect(bodyRows(one.root).map((row) => row[0])).toEqual(['beta'])
    expect(one.component.machine()).toBe('mach-beta')
    const none = await openPage('/machines?machine=mach-gamma', MachinesPage)
    expect(bodyRows(none.root)).toEqual([['No machines match the filters.']])
    const empty = await openPage('/machines', MachinesPage, fleetDocument({ machines: [] }))
    expect(bodyRows(empty.root)).toEqual([['No machines yet.']])
  })

  it('offers a repository filter that keeps the machines holding that repository', async () => {
    const { root } = await openPage('/machines?repository=r2', MachinesPage)
    expect(bodyRows(root).map((row) => row[0])).toEqual(['beta'])
  })

  it('writes a changed filter into the URL query', async () => {
    const { harness } = await openPage('/machines', MachinesPage)
    harness.fixture.debugElement.query(By.directive(FilterBar)).componentInstance.changed.emit({ key: 'machine', value: 'mach-beta' })
    await harness.fixture.whenStable()
    expect(TestBed.inject(Router).url).toBe('/machines?machine=mach-beta')
  })

  it('renders on its own over an empty, warming-up fleet', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([])] })
    const fixture = TestBed.createComponent(MachinesPage)
    await fixture.whenStable()
    expect(fixture.nativeElement.querySelector('.page-title')).toBeNull()
    expect(fixture.nativeElement.querySelectorAll('tbody tr').length).toBeGreaterThan(0)
  })
})
