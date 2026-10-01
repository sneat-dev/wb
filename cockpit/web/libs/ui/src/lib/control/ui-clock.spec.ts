import { TestBed } from '@angular/core/testing'
import { UI_CLOCK_TICK_MS, UiClock } from './ui-clock'

describe('UiClock', () => {
  afterEach(() => vi.useRealTimers())

  it('is one signal that moves on a timer, and stops when the injector is destroyed', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-10-01T10:00:00Z'))
    const clock = TestBed.inject(UiClock)
    const start = clock.now()
    expect(start).toBe(Date.parse('2026-10-01T10:00:00Z'))
    vi.advanceTimersByTime(UI_CLOCK_TICK_MS)
    expect(clock.now()).toBe(start + UI_CLOCK_TICK_MS)
    expect(TestBed.inject(UiClock)).toBe(clock)
    TestBed.resetTestingModule()
    expect(vi.getTimerCount()).toBe(0)
  })
})
