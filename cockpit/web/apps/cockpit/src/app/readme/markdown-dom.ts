import { Lexer, type Token, type Tokens } from 'marked'

// Untrusted Markdown to DOM, safe by construction.
//
// A README is shown in the origin that holds the owner's session, so nothing
// here ever turns text into markup: the parser (marked) is used only to lex the
// source into tokens, and the DOM is built from those tokens, node by node,
// with `createElement` on a fixed allow-list of tags, `createTextNode` for every
// piece of text, and `setAttribute` for a fixed allow-list of attributes whose
// values this file computes. No innerHTML, no sanitizer to configure, no
// bypassSecurityTrust*. The parser's own HTML output is never used.
//
// What a hostile README gets:
//   raw HTML      shown as literal text, never as an element;
//   images        shown as their alt text (a link to the image when its address
//                 is http or https), never as an img, so nothing is fetched;
//   links         http(s) only, normalised by the URL parser, opening with
//                 rel="noopener noreferrer"; an in-page link only to a heading
//                 of the same README; anything else (javascript:, data:,
//                 vbscript:, a relative path, a protocol-relative address,
//                 an address with credentials) is plain text;
//   attributes    none from the source: no style, no on*, no class, no id except
//                 the heading ids this file makes, under a reserved prefix.

/** The reserved prefix of every heading id a README produces, so none can collide with the application's own. */
export const ID_PREFIX = 'md-'

/** How deep blocks and inlines may nest before the rest is shown as literal text. */
export const MAX_DEPTH = 24

const LINK_PROTOCOLS = new Set(['http:', 'https:'])

type Link = { kind: 'external'; href: string } | { kind: 'page'; id: string }

