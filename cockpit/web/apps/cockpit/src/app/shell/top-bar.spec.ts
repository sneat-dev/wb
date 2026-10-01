import { Component } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { RouterTestingHarness } from '@angular/router/testing'
import { FleetStore, Session } from '@cockpit/fleet-data'
import { agent, fleetDocument, pullRequest, run, worktree } from '@cockpit/fleet-data/testing'
import { appRoutes } from '../app.routes'
import { ShellState } from './shell-state'
import { TopBar, scrollTabIntoView } from './top-bar'

const NOW = Date.parse('2026-10-01T10:05:00Z')

const text = (element: Element | null | undefined) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()

describe('TopBar', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval', 'Date'] })
    vi.setSystemTime(NOW)
    TestBed.configureTestingModule({ providers: [provideRouter(appRoutes)] })
  })
  afterEach(() => vi.useRealTimers())

  /** 3 tasks need the operator, one agent runs, a snapshot taken 70 seconds ago with a 30 second interval. */
  function threeNeedYou() {
    const worktrees = ['a', 'b', 'c'].map((name) => ({ ...worktree(`w-${name}`, 'r1', 'alpha'), task: `task-${name}` }))
    const failing = worktrees.map((tree, index) => pullRequest(`pr-${index}`, 'r1', tree.id, { number: index + 1, checks_green: false, checks_failed: 1, checks_passed: 4, failed_check: 'ci', mergeable: 'blocked' }))
    return fleetDocument({
      snapshot_at: new Date(NOW - 70_000).toISOString(),
      refresh_interval_seconds: 30,
      worktrees,
      pull_requests: failing,
      agents: [agent('a1', 'r1', 'live'), run('r1', 'completed')],
    })
  }

  async function render(prepare: (store: FleetStore) => void = () => undefined, session: Session | null = { principal: 'anonymous-local', capabilities: [], code_browser_url: '' }) {
    const store = TestBed.inject(FleetStore)
    store.loaded.set(true)
    store.document.set(threeNeedYou())
    store.session.set(session)
    store.sessionStatus.set(session ? 'ready' : 'failed')
    prepare(store)
    const fixture = TestBed.createComponent(TopBar)
    await fixture.whenStable()
    const root: HTMLElement = fixture.nativeElement
    return { fixture, root, store, tab: (name: string) => [...root.querySelectorAll('.tab')].find((tab) => tab.querySelector('span')?.textContent === name) as HTMLElement }
  }

  // cockpit-views#ac:top-bar-shows-tabs-badges-and-freshness
  it('shows the tabs with signal badges only, the search entry, New task, the freshness chip and the session chip', async () => {
    const { root, tab } = await render()
    expect([...root.querySelectorAll('.tab')].map((link) => link.querySelector('span')?.textContent)).toEqual(['Home', 'Tasks', 'Repositories', 'Worktrees', 'Agents', 'Machines'])
    expect(text(tab('Home').querySelector('.badge'))).toBe('3')
    expect(tab('Home').querySelector('.badge')?.classList.contains('hot')).toBe(true)
    expect(text(tab('Agents').querySelector('.badge'))).toBe('1')
    expect(tab('Agents').querySelector('.badge')?.classList.contains('hot')).toBe(true)
    expect(root.querySelectorAll('.badge')).toHaveLength(2)
    expect(text(tab('Home').querySelector('.visually-hidden'))).toBe('tasks need you')
    expect(text(tab('Agents').querySelector('.visually-hidden'))).toBe('agents running')
    expect(root.querySelector('.palette-trigger')?.getAttribute('aria-label')).toBe('Search')
    expect(text(root.querySelector('.new-task'))).toBe('New task')
    expect(root.querySelector('.new-task')?.getAttribute('href')).toBe('/tasks/new')
    const chip = root.querySelector('app-freshness-chip .chip') as HTMLElement
    expect(text(chip)).toBe('updated 70 s ago')
    expect(chip.classList.contains('tone-warn')).toBe(true)
    expect(text(root.querySelector('.session'))).toBe('anonymous')
    // No operation indicator.
    expect(root.querySelector('[class*="operation"], [aria-label*="peration"]')).toBeNull()
  })

  it('shows a muted zero when there is no signal, and no badge without data', async () => {
    const { root, tab, fixture, store } = await render((store) => store.document.set(fleetDocument({ agents: [], worktrees: [] })))
    expect(text(tab('Home').querySelector('.badge'))).toBe('0')
    expect(tab('Home').querySelector('.badge')?.classList.contains('hot')).toBe(false)
    store.loaded.set(false)
    await fixture.whenStable()
    // The two places are held, hidden from everyone, so that the tabs do not move when the numbers arrive.
    const pending = () => [...root.querySelectorAll('.badge')].filter((badge) => !badge.classList.contains('badge-pending'))
    expect(pending()).toHaveLength(0)
    expect(root.querySelectorAll('.badge-pending[aria-hidden="true"]')).toHaveLength(2)
    store.loaded.set(true)
    store.schemaMismatch.set('page-older')
    await fixture.whenStable()
    expect(pending()).toHaveLength(0)
  })

  it('opens the palette from its entry', async () => {
    const { root } = await render()
    ;(root.querySelector('.palette-trigger') as HTMLButtonElement).click()
    expect(TestBed.inject(ShellState).paletteOpen()).toBe(true)
  })

  it('names the session: owner, anonymous, none and not yet read', async () => {
    const owner = await render(undefined, { principal: 'owner', capabilities: [], code_browser_url: '' })
    expect(text(owner.root.querySelector('.session'))).toBe('owner')
    expect(owner.root.querySelector('.session')?.getAttribute('title')).toContain('owner session')
    owner.store.session.set(null)
    owner.store.sessionStatus.set('failed')
    await owner.fixture.whenStable()
    expect(text(owner.root.querySelector('.session'))).toBe('no session')
    owner.store.sessionStatus.set('loading')
    await owner.fixture.whenStable()
    expect(text(owner.root.querySelector('.session'))).toBe('session')
  })

  it('uses the platform key in the search hint', async () => {
    const { root } = await render()
    expect(text(root.querySelector('.trigger-keys'))).toMatch(/^(⌘|Ctrl) K$/)
  })

  // cockpit-views#ac:home-route-and-alias, the tab half
  it('marks exactly the current tab with aria-current, Home only at the root', async () => {
    const harness = await RouterTestingHarness.create()
    const fixture = TestBed.createComponent(TopBar)
    await harness.navigateByUrl('/tasks/detail?task=x')
    await fixture.whenStable()
    const current = () => [...fixture.nativeElement.querySelectorAll('[aria-current="page"]')].map((link: Element) => link.querySelector('span')?.textContent)
    expect(current()).toEqual(['Tasks'])
    await harness.navigateByUrl('/')
    await fixture.whenStable()
    expect(current()).toEqual(['Home'])
    await harness.navigateByUrl('/machines/mach-alpha')
    await fixture.whenStable()
    expect(current()).toEqual(['Machines'])
  })

  it('brings the current tab into view in the scrolling strip after a navigation', async () => {
    const harness = await RouterTestingHarness.create()
    const fixture = TestBed.createComponent(TopBar)
    await fixture.whenStable()
    const strip = fixture.nativeElement.querySelector('.tabs') as HTMLElement
    const machines = [...strip.querySelectorAll<HTMLElement>('.tab')].find((tab) => tab.textContent?.includes('Machines')) as HTMLElement
    Object.defineProperty(strip, 'clientWidth', { value: 200 })
    Object.defineProperty(strip, 'scrollLeft', { value: 0, writable: true })
    Object.defineProperty(machines, 'offsetLeft', { value: 500 })
    Object.defineProperty(machines, 'offsetWidth', { value: 80 })
    await harness.navigateByUrl('/machines')
    await new Promise((resolve) => setTimeout(resolve, 10))
    expect(strip.scrollLeft).toBe(500 + 80 - 200 + 16)
    fixture.destroy()
  })

  it('leaves the strip where it is after a navigation to somewhere that no tab names', async () => {
    @Component({ template: '' })
    class Elsewhere {}
    TestBed.resetTestingModule()
    TestBed.configureTestingModule({ providers: [provideRouter([{ path: 'zzz', component: Elsewhere }])] })
    const store = TestBed.inject(FleetStore)
    store.loaded.set(true)
    const harness = await RouterTestingHarness.create()
    const fixture = TestBed.createComponent(TopBar)
    await fixture.whenStable()
    const strip = fixture.nativeElement.querySelector('.tabs') as HTMLElement
    Object.defineProperty(strip, 'scrollLeft', { value: 7, writable: true })
    await harness.navigateByUrl('/zzz')
    await new Promise((resolve) => setTimeout(resolve, 10))
    expect(strip.scrollLeft).toBe(7)
  })
})

describe('scrollTabIntoView', () => {
  it('scrolls left to a tab before the view, right to one after it, and leaves one inside', () => {
    const strip = { scrollLeft: 300, clientWidth: 200 }
    scrollTabIntoView(strip, { offsetLeft: 100, offsetWidth: 80 })
    expect(strip.scrollLeft).toBe(84)
    scrollTabIntoView(strip, { offsetLeft: 10, offsetWidth: 80 })
    expect(strip.scrollLeft).toBe(0)
    scrollTabIntoView(strip, { offsetLeft: 400, offsetWidth: 80 })
    expect(strip.scrollLeft).toBe(296)
    scrollTabIntoView(strip, { offsetLeft: 350, offsetWidth: 80 })
    expect(strip.scrollLeft).toBe(296)
  })
})
