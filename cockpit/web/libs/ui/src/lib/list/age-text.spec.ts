import { TestBed } from '@angular/core/testing'
import { FleetStore } from '@cockpit/fleet-data'
import { AgeText } from './age-text'

const NOW = Date.parse('2026-10-01T10:05:00Z')
const DAY = 24 * 60 * 60 * 1000

async function render(at: number | string | undefined, now?: number) {
  TestBed.inject(FleetStore).now.set(NOW)
  const fixture = TestBed.createComponent(AgeText)
  fixture.componentRef.setInput('at', at)
  if (now !== undefined) fixture.componentRef.setInput('now', now)
  await fixture.whenStable()
  return fixture.nativeElement.querySelector('.age') as HTMLElement
}

describe('AgeText', () => {
  // cockpit-views#ac:repository-and-time-rendering
  it('reads 3 d ago in normal type with the absolute time in the title', async () => {
    const age = await render(NOW - 3 * DAY)
    expect(age.textContent).toBe('3 d ago')
    expect(age.classList.contains('muted')).toBe(false)
    expect(age.getAttribute('title')).toBe(new Date(NOW - 3 * DAY).toISOString())
  })

  it('mutes a time older than 30 days, and not one of exactly 30', async () => {
    const old = await render(new Date(NOW - 45 * DAY).toISOString())
    expect(old.textContent).toBe('45 d ago')
    expect(old.classList.contains('muted')).toBe(true)
    expect(old.getAttribute('title')).toBe(new Date(NOW - 45 * DAY).toISOString())
    expect((await render(NOW - 30 * DAY)).classList.contains('muted')).toBe(false)
  })

  it('shows a muted dash for no time or one that is not a time', async () => {
    for (const at of [undefined, 'yesterday']) {
      const age = await render(at)
      expect(age.textContent).toBe('—')
      expect(age.classList.contains('muted')).toBe(true)
      expect(age.hasAttribute('title')).toBe(false)
    }
  })

  it('measures against the clock it is given instead of the store\'s', async () => {
    expect((await render(NOW, NOW + 2 * 60 * 60 * 1000)).textContent).toBe('2 h ago')
  })
})
