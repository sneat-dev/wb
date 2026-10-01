import { signal } from '@angular/core'
import { ComponentFixture, TestBed } from '@angular/core/testing'
import { Router, provideRouter } from '@angular/router'
import { vi } from 'vitest'
import { FETCH, FleetDocument, FleetStore, Session } from '@cockpit/fleet-data'
import { agent, fleetDocument, machine, pullRequest, repository, run, worktree } from '@cockpit/fleet-data/testing'
import { UiClock } from '@cockpit/ui/control'
import { RepositoryPanelView } from './repository-panel'

const NOW = Date.parse('2026-10-01T10:05:00Z')
const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()
const SESSION: Session = { principal: 'anonymous-local', capabilities: ['fleet.read'], code_browser_url: 'https://codegrapher.dev/' }
const OWNER: Session = { ...SESSION, principal: 'owner', capabilities: ['fleet.read', 'repo.content.read'] }

/** `sneat-co/sneat-go` on this machine and, cached, on beta; a second repository on beta only. */
function documentOf(extra: Partial<FleetDocument> = {}): FleetDocument {
  return fleetDocument({
    machines: [machine('alpha'), machine('beta', 'cached')],
    repositories: [
      repository('go-a', 'alpha', {
        name: 'sneat-co/sneat-go',
        default_branch: 'main',
        worktree_count: 2,
        local_branch_count: 5,
        remote_branch_count: 12,
        last_activity_at: '2026-10-01T10:00:00Z',
        remote_url_web: 'https://github.com/sneat-co/sneat-go',
        code_index: [{ indexer: 'codegrapher', state: 'stale', behind: 3 }],
        error: undefined,
      }),
      repository('go-b', 'beta', { name: 'sneat-co/sneat-go', route: 'cached', worktree_count: 1, active_agent_count: undefined }),
      repository('far', 'beta', { name: 'acme/far', host: undefined, route: 'cached', worktree_count: 0, active_agent_count: undefined }),
    ],
    worktrees: [{ ...worktree('w1', 'go-a', 'alpha'), task: 'fix-ci', branch: 'topic' }, { ...worktree('w2', 'go-a', 'alpha'), task: 'add-search' }, { ...worktree('w3', 'go-b', 'beta'), task: 'far-task', route: 'cached' as const }],
    pull_requests: [pullRequest('p1', 'go-a', 'w1', { number: 7 }), pullRequest('p2', 'go-a', undefined, { number: 8, state: undefined, url: 'javascript:alert(1)' })],
    agents: [run('run-1', 'running', { repository: 'go-a' }), agent('s2', 'go-a', 'idle')],
    ...extra,
  })
}

async function render(key: string, options: { document?: FleetDocument; session?: Session | null; page?: boolean; fetcher?: typeof fetch; url?: string } = {}) {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({
    providers: [
      provideRouter([{ path: '**', children: [] }]),
      { provide: UiClock, useValue: { now: signal(NOW) } },
      { provide: FETCH, useValue: options.fetcher ?? vi.fn(async () => new Response(JSON.stringify({ branches: [] }))) },
    ],
  })
  const store = TestBed.inject(FleetStore)
  store.document.set(options.document ?? documentOf())
  store.session.set(options.session === undefined ? SESSION : options.session)
  store.sessionStatus.set(options.session === null ? 'failed' : 'ready')
  store.now.set(NOW)
  store.loaded.set(true)
  if (options.url) await TestBed.inject(Router).navigateByUrl(options.url)
  const fixture = TestBed.createComponent(RepositoryPanelView)
  fixture.componentRef.setInput('repositoryKey', key)
  fixture.componentRef.setInput('page', options.page ?? false)
  await fixture.whenStable()
  return { fixture, root: fixture.nativeElement as HTMLElement, store }
}

const facts = (root: HTMLElement) => Object.fromEntries([...root.querySelectorAll('article.content > dl.facts > dt')].map((term) => [text(term), term.nextElementSibling as HTMLElement]))

