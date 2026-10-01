import { Clipboard } from '@angular/cdk/clipboard'
import { TestBed } from '@angular/core/testing'
import { COPIED_MS, CopyButton } from './copy-button'

async function render(copy: () => boolean, extra: Record<string, unknown> = {}) {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [{ provide: Clipboard, useValue: { copy } }] })
  const fixture = TestBed.createComponent(CopyButton)
  fixture.componentRef.setInput('value', 'a-very-long-task-name-that-the-cell-shortens')
  fixture.componentRef.setInput('label', 'Copy task name')
  for (const [name, value] of Object.entries(extra)) fixture.componentRef.setInput(name, value)
  await fixture.whenStable()
  const button = fixture.nativeElement.querySelector('button') as HTMLButtonElement
  return { fixture, button, status: fixture.nativeElement.querySelector('[aria-live]') as HTMLElement }
}

describe('CopyButton', () => {
  afterEach(() => vi.useRealTimers())

  // cockpit-views#ac:copy-buttons-copy-the-full-value
  it('puts the full value in the clipboard, says so for a moment, and does not reach the row', async () => {
    const copy = vi.fn(() => true)
    const { fixture, button, status } = await render(copy)
    vi.useFakeTimers()
    expect(button.getAttribute('aria-label')).toBe('Copy task name')
    const click = new MouseEvent('click', { bubbles: true, cancelable: true })
    const reached = vi.fn()
    fixture.nativeElement.addEventListener('click', reached)
    button.dispatchEvent(click)
    fixture.detectChanges()
    expect(copy).toHaveBeenCalledWith('a-very-long-task-name-that-the-cell-shortens')
    expect(reached).not.toHaveBeenCalled()
    expect(status.textContent).toBe('Copied')
    vi.advanceTimersByTime(COPIED_MS)
    fixture.detectChanges()
    expect(status.textContent).toBe('')
  })

  it('restarts the moment when pressed again, and says nothing when the copy failed', async () => {
    const copy = vi.fn(() => true)
    const { fixture, button, status } = await render(copy)
    vi.useFakeTimers()
    button.click()
    vi.advanceTimersByTime(COPIED_MS - 100)
    button.click()
    vi.advanceTimersByTime(COPIED_MS - 100)
    fixture.detectChanges()
    expect(status.textContent).toBe('Copied')
    copy.mockReturnValue(false)
    vi.advanceTimersByTime(200)
    button.click()
    fixture.detectChanges()
    expect(status.textContent).toBe('')
  })

  it('is out of the tab order in a list row, and stops its timer when destroyed', async () => {
    const { fixture, button } = await render(() => true, { tabbable: false })
    vi.useFakeTimers()
    expect(button.getAttribute('tabindex')).toBe('-1')
    button.click()
    const clear = vi.spyOn(globalThis, 'clearTimeout')
    fixture.destroy()
    expect(clear.mock.calls.some(([timer]) => timer !== undefined)).toBe(true)
  })

  it('is in the tab order by default', async () => {
    const { button } = await render(() => true)
    expect(button.hasAttribute('tabindex')).toBe(false)
  })
})
