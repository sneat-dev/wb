import { ComponentFixture, TestBed } from '@angular/core/testing'
import { ID_PREFIX, MAX_PARSED_CHARACTERS, SYNC_PARSED_CHARACTERS } from './markdown-dom'
import * as parser from 'marked'
import { ReadmeContent } from './readme-content'

/** The renderer is fetched when a README is first shown: wait for it to have drawn, then for the view to follow. */
async function drawn(fixture: ComponentFixture<ReadmeContent>) {
  await fixture.whenStable()
  await new Promise((done) => setTimeout(done))
  await fixture.whenStable()
}

async function render(source: string) {
  const fixture = TestBed.createComponent(ReadmeContent)
  fixture.componentRef.setInput('source', source)
  await drawn(fixture)
  return { fixture, root: fixture.nativeElement as HTMLElement }
}

describe('ReadmeContent', () => {
  it('renders the Markdown as safe DOM and replaces it when the source changes', async () => {
    const { fixture, root } = await render('# Title\n\nSome *text*.\n')
    expect(root.querySelector('article.readme-content h4')?.textContent).toBe('Title')
    expect(root.querySelector('.readme-failed')).toBeNull()
    fixture.componentRef.setInput('source', 'Other.\n')
    await drawn(fixture)
    expect(root.querySelector('h4')).toBeNull()
    expect(root.querySelector('p')?.textContent).toBe('Other.')
  })

  it('drops a source that was replaced before the renderer had drawn it', async () => {
    const fixture = TestBed.createComponent(ReadmeContent)
    fixture.componentRef.setInput('source', '# First\n')
    fixture.detectChanges()
    // The renderer is on its way for the first source when the second arrives.
    fixture.componentRef.setInput('source', '# Second\n')
    fixture.detectChanges()
    await drawn(fixture)
    const root = fixture.nativeElement as HTMLElement
    expect([...root.querySelectorAll('h4')].map((heading) => heading.textContent)).toEqual(['Second'])
  })

  // cockpit#ac:hostile-readme-is-inert
  it('shows a hostile README as inert text with no element the source named', async () => {
    const { root } = await render('<script>alert(1)</script>\n\n<img src=x onerror=alert(2)>\n\n[x](javascript:alert(3))\n')
    expect(root.querySelector('script, img, a, style, iframe')).toBeNull()
    expect(root.textContent).toContain('<script>alert(1)</script>')
    expect(root.textContent).toContain('<img src=x onerror=alert(2)>')
    for (const element of root.querySelectorAll('*')) expect(element.getAttributeNames().filter((name) => name.startsWith('on'))).toEqual([])
  })

  it('scrolls to the heading an in-page link names without navigating', async () => {
    const { root } = await render('[go](#Target) [out](https://example.com/)\n\n# Target\n')
    const heading = root.querySelector(`[id="${ID_PREFIX}target"]`) as HTMLElement
    const scrolled = vi.fn()
    heading.scrollIntoView = scrolled
    const jump = root.querySelector('a.jump') as HTMLAnchorElement
    const event = new MouseEvent('click', { bubbles: true, cancelable: true })
    jump.dispatchEvent(event)
    expect(event.defaultPrevented).toBe(true)
    expect(scrolled).toHaveBeenCalledTimes(1)
  })

  it('does not navigate for an in-page link whose heading is not there, and leaves an external link alone', async () => {
    const { root } = await render('[go](#nowhere)\n\n[out](https://example.com/)\n')
    const missing = new MouseEvent('click', { bubbles: true, cancelable: true })
    root.querySelector('a.jump')?.dispatchEvent(missing)
    expect(missing.defaultPrevented).toBe(true)
    const external = new MouseEvent('click', { bubbles: true, cancelable: true })
    root.querySelector('a[target]')?.dispatchEvent(external)
    expect(external.defaultPrevented).toBe(false)
    const outside = new MouseEvent('click', { bubbles: true, cancelable: true })
    root.querySelector('article')?.dispatchEvent(outside)
    expect(outside.defaultPrevented).toBe(false)
  })

  it('shows a notice, and none of the source, when the parser cannot take it', async () => {
    const lex = vi.spyOn(parser.Lexer, 'lex').mockImplementation(() => {
      throw new RangeError('Maximum call stack size exceeded')
    })
    const { fixture, root } = await render('anything')
    expect(root.querySelector('.readme-failed')?.textContent).toBe('The README could not be rendered.')
    expect(root.querySelector('article')?.childNodes).toHaveLength(0)
    lex.mockRestore()
    fixture.componentRef.setInput('source', 'fine')
    await drawn(fixture)
    expect(root.querySelector('.readme-failed')).toBeNull()
    expect(root.querySelector('p')?.textContent).toBe('fine')
  })

  /** A README of `blocks` paragraphs, each with its own heading, a little over `parts` times the part size. */
  const long = (blocks: number): string => Array.from({ length: blocks }, (_, n) => `# Part ${n}\n\n${'word '.repeat(2000)}\n\n`).join('')

  // cockpit#ac:large-readme-is-lexed-in-parts
  it('lexes a README over 64 Ki characters in parts: the first at once, never more than a part at a time, the rest when asked for', async () => {
    const source = long(20)
    expect(source.length).toBeGreaterThan(2 * SYNC_PARSED_CHARACTERS)
    expect(source.length).toBeLessThan(MAX_PARSED_CHARACTERS)
    const lex = vi.spyOn(parser.Lexer, 'lex')
    const { fixture, root } = await render(source)
    // Only the first part was lexed, and it is a part, not the whole.
    expect(lex).toHaveBeenCalledTimes(1)
    expect((lex.mock.calls[0][0] as string).length).toBeLessThan(SYNC_PARSED_CHARACTERS * 1.5)
    const firstHeadings = root.querySelectorAll('h4').length
    expect(firstHeadings).toBeGreaterThan(0)
    expect(firstHeadings).toBeLessThan(20)
    const button = root.querySelector('.readme-more button') as HTMLButtonElement
    expect(button.textContent?.replace(/\s+/g, ' ').trim()).toMatch(/^Show the rest \(\d+ more of \d+ parts\)$/)
    // The rest is drawn one part at a time, with the button saying where it is.
    button.click()
    fixture.detectChanges()
    expect(button.disabled).toBe(true)
    expect(button.textContent).toContain('Drawing the rest')
    button.click()
    await vi.waitFor(() => expect(root.querySelector('.readme-more')).toBeNull())
    expect(lex.mock.calls.length).toBeGreaterThan(2)
    for (const call of lex.mock.calls) expect((call[0] as string).length).toBeLessThan(SYNC_PARSED_CHARACTERS * 1.5 + 100)
    // Every heading is on the page, once, with its own id.
    const headings = [...root.querySelectorAll('h4')]
    expect(headings).toHaveLength(20)
    expect(new Set(headings.map((heading) => heading.id)).size).toBe(20)
    lex.mockRestore()
  })

  it('draws the rest once however many times it is asked for', async () => {
    const lex = vi.spyOn(parser.Lexer, 'lex')
    const { fixture, root } = await render(long(12))
    const component = fixture.componentInstance as unknown as { showRest(): Promise<void> }
    const first = component.showRest()
    // Asked again while it is drawing: nothing more is started.
    await component.showRest()
    await first
    await vi.waitFor(() => expect(root.querySelector('.readme-more')).toBeNull())
    expect(root.querySelectorAll('h4')).toHaveLength(12)
    const parts = lex.mock.calls.length
    expect(parts).toBeGreaterThan(1)
    lex.mockRestore()
  })

  it('draws a README that fits one part in one go, with no button', async () => {
    const { root } = await render(long(2))
    expect(root.querySelectorAll('h4')).toHaveLength(2)
    expect(root.querySelector('.readme-more')).toBeNull()
  })

  it('drops the parts still to draw when the source is replaced, and starts over for the new one', async () => {
    const { fixture, root } = await render(long(20))
    ;(root.querySelector('.readme-more button') as HTMLButtonElement).click()
    fixture.componentRef.setInput('source', '# Other\n')
    await drawn(fixture)
    await new Promise((done) => setTimeout(done, 20))
    expect([...root.querySelectorAll('h4')].map((heading) => heading.textContent)).toEqual(['Other'])
    expect(root.querySelector('.readme-more')).toBeNull()
  })

  it('says it could not draw the rest when the parser cannot take a part, and keeps what is drawn', async () => {
    const lex = vi.spyOn(parser.Lexer, 'lex')
    const { root } = await render(long(20))
    const drawnBefore = root.querySelectorAll('h4').length
    lex.mockImplementation(() => {
      throw new RangeError('Maximum call stack size exceeded')
    })
    ;(root.querySelector('.readme-more button') as HTMLButtonElement).click()
    await vi.waitFor(() => expect(root.querySelector('.readme-failed')).not.toBeNull())
    expect(root.querySelectorAll('h4')).toHaveLength(drawnBefore)
    expect(root.querySelector('.readme-more')).toBeNull()
    lex.mockRestore()
  })

  it('shows a source over the parse budget as one block of text, without parsing it, and says so', async () => {
    const lex = vi.spyOn(parser.Lexer, 'lex')
    const big = `# Title <b onclick=1>\n${'x'.repeat(MAX_PARSED_CHARACTERS)}`
    const { fixture, root } = await render(big)
    expect(lex).not.toHaveBeenCalled()
    expect(root.querySelector('.readme-notice')?.textContent).toContain('plain text')
    expect(root.querySelector('article > pre')?.textContent).toBe(big)
    expect(root.querySelector('b, h4')).toBeNull()
    fixture.componentRef.setInput('source', '# Small\n')
    await drawn(fixture)
    expect(root.querySelector('.readme-notice')).toBeNull()
    expect(root.querySelector('h4')?.textContent).toBe('Small')
    lex.mockRestore()
  })
})
