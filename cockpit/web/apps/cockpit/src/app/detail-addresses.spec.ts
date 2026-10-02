import { TestBed } from '@angular/core/testing'
import { provideRouter, withComponentInputBinding } from '@angular/router'
import { RouterTestingHarness } from '@angular/router/testing'
import { FleetStore, agentDetailLink, hrefOf, repositoryDetailLink, worktreeDetailLink } from '@cockpit/fleet-data'
import { agent, fleetDocument, repository, worktree } from '@cockpit/fleet-data/testing'
import { LIST_SHORTCUTS } from '@cockpit/ui/list-host'
import { Shortcuts } from './shortcuts/shortcuts'
import { appRoutes } from './app.routes'

const IDS = ['plain', 'a/b', 'a b', '100%', 'é/ü ß', '%2F']

async function open(href: string) {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [provideRouter(appRoutes, withComponentInputBinding()), { provide: LIST_SHORTCUTS, useExisting: Shortcuts }] })
  const store = TestBed.inject(FleetStore)
  store.loaded.set(true)
  store.document.set(
    fleetDocument({
      repositories: IDS.map((id) => repository(id, 'alpha', { host: 'gitlab.com', name: `group/sub/${id.length}` })),
      worktrees: IDS.map((id) => ({ ...worktree(id, 'plain', 'alpha'), task: `task-${id.length}` })),
      agents: IDS.map((id) => agent(id, 'plain', 'running', { runtime: 'claude', session_id: `sess-${id}` })),
      pull_requests: [],
    }),
  )
  const harness = await RouterTestingHarness.create()
  await harness.navigateByUrl(href)
  await harness.fixture.whenStable()
  return harness.routeNativeElement as HTMLElement
}

// An id with a `/`, a space, a `%` or accents is encoded once in the address and the page finds its entity.
describe('detail pages at addresses with awkward ids', () => {
  it('open the agent page of the agent whose id it is', async () => {
    for (const id of IDS) {
      const root = await open(hrefOf(agentDetailLink(id)))
      expect(root.querySelector('app-agent-panel'), id).not.toBeNull()
      expect(root.textContent, id).not.toContain('not in the fleet document')
      expect(root.querySelector('app-agent-panel')?.textContent, id).toContain(`sess-${id}`)
    }
  })

  it('open the worktree page of the worktree whose id it is, and the repository page by entry id', async () => {
    for (const id of IDS) {
      const worktreePage = await open(hrefOf(worktreeDetailLink(id)))
      expect(worktreePage.querySelector('app-worktree-panel'), id).not.toBeNull()
      expect(worktreePage.textContent, id).toContain(`task-${id.length}`)
      const repositoryPage = await open(hrefOf(repositoryDetailLink('gitlab.com', `group/sub/${id.length}`, id)))
      expect(repositoryPage.textContent, id).toContain(`group/sub/${id.length}`)
    }
  })
})
