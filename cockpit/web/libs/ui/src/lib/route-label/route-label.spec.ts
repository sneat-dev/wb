import { TestBed } from '@angular/core/testing'
import { RouteLabel } from './route-label'

const NOW = Date.parse('2026-10-01T10:05:00Z')

async function render(entry: object) {
  const fixture = TestBed.createComponent(RouteLabel)
  fixture.componentRef.setInput('entry', entry)
  fixture.componentRef.setInput('now', NOW)
  await fixture.whenStable()
  return fixture.nativeElement.querySelector('.route') as HTMLElement
}

describe('RouteLabel', () => {
  it('says local for a local entry', async () => {
    const label = await render({ id: 'x', machine: 'm', route: 'local' })
    expect(label.textContent).toBe('local')
    expect(label.classList.contains('cached')).toBe(false)
    expect(label.hasAttribute('title')).toBe(false)
  })

  it('says cached and the age for a cached entry, with the exact time on hover', async () => {
    const label = await render({ id: 'x', machine: 'm', route: 'cached', observed_at: '2026-10-01T10:00:00Z' })
    expect(label.textContent).toBe('cached, 5 min ago')
    expect(label.classList.contains('cached')).toBe(true)
    expect(label.getAttribute('title')).toBe('2026-10-01T10:00:00Z')
  })
})
