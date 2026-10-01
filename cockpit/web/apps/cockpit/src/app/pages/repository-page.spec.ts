import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { Session, FleetDocument } from '@cockpit/fleet-data'
import { fleetDocument, repository } from '@cockpit/fleet-data/testing'
import { RepositoryPage } from './repository-page'
import { SESSION, fields, openPage, settle } from './test-harness'

const OWNER: Session = { ...SESSION, principal: 'owner', capabilities: ['fleet.read', 'repo.content.read'] }

const text = (root: HTMLElement) => (root.textContent as string).replace(/\s+/g, ' ').trim()

function readmeFetch(markdown: string, status = 200, body?: unknown) {
  return vi.fn(async () => new Response(body === undefined ? markdown : JSON.stringify(body), { status })) as unknown as typeof fetch
}

const indexed = {
  indexer: 'codegrapher',
  state: 'fresh' as const,
  receipt_at: '2026-10-01T10:00:00Z',
  statistics: { indexed: true, files: 12, symbols: 40, edges: 90, kinds: [{ kind: 'function', count: 30 }, { kind: 'struct', count: 10 }] },
}

function documentWith(extra: Partial<FleetDocument> = {}): FleetDocument {
  return fleetDocument({
    code_index_provider: 'codegrapher',
    repositories: [
      repository('r1', 'alpha', { default_branch: 'main', code_index: [indexed] }),
      repository('r2', 'beta', { route: 'cached', host: undefined, name: 'acme/r2', worktree_count: 1, active_agent_count: undefined }),
    ],
    ...extra,
  })
}

