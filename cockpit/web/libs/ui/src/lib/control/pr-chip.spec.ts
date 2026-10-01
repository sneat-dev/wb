import { signal } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { PullRequest } from '@cockpit/fleet-data'
import { pullRequest } from '@cockpit/fleet-data/testing'
import { PrChip, checksOf } from './pr-chip'
import { UiClock } from './ui-clock'

const NOW = Date.parse('2026-10-01T10:05:00Z')

/** A pull request with only the fields a test names: the daemon omits what it has not observed. */
const bare = (extra: Partial<PullRequest>): PullRequest => ({ id: 'p', machine: 'alpha', machine_id: 'mach-alpha', route: 'local', number: 1, ...extra })

async function render(extra: Partial<PullRequest>) {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [{ provide: UiClock, useValue: { now: signal(NOW) } }] })
  const fixture = TestBed.createComponent(PrChip)
  fixture.componentRef.setInput('pullRequest', pullRequest('p1', 'r1', 'w1', { number: 42, checked_at: '2026-10-01T10:00:00Z', ...extra }))
  await fixture.whenStable()
  const root: HTMLElement = fixture.nativeElement
  const badges = [...root.querySelectorAll('app-state-badge')].map((badge) => badge.textContent?.replace(/\s+/g, ' ').trim())
  return { root, badges }
}

describe('PrChip', () => {
  it('shows the number, the state, the checks and the age of the observation', async () => {
    const { root, badges } = await render({ state: 'open', checks_total: 5, checks_passed: 5, checks_green: true })
    expect(root.querySelector('.number')?.textContent).toBe('#42')
    expect(badges).toEqual(['Pull request state: open', 'Checks: 5/5'])
    expect(root.querySelector('.observed')?.textContent?.replace(/\s+/g, ' ').trim()).toBe('checked 5 min ago')
    expect(root.querySelector('.failed')).toBeNull()
  })

  it('names the first failed check, truncated by the layout with the full name as the tooltip', async () => {
    const { root, badges } = await render({ state: 'open', checks_total: 5, checks_passed: 3, checks_failed: 2, failed_check: 'build-linux-amd64-integration' })
    expect(badges[1]).toBe('Checks: 3/5')
    expect(root.querySelector('app-state-badge:nth-of-type(2) .tone-bad')).not.toBeNull()
    const failed = root.querySelector('.failed') as HTMLElement
    expect(failed.textContent).toBe('build-linux-amd64-integration')
    expect(failed.getAttribute('title')).toBe('build-linux-amd64-integration')
  })

  it('shows no failed-check name when no check failed, even if one is named', async () => {
    const { root } = await render({ checks_total: 2, checks_passed: 1, checks_pending: 1, failed_check: 'stale-name' })
    expect(root.querySelector('.failed')).toBeNull()
  })

  it('links the number to the pull request only for a web address, opening in a new tab', async () => {
    const linked = await render({ url: 'https://github.com/acme/r1/pull/42' })
    const link = linked.root.querySelector('a.number') as HTMLAnchorElement
    expect(link.getAttribute('href')).toBe('https://github.com/acme/r1/pull/42')
    expect(link.getAttribute('rel')).toBe('noopener noreferrer')
    expect(link.getAttribute('target')).toBe('_blank')
    expect(link.getAttribute('aria-label')).toBe('Pull request 42, opens in a new tab')
    const hostile = await render({ url: 'javascript:alert(1)' })
    expect(hostile.root.querySelector('a')).toBeNull()
    expect(hostile.root.querySelector('span.number')?.textContent).toBe('#42')
  })

  it('says "not yet checked" for a pull request that was never observed, and "not reported" for what the daemon omitted', async () => {
    const { root, badges } = await render({ checked_at: undefined, state: undefined, checks_total: undefined, checks_passed: undefined, checks_failed: undefined, checks_pending: undefined, checks_green: undefined })
    expect(root.querySelector('.observed')?.textContent).toBe('not yet checked')
    expect(badges).toEqual(['Pull request state: not reported', 'Checks: checks not reported'])
  })

  it('decides the checks verdict from the daemon, never from the counts', () => {
    expect(checksOf(bare({ checks_total: 3, checks_passed: 3, checks_green: true }))).toEqual({ value: 'passed', label: '3/3' })
    expect(checksOf(bare({ checks_total: 3, checks_passed: 3, checks_green: false }))).toEqual({ value: 'unknown', label: '3/3' })
    expect(checksOf(bare({ checks_total: 3, checks_failed: 1 }))).toEqual({ value: 'failed', label: '0/3' })
    expect(checksOf(bare({ checks_total: 3, checks_passed: 1, checks_pending: 2 }))).toEqual({ value: 'pending', label: '1/3' })
    expect(checksOf(bare({ checks_total: undefined, checks_passed: undefined, checks_green: undefined }))).toEqual({ value: 'unknown', label: undefined })
  })

  it('links the number only through the library\'s checked web address', async () => {
    const link = async (url: string | undefined) => (await render({ url })).root.querySelector('a.number')?.getAttribute('href') ?? null
    expect(await link('https://github.com/sneat-dev/wb/pull/12')).toBe('https://github.com/sneat-dev/wb/pull/12')
    for (const url of ['http://github.com/a', 'javascript:alert(1)', 'https://user@github.com/a', 'https://github.com/a b', undefined]) expect(await link(url), String(url)).toBeNull()
  })
})
