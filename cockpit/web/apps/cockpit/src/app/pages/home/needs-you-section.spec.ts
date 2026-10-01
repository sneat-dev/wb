import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { FIXED_CLOCK, fleet, modelOf, only } from './home-testing'
import { NeedsYouSection } from './needs-you-section'

async function render(document = fleet(), warming = false) {
  TestBed.resetTestingModule()
  TestBed.configureTestingModule({ providers: [FIXED_CLOCK, provideRouter([])] })
  const fixture = TestBed.createComponent(NeedsYouSection)
  fixture.componentRef.setInput('model', modelOf(document))
  fixture.componentRef.setInput('warming', warming)
  await fixture.whenStable()
  const root: HTMLElement = fixture.nativeElement
  return { fixture, root, rows: [...root.querySelectorAll<HTMLElement>('.home-row')] }
}

const text = (element: Element | null | undefined) => (element?.textContent ?? '').replace(/\s+/g, ' ').trim()

describe('NeedsYouSection', () => {
  it('shows one urgent row per task, worst first, each with its state, reason, age and one action', async () => {
    const { root, rows } = await render()
    expect(root.querySelector('section')?.getAttribute('aria-labelledby')).toBe('home-needs-h')
    expect(text(root.querySelector('h2'))).toBe('Needs you 6')
    expect(root.querySelector('.home-count')?.classList.contains('urgent')).toBe(true)
    expect(root.querySelector('.home-card')?.classList.contains('urgent')).toBe(true)
    expect(rows.map((row) => text(row.querySelector('.home-task')))).toEqual([
      'refactor-cache',
      'add-search',
      'fix-ci-race',
      'migrate-auth',
      'bump-deps',
      '3 blocked agents with no task',
    ])
    expect(rows.map((row) => text(row.querySelector('app-state-badge')))).toEqual(
      ['at risk', 'at risk', 'checks failed', 'blocked', 'blocked', 'blocked'].map((state) => expect.stringContaining(state)),
    )
    expect(text(rows[2].querySelector('.home-reason'))).toBe('build-linux failed on sneat-dev/wb#131')
    expect(rows[2].querySelector('time')?.getAttribute('datetime')).toBe('2026-10-01T09:20:00.000Z')
    expect(text(rows[2].querySelector('time'))).toBe('40 min ago')
    expect(rows[5].querySelector('time')).toBeNull()
    for (const row of rows.slice(2)) expect(row.querySelectorAll('.home-action a, .home-action button')).toHaveLength(1)
  })

  it('opens the failure of a pull request in a new tab, an agent in the application, and the blocked agents with no task on Agents', async () => {
    const { rows } = await render()
    const failure = rows[2].querySelector('a.home-act') as HTMLAnchorElement
    expect(text(failure)).toBe('Open failure')
    expect(failure.getAttribute('href')).toBe('https://github.com/example/r-wb/pull/131')
    expect(failure.getAttribute('target')).toBe('_blank')
    expect(failure.getAttribute('rel')).toBe('noopener noreferrer')
    expect(failure.getAttribute('aria-label')).toBe('Open failure of sneat-dev/wb#131, opens in a new tab')
    const agent = rows[3].querySelector('a.home-act') as HTMLAnchorElement
    expect(text(agent)).toBe('Open agent')
    expect(agent.getAttribute('href')).toBe('/agents/vm-blocked')
    const none = rows[5].querySelector('a.home-act') as HTMLAnchorElement
    expect(text(none)).toBe('Open agents')
    expect(none.getAttribute('href')).toBe('/agents?chips=blocked')
  })

  it("says where the work is, and which machine reported a task that is not this one's", async () => {
    const { rows } = await render()
    expect([...rows[0].querySelectorAll('.home-place')].map((place) => text(place))).toEqual(['sneat-dev/wb', 'sneat-co/sneat-go'])
    expect(rows[0].querySelector('.home-machine')).toBeNull()
    expect(text(rows[4].querySelector('.home-machine'))).toBe('mac')
    expect(rows[4].querySelector('.home-chip')).toBeNull()
    const remote = rows[3]
    expect(text(remote.querySelector('.home-machine-name'))).toBe('vm')
    expect(text(remote.querySelector('.home-chip'))).toBe('ssh')
    expect(remote.querySelector('.home-machine')?.getAttribute('title')).toBe('vm: read live over ssh')
    expect(remote.querySelector('.home-chip')?.classList.contains('stale')).toBe(false)
    expect(text(remote.querySelector('.home-place'))).toBe('as reported by vm')
  })

  it('shows the branch of a worktree only when it says more than its task', async () => {
    const document = fleet()
    document.worktrees.find((worktree) => worktree.task === 'add-search')!.branch = 'codex/split-cache'
    const { rows } = await render(document)
    expect(text(rows[1].querySelector('.home-place'))).toBe('sneat-co/sneat-gocodex/split-cache')
    expect(rows[1].querySelector('.home-place code')?.textContent).toBe('codex/split-cache')
  })

  it('mounts the action of work at risk from its lazy chunk: one copy button for the task', async () => {
    const { fixture, rows } = await render()
    await new Promise((done) => setTimeout(done, 20))
    await fixture.whenStable()
    const buttons = rows[0].querySelectorAll('.home-action button')
    expect(buttons).toHaveLength(1)
    expect(text(buttons[0])).toBe('Copy template')
  })

  it('lists "+n more" as a link to Tasks filtered to the same set', async () => {
    const { root } = await render()
    const more = root.querySelector('.home-foot a') as HTMLAnchorElement
    expect(text(more)).toBe('+1 more')
    expect(more.getAttribute('href')).toBe('/tasks?chips=needs-you')
  })

  it('has no "+n more" when every task is shown, and a linked pull request that cannot be linked opens its task', async () => {
    const document = only('fix-ci-race')
    document.pull_requests[0].url = 'ftp://example.test/pull/1'
    const { root, rows } = await render(document)
    expect(root.querySelector('.home-foot')).toBeNull()
    expect(rows).toHaveLength(1)
    const open = rows[0].querySelector('a.home-act') as HTMLAnchorElement
    expect(text(open)).toBe('Open task')
    expect(open.getAttribute('href')).toBe('/tasks?sel=fix-ci-race')
  })

  it('collapses to one calm line when nothing needs the operator, and looks urgent only with rows', async () => {
    const { root, rows } = await render(fleet('healthy'))
    expect(rows).toHaveLength(0)
    expect(text(root.querySelector('.home-calm'))).toBe('Nothing needs you.')
    expect(root.querySelector('.home-count')).toBeNull()
    expect(root.querySelector('.home-card')).toBeNull()
  })

  it('shows a skeleton and not the calm line while the daemon is still scanning', async () => {
    const { root } = await render(fleet('warming'), true)
    expect(root.querySelector('.home-calm')).toBeNull()
    expect(root.querySelector('app-skeleton-rows')).not.toBeNull()
    expect(text(root.querySelector('h2'))).toBe('Needs you')
  })
})
