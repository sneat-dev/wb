import { TestBed } from '@angular/core/testing'
import { ClipboardWriter } from './clipboard'
import { COPIED_FEEDBACK_MS, CopyButton } from './copy-button'

async function render(copyResult: boolean) {
  const copy = vi.fn().mockResolvedValue(copyResult)
  TestBed.configureTestingModule({ providers: [{ provide: ClipboardWriter, useValue: { copy } }] })
  const fixture = TestBed.createComponent(CopyButton)
  fixture.componentRef.setInput('text', 'wb fleet status')
  fixture.componentRef.setInput('label', 'Copy command: fleet status')
  const copied: string[] = []
  fixture.componentInstance.copied.subscribe((text) => copied.push(text))
  await fixture.whenStable()
  const root: HTMLElement = fixture.nativeElement
  return { fixture, root, copy, copied, button: root.querySelector('button') as HTMLButtonElement, status: root.querySelector('[role="status"]') as HTMLElement }
}

describe('CopyButton', () => {
  // The fixture renders with real timers; each test then switches to fake ones for the feedback.
  afterEach(() => vi.useRealTimers())

  it('copies its text, says "Copied" with a check, and returns to "Copy" after two seconds', async () => {
    const { fixture, copy, copied, button, status } = await render(true)
    vi.useFakeTimers()
    expect(button.getAttribute('aria-label')).toBe('Copy command: fleet status')
    expect(button.textContent?.trim()).toBe('Copy')
    button.click()
    await vi.advanceTimersByTimeAsync(0)
    fixture.detectChanges()
    expect(copy).toHaveBeenCalledWith('wb fleet status')
    expect(copied).toEqual(['wb fleet status'])
    expect(button.textContent?.trim()).toBe('Copied')
    expect(button.classList.contains('done')).toBe(true)
    expect(status.textContent).toBe('Copied')
    await vi.advanceTimersByTimeAsync(COPIED_FEEDBACK_MS)
    fixture.detectChanges()
    expect(button.textContent?.trim()).toBe('Copy')
    expect(status.textContent).toBe('')
  })

  it('says "Copy failed" and emits nothing when the browser refused', async () => {
    const { fixture, copied, button, status } = await render(false)
    vi.useFakeTimers()
    button.click()
    await vi.advanceTimersByTimeAsync(0)
    fixture.detectChanges()
    expect(copied).toEqual([])
    expect(button.textContent?.trim()).toBe('Copy failed')
    expect(button.classList.contains('failed')).toBe(true)
    expect(status.textContent).toBe('Copy failed')
  })

  it('restarts the feedback on a second press, and stops the timer when destroyed', async () => {
    const { fixture, button } = await render(true)
    vi.useFakeTimers()
    button.click()
    await vi.advanceTimersByTimeAsync(1500)
    button.click()
    await vi.advanceTimersByTimeAsync(1500)
    fixture.detectChanges()
    expect(button.textContent?.trim()).toBe('Copied')
    fixture.destroy()
    expect(vi.getTimerCount()).toBe(0)
  })
})