describe('RepositoryPanelView', () => {
  it('renders nothing for a repository the document does not list', async () => {
    expect((await render('nope')).root.querySelector('app-panel-content')).toBeNull()
  })

  // cockpit-views#ac:repositories-merge-across-machines (the panel half)
  it('is one merged header over every checkout: the facts, summed, with the counts as links to the lists they counted', async () => {
    const { root } = await render('sneat-co/sneat-go')
    expect(text(root.querySelector('h2'))).toBe('sneat-co/sneat-go')
    const shown = facts(root)
    expect(Object.keys(shown)).toEqual(['Host', 'Machines', 'Worktrees', 'Branches', 'Running agents', 'Open pull requests', 'Code index', 'Last activity'])
    expect(text(shown['Host'])).toBe('github.com')
    expect(text(shown['Machines'])).toBe('alpha, beta')
    expect(text(shown['Worktrees'])).toBe('3')
    expect(shown['Worktrees'].querySelector('a')?.getAttribute('href')).toBe('/worktrees?q=repo:%22sneat-co%2Fsneat-go%22')
    expect(text(shown['Running agents'])).toBe('1')
    expect(shown['Running agents'].querySelector('a')?.getAttribute('href')).toBe('/agents?q=repo:%22sneat-co%2Fsneat-go%22')
    expect(text(shown['Open pull requests'])).toBe('1')
    expect(shown['Open pull requests'].querySelector('a')?.getAttribute('href')).toBe('/tasks?q=repo:%22sneat-co%2Fsneat-go%22&chips=pr')
    // The branch counts do not link, and say why.
    expect(text(shown['Branches'])).toBe('5 local / 12 remote')
    expect(shown['Branches'].querySelector('a')).toBeNull()
    expect(shown['Branches'].title).toBe('Branch counts do not link: there is no branches list page')
    expect(text(shown['Code index'])).toBe('stale')
    expect(text(shown['Last activity'])).toBe('5 min ago')
  })

  it('says what no machine reports, shows a quiet zero, an unlinkable name as plain text with its reason, and each scan error', async () => {
    const document = documentOf({
      repositories: [repository('x', 'alpha', { name: 'acme/odd"name', host: undefined, worktree_count: 3, active_agent_count: undefined, error: 'timeout' }), repository('y', 'alpha', { name: 'acme/zero', worktree_count: 0, active_agent_count: 0 })],
      worktrees: [],
      pull_requests: [],
      agents: [],
    })
    const odd = await render('acme/odd"name', { document })
    const oddFacts = facts(odd.root)
    expect(Object.keys(oddFacts)).not.toContain('Host')
    expect(text(oddFacts['Branches'])).toBe('not reported')
    // Whole lists and a checkout of this machine: no agent is a real zero, not an absence.
    expect(text(oddFacts['Running agents'])).toBe('0')
    expect(text(oddFacts['Code index'])).toBe('not reported')
    expect(text(oddFacts['Last activity'])).toBe('not reported')
    expect(oddFacts['Worktrees'].querySelector('a')).toBeNull()
    expect(oddFacts['Worktrees'].title).toContain('quote')
    expect(text(oddFacts['Scan error'])).toBe('timeout')
    const half = documentOf({ repositories: [repository('l', 'alpha', { name: 'acme/l', local_branch_count: 4 }), repository('m', 'alpha', { name: 'acme/m', remote_branch_count: 6 })], worktrees: [], pull_requests: [], agents: [] })
    expect(text(facts((await render('acme/l', { document: half })).root)['Branches'])).toBe('4 local / — remote')
    expect(text(facts((await render('acme/m', { document: half })).root)['Branches'])).toBe('— local / 6 remote')
    const zero = facts((await render('acme/zero', { document })).root)
    expect(text(zero['Worktrees'])).toBe('0')
    expect(zero['Worktrees'].querySelector('a')).toBeNull()
    expect(text(zero['Running agents'])).toBe('0')
  })

  it('relates the repository to the host, the code browser, its pull requests (an address only when it is a web address) and its agents', async () => {
    const { root } = await render('sneat-co/sneat-go')
    const links = [...root.querySelectorAll('[aria-label="Links"] a')]
    expect(links.map(text)).toEqual(['Open on github.com', 'Browse code'])
    expect(links.map((link) => link.getAttribute('href'))).toEqual(['https://github.com/sneat-co/sneat-go', 'https://codegrapher.dev/github.com/sneat-co/sneat-go'])
    expect(links.every((link) => link.getAttribute('rel') === 'noopener noreferrer')).toBe(true)
    expect([...root.querySelectorAll('[aria-label="Pull requests"] li')].map(text)).toEqual(['#7 open', '#8'])
    expect(root.querySelectorAll('[aria-label="Pull requests"] a')).toHaveLength(1)
    expect([...root.querySelectorAll('[aria-label="Agents"] a')].map((link) => link.getAttribute('href'))).toEqual(['/agents/run-1', '/agents/s2'])
  })

  it('offers no link to the host without a checked address, and none to the code browser without one configured', async () => {
    const none = await render('acme/far', { session: { ...SESSION, code_browser_url: undefined } })
    expect(none.root.querySelector('[aria-label="Links"]')).toBeNull()
    const onlyHost = await render('sneat-co/sneat-go', { session: { ...SESSION, code_browser_url: undefined } })
    expect([...onlyHost.root.querySelectorAll('[aria-label="Links"] a')].map(text)).toEqual(['Open on github.com'])
    const hostless = await render('acme/solo', { document: documentOf({ repositories: [repository('s', 'alpha', { name: 'acme/solo', host: undefined, remote_url_web: 'https://example.test/acme/solo' })] }) })
    expect([...hostless.root.querySelectorAll('[aria-label="Links"] a')].map(text)).toEqual(['Open on the host'])
  })

  // cockpit-views#ac:repository-detail-loads-branches-lazily (the sections)
  it('has one section per machine, the first open, each with the worktrees of its own checkout', async () => {
    const { root } = await render('sneat-co/sneat-go')
    const sections = [...root.querySelectorAll('app-repository-machine-section')]
    expect(sections).toHaveLength(2)
    expect(sections.map((section) => (section.querySelector('details') as HTMLDetailsElement).open)).toEqual([true, false])
    expect(sections.map((section) => [...section.querySelectorAll('.worktrees .task')].map(text))).toEqual([['fix-ci', 'add-search'], ['far-task']])
    expect(sections.map((section) => section.querySelector('details')?.id)).toEqual(['machine-mach-alpha', 'machine-mach-beta'])
  })

  it('shows a repository on one machine, whose machine the document does not list, as one section', async () => {
    const { root } = await render('acme/far', { document: documentOf({ machines: [machine('alpha')] }) })
    expect(root.querySelectorAll('app-repository-machine-section')).toHaveLength(1)
    expect(root.querySelector('app-repository-machine-section app-machine-chip')).toBeNull()
  })

  it('lists the library\'s copy commands, the create-a-task one as a template, and ends with the collapsed Raw data block', async () => {
    const { root } = await render('sneat-co/sneat-go')
    const entries = [...root.querySelectorAll('[aria-label="Copy command"] li')].map(text)
    expect(entries.length).toBeGreaterThanOrEqual(3)
    expect(entries[0]).toContain('Create a task worktree')
    expect(entries[0]).toContain('Copy template')
    expect(entries.join(' ')).toContain("wb branch list --repo='sneat-co/sneat-go'")
    expect(entries.join(' ')).toContain("wb fleet status --filter='sneat-co/sneat-go'")
    expect([...root.querySelectorAll('section')].at(-1)?.getAttribute('aria-label')).toBe('Raw data')
    expect((root.querySelector('details.raw') as HTMLDetailsElement).open).toBe(false)
  })

  it('is the detail page when asked', async () => {
    expect((await render('sneat-co/sneat-go', { page: true })).root.querySelector('.content')?.classList.contains('page')).toBe(true)
    expect((await render('sneat-co/sneat-go')).root.querySelector('.content')?.classList.contains('page')).toBe(false)
  })

  // cockpit#ac:readme-needs-owner
  it('reads the README on this machine for an owner, and says that another machine\'s is read there', async () => {
    const readme = vi.fn(async (url: string | URL | Request) => (String(url).includes('readme') ? new Response('# Widgets\n') : new Response(JSON.stringify({ branches: [] }))))
    const owner = await render('sneat-co/sneat-go', { session: OWNER, fetcher: readme as unknown as typeof fetch })
    await vi.waitFor(async () => {
      await owner.fixture.whenStable()
      if (owner.root.querySelector('app-readme-content h4') === null) throw new Error('not yet')
    })
    expect(readme.mock.calls.map((call) => String(call[0]))).toContain('/api/v1/cockpit/readme?repository=go-a')
    const cached = await render('acme/far', { session: OWNER, fetcher: readme as unknown as typeof fetch })
    expect(text(cached.root.querySelector('.readme-block'))).toBe('READMEThe README is read on the machine that holds the repository; this entry is cached from another machine.')
    expect(cached.root.querySelector('app-readme-section')).toBeNull()
  })

  it('asks for an owner session without one, and never calls the README route', async () => {
    const fetcher = vi.fn(async () => new Response(JSON.stringify({ branches: [] })))
    const { root } = await render('sneat-co/sneat-go', { fetcher: fetcher as unknown as typeof fetch })
    expect(text(root.querySelector('app-readme-section'))).toContain('An owner session is needed to read the README. Run wb cockpit')
    expect(fetcher.mock.calls.map((call) => String(call[0])).some((url) => url.includes('readme'))).toBe(false)
  })

  describe('a machine chip of the list', () => {
    const revealed = (root: HTMLElement) => [...root.querySelectorAll('app-repository-machine-section details')].map((details) => (details as HTMLDetailsElement).open)

    function stubScroll(fixture: ComponentFixture<unknown>) {
      const scroll = vi.fn()
      for (const section of fixture.nativeElement.querySelectorAll('app-repository-machine-section')) section.scrollIntoView = scroll
      return scroll
    }

    it('opens the section of the machine its fragment names and scrolls to it, once', async () => {
      const frames: FrameRequestCallback[] = []
      vi.spyOn(window, 'requestAnimationFrame').mockImplementation((callback) => frames.push(callback))
      const { fixture, root, store } = await render('sneat-co/sneat-go', { url: '/#machine-mach-beta' })
      const scroll = stubScroll(fixture)
      expect(revealed(root)).toEqual([true, true])
      frames.splice(0).forEach((callback) => callback(0))
      expect(scroll).toHaveBeenCalledTimes(1)
      // A change of the sections that follows (another repository's checkout appearing) does not scroll again.
      store.document.set(documentOf({ repositories: [...documentOf().repositories, repository('go-c', 'gamma', { name: 'sneat-co/sneat-go', route: 'cached' })] }))
      await fixture.whenStable()
      frames.splice(0).forEach((callback) => callback(0))
      expect(scroll).toHaveBeenCalledTimes(1)
      vi.restoreAllMocks()
    })

    it('does nothing for no fragment, or one that names no machine of the repository', async () => {
      const none = await render('sneat-co/sneat-go')
      expect(revealed(none.root)).toEqual([true, false])
      const unknown = await render('sneat-co/sneat-go', { url: '/#machine-mach-nowhere' })
      expect(revealed(unknown.root)).toEqual([true, false])
    })
  })
})
