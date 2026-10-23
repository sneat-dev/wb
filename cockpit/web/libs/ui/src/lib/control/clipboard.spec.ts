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

  it('hands focus back to the element that had it, and leaves no field behind', async () => {
    stubClipboard(undefined)
    const input = document.createElement('input')
    document.body.appendChild(input)
    input.focus()
    stubExecCommand(true).mockImplementation(() => {
      // While copying, the selected field has focus (as a browser gives it).
      ;(document.querySelector('textarea') as HTMLTextAreaElement).focus()
      expect(document.activeElement?.tagName).toBe('TEXTAREA')
      return true
    })
    const before = document.body.children.length
    expect(await TestBed.inject(ClipboardWriter).copy('x')).toBe(true)
    expect(document.activeElement).toBe(input)
    expect(document.body.children).toHaveLength(before)
    // A refusal restores it too, and nothing focused is fine.
    stubExecCommand(false)
    expect(await TestBed.inject(ClipboardWriter).copy('x')).toBe(false)
    expect(document.activeElement).toBe(input)
    input.blur()
    ;(document.body as HTMLElement).focus()
    expect(await TestBed.inject(ClipboardWriter).copy('x')).toBe(false)
  })

  it('passes the text through exactly: no trailing newline or space is added to what is copied', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    stubClipboard(writeText)
    await TestBed.inject(ClipboardWriter).copy("wb worktree list 'fix-ci'")
    expect(writeText.mock.calls[0][0]).toBe("wb worktree list 'fix-ci'")
    expect(writeText.mock.calls[0][0]).not.toMatch(/\s$/)
    stubClipboard(undefined)
    let copied = ''
    stubExecCommand(true).mockImplementation(() => {
      copied = (document.querySelector('textarea') as HTMLTextAreaElement).value
      return true
    })
    await TestBed.inject(ClipboardWriter).copy("wb worktree list 'fix-ci'")
    expect(copied).toBe("wb worktree list 'fix-ci'")
  })
})
