import { TASK_STATES } from '@cockpit/fleet-data'
import { GLYPHS } from './glyph'
import { BadgeKind, KIND_NAME, badgeSpec } from './state-vocabulary'

const VALUES: Record<BadgeKind, string[]> = {
  task: TASK_STATES.map((info) => info.id),
  owner: ['active', 'idle', 'orphaned', 'unknown'],
  'agent-activity': ['working', 'blocked', 'idle', 'done', 'unknown'],
  'agent-state': ['running', 'live', 'parked', 'completed', 'failed', 'timeout', 'abandoned'],
  'pr-state': ['open', 'draft', 'merged', 'closed'],
  mergeable: ['clean', 'blocked', 'dirty', 'behind', 'unstable', 'has_hooks', 'draft', 'unknown'],
  'code-index': ['fresh', 'stale', 'diverged', 'pending', 'failed', 'never'],
  route: ['local', 'live', 'live-remote', 'cached', 'stale', 'none'],
  load: ['free', 'busy', 'not-reported'],
  checks: ['passed', 'failed', 'pending', 'unknown'],
}

describe('the state vocabulary', () => {
  // cockpit-views#ac:state-is-never-colour-only
  it('gives every state a colour role, a glyph that exists and a word', () => {
    for (const kind of Object.keys(VALUES) as BadgeKind[]) {
      expect(KIND_NAME[kind]).toBeTruthy()
      for (const value of VALUES[kind]) {
        const spec = badgeSpec(kind, value)
        expect(['ok', 'warn', 'bad', 'idle'], `${kind}/${value}`).toContain(spec.tone)
        expect(Object.keys(GLYPHS), `${kind}/${value}`).toContain(spec.icon)
        expect(spec.label.trim(), `${kind}/${value}`).not.toBe('')
      }
    }
  })

  it('follows the assignment: green live or fresh, amber stale or behind, red orphaned or failed, grey idle or cached', () => {
    expect(badgeSpec('route', 'live').tone).toBe('ok')
    expect(badgeSpec('code-index', 'fresh').tone).toBe('ok')
    expect(badgeSpec('route', 'stale').tone).toBe('warn')
    expect(badgeSpec('code-index', 'stale').tone).toBe('warn')
    expect(badgeSpec('mergeable', 'behind').tone).toBe('warn')
    expect(badgeSpec('owner', 'orphaned').tone).toBe('bad')
    expect(badgeSpec('agent-state', 'failed').tone).toBe('bad')
    expect(badgeSpec('task', 'checks-failed').tone).toBe('bad')
    expect(badgeSpec('owner', 'idle').tone).toBe('idle')
    expect(badgeSpec('route', 'cached').tone).toBe('idle')
  })

  it('says the task state in the library words', () => {
    expect(badgeSpec('task', 'ready')).toMatchObject({ label: 'ready to land', tone: 'ok', unreported: false })
    expect(badgeSpec('task', 'at-risk').label).toBe('at risk')
    expect(badgeSpec('task', 'not-reported')).toMatchObject({ label: 'state not reported', unreported: true })
  })

  it('marks the unknown values of a vocabulary as not reported', () => {
    expect(badgeSpec('agent-activity', 'unknown').unreported).toBe(true)
    expect(badgeSpec('owner', 'unknown').unreported).toBe(true)
    expect(badgeSpec('load', 'not-reported').unreported).toBe(true)
    expect(badgeSpec('owner', 'active').unreported).toBe(false)
  })

  it('shows an absent value as a grey "not reported", by kind', () => {
    expect(badgeSpec('task', undefined)).toEqual({ tone: 'idle', icon: 'help', label: 'state not reported', unreported: true })
    expect(badgeSpec('mergeable', undefined).label).toBe('merge state not reported')
    expect(badgeSpec('load', '').label).toBe('load unknown')
    expect(badgeSpec('checks', undefined).label).toBe('checks not reported')
  })

  it('shows a value outside the vocabulary as itself, in grey and dashed, never as a guess', () => {
    expect(badgeSpec('pr-state', 'abandoned-by-aliens')).toEqual({ tone: 'idle', icon: 'help', label: 'abandoned-by-aliens', unreported: true })
    expect(badgeSpec('task', 'whatever').label).toBe('whatever')
  })
})
