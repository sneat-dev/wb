import type { Token } from 'marked'
import { ID_PREFIX, MAX_DEPTH, MAX_PARSED_CHARACTERS, decodeEntities, renderMarkdown, renderPlain, renderTokens, resolveLink, slug } from './markdown-dom'

function render(source: string): HTMLElement {
  const root = document.createElement('div')
  root.appendChild(renderMarkdown(source, document))
  return root
}

/** Every element and attribute a render may contain, and nothing else. */
const TAGS = new Set(['A', 'BLOCKQUOTE', 'BR', 'CODE', 'DEL', 'EM', 'H4', 'H5', 'H6', 'HR', 'LI', 'OL', 'P', 'PRE', 'STRONG', 'TABLE', 'TBODY', 'TD', 'TH', 'THEAD', 'TR', 'UL'])
const ATTRIBUTES = new Set(['href', 'rel', 'target', 'id', 'class', 'start', 'scope'])
const FORBIDDEN_ELEMENTS = ['script', 'style', 'iframe', 'object', 'embed', 'form', 'base', 'meta', 'link', 'svg', 'img', 'input', 'button', 'video', 'audio', 'source', 'math', 'template', 'noscript']

/**
 * The inertness the README requirements ask for, asserted on any render: only
 * allow-listed elements and attributes, no handler or style attribute, no
 * loading element of any kind, every URL in any attribute http(s) or in-page,
 * every external link opening with rel="noopener noreferrer".
 */
