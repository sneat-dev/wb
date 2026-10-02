import { TestBed } from '@angular/core/testing'
import { ClipboardWriter } from './clipboard'
import { COPIED_FEEDBACK_MS, CopyButton } from './copy-button'
import { StatusAnnouncer } from './status-announcer'

async function render(copyResult: boolean, sharedRegion = true) {
  const copy = vi.fn().mockResolvedValue(copyResult)
  // Without the shared region (it has a timer of its own), a test can count the button's timers alone.
  TestBed.configureTestingModule({ providers: [{ provide: ClipboardWriter, useValue: { copy } }, ...(sharedRegion ? [] : [{ provide: StatusAnnouncer, useValue: { say: () => undefined, message: () => '' } }])] })
  const fixture = TestBed.createComponent(CopyButton)
  fixture.componentRef.setInput('text', 'wb fleet status')
  fixture.componentRef.setInput('label', 'Copy command: fleet status')
  const copied: string[] = []
  fixture.componentInstance.copied.subscribe((text) => copied.push(text))
  await fixture.whenStable()
  const root: HTMLElement = fixture.nativeElement
  // What the one shared status region says (the button has none of its own).
  const announcer = TestBed.inject(StatusAnnouncer)
  const status = { get textContent() { return announcer.message() } }
  expect(root.querySelector('[role="status"]')).toBeNull()
  return { fixture, root, copy, copied, button: root.querySelector('button') as HTMLButtonElement, status }
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
    const { fixture, button } = await render(true, false)
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

  it('says what the caller asked for: a template word and a longer announcement', async () => {
    const copy = vi.fn().mockResolvedValue(true)
    TestBed.resetTestingModule()
    TestBed.configureTestingModule({ providers: [{ provide: ClipboardWriter, useValue: { copy } }] })
    const fixture = TestBed.createComponent(CopyButton)
    fixture.componentRef.setInput('text', 'wb x --message=<<<edit:message>>>')
    fixture.componentRef.setInput('idleWord', 'Copy template')
    fixture.componentRef.setInput('doneStatus', 'Copied; edit the <…> parts before running')
    await fixture.whenStable()
    const root: HTMLElement = fixture.nativeElement
    expect(root.querySelector('button')?.textContent?.trim()).toBe('Copy template')
    vi.useFakeTimers()
    ;(root.querySelector('button') as HTMLButtonElement).click()
    await vi.advanceTimersByTimeAsync(0)
    fixture.detectChanges()
    expect(root.querySelector('button')?.textContent?.trim()).toBe('Copied')
    expect(TestBed.inject(StatusAnnouncer).message()).toBe('Copied; edit the <…> parts before running')
  })

  it('does nothing when the browser answers after the page has gone: no state, no event, no timer', async () => {
    let answer: (ok: boolean) => void = () => undefined
    const copy = vi.fn(() => new Promise<boolean>((resolve) => (answer = resolve)))
    TestBed.resetTestingModule()
    // The shared region has its own timer; this test is about the button's.
    TestBed.configureTestingModule({ providers: [{ provide: ClipboardWriter, useValue: { copy } }, { provide: StatusAnnouncer, useValue: { say: () => undefined } }] })
    const fixture = TestBed.createComponent(CopyButton)
    fixture.componentRef.setInput('text', 'x')
    const copied: string[] = []
    fixture.componentInstance.copied.subscribe((text) => copied.push(text))
    await fixture.whenStable()
    ;(fixture.nativeElement.querySelector('button') as HTMLButtonElement).click()
    fixture.destroy()
    vi.useFakeTimers()
    answer(true)
    await vi.advanceTimersByTimeAsync(0)
    expect(copied).toEqual([])
    expect(vi.getTimerCount()).toBe(0)
  })
})
