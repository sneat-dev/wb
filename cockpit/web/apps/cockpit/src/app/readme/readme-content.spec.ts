import { ComponentFixture, TestBed } from '@angular/core/testing'
import { ID_PREFIX, MAX_PARSED_CHARACTERS } from './markdown-dom'
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
