import { MediaMatcher } from '@angular/cdk/layout'
import { TestBed } from '@angular/core/testing'
import { SHEET_QUERY, SheetMode } from './sheet-mode'

describe('SheetMode', () => {
  it('follows the phone-sized media query, and stops listening when destroyed', () => {
    const listeners = new Set<(event: { matches: boolean }) => void>()
    const matchMedia = vi.fn((media: string) => ({ matches: true, media, addListener: (l: never) => listeners.add(l), removeListener: (l: never) => listeners.delete(l) }))
    TestBed.configureTestingModule({ providers: [{ provide: MediaMatcher, useValue: { matchMedia } }] })
    const mode = TestBed.inject(SheetMode)
    expect(matchMedia).toHaveBeenCalledWith(SHEET_QUERY)
    expect(mode.active()).toBe(true)
    listeners.forEach((listener) => listener({ matches: false }))
    expect(mode.active()).toBe(false)
    TestBed.resetTestingModule()
    expect(listeners.size).toBe(0)
  })
})
