import { MatchEnv, StepCounter, Subject, ageTermOf, globMatch, hasWildcard, idleOver30Days, matchesTerms, wholeDays } from './match'
import { AGE_TERMS, MAX_QUERY_LENGTH, MAX_TERMS, parseQuery } from './matcher'

const NOW = Date.parse('2026-10-01T12:00:00Z')
const DAY = 86_400_000

interface Row {
  repo: string
  machine: string
  task?: string
  at?: number
}

// A page that declares repo, task, machine, state and age, and searches the repository and task by default.
function envFor(counter?: StepCounter): MatchEnv {
  return { declared: new Set(['repo', 'task', 'machine', 'state', 'age']), exact: new Set(['state']), now: NOW, counter }
}

function subjectOf(row: Row): Subject {
  const repo = row.repo.toLowerCase()
  const task = row.task?.toLowerCase()
  return {
    bare: task === undefined ? [repo] : [repo, task],
    fields: { repo: [repo], machine: [row.machine.toLowerCase()], task: task === undefined ? [] : [task] },
    activityAt: row.at,
  }
}

function run(query: string, rows: Row[], counter?: StepCounter): string[] {
  const terms = parseQuery(query)
  return rows.filter((row) => matchesTerms(terms, subjectOf(row), envFor(counter))).map((row) => row.repo)
}

const ROWS: Row[] = [
  { repo: 'sneat-co/bots-go', machine: 'mac' },
  { repo: 'sneat-co/sneat-go', machine: 'vm' },
  { repo: 'sneat-dev/wb', machine: 'mac', task: 'fix ci' },
  { repo: 'Strongo/Dalgo', machine: 'vm' },
]

describe('parseQuery', () => {
  it('splits on whitespace, keeps a quoted value whole and removes the quotes', () => {
    expect(parseQuery('  a   b  ').map((term) => term.raw)).toEqual(['a', 'b'])
    expect(parseQuery('task:"fix ci" go')).toEqual([
      { negate: false, field: 'task', value: 'fix ci', raw: 'task:fix ci', exact: true },
      { negate: false, value: 'go', raw: 'go' },
    ])
    expect(parseQuery('"two words"')[0].value).toBe('two words')
    // An unterminated quote runs to the end.
    expect(parseQuery('task:"open ended')[0].value).toBe('open ended')
  })

  it('reads a leading dash as exclusion, also on a field term, and a lone dash as text', () => {
    expect(parseQuery('-wb')[0]).toEqual({ negate: true, value: 'wb', raw: 'wb' })
    expect(parseQuery('-machine:vm')[0]).toEqual({ negate: true, field: 'machine', value: 'vm', raw: 'machine:vm', exact: false })
    expect(parseQuery('-')[0]).toEqual({ negate: false, value: '-', raw: '-' })
  })

  it('lower-cases terms and treats a colon without a valid name or value as text', () => {
    expect(parseQuery('Machine:VM')[0]).toMatchObject({ field: 'machine', value: 'vm' })
    for (const text of [':x', 'a:', '1a:b', 'a b:c']) {
      expect(parseQuery(text)[0].field).toBeUndefined()
    }
    expect(parseQuery('')).toEqual([])
    expect(parseQuery('""')).toEqual([])
  })

  it('lets quotes protect a leading dash and a field prefix', () => {
    expect(parseQuery('"-x"')[0]).toEqual({ negate: false, value: '-x', raw: '-x' })
    expect(parseQuery('"a:b"')[0]).toEqual({ negate: false, value: 'a:b', raw: 'a:b' })
    expect(parseQuery('-"a b"')[0]).toEqual({ negate: true, value: 'a b', raw: 'a b' })
    expect(parseQuery('"task":x')[0].field).toBeUndefined()
    expect(parseQuery('task:""')[0].field).toBeUndefined()
    expect(parseQuery('task:x')[0].exact).toBe(false)
  })

  // cockpit-views#ac:matcher-limits-and-bare-fields
  it('applies only the first 16 terms and the first 256 characters', () => {
    const terms = Array.from({ length: 20 }, (_, index) => `t${index}`).join(' ')
    expect(parseQuery(terms)).toHaveLength(MAX_TERMS)
    expect(parseQuery(terms)[15].raw).toBe('t15')
    const long = `${'a'.repeat(250)} ${'b'.repeat(50)}`
    expect(long.length).toBeGreaterThan(MAX_QUERY_LENGTH)
    const parsed = parseQuery(long)
    expect(parsed.map((term) => term.raw.length)).toEqual([250, MAX_QUERY_LENGTH - 251])
    expect(parseQuery('x'.repeat(300))[0].raw).toHaveLength(MAX_QUERY_LENGTH)
  })
})

