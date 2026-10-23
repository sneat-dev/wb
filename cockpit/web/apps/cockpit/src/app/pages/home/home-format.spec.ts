import { counted, isoOf } from './home-format'
import { minutesBetween, spanText } from './home-time'

describe('home-format', () => {
  it('writes a time for app-relative-time, and nothing for no time', () => {
    expect(isoOf(Date.parse('2026-10-01T10:00:00Z'))).toBe('2026-10-01T10:00:00.000Z')
    expect(isoOf(undefined)).toBeUndefined()
  })

  it('counts with the right plural', () => {
    expect(counted(1, 'repository', 'repositories')).toBe('1 repository')
    expect(counted(2, 'repository', 'repositories')).toBe('2 repositories')
    expect(counted(0, 'worktree')).toBe('0 worktrees')
  })
})

describe('home-time', () => {
  it('writes how long something ran in its two largest units', () => {
    const MIN = 60_000
    expect(spanText(-5)).toBe('<1 min')
    expect(spanText(30_000)).toBe('<1 min')
    expect(spanText(35 * MIN)).toBe('35 min')
    expect(spanText(2 * 60 * MIN)).toBe('2 h')
    expect(spanText((2 * 60 + 5) * MIN)).toBe('2 h 5 min')
    expect(spanText(3 * 24 * 60 * MIN)).toBe('3 d')
    expect(spanText((3 * 24 * 60 + 4 * 60) * MIN)).toBe('3 d 4 h')
  })

  it('counts whole minutes between two instants, never below zero', () => {
    expect(minutesBetween(0, 125_000)).toBe(2)
    expect(minutesBetween(500_000, 0)).toBe(0)
  })
})
