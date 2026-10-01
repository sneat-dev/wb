import { agent, run } from '@cockpit/fleet-data/testing'
import { NOW } from '../test-harness'
import { agentFallback, agentHeadline, referenceOf, agentName, formatSpan, timeCell, timeOf } from './agent-text'

const ago = (minutes: number) => new Date(NOW - minutes * 60_000).toISOString()

describe('agent text', () => {
  it('reads a time, and nothing for an absent or unreadable one', () => {
    expect(timeOf('2026-10-01T10:00:00Z')).toBe(Date.parse('2026-10-01T10:00:00Z'))
    expect(timeOf(undefined)).toBeUndefined()
    expect(timeOf('yesterday')).toBeUndefined()
  })

  it('says a span in its largest whole unit', () => {
    expect(formatSpan(5_000)).toBe('under 1 min')
    expect(formatSpan(5 * 60_000)).toBe('5 min')
    expect(formatSpan(2 * 3_600_000 + 1)).toBe('2 h')
    expect(formatSpan(3 * 86_400_000)).toBe('3 d')
  })

  it('names an agent by runtime and model, or "agent" when it reports neither', () => {
    expect(agentName({ runtime: 'claude', model: 'sonnet-5-5' })).toBe('claude · sonnet-5-5')
    expect(agentName({ runtime: 'codex' })).toBe('codex')
    expect(agentName({ model: 'opus' })).toBe('opus')
    expect(agentName({})).toBe('agent')
  })

  it('falls back to the kind and when it started, or the kind alone without a start', () => {
    expect(agentFallback({ kind: 'session', started_at: ago(120) }, NOW)).toBe('session, started 2 h ago')
    expect(agentFallback({ kind: 'run' }, NOW)).toBe('run')
  })

  it('says how long a running agent has run, when a run ended, when anything else started, and nothing without a time', () => {
    expect(timeCell(run('r', 'running', { started_at: ago(125) }), NOW)).toBe('running 2 h')
    expect(timeCell(run('r', 'running', { started_at: ago(-5) }), NOW)).toBe('running under 1 min')
    expect(timeCell(run('r', 'running'), NOW)).toBe('')
    expect(timeCell(run('r', 'failed', { started_at: ago(300), finished_at: ago(180) }), NOW)).toBe('finished 3 h ago')
    expect(timeCell(agent('s', undefined, 'parked', { started_at: ago(60 * 24 * 3) }), NOW)).toBe('parked, started 3 d ago')
    expect(timeCell(run('r', 'completed', { started_at: ago(60 * 24 * 3) }), NOW)).toBe('started 3 d ago')
    expect(timeCell(run('r', 'completed'), NOW)).toBe('')
  })

  it('writes what is known in plain words', () => {
    expect(agentHeadline(run('r', 'running', { started_at: ago(125) }), NOW)).toBe('Running for 2 h on alpha')
    expect(agentHeadline(run('r', 'running'), NOW)).toBe('Running on alpha')
    expect(agentHeadline(agent('s', undefined, 'live', { started_at: ago(10) }), NOW)).toBe('Running for 10 min on alpha')
    expect(agentHeadline(agent('s', undefined, 'parked', { started_at: ago(180) }), NOW)).toBe('Parked on alpha, started 3 h ago')
    expect(agentHeadline(agent('s', undefined, 'parked'), NOW)).toBe('Parked on alpha')
    expect(agentHeadline(run('r', 'failed', { finished_at: ago(180), exit_code: 1 }), NOW)).toBe('Finished 3 h ago, exit code 1')
    expect(agentHeadline(run('r', 'completed', { finished_at: ago(30) }), NOW)).toBe('Finished 30 min ago')
    expect(agentHeadline(run('r', 'timeout', { finished_at: ago(30) }), NOW)).toBe('Timed out 30 min ago')
    expect(agentHeadline(run('r', 'abandoned'), NOW)).toBe('Abandoned; no end time reported')
    expect(agentHeadline(run('r', 'failed', { exit_code: 2 }), NOW)).toBe('Finished, exit code 2; no end time reported')
    expect(agentHeadline(run('r', 'weird'), NOW)).toBe('State “weird” on alpha')
  })

  it('measures an entry read from a cache to its snapshot, not to now: it ran that long as of then', () => {
    const cached = { route: 'cached', observed_at: ago(600), started_at: ago(720) } as const
    expect(referenceOf({ route: 'local', observed_at: ago(600) }, NOW)).toBe(NOW)
    expect(referenceOf({ route: 'cached', observed_at: undefined as unknown as string }, NOW)).toBe(NOW)
    expect(referenceOf(cached, NOW)).toBe(NOW - 600 * 60_000)
    expect(timeCell(run('r', 'running', cached), NOW)).toBe('running 2 h')
    expect(agentHeadline(run('r', 'running', { ...cached, machine: 'beta' }), NOW)).toBe('Running for 2 h on beta (as of its snapshot, 10 h ago)')
    expect(agentHeadline(run('r', 'running', { route: 'cached', observed_at: ago(60), machine: 'beta' }), NOW)).toBe('Running on beta (as of its snapshot, 1 h ago)')
  })
})
