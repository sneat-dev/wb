import { emptyListQuery } from './list-query'
import { StepCounter } from './match'
/// <reference types="node" />
import { FleetModel } from './fleet-model'
import { ListRow, applyListQuery, buildWorktreeRows } from './list-rows'
import { parseQuery } from './matcher'
import { PERF_NOW, performanceFixture, repeatRows } from './perf-fixture'
import { Worktree } from './fleet.types'

// The budget of REQ:fast-filtering: a median under 30 ms for 5,000 rows, with a
// multiplier of 5 allowed in CI.
const BUDGET_MS = 30
const CI_MULTIPLIER = 5
const RUNS = 31

function median(values: number[]): number {
  const sorted = [...values].sort((a, b) => a - b)
  return sorted[Math.floor(sorted.length / 2)]
}

describe('filtering 5,000 rows', () => {
  const model = new FleetModel(performanceFixture().document, { now: () => PERF_NOW })
  const rows: ListRow<Worktree>[] = repeatRows(buildWorktreeRows(model), 5000)
  // A multi-term glob query over the default fields and a field, with an exclusion.
  const text = 'task:*-ci-* *-i* sneat-*/*-go -state:orphaned -branch:*zzz* repo:*-*'

  it('has 5,000 rows to filter', () => {
    expect(rows).toHaveLength(5000)
    expect(new Set(rows.map((row) => row.id)).size).toBe(5000)
  })

  // cockpit-views#ac:filtering-5000-rows-is-fast
  it('takes a median under 30 ms (times 5 in CI) over repeated runs, with a sort', () => {
    const budget = process.env['CI'] ? BUDGET_MS * CI_MULTIPLIER : BUDGET_MS
    const query = { ...emptyListQuery(), q: text, sort: 'state', chips: [] }
    applyListQuery('worktrees', rows, query, PERF_NOW)
    const times: number[] = []
    for (let run = 0; run < RUNS; run++) {
      const started = performance.now()
      applyListQuery('worktrees', rows, query, PERF_NOW)
      times.push(performance.now() - started)
    }
    expect(median(times)).toBeLessThan(budget)
  })

  it('types a key at a time without exceeding the budget for any prefix of the query', () => {
    const budget = process.env['CI'] ? BUDGET_MS * CI_MULTIPLIER : BUDGET_MS
    const times: number[] = []
    for (let length = 1; length <= text.length; length++) {
      const started = performance.now()
      applyListQuery('worktrees', rows, { ...emptyListQuery(), q: text.slice(0, length) }, PERF_NOW)
      times.push(performance.now() - started)
    }
    expect(median(times)).toBeLessThan(budget)
  })

  it('keeps the matcher steps within a constant times pattern length times value length', () => {
    const counter: StepCounter = { steps: 0 }
    applyListQuery('worktrees', rows, { ...emptyListQuery(), q: text }, PERF_NOW, counter)
    const globs = parseQuery(text).filter((term) => term.value.includes('*') || term.value.includes('?'))
    const longestPattern = Math.max(...globs.map((term) => term.value.length)) + 1
    // Every value of every row is matched against every glob at most once.
    let valueLengths = 0
    for (const row of rows) {
      for (const value of [...row.subject.bare, ...Object.values(row.subject.fields).flat()]) valueLengths += value.length + 1
    }
    expect(counter.steps).toBeGreaterThan(0)
    expect(counter.steps).toBeLessThanOrEqual(2 * longestPattern * valueLengths * globs.length)
  })

  it('narrows the rows and reports how many there were', () => {
    const all = applyListQuery('worktrees', rows, { ...emptyListQuery(), q: text }, PERF_NOW)
    expect(all.total).toBe(5000)
    expect(all.rows.length).toBeLessThan(5000)
  })
})
