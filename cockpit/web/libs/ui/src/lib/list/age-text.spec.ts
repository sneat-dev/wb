import { signal } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { UiClock } from '../control/ui-clock'
import { AgeText } from './age-text'

const NOW = Date.parse('2026-10-01T10:05:00Z')
const DAY = 24 * 60 * 60 * 1000

async function render(at: number | string | undefined, now = NOW) {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [{ provide: UiClock, useValue: { now: signal(now) } }] })
  const fixture = TestBed.createComponent(AgeText)
  fixture.componentRef.setInput('at', at)
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

  it('measures against the shared clock, which a row does not pass down', async () => {
    expect((await render(NOW, NOW + 2 * 60 * 60 * 1000)).textContent).toBe('2 h ago')
  })
})
