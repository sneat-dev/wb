import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { taskDetailLink } from '@cockpit/fleet-data'
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
})
