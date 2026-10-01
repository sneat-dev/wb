import { TestBed } from '@angular/core/testing'
import { ShellState } from './shell-state'

describe('ShellState', () => {
  it('toggles the sign-in card without making the shell modal', () => {
    const state = TestBed.inject(ShellState)
    state.toggleOwnerHint()
    expect([state.ownerHintOpen(), state.modalOpen()]).toEqual([true, false])
    state.toggleOwnerHint()
    expect(state.ownerHintOpen()).toBe(false)
    state.toggleOwnerHint()
    state.closeOwnerHint()
    expect(state.ownerHintOpen()).toBe(false)
  })

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
