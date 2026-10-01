import { signal } from '@angular/core'
import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { vi } from 'vitest'
import { Branch, FETCH, FleetStore, Machine, RepositoryCheckout, Worktree } from '@cockpit/fleet-data'
import { fleetDocument, machine, repository, worktree } from '@cockpit/fleet-data/testing'
import { UiClock } from '@cockpit/ui/control'
import { BRANCHES_DELAY_MS, BRANCHES_SHOWN, RepositoryMachineSection } from './repository-machine-section'

const NOW = Date.parse('2026-10-01T10:05:00Z')
const text = (element: Element | null) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()

const branch = (name: string, extra: Partial<Branch> = {}): Branch => ({
  id: `b-${name}`,
  machine: 'alpha',
  machine_id: 'mach-alpha',
  route: 'local',
  repository: 'r1',
  name,
  scope: 'local',
  ...extra,
})

const answer = (body: unknown, status = 200) => vi.fn(async () => new Response(JSON.stringify(body), { status })) as unknown as typeof fetch

interface Setup {
  checkout?: RepositoryCheckout
  worktrees?: Worktree[]
  machineEntry?: Machine
  open?: boolean
  fetcher?: typeof fetch
  repositoryExtra?: Parameters<typeof repository>[2]
  document?: Parameters<typeof fleetDocument>[0]
}

async function render(setup: Setup = {}) {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [provideRouter([]), { provide: UiClock, useValue: { now: signal(NOW) } }, { provide: FETCH, useValue: setup.fetcher ?? answer({ branches: [] }) }] })
  const store = TestBed.inject(FleetStore)
  store.document.set(fleetDocument(setup.document))
  store.now.set(NOW)
  const entry = repository('r1', 'alpha', setup.repositoryExtra)
  const checkout = setup.checkout ?? { repository: entry, machineId: entry.machine_id, machine: entry.machine, route: entry.route, observedAt: entry.observed_at, stale: false }
  const fixture = TestBed.createComponent(RepositoryMachineSection)
  fixture.componentRef.setInput('checkout', checkout)
  fixture.componentRef.setInput('worktrees', setup.worktrees ?? [])
  fixture.componentRef.setInput('machine', setup.machineEntry)
  fixture.componentRef.setInput('open', setup.open ?? false)
  await fixture.whenStable()
  const root = fixture.nativeElement as HTMLElement
  const settle = (until: () => boolean) =>
    vi.waitFor(async () => {
      await fixture.whenStable()
      if (!until()) throw new Error('not settled')
    })
  return { fixture, root, store, settle, checkout, fetcher: setup.fetcher }
}

