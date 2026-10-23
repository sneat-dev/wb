import { TestBed } from '@angular/core/testing'
import { ListAnnouncer } from './list-announcer'

describe('ListAnnouncer', () => {
  afterEach(() => vi.useRealTimers())

  it('says a message, says the same one again after a change, and clears it after a moment', async () => {
    vi.useFakeTimers()
    TestBed.configureTestingModule({ providers: [ListAnnouncer] })
    const announcer = TestBed.inject(ListAnnouncer)
    announcer.say('Copied')
    expect(announcer.message()).toBe('')
    await Promise.resolve()
    expect(announcer.message()).toBe('Copied')
    announcer.say('Copied')
    expect(announcer.message()).toBe('')
    await Promise.resolve()
    expect(announcer.message()).toBe('Copied')
    vi.advanceTimersByTime(2000)
    expect(announcer.message()).toBe('')
  })
})