describe('globMatch', () => {
  it('matches the whole value with * for any run and ? for exactly one', () => {
    expect(globMatch('sneat-*/*-go', 'sneat-co/bots-go')).toBe(true)
    expect(globMatch('sneat-*/*-go', 'sneat-co/bots-go-x')).toBe(false)
    expect(globMatch('?b?', 'abc')).toBe(true)
    expect(globMatch('?b?', 'ab')).toBe(false)
    expect(globMatch('*', '')).toBe(true)
    expect(globMatch('', '')).toBe(true)
    expect(globMatch('a*', 'b')).toBe(false)
    expect(globMatch('a**', 'a')).toBe(true)
    expect(globMatch('*a*b', 'xaxab')).toBe(true)
    // A dot and a star are a literal dot and a star, never a regular expression.
    expect(globMatch('a.*', 'abc')).toBe(false)
    expect(globMatch('a.*', 'a.c')).toBe(true)
    // A literal star in the value is still matched by a star in the pattern.
    expect(globMatch('*', '*')).toBe(true)
    expect(globMatch('a*', 'a*b')).toBe(true)
  })

  // cockpit-views#ac:matcher-is-linear-time
  it('takes at most a constant times pattern length times value length steps for a hostile glob', () => {
    const pattern = '*a*a*a*a*a*a*a*a*a*a*b'
    const value = 'a'.repeat(50_000)
    const counter: StepCounter = { steps: 0 }
    expect(globMatch(pattern, value, counter)).toBe(false)
    expect(counter.steps).toBeGreaterThan(value.length)
    expect(counter.steps).toBeLessThanOrEqual(2 * pattern.length * value.length)
    // Matching is also quick in wall time: no backtracking explosion.
    const started = performance.now()
    globMatch(pattern, value)
    expect(performance.now() - started).toBeLessThan(1000)
  })

  it('stays within the bound for many other shapes', () => {
    const patterns = ['*a*a*a*a*c', 'a*a*a*a*a*a*b', '?*?*?*?*b', '*????a*', '**a**a**a**c']
    for (const pattern of patterns) {
      const value = 'ab'.repeat(500)
      const counter: StepCounter = { steps: 0 }
      globMatch(pattern, value, counter)
      expect(counter.steps).toBeLessThanOrEqual(2 * (pattern.length + 1) * (value.length + 1))
    }
  })

  it('lets ? match one code point, not one UTF-16 unit', () => {
    expect(globMatch('a?b', 'a\u{1F600}b')).toBe(true)
    expect(globMatch('a??b', 'a\u{1F600}b')).toBe(false)
    expect(globMatch('*\u{1F600}', 'x\u{1F600}')).toBe(true)
  })

  it('counts the steps of trailing stars too', () => {
    const counter: StepCounter = { steps: 0 }
    expect(globMatch('a**', 'a', counter)).toBe(true)
    expect(counter.steps).toBe(3)
  })

  it('detects wildcards', () => {
    expect(hasWildcard('a*')).toBe(true)
    expect(hasWildcard('a?')).toBe(true)
    expect(hasWildcard('a.b')).toBe(false)
  })
})

