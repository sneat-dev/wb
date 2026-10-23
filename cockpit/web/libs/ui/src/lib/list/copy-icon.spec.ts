import { TestBed } from '@angular/core/testing'
import { ClipboardWriter } from '../control/clipboard'
import { COPIED_MS, CopyIcon } from './copy-icon'
import { ListAnnouncer } from './list-announcer'

async function render(copy: () => Promise<boolean>, extra: Record<string, unknown> = {}, announcer = true) {
  TestBed.resetTestingModule()
  const say = vi.fn()
  TestBed.configureTestingModule({ providers: [{ provide: ClipboardWriter, useValue: { copy } }, ...(announcer ? [{ provide: ListAnnouncer, useValue: { say } }] : [])] })
  const fixture = TestBed.createComponent(CopyIcon)
  fixture.componentRef.setInput('value', 'a-very-long-task-name-that-the-cell-shortens')
  fixture.componentRef.setInput('label', 'Copy task name')
  for (const [name, value] of Object.entries(extra)) fixture.componentRef.setInput(name, value)
  await fixture.whenStable()
  return { fixture, button: fixture.nativeElement.querySelector('button') as HTMLButtonElement, say }
}

describe('CopyIcon', () => {
  afterEach(() => vi.useRealTimers())

  // cockpit-views#ac:copy-buttons-copy-the-full-value
  it('puts the full value in the clipboard, shows a tick and says Copied through the list, and does not reach the row', async () => {
    const copy = vi.fn(async () => true)
    const { fixture, button, say } = await render(copy)
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    const reached = vi.fn()
    fixture.nativeElement.addEventListener('click', reached)
    expect(button.getAttribute('aria-label')).toBe('Copy task name')
    button.click()
    await vi.waitFor(() => expect(say).toHaveBeenCalledWith('Copied'))
    expect(copy).toHaveBeenCalledWith('a-very-long-task-name-that-the-cell-shortens')
    expect(reached).not.toHaveBeenCalled()
    fixture.detectChanges()
    const tick = button.innerHTML
    button.click()
    await vi.waitFor(() => expect(say).toHaveBeenCalledTimes(2))
    vi.advanceTimersByTime(COPIED_MS)
    fixture.detectChanges()
    expect(button.innerHTML).not.toBe(tick)
  })

  it('says Copy failed when the browser refused, and works without a list to announce through', async () => {
    const failed = await render(async () => false)
    failed.button.click()
    await vi.waitFor(() => expect(failed.say).toHaveBeenCalledWith('Copy failed'))
    const alone = await render(async () => true, {}, false)
    alone.button.click()
    await alone.fixture.whenStable()
    const refused = await render(async () => false, {}, false)
    refused.button.click()
    await refused.fixture.whenStable()
  })

  it('is out of the tab order in a list row, in it by default, and stops its timer when destroyed', async () => {
    expect((await render(async () => true)).button.hasAttribute('tabindex')).toBe(false)
    const { fixture, button } = await render(async () => true, { tabbable: false })
    expect(button.getAttribute('tabindex')).toBe('-1')
    const clear = vi.spyOn(globalThis, 'clearTimeout')
    fixture.destroy()
    expect(clear).toHaveBeenCalled()
  })
})
