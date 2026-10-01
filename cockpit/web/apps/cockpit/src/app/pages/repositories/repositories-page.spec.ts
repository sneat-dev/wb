import { TestBed } from '@angular/core/testing'
import { Router, provideRouter } from '@angular/router'
import { vi } from 'vitest'
import { FleetDocument, Session } from '@cockpit/fleet-data'
import { VOCABULARY } from '@cockpit/fleet-data/list'
import { fleetDocument, machine, pullRequest, repository, run, worktree } from '@cockpit/fleet-data/testing'
import { LIST_SHORTCUTS } from '@cockpit/ui/list-host'
import { NOW, SESSION, openPage } from '../test-harness'
import { RepositoriesPage } from './repositories-page'

const HOUR = 60 * 60 * 1000
const DAY = 24 * HOUR
const ago = (days: number) => new Date(NOW - days * DAY).toISOString()
const noBranches = vi.fn(async () => new Response(JSON.stringify({ branches: [] }))) as unknown as typeof fetch

/**
 * `sneat-co/sneat-go` on three machines (one local, one cached 3 hours ago, one cached for two days, so stale)
 * with three code-index states and the newest activity; `sneat-dev/wb` with the most branches; `acme/lone` with a
 * scan error, no host and no address on the forge.
 */
function documentOf(extra: Partial<FleetDocument> = {}): FleetDocument {
  return fleetDocument({
    code_index_provider: 'codegrapher',
    machines: [machine('alpha'), { ...machine('beta', 'cached'), observed_at: new Date(NOW - 3 * HOUR).toISOString() }, { ...machine('gamma', 'cached'), observed_at: ago(2) }],
    repositories: [
      repository('go-a', 'alpha', {
        name: 'sneat-co/sneat-go',
        worktree_count: 2,
        local_branch_count: 5,
        remote_branch_count: 12,
        last_activity_at: ago(1),
        remote_url_web: 'https://github.com/sneat-co/sneat-go',
        code_index: [{ indexer: 'codegrapher', state: 'fresh' }],
      }),
      repository('go-b', 'beta', { name: 'sneat-co/sneat-go', route: 'cached', observed_at: new Date(NOW - 3 * HOUR).toISOString(), worktree_count: 1, active_agent_count: undefined, local_branch_count: 3, remote_branch_count: 4, code_index: [{ indexer: 'codegrapher', state: 'diverged' }] }),
      repository('go-c', 'gamma', { name: 'Sneat-Co/sneat-go', route: 'cached', observed_at: ago(2), worktree_count: 0, active_agent_count: undefined, code_index: [{ indexer: 'codegrapher', state: 'stale', behind: 3 }] }),
      repository('wb-a', 'alpha', { name: 'sneat-dev/wb', worktree_count: 1, active_agent_count: 0, local_branch_count: 9, remote_branch_count: 20, last_activity_at: ago(5), remote_url_web: 'https://github.com/sneat-dev/wb', code_index: [{ indexer: 'codegrapher', state: 'failed' }] }),
      repository('lone', 'alpha', { name: 'acme/lone', host: undefined, worktree_count: 0, active_agent_count: 0, error: 'timeout' }),
    ],
    worktrees: [
      { ...worktree('w1', 'go-a', 'alpha'), task: 'fix-ci' },
      { ...worktree('w2', 'go-a', 'alpha'), task: 'add-search' },
      { ...worktree('w3', 'go-b', 'beta'), task: 'far', route: 'cached' as const },
      { ...worktree('w4', 'wb-a', 'alpha'), task: 'wb-task' },
    ],
    pull_requests: [pullRequest('p1', 'go-a', 'w1', { number: 7 })],
    agents: [run('run-1', 'running', { repository: 'go-a' })],
    ...extra,
  })
}

