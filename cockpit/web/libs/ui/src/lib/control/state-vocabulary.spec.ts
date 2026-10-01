import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'
import { TASK_STATES } from '@cockpit/fleet-data'
import {
  BadgeKind,
  KIND_NAME,
  RAW_VALUE_LIMIT,
  agentActivityBadgeSpec,
  agentStateBadgeSpec,
  badgeSpec,
  checksBadgeSpec,
  codeIndexBadgeSpec,
  loadBadgeSpec,
  mergeableBadgeSpec,
  operationBadgeSpec,
  ownerBadgeSpec,
  prStateBadgeSpec,
  routeBadgeSpec,
  sanitisedValue,
  taskBadgeSpec,
} from './state-vocabulary'

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
  operation: ['queued', 'running', 'succeeded', 'failed', 'cancelled'],
}

describe('the state vocabulary', () => {
  // cockpit-views#ac:state-is-never-colour-only
  it('gives every state a colour role, a glyph that exists and a word', () => {
    for (const kind of Object.keys(VALUES) as BadgeKind[]) {
      expect(KIND_NAME[kind]).toBeTruthy()
      for (const value of VALUES[kind]) {
        const spec = badgeSpec(kind, value)
        expect(['ok', 'warn', 'bad', 'idle'], `${kind}/${value}`).toContain(spec.tone)
        expect(spec.icon.length, `${kind}/${value}`).toBeGreaterThan(0)
        expect(spec.unrecognised, `${kind}/${value}`).toBe(false)
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
    expect(badgeSpec('task', undefined)).toMatchObject({ tone: 'idle', label: 'state not reported', unreported: true, unrecognised: false })
    expect(badgeSpec('mergeable', undefined).label).toBe('merge state not reported')
    expect(badgeSpec('load', '').label).toBe('load unknown')
    expect(badgeSpec('checks', undefined).label).toBe('checks not reported')
  })

  it('shows a value outside the vocabulary as its sanitised self, in grey and dashed, marked not recognised, never as a guess', () => {
    expect(badgeSpec('pr-state', 'abandoned-by-aliens')).toMatchObject({ tone: 'idle', label: 'abandoned-by-aliens', unreported: true, unrecognised: true })
    expect(badgeSpec('task', 'whatever')).toMatchObject({ label: 'whatever', unrecognised: true })
    expect(badgeSpec('operation', 'exploded').unrecognised).toBe(true)
  })

  it('looks a value up as an own key only, so a prototype key is not recognised', () => {
    for (const kind of Object.keys(VALUES) as BadgeKind[]) {
      for (const key of ['constructor', 'toString', '__proto__', 'hasOwnProperty']) {
        expect(badgeSpec(kind, key), `${kind}/${key}`).toMatchObject({ tone: 'idle', label: key, unrecognised: true })
      }
    }
  })

  it('removes control, invisible and bidirectional characters from a value it shows, keeps one line and caps the length', () => {
    expect(sanitisedValue('a\u202eb\u200bc\nd\te')).toBe('a b c d e')
    expect(sanitisedValue('\u200b\u0000')).toBe('unknown')
    expect(sanitisedValue('x'.repeat(100))).toHaveLength(RAW_VALUE_LIMIT)
    expect(sanitisedValue('x'.repeat(100)).endsWith('…')).toBe(true)
    expect(sanitisedValue('x'.repeat(RAW_VALUE_LIMIT))).toBe('x'.repeat(RAW_VALUE_LIMIT))
    expect(badgeSpec('owner', '\u202egnp.exe').label).not.toMatch(/[\u202e]/)
  })

  // cockpit-views#ac:state-is-never-colour-only
  it('has one function per kind that gives exactly what badgeSpec gives, for every value, an unknown one and an absent one', () => {
    const byKind: Record<BadgeKind, (value: string | undefined) => ReturnType<typeof badgeSpec>> = {
      task: taskBadgeSpec,
      owner: ownerBadgeSpec,
      'agent-activity': agentActivityBadgeSpec,
      'agent-state': agentStateBadgeSpec,
      'pr-state': prStateBadgeSpec,
      mergeable: mergeableBadgeSpec,
      'code-index': codeIndexBadgeSpec,
      route: routeBadgeSpec,
      load: loadBadgeSpec,
      checks: checksBadgeSpec,
      operation: operationBadgeSpec,
    }
    for (const kind of Object.keys(VALUES) as BadgeKind[]) {
      for (const value of [...VALUES[kind], 'not-a-state', 'constructor', '', undefined]) expect(byKind[kind](value), `${kind}/${String(value)}`).toEqual(badgeSpec(kind, value))
    }
  })

  it('keeps each kind\'s table in its own module, which imports only the glyphs and the shared helpers, so a page that shows one kind does not carry the others', () => {
    const directory = join(__dirname, 'state-tables')
    const modules = readdirSync(directory).filter((name) => name.endsWith('.ts') && !name.endsWith('.spec.ts'))
    expect(modules.sort()).toEqual(['agent-activity.ts', 'agent-state.ts', 'checks.ts', 'code-index.ts', 'load.ts', 'mergeable.ts', 'operation.ts', 'owner.ts', 'pr-state.ts', 'route.ts', 'shared.ts', 'task.ts'])
    for (const name of modules) {
      const imports = [...readFileSync(join(directory, name), 'utf8').matchAll(/from '([^']+)'/g)].map((match) => match[1])
      for (const source of imports) expect(['../glyphs', './shared', '@cockpit/fleet-data'], `${name} imports ${source}`).toContain(source)
    }
  })
})
