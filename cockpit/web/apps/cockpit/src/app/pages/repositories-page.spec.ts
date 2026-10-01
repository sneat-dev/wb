import { By } from '@angular/platform-browser'
import { Router, provideRouter } from '@angular/router'
import { TestBed } from '@angular/core/testing'
import { fleetDocument, repository } from '@cockpit/fleet-data/testing'
import { FilterBar } from '@cockpit/ui'
import { RepositoriesPage } from './repositories-page'
import { SESSION, bodyRows, openPage } from './test-harness'

// cockpit#ac:every-page-lists-its-collection, cockpit#ac:counts-drill-down,
// cockpit#ac:repository-links-to-code-browser
describe('RepositoriesPage', () => {
  it('lists every repository with its machine, route and age, and counts', async () => {
    const { root } = await openPage('/repositories', RepositoriesPage)
    expect(bodyRows(root)).toEqual([
      ['github.com/acme/r1', 'alpha', 'local', '2', '1', '—', '—', 'Code'],
      ['acme/r2', 'beta', 'cached, 5 min ago', '1', '—', '—', '—', '—'],
    ])
  })

  // cockpit#ac:code-index-freshness-appears
  it('shows the code-index freshness of a repository, or a dash when it is not known', async () => {
    const doc = fleetDocument({
      repositories: [
        repository('r1', 'alpha', { code_index: [{ indexer: 'codegrapher', state: 'stale', behind: 3 }] }),
        repository('r2', 'alpha', { code_index: [{ indexer: 'codegrapher', state: 'never' }] }),
        repository('r3', 'alpha'),
        repository('r4', 'alpha', { code_index: [{ indexer: 'codegrapher', state: 'fresh', receipt_at: '2026-10-01T10:00:00Z' }] }),
      ],
    })
    const { root } = await openPage('/repositories', RepositoriesPage, doc)
    expect(bodyRows(root).map((row) => row[6])).toEqual(['stale, 3 behind', 'never', '—', 'fresh 5 min ago'])
  })

  it('links each repository to its page', async () => {
    const { root } = await openPage('/repositories', RepositoriesPage)
    expect([...root.querySelectorAll('a.row-link')].map((link) => link.getAttribute('href'))).toEqual(['/repositories/r1', '/repositories/r2'])
  })

  it('keeps only the machine named in the URL query', async () => {
    const { root, component } = await openPage('/repositories?machine=mach-beta', RepositoriesPage)
    expect(bodyRows(root).map((row) => row[0])).toEqual(['acme/r2'])
    expect(component.machine()).toBe('mach-beta')
  })

  it('offers repositories labelled with their machine, narrowed to the selected machine, and a "cached" dash with its own reason', async () => {
    const all = await openPage('/repositories', RepositoriesPage)
    const bar = all.harness.fixture.debugElement.query(By.directive(FilterBar)).componentInstance as FilterBar
    expect(bar.repositories()?.map((o) => o.label)).toEqual(['github.com/acme/r1 · alpha', 'acme/r2 · beta'])
    const narrowed = await openPage('/repositories?machine=mach-beta', RepositoriesPage)
    const narrowedBar = narrowed.harness.fixture.debugElement.query(By.directive(FilterBar)).componentInstance as FilterBar
    expect(narrowedBar.repositories()?.map((o) => o.id)).toEqual(['r2'])
    expect(narrowed.root.querySelector('.unknown[title^="The host is not known"]')).not.toBeNull()
  })

  it('names the origin when a local repository has no forge host', async () => {
    const doc = fleetDocument({ repositories: [repository('r1', 'alpha', { host: undefined })] })
    const { root } = await openPage('/repositories', RepositoriesPage, doc)
    expect(root.querySelector('.unknown[title="The origin names no forge host"]')).not.toBeNull()
  })

  it('keeps only the repository named in the URL query, and says when nothing matches', async () => {
    const { root } = await openPage('/repositories?repository=r1', RepositoriesPage)
    expect(bodyRows(root)).toHaveLength(1)
    const none = await openPage('/repositories?machine=mach-gamma', RepositoriesPage)
    expect(bodyRows(none.root)).toEqual([['No repositories match the filters.']])
  })

  it('says when the fleet has no repositories yet', async () => {
    const { root } = await openPage('/repositories', RepositoriesPage, fleetDocument({ repositories: [] }))
    expect(bodyRows(root)).toEqual([['No repositories yet.']])
  })

  it('links each repository with a forge host to the code browser in a new tab', async () => {
    const { root } = await openPage('/repositories', RepositoriesPage)
    const links = root.querySelectorAll<HTMLAnchorElement>('a.code-link')
    expect(links).toHaveLength(1)
    expect(links[0].getAttribute('href')).toBe('https://codegrapher.dev/github.com/acme/r1')
    expect(links[0].getAttribute('target')).toBe('_blank')
    expect(links[0].getAttribute('rel')).toBe('noopener noreferrer')
  })

  it('builds the link from the configured base', async () => {
    const { root } = await openPage('/repositories', RepositoriesPage, undefined, { ...SESSION, code_browser_url: 'https://code.example.test/' })
    expect(root.querySelector('a.code-link')?.getAttribute('href')).toBe('https://code.example.test/github.com/acme/r1')
  })

  it('has no code link when the session could not be read', async () => {
    const { root } = await openPage('/repositories', RepositoriesPage, undefined, null)
    expect(root.querySelector('a.code-link')).toBeNull()
  })

  it('shows a repository that could not be read, and one with no default branch known', async () => {
    const doc = fleetDocument({ repositories: [repository('r1', 'alpha', { error: 'timeout', default_branch: 'main' })] })
    const { root } = await openPage('/repositories', RepositoriesPage, doc)
    expect(root.querySelector('.row-error')?.textContent).toBe('could not be read: timeout')
    expect(bodyRows(root)[0][5]).toBe('main')
  })

  it('opens the worktrees of a repository from its count, and its running agents from the other', async () => {
    const { root } = await openPage('/repositories', RepositoriesPage)
    const counts = [...root.querySelectorAll<HTMLElement>('app-count')]
    const worktrees = counts[0]
    expect([...worktrees.querySelectorAll('li')].map((li) => li.textContent)).toEqual(['task-w1 (branch-w1)', 'task-w2 (branch-w2)'])
    expect(worktrees.querySelector('a')?.getAttribute('href')).toBe('/worktrees?repository=r1')
    expect(counts[1].querySelector('a')?.getAttribute('href')).toBe('/agents?repository=r1&state=running')
    expect([...counts[1].querySelectorAll('li')].map((li) => li.textContent)).toEqual(['session s-a1'])
    expect(counts[2].querySelectorAll('li')).toHaveLength(1)
  })

  it('names nothing behind a count of a repository that has no worktrees in the document', async () => {
    const doc = fleetDocument({ repositories: [repository('r1', 'alpha', { worktree_count: 0, active_agent_count: 0 })], worktrees: [], agents: [] })
    const { root } = await openPage('/repositories', RepositoriesPage, doc)
    expect(root.querySelectorAll('li')).toHaveLength(0)
  })

  it('writes a changed filter into the URL query', async () => {
    const { harness, root } = await openPage('/repositories', RepositoriesPage)
    const bar = harness.fixture.debugElement.query(By.directive(FilterBar))
    bar.componentInstance.changed.emit({ key: 'machine', value: 'mach-alpha' })
    await harness.fixture.whenStable()
    expect(TestBed.inject(Router).url).toBe('/repositories?machine=mach-alpha')
    expect(bodyRows(root)).toHaveLength(1)
    bar.componentInstance.changed.emit({ key: 'machine', value: null })
    await harness.fixture.whenStable()
    expect(TestBed.inject(Router).url).toBe('/repositories')
  })

  it('renders on its own over an empty, warming-up fleet', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([])] })
    const fixture = TestBed.createComponent(RepositoriesPage)
    await fixture.whenStable()
    expect(fixture.nativeElement.querySelector('h2')?.textContent).toBe('Repositories')
    expect(fixture.nativeElement.querySelectorAll('tbody tr').length).toBeGreaterThan(0)
  })
})
