import { TestBed } from '@angular/core/testing'
import { StatusAnnouncer, StatusRegion } from './status-announcer'

describe('the shared status region', () => {
  afterEach(() => vi.useRealTimers())

  // cockpit-views#ac:list-accessibility
  it('says what it is told once, in one polite region, clears it after a moment, and can say the same words again', async () => {
    const fixture = TestBed.createComponent(StatusRegion)
    const announcer = TestBed.inject(StatusAnnouncer)
    const region = fixture.nativeElement.querySelector('[role="status"]') as HTMLElement
    vi.useFakeTimers()
    announcer.say('Copied')
    await vi.advanceTimersByTimeAsync(0)
    fixture.detectChanges()
    expect(region.textContent).toBe('Copied')
    // Said again at once: it goes empty and then says it again, so it is announced again.
    announcer.say('Copied')
    expect(announcer.message()).toBe('')
    await vi.advanceTimersByTimeAsync(0)
    expect(announcer.message()).toBe('Copied')
    await vi.advanceTimersByTimeAsync(2000)
    fixture.detectChanges()
    expect(region.textContent).toBe('')
  })
})
