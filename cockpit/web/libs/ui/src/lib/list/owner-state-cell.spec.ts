import { TestBed } from '@angular/core/testing'
import { worktree } from '@cockpit/fleet-data/testing'
import { OwnerStateCell } from './owner-state-cell'

const text = (element: Element) => (element.textContent ?? '').replace(/\s+/g, ' ').trim()

function render(extra: Record<string, unknown>) {
  TestBed.resetTestingModule()
  const fixture = TestBed.createComponent(OwnerStateCell)
  fixture.componentRef.setInput('worktree', { ...worktree('w1', 'r1', 'alpha'), ...extra })
  return fixture.whenStable().then(() => fixture.nativeElement as HTMLElement)
}

describe('OwnerStateCell', () => {
  // cockpit-views#ac:worktrees-columns-and-badges
  it('shows the owner state badge and the sync badges of a worktree of this machine', async () => {
    const root = await render({ owner_state: 'active', ahead: 2, behind: 1, upstream_gone: true })
    expect(text(root.querySelector('app-state-badge') as Element)).toContain('active')
    expect(root.querySelector('app-sync-badges')).not.toBeNull()
    expect(text(root.querySelector('app-sync-badges') as Element)).toContain('↑2')
    expect(text(root.querySelector('app-sync-badges') as Element)).toContain('↓1')
    expect(text(root.querySelector('app-sync-badges') as Element)).toContain('gone')
  })

  it('has no sync badges for a worktree of another machine, and says an unreported owner state is not reported', async () => {
    const root = await render({ route: 'cached', ahead: 2, owner_state: undefined })
    expect(root.querySelector('app-sync-badges')).toBeNull()
    expect(text(root.querySelector('app-state-badge') as Element)).toContain('not reported')
  })
})