describe('matchesTerms', () => {
  // cockpit-views#ac:matcher-grammar
  it('applies terms, glob, exclusion, field and quotes', () => {
    expect(run('sneat-*/*-go', ROWS)).toEqual(['sneat-co/bots-go', 'sneat-co/sneat-go'])
    expect(run('WB', ROWS)).toEqual(['sneat-dev/wb'])
    expect(run('sneat -wb', ROWS)).toEqual(['sneat-co/bots-go', 'sneat-co/sneat-go'])
    expect(run('machine:vm go', ROWS)).toEqual(['sneat-co/sneat-go', 'Strongo/Dalgo'])
    expect(run('-machine:vm', ROWS)).toEqual(['sneat-co/bots-go', 'sneat-dev/wb'])
    expect(run('task:"fix ci"', ROWS)).toEqual(['sneat-dev/wb'])
    // `colour` is not declared by the page, so the text `colour:red` is plain text and matches nothing.
    expect(run('colour:red', ROWS)).toEqual([])
    expect(run('a.*', ROWS)).toEqual([])
  })

  it('matches every row for no terms, ANDs terms, and excludes a plain term', () => {
    expect(run('', ROWS)).toHaveLength(4)
    expect(run('sneat go', ROWS)).toEqual(['sneat-co/bots-go', 'sneat-co/sneat-go'])
    expect(run('sneat -sneat-go', ROWS)).toEqual(['sneat-co/bots-go', 'sneat-dev/wb'])
    // Excluding text that is no field leaves every row.
    expect(run('-colour:red', ROWS)).toHaveLength(4)
  })

  // cockpit-views#ac:matcher-limits-and-bare-fields
  it('searches only the declared default fields for a bare term: the repository without its host', () => {
    const subject: Subject = { bare: ['sneat-dev/wb', 'fix-ci', 'topic'], fields: {} }
    const env = envFor()
    expect(matchesTerms(parseQuery('topic'), subject, env)).toBe(true)
    expect(matchesTerms(parseQuery('github'), subject, env)).toBe(false)
    const twenty = Array.from({ length: 20 }, () => 'topic').join(' ') + ' nomatch'
    // The 21st term (a term that would fail) is beyond the cap and ignored.
    expect(matchesTerms(parseQuery(twenty), subject, env)).toBe(true)
    // Text past 256 characters is not read: the failing term sits beyond the cap.
    const padded = `${'topic '.repeat(43)}zzz`
    expect(padded.length).toBeGreaterThan(MAX_QUERY_LENGTH)
    expect(matchesTerms(parseQuery(padded), subject, env)).toBe(true)
  })

  // cockpit-views#ac:matcher-grammar
  it('matches a quoted field value exactly: whole-value, case-insensitive, with no wildcards, so a count cell opens what it counted', () => {
    const rows: Row[] = [
      { repo: 'sneat-co/sneat-go', machine: 'mac' },
      { repo: 'sneat-co/sneat-go-backend', machine: 'mac' },
      { repo: 'Sneat-Co/Sneat-Go', machine: 'vm' },
      { repo: 'sneat-co/*', machine: 'vm' },
    ]
    expect(run('repo:"sneat-co/sneat-go"', rows)).toEqual(['sneat-co/sneat-go', 'Sneat-Co/Sneat-Go'])
    // Unquoted stays a substring, and a glob.
    expect(run('repo:sneat-co/sneat-go', rows)).toHaveLength(3)
    expect(run('repo:sneat-co/sneat-g?', rows)).toEqual(['sneat-co/sneat-go', 'Sneat-Co/Sneat-Go'])
    // In exact mode * and ? are literal.
    expect(run('repo:"sneat-co/*"', rows)).toEqual(['sneat-co/*'])
    expect(run('-repo:"sneat-co/sneat-go"', rows)).toEqual(['sneat-co/sneat-go-backend', 'sneat-co/*'])
    expect(run('repo:"nope"', rows)).toEqual([])
  })

  it('matches a field exactly when it is declared exact, and a glob over it', () => {
    const subject: Subject = { bare: [], fields: { state: ['idle'] } }
    const env = envFor()
    expect(matchesTerms(parseQuery('state:idle'), subject, env)).toBe(true)
    expect(matchesTerms(parseQuery('state:idl'), subject, env)).toBe(false)
    expect(matchesTerms(parseQuery('state:id*'), subject, env)).toBe(true)
    expect(matchesTerms(parseQuery('-state:idle'), subject, env)).toBe(false)
    // A field with no values matches nothing.
    expect(matchesTerms(parseQuery('state:idle'), { bare: [], fields: {} }, env)).toBe(false)
  })

  it('reads age terms against last activity and matches none without one', () => {
    const at = (days: number): Subject => ({ bare: [], fields: {}, activityAt: NOW - days * DAY })
    const env = envFor()
    for (const [days, term] of [[0, '<1d'], [3, '1-7d'], [20, '8-30d'], [60, '31-90d'], [200, '>90d']] as const) {
      expect(AGE_TERMS).toContain(term)
      expect(matchesTerms(parseQuery(`age:${term}`), at(days), env)).toBe(true)
      expect(matchesTerms(parseQuery(`-age:${term}`), at(days), env)).toBe(false)
    }
    expect(matchesTerms(parseQuery('age:<1d'), at(3), env)).toBe(false)
    expect(matchesTerms(parseQuery('age:<1d'), { bare: [], fields: {} }, env)).toBe(false)
    expect(matchesTerms(parseQuery('age:30d'), at(3), env)).toBe(false)
  })

  it('counts the glob steps of a whole filter when asked', () => {
    const counter: StepCounter = { steps: 0 }
    run('sneat-*/*-go', ROWS, counter)
    expect(counter.steps).toBeGreaterThan(0)
  })
})

describe('ages', () => {
  it('puts whole days in the five age terms, with the boundaries', () => {
    expect(ageTermOf(0)).toBe('<1d')
    expect(ageTermOf(1)).toBe('1-7d')
    expect(ageTermOf(7)).toBe('1-7d')
    expect(ageTermOf(8)).toBe('8-30d')
    expect(ageTermOf(30)).toBe('8-30d')
    expect(ageTermOf(31)).toBe('31-90d')
    expect(ageTermOf(90)).toBe('31-90d')
    expect(ageTermOf(91)).toBe('>90d')
  })

  it('counts whole days, never negative, and finds idleness over 30 days', () => {
    expect(wholeDays(NOW - 2.5 * DAY, NOW)).toBe(2)
    expect(wholeDays(NOW + DAY, NOW)).toBe(0)
    expect(idleOver30Days(NOW - 30 * DAY, NOW)).toBe(false)
    expect(idleOver30Days(NOW - 31 * DAY, NOW)).toBe(true)
    expect(idleOver30Days(undefined, NOW)).toBe(false)
  })
})
