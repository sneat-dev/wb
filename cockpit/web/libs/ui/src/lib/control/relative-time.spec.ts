import { signal } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { RelativeTime } from './relative-time'
import { UiClock } from './ui-clock'

async function render(at: string | undefined, now = signal(Date.now())) {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [{ provide: UiClock, useValue: { now } }] })
  const fixture = TestBed.createComponent(RelativeTime)
  fixture.componentRef.setInput('at', at)
  await fixture.whenStable()
  return { fixture, now, time: fixture.nativeElement.querySelector('time') as HTMLTimeElement }
}

describe('RelativeTime', () => {
  const at = '2026-10-01T10:00:00Z'

  it('shows the age in a time element, with the time in the local zone and the UTC value as the tooltip', async () => {
    const { time } = await render(at, signal(Date.parse(at) + 5 * 60_000))
    expect(time.textContent).toBe('5 min ago')
    expect(time.getAttribute('datetime')).toBe(at.replace('Z', '.000Z'))
    expect(time.getAttribute('title')).toBe(`${new Date(at).toLocaleString()} (UTC 2026-10-01T10:00:00.000Z)`)
  })

  it('normalises a time with an offset to ISO UTC', async () => {
    const { time } = await render('2026-10-01T11:00:00+01:00', signal(Date.now()))
    expect(time.getAttribute('datetime')).toBe('2026-10-01T10:00:00.000Z')
  })

  it('follows the shared clock, with no clock passed in', async () => {
    const { fixture, now, time } = await render(at, signal(Date.parse(at) + 30_000))
    expect(time.textContent).toBe('just now')
    now.set(Date.parse(at) + 3 * 3_600_000)
    await fixture.whenStable()
    expect(time.textContent).toBe('3 h ago')
  })

  it('says "age unknown" with no datetime and no tooltip for an absent or unreadable time', async () => {
    for (const value of [undefined, 'yesterday']) {
      const { time } = await render(value)
      expect(time.textContent).toBe('age unknown')
      expect(time.hasAttribute('datetime')).toBe(false)
      expect(time.hasAttribute('title')).toBe(false)
    }
  })
})
