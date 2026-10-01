import { TestBed } from '@angular/core/testing'
import { ActivatedRouteSnapshot, Router, provideRouter, withComponentInputBinding } from '@angular/router'
import { RouterTestingHarness } from '@angular/router/testing'
import { FleetStore, agentDetailLink, hrefOf, machineDetailLink, repositoryDetailLink, worktreeDetailLink } from '@cockpit/fleet-data'
import { fleetDocument } from '@cockpit/fleet-data/testing'
import { LIST_SHORTCUTS } from '@cockpit/ui/list-host'
import { Shortcuts } from './shortcuts/shortcuts'
import { appRoutes } from './app.routes'

// The detail routes decode the id from the address once: an id with a `/`, a space, a `%` or accents arrives as it was.
describe('detail addresses', () => {
  const IDS = ['plain', 'a/b', 'a b', '100%', 'é/ü ß', '%2F']

  async function idOf(href: string): Promise<string> {
    TestBed.resetTestingModule()
    TestBed.configureTestingModule({ providers: [provideRouter(appRoutes, withComponentInputBinding()), { provide: LIST_SHORTCUTS, useExisting: Shortcuts }] })
    const store = TestBed.inject(FleetStore)
    store.loaded.set(true)
    store.document.set(fleetDocument())
    const harness = await RouterTestingHarness.create()
    await harness.navigateByUrl(href)
    // The route's `id` parameter, as the page that is routed to receives it.
    let route: ActivatedRouteSnapshot = TestBed.inject(Router).routerState.snapshot.root
    while (route.firstChild) route = route.firstChild
    return route.params['id']
  }

  it('reach the worktree, agent, machine and repository routes with the id they were built from', async () => {
    for (const id of IDS) {
      for (const link of [worktreeDetailLink(id), agentDetailLink(id), machineDetailLink(id), repositoryDetailLink('gitlab.com', 'group/sub/project', id)]) {
        expect(await idOf(hrefOf(link)), `${link.path}`).toBe(id)
      }
    }
  })
})
