import { TestBed } from '@angular/core/testing'
import { CodeIndex } from '@cockpit/fleet-data'
import { CodeIndexLabel } from './code-index-label'

const NOW = Date.parse('2026-10-01T10:05:00Z')

async function render(states: CodeIndex[] | undefined) {
  const fixture = TestBed.createComponent(CodeIndexLabel)
  fixture.componentRef.setInput('states', states)
  fixture.componentRef.setInput('now', NOW)
  await fixture.whenStable()
  return fixture.nativeElement as HTMLElement
}

const text = (element: Element) => (element.textContent as string).replace(/\s+/g, ' ').trim()

describe('CodeIndexLabel', () => {
  it('says fresh in plain text with the receipt age as visible text, without drawing attention', async () => {
    const root = await render([{ indexer: 'codegrapher', state: 'fresh', receipt_at: '2026-10-01T08:00:00Z' }])
    const label = root.querySelector('.code-index') as HTMLElement
    expect(text(label)).toBe('fresh 2 h ago')
    expect(label.classList.contains('attention')).toBe(false)
    expect(label.getAttribute('title')).toBe('Receipt 2026-10-01T08:00:00Z')
  })

  it('says stale with how many commits behind, and marks it beyond colour', async () => {
    const root = await render([{ indexer: 'codegrapher', state: 'stale', behind: 3 }])
    const label = root.querySelector('.code-index') as HTMLElement
    expect(text(label)).toBe('stale, 3 behind')
    expect(label.classList.contains('attention')).toBe(true)
    expect(label.hasAttribute('title')).toBe(false)
  })

  it('says never, and counts a stale index with no count as zero behind', async () => {
    expect(text(await render([{ indexer: 'a', state: 'never' }]))).toBe('never')
    expect(text(await render([{ indexer: 'a', state: 'stale' }]))).toBe('stale, 0 behind')
  })

  it('names the indexer of each state when there are several', async () => {
    const root = await render([
      { indexer: 'graph', state: 'fresh' },
      { indexer: 'search', state: 'failed' },
    ])
    expect([...root.querySelectorAll('.code-index')].map(text)).toEqual(['graph: fresh', 'search: failed'])
  })

  it('shows a dash when the freshness is not known', async () => {
    expect(text(await render(undefined))).toBe('—')
    const none = await render([])
    expect(text(none)).toBe('—')
    expect(none.querySelector('.unknown')?.getAttribute('title')).toBe('Not known for this entry')
  })
})