/** Lower-case words joined by hyphens: the only characters an id carries. */
export function slug(text: string): string {
  return text
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

/** Hosts that are this machine: a README must not link the owner into the daemon's own origin or any local service. */
function isLoopbackHost(name: string): boolean {
  // A trailing dot names the same host; the URL parser has already turned
  // decimal, octal, hex and short IPv4 spellings into dotted form.
  const hostname = name.replace(/\.+$/, '')
  return (
    hostname === 'localhost' ||
    hostname.endsWith('.localhost') ||
    hostname === '0.0.0.0' ||
    /^127(\.\d{1,3}){3}$/.test(hostname) ||
    hostname === '[::1]' ||
    hostname === '[::]' ||
    hostname.startsWith('[::ffff:7f')
  )
}

/**
 * What a link destination is allowed to be, or null for text only. `ownOrigin`
 * is the page's own origin: a link into it, or to any loopback host, is text.
 */
export function resolveLink(href: string, ownOrigin = ''): Link | null {
  const trimmed = href.trim()
  if (trimmed.startsWith('#')) {
    const name = slug(decodeFragment(trimmed.slice(1)))
    return name ? { kind: 'page', id: ID_PREFIX + name } : null
  }
  let url: URL
  try {
    url = new URL(trimmed)
  } catch {
    return null
  }
  const plain =
    LINK_PROTOCOLS.has(url.protocol) &&
    url.hostname !== '' &&
    url.username === '' &&
    url.password === '' &&
    url.origin !== ownOrigin &&
    !isLoopbackHost(url.hostname)
  return plain ? { kind: 'external', href: url.href } : null
}

function decodeFragment(fragment: string): string {
  try {
    return decodeURIComponent(fragment)
  } catch {
    return fragment
  }
}

const NAMED_REFERENCE = /&(amp|lt|gt|quot|apos);/gi
const NAMED: Record<string, string> = { amp: '&', lt: '<', gt: '>', quot: '"', apos: "'" }

/**
 * Decodes the five named character references Markdown text keeps. The parser
 * has already decoded the numeric ones (an invalid one it turns into U+FFFD);
 * anything else stays as written.
 */
export function decodeEntities(text: string): string {
  return text.replace(NAMED_REFERENCE, (_whole, name: string) => NAMED[name.toLowerCase()])
}

type Tag = 'a' | 'blockquote' | 'br' | 'code' | 'del' | 'em' | 'h4' | 'h5' | 'h6' | 'hr' | 'li' | 'ol' | 'p' | 'pre' | 'strong' | 'table' | 'tbody' | 'td' | 'th' | 'thead' | 'tr' | 'ul'

class Builder {
  private readonly ids = new Set<string>()
  /** Whether the node being built is inside an anchor, which may not hold another. */
  private inLink = false

  constructor(
    private readonly doc: Document,
    private readonly ownOrigin: string,
  ) {}

  private element(parent: Node, tag: Tag, attributes: Record<string, string> = {}): HTMLElement {
    const created = this.doc.createElement(tag)
    for (const [name, value] of Object.entries(attributes)) created.setAttribute(name, value)
    parent.appendChild(created)
    return created
  }

  private text(parent: Node, value: string): void {
    parent.appendChild(this.doc.createTextNode(value))
  }

  /** The heading's id, unique within the README, or none when it has no usable words. */
  private headingId(text: string): Record<string, string> {
    const base = slug(decodeEntities(text))
    if (!base) return {}
    let id = ID_PREFIX + base
    for (let n = 1; this.ids.has(id); n++) id = `${ID_PREFIX}${base}-${n}`
    this.ids.add(id)
    return { id }
  }

  /** A link element for a destination, or null when the destination may not be a link or the node is inside one already. */
  private link(parent: Node, href: string): HTMLElement | null {
    const link = this.inLink ? null : resolveLink(href, this.ownOrigin)
    if (link === null) return null
    return link.kind === 'external'
      ? this.element(parent, 'a', { href: link.href, rel: 'noopener noreferrer', target: '_blank' })
      : this.element(parent, 'a', { href: `#${link.id}`, class: 'jump' })
  }

  inlines(parent: Node, tokens: Token[] | undefined, depth: number): void {
    for (const token of tokens ?? []) this.inline(parent, token, depth)
  }

  private inline(parent: Node, token: Token, depth: number): void {
    if (depth > MAX_DEPTH) return this.text(parent, token.raw)
    const next = depth + 1
    switch (token.type) {
      case 'text': {
        const text = token as Tokens.Text
        return text.tokens ? this.inlines(parent, text.tokens, next) : this.text(parent, decodeEntities(text.text))
      }
      case 'escape':
        return this.text(parent, (token as Tokens.Escape).text)
      case 'strong':
      case 'em':
      case 'del':
        return this.inlines(this.element(parent, token.type as 'strong' | 'em' | 'del'), (token as Tokens.Strong).tokens, next)
      case 'codespan':
        return this.text(this.element(parent, 'code'), (token as Tokens.Codespan).text)
      case 'br':
        this.element(parent, 'br')
        return
      case 'link': {
        const { href, tokens } = token as Tokens.Link
        // A destination that may not be a link leaves its text, not a dead anchor.
        const anchor = this.link(parent, href)
        const wasInLink = this.inLink
        this.inLink = this.inLink || anchor !== null
        this.inlines(anchor ?? parent, tokens, next)
        this.inLink = wasInLink
        return
      }
      case 'image': {
        const { href, text } = token as Tokens.Image
        const label = `Image: ${decodeEntities(text) || 'no description'}`
        return this.text(this.link(parent, href) ?? parent, label)
      }
      case 'html':
        // Raw HTML is text, exactly as written.
        return this.text(parent, (token as Tokens.HTML).text)
      case 'checkbox':
        return this.checkbox(parent, token as Tokens.Checkbox)
      case 'def':
      case 'space':
        return
      default:
        return this.text(parent, token.raw)
    }
  }

  /** A task item's box is text, never an input. */
  private checkbox(parent: Node, token: Tokens.Checkbox): void {
    this.text(parent, token.checked ? '\u2611 ' : '\u2610 ')
  }

  blocks(parent: Node, tokens: Token[] | undefined, depth: number): void {
    for (const token of tokens ?? []) this.block(parent, token, depth)
  }

  private block(parent: Node, token: Token, depth: number): void {
    if (depth > MAX_DEPTH) return this.text(this.element(parent, 'p'), token.raw)
    const next = depth + 1
    switch (token.type) {
      case 'space':
      case 'def':
        return
      case 'heading': {
        const heading = token as Tokens.Heading
        // The page's own headings are levels 2 and 3, so a README's start at 4.
        const tag = `h${Math.min(Math.max(heading.depth, 1) + 3, 6)}` as 'h4' | 'h5' | 'h6'
        return this.inlines(this.element(parent, tag, this.headingId(heading.text)), heading.tokens, next)
      }
      case 'paragraph':
        return this.inlines(this.element(parent, 'p'), (token as Tokens.Paragraph).tokens, next)
      case 'checkbox':
        // A task item's box leads the item, among its blocks.
        return this.checkbox(parent, token as Tokens.Checkbox)
      case 'text': {
        // A tight list item's text sits directly in the item.
        const text = token as Tokens.Text
        return text.tokens ? this.inlines(parent, text.tokens, next) : this.text(parent, decodeEntities(text.text))
      }
      case 'code':
        return this.text(this.element(this.element(parent, 'pre'), 'code'), (token as Tokens.Code).text)
      case 'blockquote':
        return this.blocks(this.element(parent, 'blockquote'), (token as Tokens.Blockquote).tokens, next)
      case 'list':
        return this.list(parent, token as Tokens.List, next)
      case 'hr':
        this.element(parent, 'hr')
        return
      case 'table':
        return this.table(parent, token as Tokens.Table, next)
      case 'html':
        // Raw HTML is shown as the text it is.
        return this.text(this.element(parent, 'pre', { class: 'raw-html' }), token.raw.trimEnd())
      default:
        return this.text(this.element(parent, 'p'), token.raw)
    }
  }

  private list(parent: Node, list: Tokens.List, depth: number): void {
    const numbered = list.ordered && typeof list.start === 'number' && Number.isSafeInteger(list.start) && list.start !== 1
    const container = this.element(parent, list.ordered ? 'ol' : 'ul', numbered ? { start: String(list.start) } : {})
    for (const item of list.items) this.blocks(this.element(container, 'li'), item.tokens, depth)
  }

  private table(parent: Node, table: Tokens.Table, depth: number): void {
    const element = this.element(parent, 'table')
    const row = (container: Node, cells: Tokens.TableCell[], tag: 'th' | 'td') => {
      const tr = this.element(container, 'tr')
      for (const cell of cells) {
        const attributes: Record<string, string> = cell.align ? { class: `align-${cell.align}` } : {}
        if (tag === 'th') attributes['scope'] = 'col'
        this.inlines(this.element(tr, tag, attributes), cell.tokens, depth)
      }
    }
    row(this.element(element, 'thead'), table.header, 'th')
    const body = this.element(element, 'tbody')
    for (const cells of table.rows) row(body, cells, 'td')
  }
}

/**
 * Builds the DOM for `source` in `doc`, which is never touched until the
 * fragment is attached. `origin` is the page's own origin, whose links and
 * those to loopback hosts are shown as text.
 */
export function renderMarkdown(source: string, doc: Document, origin: string = doc.location?.origin ?? ''): DocumentFragment {
  const fragment = doc.createDocumentFragment()
  new Builder(doc, origin).blocks(fragment, Lexer.lex(source), 0)
  return fragment
}

/** Builds the DOM for tokens already lexed; the tests use it for token types the parser does not produce today. */
export function renderTokens(tokens: Token[], doc: Document): DocumentFragment {
  const fragment = doc.createDocumentFragment()
  new Builder(doc, '').blocks(fragment, tokens, 0)
  return fragment
}

/** The README as one block of text, unparsed: for a source too large to lex in the browser. */
export function renderPlain(source: string, doc: Document): DocumentFragment {
  const fragment = doc.createDocumentFragment()
  const pre = doc.createElement('pre')
  pre.appendChild(doc.createTextNode(source))
  fragment.appendChild(pre)
  return fragment
}

export { MAX_PARSED_CHARACTERS } from './readme-limit'
