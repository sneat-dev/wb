import { TestBed } from '@angular/core/testing'
import { Router, provideRouter } from '@angular/router'
import { agentDetailLink, repositoryDetailLink, taskDetailLink, worktreeDetailLink } from '@cockpit/fleet-data'
import { IdentityCell } from './identity-cell'

function render(inputs: Record<string, unknown>) {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [provideRouter([])] })
  const fixture = TestBed.createComponent(IdentityCell)
  for (const [name, value] of Object.entries(inputs)) fixture.componentRef.setInput(name, value)
  return fixture.whenStable().then(() => fixture.nativeElement as HTMLElement)
}

describe('IdentityCell', () => {
  it('shows the name strong and the secondary text muted, with a copy icon outside the tab order', async () => {
    const root = await render({ name: 'fix-ci', secondary: 'sneat-dev/wb', copyLabel: 'Copy task name' })
    expect(root.querySelector('strong.name')?.textContent).toBe('fix-ci')
    expect(root.querySelector('.secondary')?.textContent).toBe('sneat-dev/wb')
    expect(root.querySelector('button')?.getAttribute('aria-label')).toBe('Copy task name')
    expect(root.querySelector('button')?.getAttribute('tabindex')).toBe('-1')
  })

  it('makes the name a link out of the tab order when it has a target, and leaves out an absent secondary', async () => {
    const root = await render({ name: 'fix-ci', link: taskDetailLink('fix-ci') })
    const link = root.querySelector('a.name') as HTMLAnchorElement
    expect(link.getAttribute('href')).toBe('/tasks/detail?task=fix-ci')
    expect(link.getAttribute('tabindex')).toBe('-1')
    expect(root.querySelector('.secondary')).toBeNull()
    expect(root.querySelector('button')?.getAttribute('aria-label')).toBe('Copy name')
  })

  // The router encodes router commands itself, so an id with a slash, a space or a percent sign is encoded once.
  it('links to a detail address whose id has a slash, a space, a percent sign or accents, encoded once and decoded back by the router', async () => {
    for (const id of ['a/b', 'a b', '100%', 'é/ü ß', '%2F']) {
      for (const link of [agentDetailLink(id), worktreeDetailLink(id), repositoryDetailLink('gitlab.com', 'g/s/p', id)]) {
        const root = await render({ name: 'n', link })
        const href = (root.querySelector('a.name') as HTMLAnchorElement).getAttribute('href') as string
        expect(href, id).toBe(link.path)
        const segments = TestBed.inject(Router).parseUrl(href).root.children['primary'].segments.map((segment) => segment.path)
        expect(segments[segments.length - 1], id).toBe(id)
      }
    }
  })

  // cockpit-views#ac:filter-vocabulary-is-the-only-link-target
  it('keeps the secondary text right after the name, in one row, and lets the muted text give way first', async () => {
    const root = await render({ name: 'fix-ci', secondary: 'sneat-dev/wb' })
    const text = root.querySelector('.text') as HTMLElement
    expect([...text.children].map((child) => child.className)).toEqual(['name', 'secondary'])
    expect(root.querySelector('app-copy-icon')?.previousElementSibling).toBe(text)
  })
})
