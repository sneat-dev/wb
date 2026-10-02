import { TestBed } from '@angular/core/testing'
import { CodeIndexCell } from './code-index-cell'

const text = (element: Element) => (element.textContent ?? '').replace(/\s+/g, ' ').trim()

function render(states: unknown) {
  TestBed.resetTestingModule()
  const fixture = TestBed.createComponent(CodeIndexCell)
  fixture.componentRef.setInput('states', states)
  return fixture.whenStable().then(() => fixture.nativeElement as HTMLElement)
}

describe('CodeIndexCell', () => {
  it('shows each indexer\'s state in words (stale with its count) and the age of its receipt', async () => {
    const root = await render([
      { indexer: 'codegrapher', state: 'stale', behind: 3, receipt_at: '2026-10-01T10:00:00Z' },
      { indexer: 'other', state: 'never' },
    ])
    expect(root.querySelectorAll('app-state-badge')).toHaveLength(2)
    expect(text(root)).toContain('stale, 3 behind')
    expect(text(root)).toContain('never')
    expect(root.querySelectorAll('app-relative-time')).toHaveLength(1)
  })

  it('shows a dash when the freshness is not known', async () => {
    for (const states of [undefined, []]) {
      const root = await render(states)
      expect(text(root)).toBe('—')
      expect(root.querySelector('span')?.getAttribute('title')).toBe('Not known for this entry')
    }
  })
})
