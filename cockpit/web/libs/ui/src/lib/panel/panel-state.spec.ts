import { TestBed } from '@angular/core/testing'
import { PanelState } from './panel-state'

const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()

describe('PanelState', () => {
  it('shows its badges, the reason in one line of words, and a note under it', async () => {
    TestBed.resetTestingModule()
    const fixture = TestBed.createComponent(PanelState)
    fixture.componentRef.setInput('reason', 'At risk: 1 commit only on this machine in specscore-go (worktree idle)')
    await fixture.whenStable()
    const root = fixture.nativeElement as HTMLElement
    expect(root.querySelector('section')?.getAttribute('aria-label')).toBe('State')
    expect(text(root.querySelector('.why'))).toBe('At risk: 1 commit only on this machine in specscore-go (worktree idle)')
  })
})
