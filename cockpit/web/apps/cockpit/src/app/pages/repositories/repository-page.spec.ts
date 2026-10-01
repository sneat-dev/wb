import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { vi } from 'vitest'
import { fleetDocument, repository } from '@cockpit/fleet-data/testing'
import { RepositoryPage } from './repository-page'
import { SESSION, openPage } from '../test-harness'

const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()
const noBranches = vi.fn(async () => new Response(JSON.stringify({ branches: [] }))) as unknown as typeof fetch

const document = fleetDocument({
  repositories: [repository('r1', 'alpha', { name: 'acme/r1' }), repository('r1b', 'beta', { route: 'cached', name: 'acme/r1' })],
  worktrees: [],
})

describe('RepositoryPage', () => {
  // cockpit-views#ac:repository-detail-loads-branches-lazily: the older repository id still opens the page
  it('opens the merged repository from the entry id of any of its checkouts, as the address that worked before the machines were merged', async () => {
    for (const id of ['r1', 'r1b']) {
      const { root, component } = await openPage(`/repositories/${id}`, RepositoryPage, document, SESSION, noBranches)
      expect(component.id()).toBe(id)
      expect(text(root.querySelector('h2'))).toBe('acme/r1')
      expect(root.querySelectorAll('app-repository-machine-section')).toHaveLength(2)
      expect(root.querySelector('.back a')?.getAttribute('href')).toBe('/repositories')
    }
  })

  it('says it is waiting for the snapshot, and that the repository is not there once the snapshot is complete', async () => {
    const warming = await openPage('/repositories/r1', RepositoryPage, fleetDocument({ warming_up: true, repositories: [] }))
    expect(text(warming.root)).toContain('Waiting for the fleet snapshot')
    const gone = await openPage('/repositories/r-gone', RepositoryPage, document, SESSION, noBranches)
    expect(gone.store.loaded()).toBe(true)
    expect(text(gone.root)).toContain('This repository is not in the fleet document')
  })

  it('renders on its own over an empty, warming-up fleet', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([])] })
    const fixture = TestBed.createComponent(RepositoryPage)
    fixture.componentRef.setInput('id', 'anything')
    await fixture.whenStable()
    expect(fixture.nativeElement.textContent).toContain('Waiting for the fleet snapshot')
  })
})
