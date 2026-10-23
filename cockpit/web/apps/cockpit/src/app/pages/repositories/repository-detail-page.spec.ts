import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { vi } from 'vitest'
import { FleetDocument, Session } from '@cockpit/fleet-data'
import { fleetDocument, repository, worktree } from '@cockpit/fleet-data/testing'
import { RepositoryDetailPage } from './repository-detail-page'
import { RepositoriesPage } from './repositories-page'
import { SESSION, openPage, settle } from '../test-harness'

const OWNER: Session = { ...SESSION, principal: 'owner', capabilities: ['fleet.read', 'repo.content.read'] }
const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()

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
      repository('r1b', 'beta', { route: 'cached', name: 'acme/r1' }),
      repository('r2', 'beta', { route: 'cached', host: undefined, name: 'acme/r2', worktree_count: 1, active_agent_count: undefined }),
    ],
    worktrees: [worktree('w1', 'r1', 'alpha'), worktree('w2', 'r1', 'alpha')],
    ...extra,
  })
}

function branches(...names: string[]) {
  return vi.fn(async () => new Response(JSON.stringify({ branches: names.map((name) => ({ id: `b-${name}`, machine: 'alpha', machine_id: 'mach-alpha', route: 'local', repository: 'r1', name, scope: 'local' })) }))) as unknown as typeof fetch
}