/** One machine, no branch counts and no code index: the columns that the Machines, Branches and Code index columns would otherwise push out of the seven show. */
function oneMachine(): FleetDocument {
  return fleetDocument({
    machines: [machine('alpha')],
    repositories: [repository('a', 'alpha', { name: 'acme/a', worktree_count: 2, active_agent_count: 1, last_activity_at: ago(1) }), repository('b', 'alpha', { name: 'acme/b', worktree_count: 0, active_agent_count: 0, open_pull_request_count: 0, last_activity_at: ago(2) })],
    worktrees: [],
    pull_requests: [pullRequest('p1', 'a', undefined, { number: 3 })],
    agents: [run('run-1', 'running', { repository: 'a' })],
  })
}

const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()
const rowsOf = (root: HTMLElement) => [...root.querySelectorAll<HTMLElement>('[role=row][data-index]')]
const namesOf = (root: HTMLElement) => rowsOf(root).map((row) => text(row.querySelector('app-repo-name')))
const headersOf = (root: HTMLElement) => [...root.querySelectorAll('.head [role=columnheader]:not(.open-cell)')].map(text)
const HEADERS: Record<string, string> = { repository: 'Repository', machines: 'Machines', worktrees: 'Worktrees', branches: 'Branches', agents: 'Agents', prs: 'PRs', index: 'Code index', activity: 'Last activity', links: 'Links' }
/** The cell of a row under the column of that id, wherever the columns that fit have put it. */
const cell = (row: HTMLElement, id: string) => {
  const grid = row.closest('[role=grid]') as HTMLElement
  const at = [...grid.querySelectorAll('.head [role=columnheader]')].findIndex((header) => text(header) === HEADERS[id])
  return row.querySelectorAll<HTMLElement>('[role=gridcell]')[at]
}
const open = (url: string, doc: FleetDocument = documentOf(), session: Session | null = SESSION) => openPage(url, RepositoriesPage, doc, session, noBranches)

