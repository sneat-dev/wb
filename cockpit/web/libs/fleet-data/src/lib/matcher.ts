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
  /** A `-` prefix: the term excludes the rows that match the rest of it. */
  negate: boolean
  /** The field of a `field:value` term; undefined for a bare term. */
  field?: string
  /** What follows the colon of a field term, or the whole term. */
  value: string
  /** The term without its `-`, colon and all: what a bare match uses when the field is not declared. */
  raw: string
}

const FIELD_NAME = /^[a-z][a-z0-9-]*$/

/** Splits `input` into raw tokens: on whitespace outside double quotes, with the quotes removed. */
function tokenize(input: string): string[] {
  const tokens: string[] = []
  let current = ''
  let quoted = false
  for (const char of input) {
    if (char === '"') {
      quoted = !quoted
    } else if (!quoted && /\s/.test(char)) {
      if (current) tokens.push(current)
      current = ''
    } else {
      current += char
    }
  }
  if (current) tokens.push(current)
  return tokens
}

/** Reads the filter text into terms, applying the caps. Empty input gives no terms. */
export function parseQuery(input: string): Term[] {
  const terms: Term[] = []
  for (const token of tokenize(input.slice(0, MAX_QUERY_LENGTH))) {
    if (terms.length === MAX_TERMS) break
    const negate = token.length > 1 && token.startsWith('-')
    const raw = (negate ? token.slice(1) : token).toLowerCase()
    const colon = raw.indexOf(':')
    const isField = colon > 0 && colon < raw.length - 1 && FIELD_NAME.test(raw.slice(0, colon))
    terms.push(isField ? { negate, field: raw.slice(0, colon), value: raw.slice(colon + 1), raw } : { negate, value: raw, raw })
  }
  return terms
}

/** Counts the steps a glob match took, so a test can bound them. */
export interface StepCounter {
  steps: number
}

/** Whether the term text has a wildcard, which makes it a glob over the whole value. */
export function hasWildcard(text: string): boolean {
  return text.includes('*') || text.includes('?')
}

/**
 * Whether `pattern` (with `*` for any run of characters and `?` for exactly one)
 * matches the whole of `text`. Both are expected lower-cased. Steps are at most
 * about the pattern length times the text length: each step either advances in
 * the text or backs up to the last star, which only moves forward.
 */
export function globMatch(pattern: string, text: string, counter?: StepCounter): boolean {
  let p = 0
  let t = 0
  let star = -1
  let mark = 0
  while (t < text.length) {
    if (counter) counter.steps++
    const symbol = p < pattern.length ? pattern[p] : undefined
    if (symbol === '*') {
      star = p
      mark = t
      p++
    } else if (symbol === '?' || (symbol !== undefined && symbol === text[t])) {
      p++
      t++
    } else if (star >= 0) {
      p = star + 1
      mark++
      t = mark
    } else {
      return false
    }
  }
  while (p < pattern.length && pattern[p] === '*') {
    p++
    if (counter) counter.steps++
  }
  return p === pattern.length
}

const DAY_MS = 86_400_000

/** The five `age:` terms, exactly (REQ:filter-vocabulary). */
export const AGE_TERMS = ['<1d', '1-7d', '8-30d', '31-90d', '>90d'] as const
export type AgeTerm = (typeof AGE_TERMS)[number]

/** Whole days between `then` and `now`; a time in the future counts as 0. */
export function wholeDays(then: number, now: number): number {
  return Math.max(0, Math.floor((now - then) / DAY_MS))
}

/** The age term that whole days fall in. */
export function ageTermOf(days: number): AgeTerm {
  if (days < 1) return '<1d'
  if (days <= 7) return '1-7d'
  if (days <= 30) return '8-30d'
  return days <= 90 ? '31-90d' : '>90d'
}

/** Whether more than 30 whole days have passed since `then`: the chip `idle30` and the cleanup rule. */
export function idleOver30Days(then: number | undefined, now: number): boolean {
  return then !== undefined && wholeDays(then, now) > 30
}

/** What a term is matched against; every string is lower-cased by whoever built it. */
export interface Subject {
  /** The values a bare term searches (the page's default fields). */
  bare: readonly string[]
  /** The values of each declared field, by name. */
  fields: Readonly<Record<string, readonly string[]>>
  /** The last activity, for `age:` terms; undefined when unknown. */
  activityAt?: number
}

/** What a page declares and the clock the terms are read against. */
export interface MatchEnv {
  /** The `field:` names the page declares; any other `field:value` is plain text. */
  declared: ReadonlySet<string>
  /** Fields matched whole (the value or a glob over it) rather than as a substring, such as `state`. */
  exact: ReadonlySet<string>
  now: number
  counter?: StepCounter
}

function valueMatches(text: string, value: string, exact: boolean, counter: StepCounter | undefined): boolean {
  if (hasWildcard(text)) return globMatch(text, value, counter)
  return exact ? value === text : value.includes(text)
}

function termMatches(term: Term, subject: Subject, env: MatchEnv): boolean {
  if (term.field !== undefined && env.declared.has(term.field)) {
    if (term.field === 'age') {
      return subject.activityAt !== undefined && ageTermOf(wholeDays(subject.activityAt, env.now)) === term.value
    }
    const exact = env.exact.has(term.field)
    return (subject.fields[term.field] ?? []).some((value) => valueMatches(term.value, value, exact, env.counter))
  }
  return subject.bare.some((value) => valueMatches(term.raw, value, false, env.counter))
}

/** Whether the subject matches every term (AND); an excluding term passes when it does not match. */
export function matchesTerms(terms: readonly Term[], subject: Subject, env: MatchEnv): boolean {
  for (const term of terms) {
    if (termMatches(term, subject, env) === term.negate) return false
  }
  return true
}