function expectInert(root: HTMLElement): void {
  for (const name of FORBIDDEN_ELEMENTS) expect(root.querySelector(name), `a ${name} element`).toBeNull()
  for (const element of root.querySelectorAll('*')) {
    expect(TAGS.has(element.tagName), `element ${element.tagName}`).toBe(true)
    for (const attribute of element.getAttributeNames()) {
      expect(ATTRIBUTES.has(attribute), `attribute ${attribute}`).toBe(true)
      expect(attribute.startsWith('on')).toBe(false)
      expect(attribute).not.toBe('style')
      const value = element.getAttribute(attribute) as string
      expect(value).not.toMatch(/^\s*(javascript|data|vbscript):/i)
      if (attribute === 'src' || attribute === 'srcset') throw new Error('a loading attribute')
    }
    if (element.hasAttribute('href')) {
      const href = element.getAttribute('href') as string
      expect(/^https?:\/\//.test(href) || href.startsWith(`#${ID_PREFIX}`), `href ${href}`).toBe(true)
      if (!href.startsWith('#')) {
        expect(element.getAttribute('rel')).toBe('noopener noreferrer')
        expect(element.getAttribute('target')).toBe('_blank')
      }
    }
    if (element.hasAttribute('id')) expect((element.getAttribute('id') as string).startsWith(ID_PREFIX)).toBe(true)
  }
}

const text = (root: Element) => (root.textContent as string).replace(/\s+/g, ' ').trim()

describe('renderMarkdown', () => {
  it('renders the common structure with page-relative heading levels', () => {
    const root = render('# One\n\n## Two\n\n###### Six\n\n####### Seven\n\nSome *em*, **strong**, ~~gone~~, `code` and a  \nbreak.\n\n---\n')
    expect([...root.querySelectorAll('h4,h5,h6')].map((heading) => `${heading.tagName} ${heading.textContent}`)).toEqual(['H4 One', 'H5 Two', 'H6 Six'])
    expect(root.querySelector('em')?.textContent).toBe('em')
    expect(root.querySelector('strong')?.textContent).toBe('strong')
    expect(root.querySelector('del')?.textContent).toBe('gone')
    expect(root.querySelector('p code')?.textContent).toBe('code')
    expect(root.querySelector('br')).not.toBeNull()
    expect(root.querySelector('hr')).not.toBeNull()
    expect(text(root)).toContain('####### Seven')
    expectInert(root)
  })

  it('gives each heading a unique id under the reserved prefix, and none to a heading with no words', () => {
    const root = render('# Setup & Run\n\n# Setup & Run\n\n# Setup & Run\n\n# !!!\n')
    expect([...root.querySelectorAll('h4')].map((heading) => heading.getAttribute('id'))).toEqual([`${ID_PREFIX}setup-run`, `${ID_PREFIX}setup-run-1`, `${ID_PREFIX}setup-run-2`, null])
    expectInert(root)
  })

  it('renders lists, tight and loose, ordered with its start, nested, and task items as text', () => {
    const tight = render('- a\n- b\n  - nested\n')
    expect([...tight.querySelectorAll('ul')].map((list) => list.parentElement?.tagName)).toEqual(['DIV', 'LI'])
    expect(tight.querySelectorAll('li p')).toHaveLength(0)
    const ordered = render('3. three\n4. four\n\ntext\n\n1. one\n')
    expect([...ordered.querySelectorAll('ol')].map((list) => list.getAttribute('start'))).toEqual(['3', null])
    expect(text(render('- [x] done\n- [ ] todo\n'))).toBe('\u2611 done\u2610 todo')
    const loose = render('- loose\n\n  para\n\n- second\n')
    expect(loose.querySelectorAll('li p').length).toBeGreaterThan(1)
    for (const root of [tight, ordered, loose]) expectInert(root)
  })

  it('renders blockquotes, fenced and indented code as text, and tables with alignment classes', () => {
    const root = render('> quoted\n> > deeper\n\n```js\nlet a = "<b>" && 1\n```\n\n    indented\n\n| a | b | c |\n|:--|:-:|--:|\n| 1 | 2 | 3 |\n')
    expect(root.querySelectorAll('blockquote')).toHaveLength(2)
    expect(root.querySelector('pre code')?.textContent).toBe('let a = "<b>" && 1')
    expect(root.querySelectorAll('pre')[1].textContent).toBe('indented')
    expect([...root.querySelectorAll('th')].map((cell) => [cell.textContent, cell.getAttribute('class'), cell.getAttribute('scope')])).toEqual([
      ['a', 'align-left', 'col'],
      ['b', 'align-center', 'col'],
      ['c', 'align-right', 'col'],
    ])
    expect([...root.querySelectorAll('td')].map((cell) => cell.getAttribute('class'))).toEqual(['align-left', 'align-center', 'align-right'])
    expectInert(root)
  })

  it('renders a table whose columns have no alignment without a class', () => {
    const root = render('| a |\n|---|\n| 1 |\n')
    expect(root.querySelector('th')?.hasAttribute('class')).toBe(false)
    expect(root.querySelector('td')?.hasAttribute('class')).toBe(false)
  })

  it('shows character references as the characters, but never inside code', () => {
    const root = render('a &amp; b &lt;i&gt; &#65; &#x42; &bogus; &#0; &AMP;\n\n`&amp;`\n\n\\*not em\\*\n')
    expect(text(root)).toContain('a & b <i> A B &bogus; \ufffd &')
    expect(root.querySelector('code')?.textContent).toBe('&amp;')
    expect(text(root)).toContain('*not em*')
    expect(root.querySelector('i')).toBeNull()
  })

  it('leaves a link reference definition out and resolves a use of it', () => {
    const root = render('[site][ref]\n\n[ref]: https://example.com/page\n')
    expect(root.querySelectorAll('a')).toHaveLength(1)
    expect(root.querySelector('a')?.getAttribute('href')).toBe('https://example.com/page')
    expect(text(root)).toBe('site')
  })

  it('renders nothing for an empty README', () => {
    expect(render('').childNodes).toHaveLength(0)
    expect(render('\n\n').childNodes).toHaveLength(0)
  })
})

describe('links', () => {
  it('opens an http(s) link in a new tab, without opener or referrer, from the normalised address', () => {
    const root = render('[a](http://example.com/x?y=1 "title") [b](HTTPS://Example.com:8443/p) <https://auto.example/?q=1&r=2>')
    const links = [...root.querySelectorAll('a')]
    expect(links.map((link) => link.getAttribute('href'))).toEqual(['http://example.com/x?y=1', 'https://example.com:8443/p', 'https://auto.example/?q=1&r=2'])
    for (const link of links) {
      expect(link.getAttribute('rel')).toBe('noopener noreferrer')
      expect(link.getAttribute('target')).toBe('_blank')
      expect(link.hasAttribute('title')).toBe(false)
    }
    expectInert(root)
  })

  it('links within the README to a heading by its id, and an in-page link to nothing is text', () => {
    const root = render('[go](#Setup%20&%20Run) [bad escape](#100%) [nothing](#) [punct](#!!!)\n\n# Setup & Run\n')
    const links = [...root.querySelectorAll('a')]
    expect(links.map((link) => [link.getAttribute('href'), link.getAttribute('class'), link.hasAttribute('target')])).toEqual([
      [`#${ID_PREFIX}setup-run`, 'jump', false],
      [`#${ID_PREFIX}100`, 'jump', false],
    ])
    expect(text(root)).toContain('nothing punct')
    expectInert(root)
  })

  // The README requirements: no javascript:, data: or vbscript: URL survives in any attribute.
  it('keeps no javascript:, data: or vbscript: destination, whatever its spelling, as an attribute', () => {
    const hostile = [
      '[a](javascript:alert(1))',
      '[b](JaVaScRiPt:alert(1))',
      '[c]( javascript:alert(1))',
      '[d](&#106;avascript:alert(1))',
      '[e](java&#9;script:alert(1))',
      '[f](data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==)',
      '[g](vbscript:msgbox(1))',
      '[h](<javascript:alert(1)>)',
      '<javascript:alert(1)>',
      '![i](data:image/svg+xml,<svg onload=alert(1)>)',
      '![j](javascript:alert(1))',
      '[k][r]\n\n[r]: javascript:alert(1)',
      '[l](file:///etc/passwd)',
      '[m](//evil.example/x)',
      '[n](ftp://example.com/x)',
      '[o](blob:https://example.com/x)',
    ].join('\n\n')
    const root = render(hostile)
    expectInert(root)
    expect(root.querySelectorAll('a')).toHaveLength(0)
    expect(text(root)).toContain('abcdefgh')
  })

  it('shows relative links, and links with credentials, as text only', () => {
    const root = render('[doc](docs/guide.md) [up](../x) [abs](/etc/passwd) [creds](https://user:pw@example.com/)')
    expect(root.querySelectorAll('a')).toHaveLength(0)
    expect(text(root)).toBe('doc up abs creds')
  })
})

describe('links to this machine', () => {
  it('shows a link into the page\'s own origin, or to any loopback host, as text', () => {
    const root = document.createElement('div')
    root.appendChild(
      renderMarkdown(
        [
          '[own](https://cockpit.example/api/v1/cockpit/fleet)',
          '[local](http://localhost:8766/) [sub](http://app.localhost/) [v4](http://127.0.0.1:8766/) [v4b](http://127.9.9.9/) [any](http://0.0.0.0/)',
          '[v6](http://[::1]:8766/) [any6](http://[::]/) [mapped](http://[::ffff:127.0.0.1]/)',
          '[dot](http://localhost./) [dec](http://2130706433/) [hex](http://0x7f.1/) [short](http://127.1/) [subdot](http://app.localhost./)',
          '[other](https://cockpit.example.org/) [fine](https://example.com/)',
        ].join('\n\n'),
        document,
        'https://cockpit.example',
      ),
    )
    expect([...root.querySelectorAll('a')].map((link) => link.getAttribute('href'))).toEqual(['https://cockpit.example.org/', 'https://example.com/'])
    expect(text(root)).toContain('local sub v4 v4b any')
    expect(text(root)).toContain('v6 any6 mapped')
    expect(text(root)).toContain('dot dec hex short subdot')
    expectInert(root)
  })

  it('takes the page\'s origin from the document by default', () => {
    const own = `${document.location.origin}/x`
    const root = render(`[own](${own}) [fine](https://example.com/)`)
    expect([...root.querySelectorAll('a')].map((link) => link.getAttribute('href'))).toEqual(['https://example.com/'])
    const detached = document.implementation.createHTMLDocument('')
    const fragment = renderMarkdown('[fine](https://example.com/)', detached)
    expect(fragment.querySelector('a')?.getAttribute('href')).toBe('https://example.com/')
  })

  it('never nests an anchor: an image or a link inside a link is text', () => {
    const root = render('[![logo](https://example.com/logo.png)](https://example.com/page) [a](https://example.com/a)')
    expect(root.querySelectorAll('a a')).toHaveLength(0)
    expect([...root.querySelectorAll('a')].map((link) => link.getAttribute('href'))).toEqual(['https://example.com/page', 'https://example.com/a'])
    expect(text(root)).toContain('Image: logo')
    expectInert(root)
  })
})

describe('renderPlain', () => {
  it('is one text block, nothing parsed', () => {
    const root = document.createElement('div')
    root.appendChild(renderPlain('# <b onclick=1>x</b>\n[a](javascript:1)', document))
    expect(root.children).toHaveLength(1)
    expect(root.querySelector('pre')?.textContent).toBe('# <b onclick=1>x</b>\n[a](javascript:1)')
    expect(MAX_PARSED_CHARACTERS).toBe(262144)
  })
})

describe('images', () => {
  it('never emits an image: an http(s) one is a link to it, any other only its alt text', () => {
    const root = render('![logo & mark](https://tracker.example/pixel.png) ![](https://tracker.example/b.png) ![local](./logo.png) ![x][r]\n\n[r]: https://tracker.example/ref.png\n')
    expect(root.querySelector('img')).toBeNull()
    expect([...root.querySelectorAll('a')].map((link) => [link.getAttribute('href'), link.textContent])).toEqual([
      ['https://tracker.example/pixel.png', 'Image: logo & mark'],
      ['https://tracker.example/b.png', 'Image: no description'],
      ['https://tracker.example/ref.png', 'Image: x'],
    ])
    expect(text(root)).toContain('Image: local')
    expectInert(root)
  })
})

describe('raw HTML', () => {
  it('is shown as text and never becomes an element or an attribute', () => {
    const source = [
      '<script>window.__pwned = true; alert(1)</script>',
      '',
      'Inline <b onclick="alert(2)">bold</b> and <img src=x onerror="alert(3)"> and <a href="javascript:alert(4)" onmouseover="alert(5)">x</a>.',
      '',
      '<iframe src="https://evil.example/"></iframe>',
      '',
      '<object data="x"></object><embed src="x"><form action="https://evil.example/"><input name=a></form><base href="https://evil.example/"><meta http-equiv="refresh" content="0;url=https://evil.example/"><link rel="stylesheet" href="https://evil.example/x.css">',
      '',
      '<svg onload="alert(6)"><script>alert(7)</script></svg>',
      '',
      '<div style="position:fixed;inset:0" id="app-root">cover</div>',
      '',
      '<style>body{display:none}</style>',
      '',
      '<!-- comment --> <![CDATA[ x ]]> <?php echo 1 ?>',
    ].join('\n')
    const root = render(source)
    expectInert(root)
    expect((globalThis as { __pwned?: boolean }).__pwned).toBeUndefined()
    expect(text(root)).toContain('<script>window.__pwned = true; alert(1)</script>')
    expect(text(root)).toContain('<b onclick="alert(2)">bold</b>')
    expect(text(root)).toContain('<img src=x onerror="alert(3)">')
    expect(root.querySelectorAll('pre.raw-html').length).toBeGreaterThan(0)
    expect(root.querySelectorAll('[class]:not(.raw-html)')).toHaveLength(0)
  })

  it('cannot carry a handler or style attribute through any inline or link construct', () => {
    const root = render('[a](https://example.com "x\\" onmouseover=\\"alert(1)") **<u onclick=1>** *<i style=x>* `<b onclick=1>` ~~<s onclick=1>~~\n\n> <b onclick=1>\n\n- <b onclick=1>\n\n| <b onclick=1> |\n|---|\n| <i onclick=1> |\n')
    expectInert(root)
    expect(root.querySelector('b,i,u,s')).toBeNull()
  })
})

describe('depth and unknown tokens', () => {
  const nested = (depth: number) => `${'> '.repeat(depth)}deep\n`

  it('shows what is nested past the limit as literal text', () => {
    const root = render(nested(MAX_DEPTH + 6))
    expect(root.querySelectorAll('blockquote').length).toBeLessThanOrEqual(MAX_DEPTH + 1)
    expect(text(root)).toContain('deep')
    expectInert(root)
  })

  it('renders at the limit and one past it for tokens built by hand', () => {
    const emphasis = (inner: Token): Token => ({ type: 'em', raw: '*x*', text: 'x', tokens: [inner] }) as Token
    let inner: Token = { type: 'text', raw: 'core', text: 'core' } as Token
    for (let level = 0; level < MAX_DEPTH + 4; level++) inner = emphasis(inner)
    const root = document.createElement('div')
    root.appendChild(renderTokens([{ type: 'paragraph', raw: 'p', text: 'p', tokens: [inner] } as Token], document))
    expect(root.querySelectorAll('em').length).toBeLessThanOrEqual(MAX_DEPTH)
    expect(text(root)).toContain('*x*')
    expectInert(root)
  })

  it('shows a token type it does not know as its source text, in a block and in an inline', () => {
    const root = document.createElement('div')
    root.appendChild(
      renderTokens(
        [
          { type: 'future', raw: '<future onclick=1>' } as Token,
          { type: 'paragraph', raw: 'p', text: 'p', tokens: [{ type: 'future', raw: '<inline onclick=1>' } as Token, { type: 'strong', raw: '**x**', text: 'x' } as Token] } as Token,
          { type: 'text', raw: 'plain &amp; text', text: 'plain &amp; text' } as Token,
        ],
        document,
      ),
    )
    expect(text(root)).toBe('<future onclick=1><inline onclick=1>plain & text')
    expect(root.querySelector('strong')?.childNodes).toHaveLength(0)
    expectInert(root)
  })
})

describe('tokens by hand', () => {
  it('renders the inline tokens the parser puts in other shapes, and leaves out those that show nothing', () => {
    const inline = (...tokens: Token[]) => [{ type: 'paragraph', raw: 'p', text: 'p', tokens } as Token]
    const root = document.createElement('div')
    root.appendChild(
      renderTokens(
        [
          ...inline(
            { type: 'text', raw: 'outer', text: 'outer', tokens: [{ type: 'text', raw: 'inner &lt;', text: 'inner &lt;' } as Token] } as Token,
            { type: 'escape', raw: '\\*', text: '*' } as Token,
            { type: 'checkbox', raw: '[x] ', checked: true } as Token,
            { type: 'checkbox', raw: '[ ] ', checked: false } as Token,
            { type: 'def', raw: '[a]: b', tag: 'a', href: 'b', title: '' } as Token,
            { type: 'space', raw: '\n' } as Token,
            { type: 'html', raw: '<b>', text: '<b>', pre: false, block: false } as Token,
          ),
          { type: 'blockquote', raw: '>', text: '' } as Token,
          { type: 'list', raw: '-', ordered: true, start: '', loose: false, items: [{ type: 'list_item', raw: '1.', task: false, loose: false, text: '' }] } as Token,
        ],
        document,
      ),
    )
    expect(text(root)).toBe('inner <*\u2611 \u2610 <b>')
    expect(root.querySelector('blockquote')?.childNodes).toHaveLength(0)
    expect(root.querySelector('ol')?.hasAttribute('start')).toBe(false)
    expect(root.querySelector('li')?.childNodes).toHaveLength(0)
    expectInert(root)
  })
})

describe('helpers', () => {
  it('slug keeps lower-case words joined by hyphens', () => {
    expect(slug('  Hello, World! 2 ')).toBe('hello-world-2')
    expect(slug('---')).toBe('')
  })

  it('resolveLink allows only http(s) and fragments', () => {
    expect(resolveLink('https://own.example/x', 'https://own.example')).toBeNull()
    expect(resolveLink(' https://example.com ')).toEqual({ kind: 'external', href: 'https://example.com/' })
    expect(resolveLink('#A b')).toEqual({ kind: 'page', id: `${ID_PREFIX}a-b` })
    for (const href of ['', 'javascript:alert(1)', 'mailto:a@example.com', 'tel:1', '/x', 'x', '#', 'http://u:p@h/', 'https://']) expect(resolveLink(href)).toBeNull()
  })

  it('decodeEntities decodes only the five named references', () => {
    expect(decodeEntities('&AMP; &Lt; &gt; &quot; &apos; &#65;')).toBe('& < > " \' &#65;')
    expect(decodeEntities('&nbsp; &amp')).toBe('&nbsp; &amp')
  })
})
