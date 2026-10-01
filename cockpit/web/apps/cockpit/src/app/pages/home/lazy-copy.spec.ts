import { TestBed } from '@angular/core/testing'
import { CopyCommand } from '@cockpit/fleet-data'
import { ClipboardWriter, COPIED_FEEDBACK_MS } from '@cockpit/ui/control'
import { LazyCopy } from './lazy-copy'

async function render(command: CopyCommand, copyResult = true) {
  const copy = vi.fn().mockResolvedValue(copyResult)
  TestBed.configureTestingModule({ providers: [{ provide: ClipboardWriter, useValue: { copy } }] })
  const fixture = TestBed.createComponent(LazyCopy)
  const build = vi.fn().mockResolvedValue(command)
  fixture.componentRef.setInput('build', build)
  fixture.componentRef.setInput('idleWord', 'Copy template')
  fixture.componentRef.setInput('label', 'Copy command template: commit')
  await fixture.whenStable()
  const root: HTMLElement = fixture.nativeElement
  return { fixture, copy, build, button: root.querySelector('button') as HTMLButtonElement, status: root.querySelector('[role="status"]') as HTMLElement }
}

describe('LazyCopy', () => {
  afterEach(() => vi.useRealTimers())

  it('builds the command only when pressed, copies it, says so, and returns to its word', async () => {
    const { fixture, copy, build, button, status } = await render({ ok: true, text: "wb pr create 'x'", needsEdit: false })
    expect(button.textContent?.trim()).toBe('Copy template')
    expect(button.getAttribute('aria-label')).toBe('Copy command template: commit')
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