// cockpit#ac:readme-needs-owner
describe('RepositoryPage', () => {
  it('shows the entry\'s metadata from the fleet document', async () => {
    const { root, component } = await openPage('/repositories/r1', RepositoryPage, documentWith())
    expect(component.id()).toBe('r1')
    expect(root.querySelector('h2')?.textContent).toBe('github.com/acme/r1')
    expect(Object.keys(fields(root))).toEqual(['Machine', 'Source', 'Host', 'Default branch', 'Worktrees', 'Running agents', 'Code index', 'Code browser'])
    expect(fields(root)).toEqual({
      Machine: 'alpha',
      Source: 'local',
      Host: 'github.com',
      'Default branch': 'main',
      Worktrees: '2',
      'Running agents': '1',
      'Code index': 'fresh 5 min ago',
      'Code browser': 'Code',
    })
    expect(root.querySelector('a.code-link')?.getAttribute('href')).toBe('https://codegrapher.dev/github.com/acme/r1')
  })

  it('drills down from its counts to the filtered lists', async () => {
    const { root } = await openPage('/repositories/r1', RepositoryPage, documentWith())
    const counts = [...root.querySelectorAll('app-count')]
    expect(counts[0].querySelector('a')?.getAttribute('href')).toBe('/worktrees?repository=r1')
    expect([...counts[0].querySelectorAll('li')].map((item) => item.textContent)).toEqual(['task-w1 (branch-w1)', 'task-w2 (branch-w2)'])
    expect(counts[1].querySelector('a')?.getAttribute('href')).toBe('/agents?repository=r1&state=running')
    expect(root.querySelector('.back a')?.getAttribute('href')).toBe('/repositories')
  })

  it('shows dashes for what another machine\'s snapshot does not carry, and does not read its README', async () => {
    const fetcher = readmeFetch('# never')
    const { root } = await openPage('/repositories/r2', RepositoryPage, documentWith(), OWNER, fetcher)
    expect(root.querySelector('.unknown[title^="Not known"]')).not.toBeNull()
    expect(text(root)).toContain('The README is read on the machine that holds the repository')
    expect(root.querySelector('app-readme-section')).toBeNull()
    expect(fetcher).not.toHaveBeenCalled()
    expect(text(root)).toContain('Not known for a checkout on another machine')
  })

  it('shows a dash for the code browser when the origin names no forge host, and an error the repository carries', async () => {
    const doc = documentWith({ repositories: [repository('r1', 'alpha', { host: undefined, error: 'timeout' })] })
    const { root } = await openPage('/repositories/r1', RepositoryPage, doc)
    expect(root.querySelector('a.code-link')).toBeNull()
    expect(root.querySelector('.row-error')?.textContent).toBe('could not be read: timeout')
    expect(fields(root)['Host']).toBe('—')
  })

  it('renders the README of a repository on this machine for an owner session', async () => {
    const fetcher = readmeFetch('# Widgets\n\nHello **world**.\n')
    const { root, harness } = await openPage('/repositories/r1', RepositoryPage, documentWith(), OWNER, fetcher)
    await settle(harness.fixture, () => root.querySelector('app-readme-content h4') !== null)
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect((fetcher as unknown as ReturnType<typeof vi.fn>).mock.calls[0][0]).toBe('/api/v1/cockpit/readme?repository=r1')
    expect(root.querySelector('app-readme-content h4')?.textContent).toBe('Widgets')
    expect(root.querySelector('app-readme-content strong')?.textContent).toBe('world')
  })

  it('asks for an owner session, naming wb cockpit, and does not call the README route without one', async () => {
    const fetcher = readmeFetch('# never')
    const { root } = await openPage('/repositories/r1', RepositoryPage, documentWith(), SESSION, fetcher)
    expect(text(root.querySelector('app-readme-section') as HTMLElement)).toBe('READMEAn owner session is needed to read the README. Run wb cockpit to open one.')
    // A session read that failed says so, and still names the command.
    const failed = await openPage('/repositories/r1', RepositoryPage, documentWith(), null, fetcher)
    expect(text(failed.root.querySelector('app-readme-section') as HTMLElement)).toContain('The session could not be read')
    expect(text(failed.root.querySelector('app-readme-section') as HTMLElement)).toContain('wb cockpit')
    // Until the session read has answered nothing is claimed.
    failed.store.sessionStatus.set('loading')
    failed.harness.detectChanges()
    expect(text(failed.root.querySelector('app-readme-section') as HTMLElement)).toBe('READMEReading the README…')
    expect(fetcher).not.toHaveBeenCalled()
  })

  // cockpit#ac:hostile-readme-is-inert
  it('renders a hostile README as inert text with no script, handler or loading element', async () => {
    const hostile = '# Hi\n\n<script>window.__pwned = 1</script>\n\n<img src="https://evil.example/p.png" onerror="window.__pwned = 2">\n\n[click](javascript:window.__pwned=3)\n\n![x](https://evil.example/q.png)\n'
    const { root, harness } = await openPage('/repositories/r1', RepositoryPage, documentWith(), OWNER, readmeFetch(hostile))
    await settle(harness.fixture, () => root.querySelector('app-readme-content h4') !== null)
    const readme = root.querySelector('app-readme-content') as HTMLElement
    expect(readme.querySelector('script, img, iframe, style, object, embed, form, svg')).toBeNull()
    expect([...readme.querySelectorAll('a')].map((link) => link.getAttribute('href'))).toEqual(['https://evil.example/q.png'])
    expect(text(readme)).toContain('<script>window.__pwned = 1</script>')
    expect((globalThis as { __pwned?: number }).__pwned).toBeUndefined()
    for (const element of readme.querySelectorAll('*')) {
      expect(element.getAttributeNames().filter((name) => name === 'style' || name.startsWith('on'))).toEqual([])
    }
  })

  it('says a README that is not a regular file is not shown, and shows no content', async () => {
    const fetcher = readmeFetch('', 403, { error: 'readme_not_a_regular_file' })
    const { root, harness } = await openPage('/repositories/r1', RepositoryPage, documentWith(), OWNER, fetcher)
    await settle(harness.fixture, () => text(root.querySelector('app-readme-section') as HTMLElement).includes('not a regular file'))
    expect(text(root.querySelector('app-readme-section') as HTMLElement)).toContain('not a regular file')
    expect(root.querySelector('app-readme-content')).toBeNull()
  })

  // cockpit#ac:code-index-panel
  it('shows the code-index panel: the totals and the symbols by kind', async () => {
    const { root } = await openPage('/repositories/r1', RepositoryPage, documentWith())
    const panel = root.querySelector('app-code-index-panel') as HTMLElement
    expect([...panel.querySelectorAll('.totals div')].map(text)).toEqual(['Files12', 'Symbols40', 'Edges90'])
    expect([...panel.querySelectorAll('.kinds li')].map(text)).toEqual(['function30', 'struct10'])
  })

  it('says not indexed, and says no provider is configured', async () => {
    const notIndexed = documentWith({ repositories: [repository('r1', 'alpha', { code_index: [{ indexer: 'codegrapher', state: 'never', statistics: { indexed: false, files: 0, symbols: 0, edges: 0, kinds: [] } }] })] })
    expect(text((await openPage('/repositories/r1', RepositoryPage, notIndexed)).root.querySelector('app-code-index-panel') as HTMLElement)).toContain('Not indexed.')
    const none = documentWith({ code_index_provider: undefined })
    expect(text((await openPage('/repositories/r1', RepositoryPage, none)).root.querySelector('app-code-index-panel') as HTMLElement)).toContain('No code-index provider is configured.')
  })

  it('says it is waiting while the first pass runs, and that the repository is not there once it is complete', async () => {
    const warming = await openPage('/repositories/r1', RepositoryPage, fleetDocument({ warming_up: true, repositories: [] }))
    expect(text(warming.root)).toContain('Waiting for the fleet snapshot')
    const gone = await openPage('/repositories/r-gone', RepositoryPage, documentWith())
    expect(gone.store.loaded()).toBe(true)
    expect(text(gone.root)).toContain('This repository is not in the fleet document')
    expect(gone.root.querySelector('app-readme-section')).toBeNull()
  })

  it('says it is waiting before the first read has been answered', async () => {
    const { store, harness } = await openPage('/repositories/r1', RepositoryPage, fleetDocument({ warming_up: false, repositories: [] }))
    store.loaded.set(false)
    harness.detectChanges()
    expect(text(harness.routeNativeElement as HTMLElement)).toContain('Waiting for the fleet snapshot')
  })

  it('renders on its own over an empty, warming-up fleet', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([])] })
    const fixture = TestBed.createComponent(RepositoryPage)
    fixture.componentRef.setInput('id', 'anything')
    await fixture.whenStable()
    expect(fixture.nativeElement.querySelector('h2')?.textContent).toBe('Repository')
    expect(fixture.nativeElement.textContent).toContain('Waiting for the fleet snapshot')
  })
})
