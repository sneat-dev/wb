import { TestBed } from '@angular/core/testing'
import { ShellState } from './shell-state'

describe('ShellState', () => {
  it('opens one overlay at a time', () => {
    const state = TestBed.inject(ShellState)
    expect(state.modalOpen()).toBe(false)
    state.openPalette()
    expect([state.paletteOpen(), state.sheetOpen(), state.modalOpen()]).toEqual([true, false, true])
    state.openSheet()
    expect([state.paletteOpen(), state.sheetOpen()]).toEqual([false, true])
    state.openPalette()
    expect([state.paletteOpen(), state.sheetOpen()]).toEqual([true, false])
    state.closePalette()
    state.openSheet()
    state.closeSheet()
    expect(state.modalOpen()).toBe(false)
  })
})
