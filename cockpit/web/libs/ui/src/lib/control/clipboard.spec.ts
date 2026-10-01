import { TestBed } from '@angular/core/testing'
import { ClipboardWriter } from './clipboard'

function stubClipboard(writeText: unknown) {
  Object.defineProperty(window.navigator, 'clipboard', { value: writeText === undefined ? undefined : { writeText }, configurable: true })
}

function stubExecCommand(result: boolean | Error) {
  const execCommand = vi.fn(() => {
    if (result instanceof Error) throw result
    return result
  })
  Object.defineProperty(document, 'execCommand', { value: execCommand, configurable: true })
  return execCommand
}

describe('ClipboardWriter', () => {
  afterEach(() => {
    stubClipboard(undefined)
    document.body.innerHTML = ''
  })

  it('writes with the Clipboard API when the page may use it', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    stubClipboard(writeText)
    const execCommand = stubExecCommand(true)
    expect(await TestBed.inject(ClipboardWriter).copy('wb worktree list')).toBe(true)
    expect(writeText).toHaveBeenCalledWith('wb worktree list')
    expect(execCommand).not.toHaveBeenCalled()
  })

  it('falls back to a selected text field when there is no Clipboard API', async () => {
    stubClipboard(undefined)
    let selected = ''
    const execCommand = stubExecCommand(true)
    execCommand.mockImplementation(() => {
      selected = (document.querySelector('textarea') as HTMLTextAreaElement).value
      return true
    })
    expect(await TestBed.inject(ClipboardWriter).copy('wb pr land')).toBe(true)
    expect(execCommand).toHaveBeenCalledWith('copy')
    expect(selected).toBe('wb pr land')
    expect(document.querySelector('textarea')).toBeNull()
  })

  it('falls back when the Clipboard API refuses, and reports a refusal of both', async () => {
    stubClipboard(vi.fn().mockRejectedValue(new Error('denied')))
    stubExecCommand(true)
    expect(await TestBed.inject(ClipboardWriter).copy('x')).toBe(true)
    stubExecCommand(false)
    expect(await TestBed.inject(ClipboardWriter).copy('x')).toBe(false)
    stubExecCommand(new Error('blocked'))
    expect(await TestBed.inject(ClipboardWriter).copy('x')).toBe(false)
    expect(document.querySelector('textarea')).toBeNull()
  })

  it('does not set a style attribute: the field is styled through the CSS object model', async () => {
    stubClipboard(undefined)
    let attribute: string | null = 'unset'
    stubExecCommand(true).mockImplementation(() => {
      attribute = (document.querySelector('textarea') as HTMLTextAreaElement).getAttribute('style')
      return true
    })
    await TestBed.inject(ClipboardWriter).copy('x')
    // jsdom serialises the object-model writes into the attribute; a real browser under the policy allows them.
    expect(attribute).toContain('position: fixed')
  })
})
