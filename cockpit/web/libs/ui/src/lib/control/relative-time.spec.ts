import { TestBed } from '@angular/core/testing'
import { RelativeTime } from './relative-time'

async function render(at: string | undefined, now: number) {
  const fixture = TestBed.createComponent(RelativeTime)
  fixture.componentRef.setInput('at', at)
  fixture.componentRef.setInput('now', now)
  await fixture.whenStable()
  return { fixture, time: fixture.nativeElement.querySelector('time') as HTMLTimeElement }
}

describe('RelativeTime', () => {
  const at = '2026-10-01T10:00:00Z'

  it('shows the age in a time element with the exact time as the tooltip', async () => {
    const { time } = await render(at, Date.parse(at) + 5 * 60_000)
    expect(time.textContent).toBe('5 min ago')
    expect(time.getAttribute('datetime')).toBe(at)
    expect(time.getAttribute('title')).toBe(at)
  })

  it('follows the clock it is given', async () => {
    const { fixture, time } = await render(at, Date.parse(at) + 30_000)
    expect(time.textContent).toBe('just now')
    fixture.componentRef.setInput('now', Date.parse(at) + 3 * 3_600_000)
    await fixture.whenStable()
    expect(time.textContent).toBe('3 h ago')
  })

  it('says "age unknown" with no datetime for an absent or unreadable time', async () => {
    for (const value of [undefined, 'yesterday']) {
      const { time } = await render(value, Date.now())
      expect(time.textContent).toBe('age unknown')
      expect(time.hasAttribute('datetime')).toBe(false)
      expect(time.hasAttribute('title')).toBe(false)
    }
  })
})
