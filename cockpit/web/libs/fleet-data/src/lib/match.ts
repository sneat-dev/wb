// The matching half of the matcher (REQ:list-filter-and-matcher): the glob scan, the
// age terms of a row and `matchesTerms`. `matcher.ts` reads the text into terms
// and holds the caps; this is only needed where rows are matched, so it is behind
// `@cockpit/fleet-data/list` and the first page does not carry it.

import { AgeTerm, Term } from './matcher'

const DAY_MS = 86_400_000

/** Counts the steps a glob match took, so a test can bound them. */
export interface StepCounter {
  steps: number
}

/** Whether the term text has a wildcard, which makes it a glob over the whole value. */
export function hasWildcard(text: string): boolean {
  return text.includes('*') || text.includes('?')
}

const SURROGATE = /[\ud800-\udfff]/

/** The text as something indexable by code point: the string itself unless it holds surrogate pairs, so `?` is one code point. */
function units(value: string): string | string[] {
  return SURROGATE.test(value) ? Array.from(value) : value
}

/**
 * Whether `pattern` (with `*` for any run of characters and `?` for exactly one)
 * matches the whole of `text`. Both are expected lower-cased. Steps are at most
 * about the pattern length times the text length: each step either advances in
 * the text or backs up to the last star, which only moves forward.
 */
export function globMatch(patternText: string, valueText: string, counter?: StepCounter): boolean {
  const pattern = units(patternText)
  const text = units(valueText)
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

function valueMatches(term: string, value: string, whole: boolean, counter: StepCounter | undefined): boolean {
  if (hasWildcard(term)) return globMatch(term, value, counter)
  return whole ? value === term : value.includes(term)
}

function termMatches(term: Term, subject: Subject, env: MatchEnv): boolean {
  if (term.field !== undefined && env.declared.has(term.field)) {
    if (term.field === 'age') {
      return subject.activityAt !== undefined && ageTermOf(wholeDays(subject.activityAt, env.now)) === term.value
    }
    const values = subject.fields[term.field] ?? []
    // A quoted value is an exact, whole-value match with no wildcards.
    if (term.exact) return values.some((value) => value === term.value)
    const whole = env.exact.has(term.field)
    return values.some((value) => valueMatches(term.value, value, whole, env.counter))
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
