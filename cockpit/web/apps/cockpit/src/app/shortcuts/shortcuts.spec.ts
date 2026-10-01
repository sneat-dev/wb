import { TestBed } from '@angular/core/testing'
import { Router, provideRouter } from '@angular/router'
import { SEQUENCE_TIMEOUT_MS, Shortcuts, isEditable } from './shortcuts'
import { ShellState } from '../shell/shell-state'

function press(key: string, init: KeyboardEventInit = {}, target: EventTarget = document.body): KeyboardEvent {
  const event = new KeyboardEvent('keydown', { key, bubbles: true, cancelable: true, ...init })
  target.dispatchEvent(event)
  return event
}

describe('Shortcuts', () => {
  let shortcuts: Shortcuts
  let shell: ShellState
  let navigate: ReturnType<typeof vi.spyOn>
  let detach: () => void

  beforeEach(() => {
    vi.useFakeTimers()
    TestBed.configureTestingModule({ providers: [provideRouter([])] })
    shortcuts = TestBed.inject(Shortcuts)
    shell = TestBed.inject(ShellState)
    navigate = vi.spyOn(TestBed.inject(Router), 'navigateByUrl').mockResolvedValue(true)
    detach = shortcuts.attach()
  })

  afterEach(() => {
    detach()
    vi.useRealTimers()
    document.body.innerHTML = ''
  })

  it('switches tabs with g and a second key', () => {
    for (const [key, path] of [['h', '/'], ['t', '/tasks'], ['r', '/repositories'], ['w', '/worktrees'], ['a', '/agents'], ['m', '/machines']]) {
      press('g')
      expect(press(key).defaultPrevented).toBe(true)
      expect(navigate).toHaveBeenLastCalledWith(path)
    }
    expect(navigate).toHaveBeenCalledTimes(6)
  })

  it('forgets a lone g after the timeout and ignores an unknown second key', () => {
    press('g')
    vi.advanceTimersByTime(SEQUENCE_TIMEOUT_MS + 1)
    press('w')
    press('g')
    press('x')
    press('w')
    expect(navigate).not.toHaveBeenCalled()
  })

  it('lets the key after an abandoned g work as itself', () => {
    press('g')
    press('?')
    expect(shell.sheetOpen()).toBe(true)
  })

  it('does not fire while the focus is in an input, a text area or editable content', () => {
    const input = document.createElement('input')
    const area = document.createElement('textarea')
    const editable = document.createElement('div')
    editable.setAttribute('contenteditable', 'true')
    const inner = document.createElement('span')
    editable.append(inner)
    document.body.append(input, area, editable)
    for (const target of [input, area, editable, inner]) {
      press('g', {}, target)
      press('w', {}, target)
      expect(press('?', {}, target).defaultPrevented).toBe(false)
    }
    expect(navigate).not.toHaveBeenCalled()
    expect(shell.sheetOpen()).toBe(false)
    expect(isEditable(null)).toBe(false)
    expect(isEditable(document.body)).toBe(false)
  })

  it('ignores g with a modifier, Shift+G and keys while an overlay is open', () => {
    press('g', { ctrlKey: true })
    press('g', { altKey: true })
    press('G', { shiftKey: true })
    press('w')
    shell.openSheet()
    press('g')
    press('w')
    expect(navigate).not.toHaveBeenCalled()
  })

  it('opens the palette with Cmd+K and Ctrl+K anywhere, even in an input', () => {
    const input = document.createElement('input')
    document.body.append(input)
    expect(press('k', { metaKey: true }, input).defaultPrevented).toBe(true)
    expect(shell.paletteOpen()).toBe(true)
    shell.closePalette()
    press('K', { ctrlKey: true })
    expect(shell.paletteOpen()).toBe(true)
  })

  it('opens the palette with / when no list filter is registered, and the sheet with ?', () => {
    expect(press('/').defaultPrevented).toBe(true)
    expect(shell.paletteOpen()).toBe(true)
    shell.closePalette()
    expect(press('?', { shiftKey: true }).defaultPrevented).toBe(true)
    expect(shell.sheetOpen()).toBe(true)
  })

  it('focuses the registered filter with /, and goes back to the palette when it unregisters', () => {
    const element = document.createElement('input')
    const target = { element, focus: vi.fn(), clear: vi.fn() }
    const unregister = shortcuts.registerFilter(target)
    press('/')
    expect(target.focus).toHaveBeenCalledTimes(1)
    expect(shell.paletteOpen()).toBe(false)
    // Unregistering a filter that was replaced leaves the newer one.
    const newer = { element, focus: vi.fn(), clear: vi.fn() }
    shortcuts.registerFilter(newer)
    unregister()
    press('/')
    expect(newer.focus).toHaveBeenCalledTimes(1)
    expect(target.focus).toHaveBeenCalledTimes(1)
  })

  it('closes the palette, then the sheet, then clears the focused filter, then closes the panel on Esc', () => {
    const element = document.createElement('input')
    document.body.append(element)
    const filter = { element, focus: vi.fn(), clear: vi.fn() }
    shortcuts.registerFilter(filter)
    const older = vi.fn(() => false)
    const newer = vi.fn(() => true)
    shortcuts.registerPanel(older)
    const unregister = shortcuts.registerPanel(newer)

    shell.openPalette()
    expect(press('Escape').defaultPrevented).toBe(true)
    expect(shell.paletteOpen()).toBe(false)
    shell.openSheet()
    press('Escape')
    expect(shell.sheetOpen()).toBe(false)

    element.focus()
    press('Escape', {}, element)
    expect(filter.clear).toHaveBeenCalledTimes(1)
    expect(newer).not.toHaveBeenCalled()

    element.blur()
    expect(press('Escape').defaultPrevented).toBe(true)
    expect(newer).toHaveBeenCalledTimes(1)
    expect(older).not.toHaveBeenCalled()

    // The newest panel declines, so the older one is asked.
    newer.mockReturnValue(false)
    older.mockReturnValue(true)
    expect(press('Escape').defaultPrevented).toBe(true)
    expect(older).toHaveBeenCalledTimes(1)
    unregister()
    unregister()
    older.mockReturnValue(false)
    expect(press('Escape').defaultPrevented).toBe(false)
  })

  it('stops listening when detached', () => {
    detach()
    press('?')
    expect(shell.sheetOpen()).toBe(false)
    detach = shortcuts.attach()
  })
})