describe('RepositoryDetailPage', () => {
  // cockpit-views#ac:repository-detail-loads-branches-lazily
  it('shows a merged header and one section per machine, asks for the branches of the first only when it opens, with placeholder rows until they come', async () => {
    let release: (response: Response) => void = () => undefined
    const fetcher = vi.fn(() => new Promise<Response>((done) => (release = done))) as unknown as typeof fetch
    const { root, harness } = await openPage('/repositories/github.com/acme/r1', RepositoryDetailPage, documentWith(), SESSION, fetcher)
    expect(text(root.querySelector('h2'))).toBe('acme/r1')
    expect(root.querySelectorAll('app-repository-machine-section')).toHaveLength(2)
    expect(root.querySelector('.back a')?.getAttribute('href')).toBe('/repositories')
    expect(fetcher).not.toHaveBeenCalled()
    await settle(harness.fixture, () => root.querySelectorAll('.skeleton').length === 3)
    await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(1))
    expect((fetcher as unknown as ReturnType<typeof vi.fn>).mock.calls[0][0]).toBe('/api/v1/cockpit/branches?repository=r1')
    release(new Response(JSON.stringify({ branches: [{ id: 'b1', machine: 'alpha', machine_id: 'mach-alpha', route: 'local', repository: 'r1', name: 'main', scope: 'local' }] })))
    await settle(harness.fixture, () => root.querySelector('.branches .branch-name') !== null)
    expect(root.querySelectorAll('.skeleton')).toHaveLength(0)
    expect(text(root.querySelector('.branches .branch-name'))).toBe('main')
  })

  it('finds a repository with no host by `-`, and takes the host to choose between two of one name', async () => {
    const hostless = await openPage('/repositories/-/acme/r2', RepositoryDetailPage, documentWith(), SESSION, branches())
    expect(text(hostless.root.querySelector('h2'))).toBe('acme/r2')
    const twoHosts = documentWith({ repositories: [repository('a', 'alpha', { name: 'acme/dup', host: 'github.com' }), repository('b', 'alpha', { name: 'acme/dup', host: 'gitlab.example.com' })] })
    const second = await openPage('/repositories/gitlab.example.com/acme/dup', RepositoryDetailPage, twoHosts, SESSION, branches())
    expect(text(second.root.querySelector('.facts'))).toContain('gitlab.example.com')
  })

  it('says it is waiting for the snapshot, and that the repository is not there once the snapshot is complete', async () => {
    const warming = await openPage('/repositories/github.com/acme/r1', RepositoryDetailPage, fleetDocument({ warming_up: true, repositories: [] }))
    expect(text(warming.root)).toContain('Waiting for the fleet snapshot')
    const gone = await openPage('/repositories/github.com/acme/nope', RepositoryDetailPage, documentWith(), SESSION, branches())
    expect(text(gone.root)).toContain('This repository is not in the fleet document')
    expect(gone.root.querySelector('app-repository-panel')).toBeNull()
    const unread = await openPage('/repositories/github.com/acme/r1', RepositoryDetailPage, fleetDocument({ repositories: [] }))
    unread.store.loaded.set(false)
    unread.harness.detectChanges()
    expect(text(unread.root)).toContain('Waiting for the fleet snapshot')
  })

  it('renders on its own over an empty, warming-up fleet', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([])] })
    const fixture = TestBed.createComponent(RepositoryDetailPage)
    fixture.componentRef.setInput('host', 'github.com')
    fixture.componentRef.setInput('owner', 'acme')
    fixture.componentRef.setInput('name', 'r1')
    await fixture.whenStable()
    expect(fixture.nativeElement.textContent).toContain('Waiting for the fleet snapshot')
  })

  // cockpit-views#ac:detail-routes-render-the-same-panel
  it('is the side panel\'s content, from the same component, ending with the collapsed Raw data block', async () => {
    const page = await openPage('/repositories/github.com/acme/r1', RepositoryDetailPage, documentWith(), SESSION, branches())
    const list = await openPage('/repositories?sel=r1', RepositoriesPage, documentWith(), SESSION, branches())
    const content = (root: HTMLElement) => root.querySelector('app-repository-panel') as HTMLElement
    for (const selector of ['article.content > dl.facts', '[aria-label="Machines"]', '[aria-label="Copy command"]']) {
      expect(text(content(page.root).querySelector(selector)), selector).toBe(text(content(list.root).querySelector(selector)))
    }
    expect(content(page.root).querySelector('.content')?.classList.contains('page')).toBe(true)
    expect(content(list.root).querySelector('.content')?.classList.contains('page')).toBe(false)
    const blocks = [...content(page.root).querySelectorAll('section')]
    expect(blocks.at(-1)?.getAttribute('aria-label')).toBe('Raw data')
    expect((content(page.root).querySelector('details.raw') as HTMLDetailsElement).open).toBe(false)
  })

  // cockpit#ac:code-index-panel
  it('shows the code-index panel of each machine: the totals and the symbols by kind, and says another machine has none here', async () => {
    const { root } = await openPage('/repositories/github.com/acme/r1', RepositoryDetailPage, documentWith(), SESSION, branches())
    const panels = [...root.querySelectorAll('app-code-index-panel')]
    expect([...panels[0].querySelectorAll('.totals div')].map(text)).toEqual(['Files12', 'Symbols40', 'Edges90'])
    expect([...panels[0].querySelectorAll('.kinds li')].map(text)).toEqual(['function30', 'struct10'])
    expect(text(panels[1])).toContain('another machine')
    const notIndexed = documentWith({ repositories: [repository('r1', 'alpha', { code_index: [{ indexer: 'codegrapher', state: 'never', statistics: { indexed: false, files: 0, symbols: 0, edges: 0, kinds: [] } }] })] })
    expect(text((await openPage('/repositories/github.com/acme/r1', RepositoryDetailPage, notIndexed, SESSION, branches())).root.querySelector('app-code-index-panel'))).toContain('Not indexed.')
    const none = documentWith({ code_index_provider: undefined })
    expect(text((await openPage('/repositories/github.com/acme/r1', RepositoryDetailPage, none, SESSION, branches())).root.querySelector('app-code-index-panel'))).toContain('No code-index provider is configured.')
  })

  // cockpit#ac:readme-needs-owner
  it('renders the README of the checkout on this machine for an owner session, as inert text', async () => {
    const hostile = '# Hi\n\n<script>window.__pwned = 1</script>\n\n<img src="https://evil.example/p.png" onerror="window.__pwned = 2">\n\n[click](javascript:window.__pwned=3)\n\n![x](https://evil.example/q.png)\n'
    const fetcher = vi.fn(async (url: string | URL | Request) => (String(url).includes('readme') ? new Response(hostile) : new Response(JSON.stringify({ branches: [] })))) as unknown as typeof fetch
    const { root, harness } = await openPage('/repositories/github.com/acme/r1', RepositoryDetailPage, documentWith(), OWNER, fetcher)
    await settle(harness.fixture, () => root.querySelector('app-readme-content h4') !== null)
    expect((fetcher as unknown as ReturnType<typeof vi.fn>).mock.calls.map((call) => String(call[0]))).toContain('/api/v1/cockpit/readme?repository=r1')
    const readme = root.querySelector('app-readme-content') as HTMLElement
    expect(readme.querySelector('script, img, iframe, style, object, embed, form, svg')).toBeNull()
    expect([...readme.querySelectorAll('a')].map((link) => link.getAttribute('href'))).toEqual(['https://evil.example/q.png'])
    expect(text(readme)).toContain('<script>window.__pwned = 1</script>')
    expect((globalThis as { __pwned?: number }).__pwned).toBeUndefined()
    for (const element of readme.querySelectorAll('*')) expect(element.getAttributeNames().filter((name) => name === 'style' || name.startsWith('on'))).toEqual([])
  })

  it('asks for an owner session, naming wb cockpit, and does not call the README route without one, or before the session has answered', async () => {
    const fetcher = branches()
    const anonymous = await openPage('/repositories/github.com/acme/r1', RepositoryDetailPage, documentWith(), SESSION, fetcher)
    expect(text(anonymous.root.querySelector('app-readme-section'))).toBe('READMEAn owner session is needed to read the README. Run wb cockpit to open one.')
    const failed = await openPage('/repositories/github.com/acme/r1', RepositoryDetailPage, documentWith(), null, fetcher)
    expect(text(failed.root.querySelector('app-readme-section'))).toContain('The session could not be read')
    expect(text(failed.root.querySelector('app-readme-section'))).toContain('wb cockpit')
    failed.store.sessionStatus.set('loading')
    failed.harness.detectChanges()
    expect(text(failed.root.querySelector('app-readme-section'))).toBe('READMEReading the README…')
    expect((fetcher as unknown as ReturnType<typeof vi.fn>).mock.calls.map((call) => String(call[0])).some((url) => url.includes('readme'))).toBe(false)
  })

  it('says a README that is not a regular file is not shown', async () => {
    const fetcher = vi.fn(async (url: string | URL | Request) => (String(url).includes('readme') ? new Response(JSON.stringify({ error: 'readme_not_a_regular_file' }), { status: 403 }) : new Response(JSON.stringify({ branches: [] })))) as unknown as typeof fetch
    const { root, harness } = await openPage('/repositories/github.com/acme/r1', RepositoryDetailPage, documentWith(), OWNER, fetcher)
    await settle(harness.fixture, () => text(root.querySelector('app-readme-section')).includes('not a regular file'))
    expect(root.querySelector('app-readme-content')).toBeNull()
  })
})
