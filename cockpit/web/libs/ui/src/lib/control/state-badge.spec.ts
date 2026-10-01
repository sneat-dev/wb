import { TestBed } from '@angular/core/testing'
import { StateBadge } from './state-badge'
import { BadgeKind } from './state-vocabulary'

async function render(inputs: { kind: BadgeKind; value?: string; label?: string; size?: 'normal' | 'small'; hint?: string }) {
  const fixture = TestBed.createComponent(StateBadge)
  for (const [name, value] of Object.entries(inputs)) fixture.componentRef.setInput(name, value)
  await fixture.whenStable()
  const root: HTMLElement = fixture.nativeElement
  return { fixture, root, badge: root.querySelector('.badge') as HTMLElement }
}

describe('StateBadge', () => {
  // cockpit-views#ac:state-is-never-colour-only
  it('shows a colour role, a glyph and a word, and says the kind ahead of the word for assistive technology', async () => {
    const { root, badge } = await render({ kind: 'task', value: 'ready' })
    expect(badge.classList.contains('tone-ok')).toBe(true)
    expect(badge.querySelector('svg path')).not.toBeNull()
    expect(badge.querySelector('.text')?.textContent).toBe('ready to land')
    expect(badge.querySelector('.visually-hidden')?.textContent).toBe('Task state: ')
    expect(root.textContent).toContain('ready to land')
    expect(badge.getAttribute('title')).toBeNull()
  })

  it('takes a word and a tooltip from the caller while the colour and glyph follow the value', async () => {
    const { badge } = await render({ kind: 'code-index', value: 'stale', label: 'stale, 3 behind', hint: 'Receipt 2026-10-01T10:00:00Z' })
    expect(badge.classList.contains('tone-warn')).toBe(true)
    expect(badge.querySelector('.text')?.textContent).toBe('stale, 3 behind')
    expect(badge.getAttribute('title')).toBe('Receipt 2026-10-01T10:00:00Z')
  })

  it('has a normal and a small size', async () => {
    expect((await render({ kind: 'owner', value: 'idle' })).badge.classList.contains('small')).toBe(false)
    expect((await render({ kind: 'owner', value: 'idle', size: 'small' })).badge.classList.contains('small')).toBe(true)
  })

  it('draws an absent or unknown value grey and dashed', async () => {
    const absent = await render({ kind: 'agent-activity' })
    expect(absent.badge.classList.contains('unreported')).toBe(true)
    expect(absent.badge.classList.contains('tone-idle')).toBe(true)
    expect(absent.badge.querySelector('.text')?.textContent).toBe('not reported')
    expect((await render({ kind: 'owner', value: 'active' })).badge.classList.contains('unreported')).toBe(false)
  })
})

describe('StateBadge, a value it does not know', () => {
  it('shows the sanitised value, dashed, and tells assistive technology "<kind>: not recognised" rather than the raw text', async () => {
    const fixture = TestBed.createComponent(StateBadge)
    fixture.componentRef.setInput('kind', 'pr-state')
    fixture.componentRef.setInput('value', 'abandoned\u202e!')
    await fixture.whenStable()
    const badge = fixture.nativeElement.querySelector('.badge') as HTMLElement
    expect(badge.classList.contains('unreported')).toBe(true)
    expect(badge.querySelector('.visually-hidden')?.textContent?.trim()).toBe('Pull request state: not recognised')
    expect(badge.querySelector('.text')?.textContent).toBe('abandoned !')
    expect(badge.querySelector('.text')?.getAttribute('aria-hidden')).toBe('true')
  })
})
