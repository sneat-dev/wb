import { TestBed } from '@angular/core/testing'
import { CopyCommand } from '@cockpit/fleet-data'
import { ClipboardWriter } from './clipboard'
import { COPIED_FEEDBACK_MS } from './copy-button'
import { LazyCopy } from './lazy-copy'
import { StatusAnnouncer } from './status-announcer'

async function render(command: CopyCommand, copyResult = true) {
  const copy = vi.fn().mockResolvedValue(copyResult)
  TestBed.configureTestingModule({ providers: [{ provide: ClipboardWriter, useValue: { copy } }] })
  const fixture = TestBed.createComponent(LazyCopy)
  const build = vi.fn().mockResolvedValue(command)
  fixture.componentRef.setInput('build', build)
  fixture.componentRef.setInput('idleWord', 'Copy template')
  fixture.componentRef.setInput('label', 'Copy template wb pr create: commit')
  await fixture.whenStable()
  const root: HTMLElement = fixture.nativeElement
  // What the one shared status region says (the button has none of its own).
  const announcer = TestBed.inject(StatusAnnouncer)
  const status = { get textContent() { return announcer.message() } }
  expect(root.querySelector('[role="status"]')).toBeNull()
  return { fixture, copy, build, button: root.querySelector('button') as HTMLButtonElement, status }
}

describe('LazyCopy', () => {
  afterEach(() => vi.useRealTimers())

  it('builds the command only when pressed, copies it, says so, and returns to its word', async () => {
    const { fixture, copy, build, button, status } = await render({ ok: true, text: "wb pr create 'x'", needsEdit: false })
    expect(button.textContent?.trim()).toBe('Copy template')
    expect(button.getAttribute('aria-label')).toBe('Copy template wb pr create: commit')
    expect(build).not.toHaveBeenCalled()
    vi.useFakeTimers()
    button.click()
    await vi.advanceTimersByTimeAsync(0)
    fixture.detectChanges()
    expect(copy).toHaveBeenCalledWith("wb pr create 'x'")
    expect(button.textContent?.trim()).toBe('Copied')
    expect(button.classList.contains('done')).toBe(true)
    expect(status.textContent).toBe('Copied')
    await vi.advanceTimersByTimeAsync(COPIED_FEEDBACK_MS)
    fixture.detectChanges()
    expect(button.textContent?.trim()).toBe('Copy template')
    expect(status.textContent).toBe('')
  })

  it('can be quiet, and icon-only: the word stays for assistive technology and as the tooltip, and shows again once pressed', async () => {
    const { fixture, button } = await render({ ok: true, text: 'x', needsEdit: false })
    expect(button.classList.contains('quiet')).toBe(false)
    expect(button.querySelector('.visually-hidden')).toBeNull()
    expect(button.getAttribute('title')).toBeNull()
    fixture.componentRef.setInput('quiet', true)
    fixture.componentRef.setInput('iconOnly', true)
    fixture.detectChanges()
    expect(button.classList.contains('quiet')).toBe(true)
    expect(button.classList.contains('icon-only')).toBe(true)
    expect(button.querySelector('.visually-hidden')?.textContent).toBe('Copy template')
    expect(button.getAttribute('title')).toBe('Copy template wb pr create: commit')
    expect(button.getAttribute('aria-label')).toBe('Copy template wb pr create: commit')
    button.click()
    await vi.waitFor(() => {
      fixture.detectChanges()
      expect(button.textContent?.trim()).toBe('Copied')
    })
    expect(button.querySelector('.visually-hidden')).toBeNull()
  })

  it('says so when the browser refused', async () => {
    const { fixture, button, status } = await render({ ok: true, text: 'wb x', needsEdit: false }, false)
    vi.useFakeTimers()
    button.click()
    await vi.advanceTimersByTimeAsync(0)
    fixture.detectChanges()
    expect(button.textContent?.trim()).toBe('Copy failed')
    expect(button.classList.contains('failed')).toBe(true)
    expect(status.textContent).toBe('Copy failed')
  })

  it('copies nothing for a command the library refused, and says why', async () => {
    const { fixture, copy, button, status } = await render({ ok: false, reason: 'a task name may not start with -' })
    vi.useFakeTimers()
    button.click()
    await vi.advanceTimersByTimeAsync(0)
    fixture.detectChanges()
    expect(copy).not.toHaveBeenCalled()
    expect(button.textContent?.trim()).toBe('Not copyable')
    expect(button.getAttribute('title')).toBe('a task name may not start with -')
    expect(status.textContent).toBe('a task name may not start with -')
  })

  it('does nothing once its page has gone', async () => {
    const { fixture, copy, button } = await render({ ok: true, text: 'wb x', needsEdit: false })
    button.click()
    fixture.destroy()
    await Promise.resolve()
    await Promise.resolve()
    expect(copy).not.toHaveBeenCalled()
  })

  it('does not set its state when the page goes while the browser answers', async () => {
    const { fixture, copy, button } = await render({ ok: true, text: 'wb x', needsEdit: false })
    copy.mockImplementation(async () => {
      fixture.destroy()
      return true
    })
    button.click()
    await new Promise((done) => setTimeout(done, 0))
    expect(copy).toHaveBeenCalled()
  })
})
