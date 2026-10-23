// The one pure matcher behind every list filter and the palette (REQ:list-filter-and-matcher).
// Grammar: whitespace-separated terms, "double quotes" keep spaces, a `*` or `?`
// makes a glob over the whole field value, a bare term is a substring, a `-`
// prefix excludes, `field:value` restricts to one declared field, case is ignored.
// No regular expressions: a glob is matched by a two-pointer scan whose step
// count is at most the pattern length times the value length.

/** The filter text is cut to this many characters before it is read. */
export const MAX_QUERY_LENGTH = 256
/** At most this many terms of the filter text are applied; the rest are ignored. */
export const MAX_TERMS = 16

/** One term of a filter. `value` and `raw` are lower-cased. */
export interface Term {
  /** A leading unquoted `-`: the term excludes the rows that match the rest of it. */
  negate: boolean
  /** The field of a `field:value` term; undefined for a bare term. */
  field?: string
  /** What follows the colon of a field term, or the whole term. */
  value: string
  /** The term without its `-`, colon and all: what a bare match uses when the field is not declared. */
  raw: string
  /** A field term whose value was quoted: matched whole and literally (`*` and `?` are ordinary characters). */
  exact?: boolean
}

const FIELD_NAME = /^[a-z][a-z0-9-]*$/

/** One character of a token, with whether it was inside double quotes. */
interface Mark {
  char: string
  quoted: boolean
}

/** Splits `input` into tokens on whitespace outside double quotes; the quotes themselves are dropped. */
function tokenize(input: string): Mark[][] {
  const tokens: Mark[][] = []
  let current: Mark[] = []
  let started = false
  let quoted = false
  for (const char of input) {
    if (char === '"') {
      quoted = !quoted
      started = true
    } else if (!quoted && /\s/.test(char)) {
      if (current.length > 0) tokens.push(current)
      current = []
      started = false
    } else {
      current.push({ char, quoted })
      started = true
    }
  }
  if (started && current.length > 0) tokens.push(current)
  return tokens
}

const text = (marks: readonly Mark[]): string => marks.map((mark) => mark.char).join('').toLowerCase()

/**
 * Reads the filter text into terms, applying the caps. Quotes protect what they
 * enclose: a quoted `-` does not exclude and a quoted `:` does not make a field.
 * Empty input gives no terms.
 */
export function parseQuery(input: string): Term[] {
  const terms: Term[] = []
  for (const token of tokenize(input.slice(0, MAX_QUERY_LENGTH))) {
    if (terms.length === MAX_TERMS) break
    const negate = token.length > 1 && token[0].char === '-' && !token[0].quoted
    const marks = negate ? token.slice(1) : token
    const raw = text(marks)
    const colon = marks.findIndex((mark) => mark.char === ':' && !mark.quoted)
    const name = text(marks.slice(0, colon))
    const isField = colon > 0 && colon < marks.length - 1 && marks.slice(0, colon).every((mark) => !mark.quoted) && FIELD_NAME.test(name)
    if (!isField) {
      terms.push({ negate, value: raw, raw })
      continue
    }
    const rest = marks.slice(colon + 1)
    terms.push({ negate, field: name, value: text(rest), raw, exact: rest.some((mark) => mark.quoted) })
  }
  return terms
}

/** The five `age:` terms, exactly (REQ:filter-vocabulary). */
export const AGE_TERMS = ['<1d', '1-7d', '8-30d', '31-90d', '>90d'] as const
export type AgeTerm = (typeof AGE_TERMS)[number]