describe('RepositoriesPage', () => {
  it('renders on its own over an empty, warming-up fleet, with placeholder rows', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([]), { provide: LIST_SHORTCUTS, useValue: { registerFilter: () => () => undefined, registerPanel: () => () => undefined } }] })
    const fixture = TestBed.createComponent(RepositoriesPage)
    await fixture.whenStable()
    expect(fixture.nativeElement.querySelectorAll('.skeleton').length).toBeGreaterThan(0)
  })

  // cockpit-views#ac:repositories-merge-across-machines
  it('lists one row per repository identity, newest activity first, however many machines hold it', async () => {
    const { root } = await open('/repositories')
    expect(namesOf(root)).toEqual(['sneat-co/sneat-go', 'sneat-dev/wb', 'acme/lone'])
    expect(text(root.querySelector('.count'))).toBe('3 of 3')
    expect(headersOf(root)).toEqual(['Repository', 'Machines', 'Worktrees', 'Branches', 'Code index', 'Last activity', 'Links'])
  })

  // cockpit-views#ac:repository-identity-merges-local-and-cached
  it('shows a chip for each machine of a repository, the cached ones with their age and a stale mark, and this machine by its name alone', async () => {
    const { root } = await open('/repositories')
    const chips = [...cell(rowsOf(root)[0], 'machines').querySelectorAll('a.machine')]
    expect(chips.map((chip) => text(chip))).toEqual(['alpha', 'beta 3 h', 'gamma 2 d · stale'])
    expect(chips[2].classList.contains('stale')).toBe(true)
    expect(chips[1].classList.contains('stale')).toBe(false)
    expect(chips.map((chip) => chip.getAttribute('title'))).toEqual(['alpha: this machine', 'beta: cached, 3 h ago', 'gamma: cached, 2 d ago; older than the freshness window'])
    // Each chip leads to that machine's checkout: the panel of the row, at that machine's section.
    expect(chips.map((chip) => chip.getAttribute('href'))).toEqual(['/repositories?sel=go-a#machine-mach-alpha', '/repositories?sel=go-a#machine-mach-beta', '/repositories?sel=go-a#machine-mach-gamma'])
    expect([...cell(rowsOf(root)[2], 'machines').querySelectorAll('a.machine')].map(text)).toEqual(['alpha'])
  })

  it('shows three chips and "+n" for the rest, which the panel lists', async () => {
    const many = documentOf({
      machines: ['alpha', 'beta', 'gamma', 'delta', 'epsilon'].map((name) => (name === 'alpha' ? machine(name) : machine(name, 'cached'))),
      repositories: ['alpha', 'beta', 'gamma', 'delta', 'epsilon'].map((name) => repository(`r-${name}`, name, { name: 'acme/many', route: name === 'alpha' ? 'local' : 'cached' })),
    })
    const { root } = await open('/repositories', many)
    const machines = cell(rowsOf(root)[0], 'machines')
    expect(machines.querySelectorAll('a.machine')).toHaveLength(3)
    expect(text(machines.querySelector('.more'))).toBe('+2')
    expect(machines.querySelector('.more')?.getAttribute('title')).toBe('delta, epsilon')
  })

  it('hides the Machines column for a fleet of one machine', async () => {
    const { root } = await open('/repositories', oneMachine())
    expect(headersOf(root)).toEqual(['Repository', 'Worktrees', 'Agents', 'PRs', 'Last activity', 'Links'])
  })

  // cockpit-views#ac:every-number-is-a-link
  it('sums the counts, links the worktree, agent and pull request counts to the lists they counted, and does not link the branch counts', async () => {
    const { root } = await open('/repositories')
    const [go, wb, lone] = rowsOf(root)
    expect(text(cell(go, 'worktrees'))).toBe('3')
    expect(cell(go, 'worktrees').querySelector('a')?.getAttribute('href')).toBe('/worktrees?q=repo:%22sneat-co%2Fsneat-go%22')
    expect(text(cell(go, 'branches'))).toBe('8 / 16')
    expect(cell(go, 'branches').querySelector('a')).toBeNull()
    expect(cell(go, 'branches').querySelector('span')?.getAttribute('title')).toContain('Not a link: there is no branches list page')
    expect(text(cell(wb, 'branches'))).toBe('9 / 20')
    // A repository whose machines report no branch counts says so, and a zero is quiet and not a link.
    expect(text(cell(lone, 'branches'))).toBe('—')
    expect(text(cell(lone, 'worktrees'))).toBe('0')
    expect(cell(lone, 'worktrees').querySelector('a')).toBeNull()
    expect(cell(lone, 'worktrees').querySelector('.quiet')?.getAttribute('title')).toBe('No worktrees')
    const single = await open('/repositories', oneMachine())
    const [a, b] = rowsOf(single.root)
    const agents = (row: HTMLElement) => cell(row, 'agents')
    const prs = (row: HTMLElement) => cell(row, 'prs')
    expect(agents(a).querySelector('a')?.getAttribute('href')).toBe('/agents?q=repo:%22acme%2Fa%22')
    expect(prs(a).querySelector('a')?.getAttribute('href')).toBe('/tasks?q=repo:%22acme%2Fa%22&chips=pr')
    // A zero is an empty cell, not a "0".
    expect(text(agents(b))).toBe('')
    expect(text(prs(b))).toBe('')
  })

  it('shows a dash for the side of the branch counts that no machine reports, and says so in the hover text', async () => {
    const half = documentOf({ repositories: [repository('l', 'alpha', { name: 'acme/l', local_branch_count: 2 }), repository('m', 'alpha', { name: 'acme/m', remote_branch_count: 7, last_activity_at: ago(1) })], worktrees: [], pull_requests: [], agents: [] })
    const { root } = await open('/repositories', half)
    const branches = rowsOf(root).map((row) => cell(row, 'branches').querySelector('span') as HTMLElement)
    expect(branches.map(text)).toEqual(['— / 7', '2 / —'])
    expect(branches.map((span) => span.title)).toEqual(['not reported local, 7 remote. Not a link: there is no branches list page', '2 local, not reported remote. Not a link: there is no branches list page'])
  })

  it('does not link a count whose repository name cannot be written as a filter, and says why', async () => {
    const odd = documentOf({ repositories: [repository('q', 'alpha', { name: 'acme/odd"name', host: undefined, worktree_count: 2 })], worktrees: [], pull_requests: [], agents: [] })
    const { root } = await open('/repositories', odd)
    const count = cell(rowsOf(root)[0], 'worktrees').querySelector('app-count-link span') as HTMLElement
    expect(count.textContent).toBe('2')
    expect(count.title).toContain('quote')
  })

  it('shows the worst code-index state across the machines, as a word with an icon, and a dash where none is reported', async () => {
    const { root } = await open('/repositories')
    const [go, wb, lone] = rowsOf(root)
    expect(text(cell(go, 'index'))).toContain('diverged')
    expect(cell(go, 'index').querySelector('app-state-badge')).not.toBeNull()
    expect(cell(go, 'index').querySelector('[title]')?.getAttribute('title')).toBe('The worst code-index state across 3 machines')
    expect(text(cell(wb, 'index'))).toContain('failed')
    expect(cell(wb, 'index').querySelector('[title]')?.getAttribute('title')).toBe('The worst code-index state across 1 machine')
    expect(text(cell(lone, 'index'))).toBe('—')
  })

  it('shows the last activity as a relative age with the exact time, muted past 30 days, and a dash for none', async () => {
    const old = documentOf({ repositories: [repository('o', 'alpha', { name: 'acme/old', last_activity_at: ago(45) }), repository('n', 'alpha', { name: 'acme/new', last_activity_at: ago(3) })] })
    const { root } = await open('/repositories', old)
    const [fresh, stale] = rowsOf(root).map((row) => cell(row, 'activity').querySelector('.age') as HTMLElement)
    expect(text(fresh)).toBe('3 d ago')
    expect(fresh.classList.contains('muted')).toBe(false)
    expect(text(stale)).toBe('45 d ago')
    expect(stale.classList.contains('muted')).toBe(true)
    expect(fresh.title).toBe(ago(3))
  })

  // cockpit-views#ac:repository-and-time-rendering
  it('shows the owner muted before the name in strong type, and the host only when the fleet has more than one', async () => {
    const { root } = await open('/repositories')
    const name = rowsOf(root)[0].querySelector('app-repo-name') as HTMLElement
    expect(text(name.querySelector('.muted'))).toBe('sneat-co/')
    expect(text(name.querySelector('strong'))).toBe('sneat-go')
    const hosts = documentOf({ repositories: [repository('a', 'alpha', { name: 'acme/a' }), repository('b', 'alpha', { name: 'acme/b', host: 'gitlab.example.com' })] })
    const two = await open('/repositories', hosts)
    expect(namesOf(two.root).sort()).toEqual(['github.com/acme/a', 'gitlab.example.com/acme/b'])
  })

  it('marks a repository with a scan error in words, with the error as its hover text', async () => {
    const { root } = await open('/repositories')
    const lone = rowsOf(root)[2]
    expect(text(lone.querySelector('.scan-error'))).toBe('scan error')
    expect(lone.querySelector('.scan-error')?.getAttribute('title')).toBe('timeout')
    expect(rowsOf(root)[0].querySelector('.scan-error')).toBeNull()
  })

  // cockpit-views#ac:repositories-merge-across-machines (the icons)
  it('ends each row with icon buttons that have an accessible name and a tooltip, and no text "Code" link', async () => {
    const { root } = await open('/repositories')
    const links = [...cell(rowsOf(root)[0], 'links').querySelectorAll('a.icon')]
    expect(links.map((link) => link.getAttribute('aria-label'))).toEqual(['Browse the code of sneat-co/sneat-go', 'Open sneat-co/sneat-go on github.com'])
    expect(links.map((link) => link.getAttribute('title'))).toEqual(['Browse the code', 'Open on github.com'])
    expect(links.map((link) => link.getAttribute('href'))).toEqual(['https://codegrapher.dev/github.com/sneat-co/sneat-go', 'https://github.com/sneat-co/sneat-go'])
    expect(links.every((link) => link.getAttribute('rel') === 'noopener noreferrer' && link.getAttribute('target') === '_blank')).toBe(true)
    expect(links.every((link) => link.querySelector('svg') !== null && text(link) === '')).toBe(true)
    expect([...root.querySelectorAll('a')].some((link) => text(link) === 'Code')).toBe(false)
  })

  // cockpit-views#ac:repository-actions-follow-configuration
  it('shows no browse-code button without a code browser, and no open-on-host button for a repository with no address', async () => {
    const { root } = await open('/repositories', documentOf(), { ...SESSION, code_browser_url: undefined })
    const [go, , lone] = rowsOf(root)
    expect([...cell(go, 'links').querySelectorAll('a.icon')].map((link) => link.getAttribute('aria-label'))).toEqual(['Open sneat-co/sneat-go on github.com'])
    expect(cell(lone, 'links').querySelectorAll('a.icon')).toHaveLength(0)
    const configured = await open('/repositories')
    const loneLinks = [...cell(rowsOf(configured.root)[2], 'links').querySelectorAll('a.icon')]
    // A repository with no host has no code-browser address either, and no address on the forge.
    expect(loneLinks).toHaveLength(0)
  })

  it('names the host in the open button only when the repository has one', async () => {
    const doc = documentOf({ repositories: [repository('h', 'alpha', { name: 'acme/h', host: undefined, remote_url_web: 'https://example.test/acme/h' })] })
    const { root } = await open('/repositories', doc, { ...SESSION, code_browser_url: undefined })
    const link = cell(rowsOf(root)[0], 'links').querySelector('a.icon') as HTMLElement
    expect(link.getAttribute('aria-label')).toBe('Open acme/h on its host')
    expect(link.getAttribute('title')).toBe('Open on the host')
  })

  // cockpit-views#ac:repositories-sort-presets
  it('switches the order in one click with a radio group, writes the sort in the address and restores it on a reload', async () => {
    const { root, harness } = await open('/repositories')
    const radios = [...root.querySelectorAll<HTMLInputElement>('fieldset.presets input[type=radio]')]
    expect(radios.map((radio) => text(radio.parentElement))).toEqual(['Recent', 'Most worktrees', 'Most branches'])
    expect(root.querySelector('fieldset.presets legend')?.textContent).toBe('Show')
    expect(radios.map((radio) => radio.checked)).toEqual([true, false, false])
    expect(namesOf(root)).toEqual(['sneat-co/sneat-go', 'sneat-dev/wb', 'acme/lone'])

    radios[2].click()
    await harness.fixture.whenStable()
    expect(TestBed.inject(Router).url).toBe('/repositories?sort=branches&dir=desc')
    expect(namesOf(root)).toEqual(['sneat-dev/wb', 'sneat-co/sneat-go', 'acme/lone'])
    expect(radios.map((radio) => radio.checked)).toEqual([false, false, true])

    radios[1].click()
    await harness.fixture.whenStable()
    expect(TestBed.inject(Router).url).toBe('/repositories?sort=worktrees&dir=desc')
    expect(namesOf(root)).toEqual(['sneat-co/sneat-go', 'sneat-dev/wb', 'acme/lone'])
    expect(radios.map((radio) => radio.checked)).toEqual([false, true, false])

    // After a reload the address holds the choice.
    const reloaded = await open('/repositories?sort=worktrees&dir=desc')
    expect([...reloaded.root.querySelectorAll<HTMLInputElement>('fieldset.presets input')].map((radio) => radio.checked)).toEqual([false, true, false])
    ;(reloaded.root.querySelector('fieldset.presets input') as HTMLInputElement).click()
    await reloaded.harness.fixture.whenStable()
    expect(TestBed.inject(Router).url).toBe('/repositories?sort=activity&dir=desc')
  })

  it('orders by the count each preset names', async () => {
    const doc = documentOf({
      repositories: [
        repository('a', 'alpha', { name: 'acme/few-everything', worktree_count: 1, local_branch_count: 1, remote_branch_count: 1, last_activity_at: ago(1) }),
        repository('b', 'alpha', { name: 'acme/most-worktrees', worktree_count: 9, local_branch_count: 2, remote_branch_count: 1, last_activity_at: ago(30) }),
        repository('c', 'alpha', { name: 'acme/most-branches', worktree_count: 3, local_branch_count: 30, remote_branch_count: 30, last_activity_at: ago(20) }),
      ],
      worktrees: [],
      pull_requests: [],
      agents: [],
    })
    expect(namesOf((await open('/repositories?sort=activity&dir=desc', doc)).root)).toEqual(['acme/few-everything', 'acme/most-branches', 'acme/most-worktrees'])
    expect(namesOf((await open('/repositories?sort=worktrees&dir=desc', doc)).root)).toEqual(['acme/most-worktrees', 'acme/most-branches', 'acme/few-everything'])
    expect(namesOf((await open('/repositories?sort=branches&dir=desc', doc)).root)).toEqual(['acme/most-branches', 'acme/most-worktrees', 'acme/few-everything'])
  })

  it('shows the preset that a click on a header chose, and none for any other order', async () => {
    const { root, harness } = await open('/repositories')
    const checked = () => [...root.querySelectorAll<HTMLInputElement>('fieldset.presets input')].map((radio) => radio.checked)
    const header = (name: string) => [...root.querySelectorAll<HTMLButtonElement>('.head button.sort')].find((button) => text(button) === name) as HTMLButtonElement
    header('Worktrees').click()
    await harness.fixture.whenStable()
    expect(TestBed.inject(Router).url).toContain('sort=worktrees')
    expect(checked()).toEqual([false, true, false])
    header('Worktrees').click()
    await harness.fixture.whenStable()
    expect(checked()).toEqual([false, false, false])
    header('Repository').click()
    await harness.fixture.whenStable()
    expect(checked()).toEqual([false, false, false])
  })

  // cockpit-views#ac:repositories-quick-filters
  it('has the chips of the vocabulary, and each leaves exactly the repositories that satisfy it', async () => {
    const { root } = await open('/repositories')
    expect([...root.querySelectorAll('[aria-label="Quick filters"] button')].map(text)).toEqual(VOCABULARY.repositories.chips.map((chip) => chip.label))
    const everything = documentOf({
      repositories: [
        ...documentOf().repositories,
        repository('p', 'alpha', { name: 'acme/pending', worktree_count: 0, active_agent_count: 0, code_index: [{ indexer: 'codegrapher', state: 'pending' }] }),
        repository('n', 'alpha', { name: 'acme/never', worktree_count: 0, active_agent_count: 0, code_index: [{ indexer: 'codegrapher', state: 'never' }] }),
      ],
    })
    const expected: Record<string, string[]> = {
      worktrees: ['sneat-co/sneat-go', 'sneat-dev/wb'],
      agents: ['sneat-co/sneat-go'],
      prs: ['sneat-co/sneat-go'],
      // `index` means stale, diverged or failed: not fresh, pending or never.
      index: ['sneat-co/sneat-go', 'sneat-dev/wb'],
      errors: ['acme/lone'],
    }
    for (const [chip, names] of Object.entries(expected)) expect(namesOf((await open(`/repositories?chips=${chip}`, everything)).root), chip).toEqual(names)
    expect(namesOf((await open('/repositories', everything)).root)).toHaveLength(5)
  })

  it('toggles a chip with one click and holds it in the address', async () => {
    const { root, harness } = await open('/repositories')
    const chip = (label: string) => [...root.querySelectorAll<HTMLButtonElement>('[aria-label="Quick filters"] button')].find((button) => text(button).includes(label)) as HTMLButtonElement
    chip('Scan errors').click()
    await harness.fixture.whenStable()
    expect(TestBed.inject(Router).url).toBe('/repositories?chips=errors')
    expect(namesOf(root)).toEqual(['acme/lone'])
    expect(text(root.querySelector('.count'))).toBe('1 of 3')
  })

  it('filters with a wildcard on the repository name, and offers to clear a filter that matches nothing', async () => {
    const { root, harness } = await open('/repositories?q=sneat-*%2F*')
    expect(namesOf(root)).toEqual(['sneat-co/sneat-go', 'sneat-dev/wb'])
    const none = await open('/repositories?q=zzz')
    expect(text(none.root.querySelector('.empty'))).toContain('No repositories match the filter “zzz”.')
    ;(none.root.querySelector('button.clear-filters') as HTMLButtonElement).click()
    await none.harness.fixture.whenStable()
    expect(namesOf(none.root)).toHaveLength(3)
    expect(harness).toBeDefined()
  })

  // cockpit-views#ac:empty-states-offer-clear
  it('says nothing has been observed yet for a fleet with no repositories', async () => {
    const { root } = await open('/repositories', fleetDocument({ machines: [machine('alpha')], repositories: [], worktrees: [], pull_requests: [], agents: [] }))
    expect(text(root.querySelector('.empty'))).toBe('Nothing has been observed yet: no repositories.')
  })

  it('opens the merged repository\'s panel for a selection, from the entry id of any of its checkouts', async () => {
    for (const sel of ['go-a', 'go-b', 'go-c']) {
      const { root } = await open(`/repositories?sel=${sel}`)
      const panel = root.querySelector('app-side-panel') as HTMLElement
      expect(panel.querySelector('aside')?.getAttribute('aria-label')).toBe('Repository sneat-co/sneat-go')
      expect(text(panel.querySelector('h2'))).toBe('sneat-co/sneat-go')
      expect(panel.querySelectorAll('app-repository-machine-section')).toHaveLength(3)
      expect(rowsOf(root).filter((row) => row.classList.contains('selected'))).toHaveLength(1)
    }
  })

  it('opens the panel when a row is clicked, and the page when its open button is', async () => {
    const { root, harness } = await open('/repositories')
    ;(rowsOf(root)[1].querySelectorAll('[role=gridcell]')[3] as HTMLElement).click()
    await harness.fixture.whenStable()
    expect(text(root.querySelector('app-side-panel h2'))).toBe('sneat-dev/wb')
    expect(TestBed.inject(Router).url).toBe('/repositories?sel=wb-a')
    expect(rowsOf(root)[1].querySelector('a.open')?.getAttribute('href')).toBe('/repositories/github.com/sneat-dev/wb')
    expect(rowsOf(root)[2].querySelector('a.open')?.getAttribute('href')).toBe('/repositories/-/acme/lone')
  })

  // cockpit-views#ac:repositories-merge-across-machines (the chip opens the panel at that machine)
  it('opens the panel at the machine of a chip that is clicked', async () => {
    const { root, harness } = await open('/repositories')
    const frames: FrameRequestCallback[] = []
    vi.spyOn(window, 'requestAnimationFrame').mockImplementation((callback) => frames.push(callback))
    const beta = cell(rowsOf(root)[0], 'machines').querySelectorAll<HTMLAnchorElement>('a.machine')[1]
    beta.click()
    await harness.fixture.whenStable()
    await harness.fixture.whenStable()
    expect(TestBed.inject(Router).url).toBe('/repositories?sel=go-a#machine-mach-beta')
    const sections = [...root.querySelectorAll<HTMLDetailsElement>('app-side-panel app-repository-machine-section details')]
    expect(sections.map((section) => section.open)).toEqual([true, true, false])
    vi.restoreAllMocks()
  })

  it('keeps the filter, the sort and the machine selection when a chip opens the panel', async () => {
    const { root, harness } = await open('/repositories?sort=worktrees&dir=desc&chips=worktrees')
    ;(cell(rowsOf(root)[0], 'machines').querySelector('a.machine') as HTMLAnchorElement).click()
    await harness.fixture.whenStable()
    expect(TestBed.inject(Router).url).toContain('sort=worktrees')
    expect(TestBed.inject(Router).url).toContain('chips=worktrees')
    expect(TestBed.inject(Router).url).toContain('sel=')
  })
})