describe('RepositoryMachineSection', () => {
  it('shows the checkout\'s facts: where it is read from, the default branch and the branch counts', async () => {
    const { root } = await render({ repositoryExtra: { default_branch: 'main', local_branch_count: 5, remote_branch_count: 12 }, machineEntry: machine('alpha') })
    expect(text(root.querySelector('app-machine-chip'))).toContain('alpha')
    expect([...root.querySelectorAll('dl.facts dt')].map(text)).toEqual(['Source', 'Default branch', 'Branches'])
    expect([...root.querySelectorAll('dl.facts dd')].map(text)).toEqual(['local', 'main', '5 local / 12 remote'])
  })

  it('names the machine plainly when the document does not list it, says what is not reported and shows a scan error', async () => {
    const { root } = await render({ repositoryExtra: { error: 'timeout' } })
    expect(root.querySelector('app-machine-chip')).toBeNull()
    expect(text(root.querySelector('summary .name'))).toBe('alpha')
    const facts = [...root.querySelectorAll('dl.facts dd')].map(text)
    expect(facts).toEqual(['local', 'not reported', 'not reported', 'could not be read: timeout'])
    const half = await render({ repositoryExtra: { local_branch_count: 2 } })
    expect(text(half.root.querySelectorAll('dl.facts dd')[2])).toBe('2 local / — remote')
    const other = await render({ repositoryExtra: { remote_branch_count: 7 } })
    expect(text(other.root.querySelectorAll('dl.facts dd')[2])).toBe('— local / 7 remote')
  })

  it('lists the worktrees as compact rows linking to their pages, with the branch only when it differs from the task', async () => {
    const same = { ...worktree('w1', 'r1', 'alpha'), task: 't1', branch: 't1', owner_state: 'active' as const, last_activity_at: '2026-10-01T10:00:00Z' }
    const other = { ...worktree('w2', 'r1', 'alpha'), task: 't2', branch: 'topic' }
    const { root } = await render({ worktrees: [same, other] })
    const rows = [...root.querySelectorAll('.worktrees li')]
    expect(rows.map((row) => text(row.querySelector('.task')))).toEqual(['t1', 't2'])
    expect(rows.map((row) => text(row.querySelector('.branch')))).toEqual(['', 'topic'])
    expect(text(rows[0])).toContain('active')
    expect(text(rows[0])).toContain('5 min ago')
    expect(text(rows[1])).toContain('not reported')
    expect(rows[0].querySelector('a')?.getAttribute('href')).toBe('/worktrees/w1')
    expect(root.querySelector('summary .summary-counts')?.textContent).toBe('2 worktrees')
    const none = await render()
    expect(text(none.root.querySelector('.worktrees'))).toBe('None on this machine')
    expect(none.root.querySelector('summary .summary-counts')?.textContent).toBe('0 worktrees')
    const one = await render({ worktrees: [same] })
    expect(one.root.querySelector('summary .summary-counts')?.textContent).toBe('1 worktree')
  })

  it('carries the code-index panel of this checkout', async () => {
    const { root } = await render({ document: { code_index_provider: 'codegrapher' }, repositoryExtra: { code_index: [{ indexer: 'codegrapher', state: 'never', statistics: { indexed: false, files: 0, symbols: 0, edges: 0, kinds: [] } }] } })
    expect(text(root.querySelector('app-code-index-panel'))).toContain('Not indexed.')
  })

  // cockpit-views#ac:repository-detail-loads-branches-lazily
  it('asks for no branches until it is opened, and then only for its own checkout', async () => {
    const fetcher = answer({ branches: [branch('main')] })
    const { fixture, root, settle } = await render({ fetcher })
    await new Promise((done) => setTimeout(done, BRANCHES_DELAY_MS + 50))
    expect(fetcher).not.toHaveBeenCalled()
    expect(text(root.querySelector('[aria-label="Branches on this machine"]'))).toContain('Branches are read when this section is opened.')
    const details = root.querySelector('details') as HTMLDetailsElement
    details.open = true
    details.dispatchEvent(new Event('toggle'))
    await settle(() => root.querySelector('.branches .branch-name') !== null)
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect((fetcher as unknown as ReturnType<typeof vi.fn>).mock.calls[0][0]).toBe('/api/v1/cockpit/branches?repository=r1')
    expect(fixture.componentInstance).toBeDefined()
  })

  it('shows placeholder rows of a fixed height until the daemon answers, and then the branches with their sync badges, task and age', async () => {
    let release: (response: Response) => void = () => undefined
    const fetcher = vi.fn(() => new Promise<Response>((done) => (release = done))) as unknown as typeof fetch
    const { root, settle } = await render({ fetcher, open: true })
    await settle(() => root.querySelectorAll('.skeleton').length === 3)
    expect(root.querySelector('[aria-label="Branches on this machine"]')?.getAttribute('aria-busy')).toBe('true')
    release(
      new Response(
        JSON.stringify({
          branches: [
            branch('agent/fix', { ahead: 2, behind: 1, upstream: 'origin/agent/fix', task: 'fix-ci', last_activity_at: '2026-10-01T10:00:00Z' }),
            branch('lonely', { upstream_gone: true }),
            branch('origin/main', { scope: 'remote' }),
            branch('untracked'),
          ],
        }),
      ),
    )
    await settle(() => root.querySelector('.branches .branch-name') !== null)
    expect(root.querySelectorAll('.skeleton')).toHaveLength(0)
    const rows = [...root.querySelectorAll('.branches li')]
    expect(rows.map((row) => text(row.querySelector('.branch-name')))).toEqual(['agent/fix', 'lonely', 'origin/main', 'untracked'])
    expect(text(rows[0])).toContain('↑2')
    expect(text(rows[0])).toContain('↓1')
    expect(text(rows[0])).toContain('fix-ci')
    expect(rows[0].querySelector('a')?.getAttribute('href')).toBe('/tasks/detail?task=fix-ci')
    expect(text(rows[1])).toContain('gone')
    // A remote branch carries no "no upstream" claim; a local one without an upstream says so.
    expect(text(rows[2])).not.toContain('no upstream')
    expect(text(rows[3])).toContain('no upstream')
    expect(root.querySelector('[aria-label="Branches on this machine"]')?.getAttribute('aria-busy')).toBe('false')
  })

  it('says the daemon\'s reason for a checkout it has no branches for, and a plain sentence when it gives none', async () => {
    const cached = await render({ open: true, fetcher: answer({ branches: [], reason: 'cached from another machine' }) })
    await cached.settle(() => !text(cached.root.querySelector('[aria-label="Branches on this machine"] p')).startsWith('Branches are read'))
    expect(text(cached.root.querySelector('[aria-label="Branches on this machine"] p'))).toBe('cached from another machine')
    const bare = await render({ open: true, fetcher: answer({ branches: [] }) })
    await bare.settle(() => !text(bare.root.querySelector('[aria-label="Branches on this machine"] p')).startsWith('Branches are read'))
    expect(text(bare.root.querySelector('[aria-label="Branches on this machine"] p'))).toBe('No branches reported for this checkout.')
  })

  it('lists the first branches and offers the rest', async () => {
    const many = Array.from({ length: BRANCHES_SHOWN + 5 }, (_, index) => branch(`b${index}`))
    const { root, settle } = await render({ open: true, fetcher: answer({ branches: many }) })
    await settle(() => root.querySelector('.branches li') !== null)
    expect(root.querySelectorAll('.branches li')).toHaveLength(BRANCHES_SHOWN)
    const more = root.querySelector('button.more') as HTMLButtonElement
    expect(text(more)).toBe(`Show all ${BRANCHES_SHOWN + 5}`)
    more.click()
    await settle(() => root.querySelectorAll('.branches li').length === BRANCHES_SHOWN + 5)
    expect(root.querySelector('button.more')).toBeNull()
  })

  it('says a failed read failed, with what the daemon answered, and reads again on request', async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(new Response('', { status: 503 })).mockResolvedValueOnce(new Response(JSON.stringify({ branches: [branch('main')] }))) as unknown as typeof fetch
    const { root, settle } = await render({ open: true, fetcher })
    await settle(() => root.querySelector('[role=alert]') !== null)
    expect(text(root.querySelector('[role=alert]'))).toBe('Branches could not be read. The daemon answered with status 503.')
    ;(root.querySelector('button.more') as HTMLButtonElement).click()
    await settle(() => root.querySelector('.branches .branch-name') !== null)
    expect(fetcher).toHaveBeenCalledTimes(2)
  })

  it('says so when the daemon did not answer at all', async () => {
    const fetcher = vi.fn().mockRejectedValue(new TypeError('network')) as unknown as typeof fetch
    const { root, settle } = await render({ open: true, fetcher })
    await settle(() => root.querySelector('[role=alert]') !== null)
    expect(text(root.querySelector('[role=alert]'))).toContain('The daemon did not answer, or answered with something else.')
  })

  it('opens and brings itself into view when asked to reveal, and tolerates a browser that cannot scroll', async () => {
    const { fixture, root, settle } = await render()
    const element = fixture.nativeElement as HTMLElement
    const scroll = vi.fn()
    element.scrollIntoView = scroll
    const frames: FrameRequestCallback[] = []
    const request = vi.spyOn(window, 'requestAnimationFrame').mockImplementation((callback) => frames.push(callback))
    fixture.componentInstance.reveal()
    await settle(() => (root.querySelector('details') as HTMLDetailsElement).open)
    frames.forEach((callback) => callback(0))
    expect(scroll).toHaveBeenCalledWith({ block: 'start' })
    delete (element as { scrollIntoView?: unknown }).scrollIntoView
    Object.defineProperty(element, 'scrollIntoView', { value: undefined, configurable: true })
    frames.length = 0
    fixture.componentInstance.reveal()
    frames.forEach((callback) => callback(0))
    request.mockRestore()
    expect(fixture.componentInstance.domId()).toBe('machine-mach-alpha')
  })

  it('reads nothing when it is closed again before the wait is over', async () => {
    const fetcher = answer({ branches: [] })
    const { root, fixture } = await render({ open: true, fetcher })
    const details = root.querySelector('details') as HTMLDetailsElement
    details.open = false
    details.dispatchEvent(new Event('toggle'))
    await fixture.whenStable()
    await new Promise((done) => setTimeout(done, BRANCHES_DELAY_MS + 50))
    expect(fetcher).not.toHaveBeenCalled()
  })
})
