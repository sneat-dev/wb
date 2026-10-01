import { TestBed } from '@angular/core/testing'
import { provideRouter } from '@angular/router'
import { fleetDocument, worktree } from '@cockpit/fleet-data/testing'
import { DashboardPage, RECENT_WORKTREES } from './dashboard-page'
import { bodyRows, openPage } from './test-harness'

describe('DashboardPage', () => {
  it('shows a tile per collection whose count opens its list', async () => {
    const { root } = await openPage('/dashboard', DashboardPage)
    const tiles = [...root.querySelectorAll('.tile')].map((tile) => ({
      title: tile.querySelector('.tile-title')?.textContent,
      count: tile.querySelector('a')?.textContent,
      href: tile.querySelector('a')?.getAttribute('href'),
      names: [...tile.querySelectorAll('li')].map((li) => li.textContent),
    }))
    expect(tiles).toEqual([
      { title: 'Machines', count: '2', href: '/machines', names: ['alpha', 'beta'] },
      { title: 'Repositories', count: '2', href: '/repositories', names: ['github.com/acme/r1', 'acme/r2'] },
      { title: 'Worktrees', count: '3', href: '/worktrees', names: ['task-w1 (branch-w1)', 'task-w2 (branch-w2)', 'task-w3 (branch-w3)'] },
      { title: 'Agents', count: '3', href: '/agents', names: ['session s-a1', 'session s-a2', 'claude run run9'] },
      { title: 'Running agents', count: '2', href: '/agents?state=running', names: ['session s-a1', 'claude run run9'] },
    ])
  })

  it('lists the machines and the most recent worktrees, with route and age', async () => {
    const { root } = await openPage('/dashboard', DashboardPage)
    const [machines, worktrees] = [...root.querySelectorAll<HTMLElement>('p-table')]
    expect(bodyRows(machines)).toEqual([
      ['alpha', 'local', '—'],
      ['beta', 'cached, 5 min ago', '—'],
    ])
    expect(bodyRows(worktrees)).toEqual([
      ['task-w1', 'github.com/acme/r1', 'alpha', 'local'],
      ['task-w2', 'github.com/acme/r1', 'alpha', 'local'],
      ['task-w3', 'acme/r2', 'beta', 'local'],
    ])
  })

  it('shows a machine version, names a repository by its id when unlisted, and caps the recent list', async () => {
    const many = Array.from({ length: RECENT_WORKTREES + 2 }, (_, i) => worktree(`w${i}`, 'r-gone', 'alpha'))
    const doc = fleetDocument({ worktrees: many })
    doc.machines[0].wb_version = '1.2.3'
    const { root } = await openPage('/dashboard', DashboardPage, doc)
    const [machines, worktrees] = [...root.querySelectorAll<HTMLElement>('p-table')]
    expect(bodyRows(machines)[0][2]).toBe('1.2.3')
    expect(bodyRows(worktrees)).toHaveLength(RECENT_WORKTREES)
    expect(bodyRows(worktrees)[0][1]).toBe('r-gone')
  })

  it('says when the fleet is empty', async () => {
    const { root } = await openPage('/dashboard', DashboardPage, fleetDocument({ machines: [], worktrees: [], repositories: [], agents: [] }))
    const [machines, worktrees] = [...root.querySelectorAll<HTMLElement>('p-table')]
    expect(bodyRows(machines)).toEqual([['No machines yet.']])
    expect(bodyRows(worktrees)).toEqual([['No worktrees yet.']])
    expect(root.querySelector('.tile a')?.textContent).toBe('0')
  })

  it('renders on its own over an empty, warming-up fleet', async () => {
    TestBed.configureTestingModule({ providers: [provideRouter([])] })
    const fixture = TestBed.createComponent(DashboardPage)
    await fixture.whenStable()
    expect(fixture.nativeElement.querySelector('h2')?.textContent).toBe('Dashboard')
    expect(fixture.nativeElement.querySelectorAll('tbody tr').length).toBeGreaterThan(0)
  })
})
