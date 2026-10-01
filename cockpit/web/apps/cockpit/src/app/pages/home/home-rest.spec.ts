import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { FETCH, FleetStore, Session } from '@cockpit/fleet-data'
import { CHART_ENGINE } from '@cockpit/ui/chart'
import { ClipboardWriter } from '@cockpit/ui/control'
import { FIXED_CLOCK, fleet, modelOf } from './home-testing'
import { ACTION_CAPABILITIES, HomeRest, PHONE_QUERY, registryTargets } from './home-rest'
import { HomeRegistry } from './home-registry'

const session = (capabilities: string[]): Session => ({ principal: 'owner', capabilities, code_browser_url: 'https://codegrapher.dev/' })

interface Media {
  matches: boolean
  listeners: Set<(event: MediaQueryListEvent) => void>
}

function stubPhone(matches: boolean): Media {
  const media: Media = { matches, listeners: new Set() }
  // The charts ask about the colour scheme and motion too: only the phone query is the test's.
  vi.stubGlobal('matchMedia', (query: string) => ({
    get matches() {
      return query === PHONE_QUERY && media.matches
    },
    addEventListener: (_: string, listener: (event: MediaQueryListEvent) => void) => query === PHONE_QUERY && media.listeners.add(listener),
    removeEventListener: (_: string, listener: (event: MediaQueryListEvent) => void) => query === PHONE_QUERY && media.listeners.delete(listener),
  }))
  return media
}

async function render(options: { capabilities?: string[]; warming?: boolean; document?: ReturnType<typeof fleet> } = {}) {
  const request = vi.fn().mockResolvedValue(undefined)
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({
    providers: [
      FIXED_CLOCK,
      provideRouter([]),
      { provide: HomeRegistry, useValue: { request, offered: () => undefined } },
      { provide: FETCH, useValue: async () => new Response('{}', { status: 404 }) },
      { provide: CHART_ENGINE, useValue: async () => ({ create: () => ({ update: vi.fn(), destroy: vi.fn() }) }) },
      { provide: ClipboardWriter, useValue: { copy: vi.fn() } },
    ],
  })
  TestBed.inject(FleetStore).session.set(options.capabilities === undefined ? null : session(options.capabilities))
  const fixture = TestBed.createComponent(HomeRest)
  fixture.componentRef.setInput('model', modelOf(options.document ?? fleet()))
  fixture.componentRef.setInput('warming', options.warming ?? false)
  await fixture.whenStable()
  await new Promise((done) => setTimeout(done, 10))
  await fixture.whenStable()
  return { fixture, root: fixture.nativeElement as HTMLElement, request }
}

const headings = (root: HTMLElement) => [...root.querySelectorAll('h2')].map((heading) => (heading.textContent ?? '').replace(/\s+/g, ' ').trim())

describe('registryTargets', () => {
  it('names the worktrees at risk and the local pull requests ready to land, and no pull request of another machine', () => {
    expect(registryTargets(modelOf(fleet()))).toEqual(['worktree:wt-1', 'worktree:wt-2', 'worktree:wt-7', 'pull_request:pr-r1', 'pull_request:pr-r2', 'pull_request:pr-r3'])
    const document = fleet()
    Object.assign(
      document.pull_requests.find((pullRequest) => pullRequest.id === 'pr-r2')!,
      { machine: 'vm', machine_id: 'mach-vm', route: 'live-remote' },
    )
    expect(registryTargets(modelOf(document))).not.toContain('pull_request:pr-r2')
  })
})

describe('HomeRest', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('shows every section after "Needs you", in order, on a desktop', async () => {
    const { root } = await render()
    expect(headings(root)).toEqual(['Ready to land 2', 'In flight 4', 'Resume', 'Cleanup', 'Fleet health 2', 'Throughput'])
    expect(root.querySelector('button.home-more-toggle')).toBeNull()
  })

  it('asks the registry for the actions of its rows only for a session that holds an action capability', async () => {
    const anonymous = await render({ capabilities: ['fleet.read'] })
    expect(anonymous.request).not.toHaveBeenCalled()
    const unknown = await render()
    expect(unknown.request).not.toHaveBeenCalled()
    for (const capability of ACTION_CAPABILITIES) {
      const owner = await render({ capabilities: ['fleet.read', capability] })
      expect(owner.request, capability).toHaveBeenCalledWith(registryTargets(modelOf(fleet())))
    }
  })

  it('puts everything after "In flight" behind one "More" disclosure on a phone, and opens and closes it', async () => {
    const media = stubPhone(true)
    const { fixture, root } = await render()
    expect(headings(root)).toEqual(['Ready to land 2', 'In flight 4'])
    const toggle = root.querySelector('button.home-more-toggle') as HTMLButtonElement
    expect(toggle.textContent?.trim()).toBe('More')
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    toggle.click()
    await fixture.whenStable()
    await new Promise((done) => setTimeout(done, 10))
    await fixture.whenStable()
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    expect(headings(root)).toEqual(['Ready to land 2', 'In flight 4', 'Resume', 'Cleanup', 'Fleet health 2', 'Throughput'])
    expect(root.querySelector('#home-more')).not.toBeNull()
    toggle.click()
    await fixture.whenStable()
    expect(headings(root)).toEqual(['Ready to land 2', 'In flight 4'])
    expect(media.listeners.size).toBe(1)
  })

  it('follows the window when it becomes a phone or stops being one, and lets go of the query when it is destroyed', async () => {
    const media = stubPhone(false)
    const { fixture, root } = await render()
    expect(root.querySelector('button.home-more-toggle')).toBeNull()
    media.matches = true
    for (const listener of media.listeners) listener({ matches: true } as MediaQueryListEvent)
    await fixture.whenStable()
    expect(root.querySelector('button.home-more-toggle')).not.toBeNull()
    for (const listener of media.listeners) listener({ matches: false } as MediaQueryListEvent)
    await fixture.whenStable()
    expect(root.querySelector('button.home-more-toggle')).toBeNull()
    fixture.destroy()
    expect(media.listeners.size).toBe(0)
  })

  it('is a desktop where there is no matchMedia', async () => {
    vi.stubGlobal('matchMedia', undefined)
    const { root } = await render()
    expect(root.querySelector('button.home-more-toggle')).toBeNull()
  })
})
