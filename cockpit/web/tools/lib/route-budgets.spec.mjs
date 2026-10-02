import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { HOME_BUDGET, ROUTE_BUDGET, budgetOf, budgetTable, overBudget, routeBudgets, routeEntries } from './route-budgets.mjs'

const ROUTES = `
const page = (path, title, loadComponent) => ({ path, title, loadComponent })
export const pageRoutes = [
  { ...page('', 'Home', () => import('./pages/home/home-page').then((m) => m.HomePage)), pathMatch: 'full' },
  { path: 'dashboard', redirectTo: '' },
  page('tasks', 'Tasks', () => import('./pages/tasks/tasks-page').then((m) => m.TasksPage)),
  page('tasks/new', 'New task', () => import('./pages/new-task/new-task-page').then((m) => m.NewTaskPage)),
  page('repositories/:host/:owner/:name', 'Repository', () => import('./pages/repositories/repository-detail-page').then((m) => m.RepositoryDetailPage)),
  ...galleryRoutes,
  { path: '**', redirectTo: '' },
]`

// Three routes: the fourth of ROUTES has no page in the build.
const THREE = ROUTES.replace(/\n {2}page\('repositories[^\n]*/, '')

let dist

beforeEach(() => {
  dist = mkdtempSync(join(tmpdir(), 'route-budgets-'))
})

afterEach(() => {
  rmSync(dist, { recursive: true, force: true })
})

// main loads a shared chunk statically; each page is a lazy chunk of its own that imports the library chunk statically.
function writeBuild({ homeBytes = 500, tasksBytes = 500, newTaskBytes = 500, omit = [] } = {}) {
  writeFileSync(join(dist, 'index.html'), '<script src="main.js" type="module"></script>')
  writeFileSync(join(dist, 'main.js'), `import"./chunk-LIB.js";${'m'.repeat(1000)}`)
  writeFileSync(join(dist, 'chunk-LIB.js'), 'l'.repeat(300))
  writeFileSync(join(dist, 'chunk-HOME.js'), 'h'.repeat(homeBytes))
  writeFileSync(join(dist, 'chunk-TASKS.js'), 'import"./chunk-LIB.js";' + 't'.repeat(tasksBytes))
  writeFileSync(join(dist, 'chunk-NEW.js'), 'n'.repeat(newTaskBytes))
  const page = (file, entry) => [file, { entryPoint: `apps/cockpit/src/app/${entry}`, imports: [{ path: 'chunk-LIB.js', kind: 'import-statement' }] }]
  const outputs = Object.fromEntries(
    [
      ['main.js', { imports: [{ path: 'chunk-LIB.js', kind: 'import-statement' }] }],
      ['chunk-LIB.js', {}],
      page('chunk-HOME.js', 'pages/home/home-page.ts'),
      page('chunk-TASKS.js', 'pages/tasks/tasks-page.ts'),
      page('chunk-NEW.js', 'pages/new-task/new-task-page.ts'),
    ].filter(([file]) => !omit.includes(file)),
  )
  writeFileSync(join(dist, 'stats.json'), JSON.stringify({ outputs }))
}

describe('routeEntries', () => {
  it('lists the lazy page routes, behind a spread too, and not a redirect or the gallery', () => {
    expect(routeEntries(ROUTES)).toEqual([
      { route: '/', entry: 'pages/home/home-page.ts' },
      { route: '/tasks', entry: 'pages/tasks/tasks-page.ts' },
      { route: '/tasks/new', entry: 'pages/new-task/new-task-page.ts' },
      { route: '/repositories/:host/:owner/:name', entry: 'pages/repositories/repository-detail-page.ts' },
    ])
    expect(routeEntries('export const nothing = []')).toEqual([])
  })

  it('reads every page route of the real routes file', async () => {
    const { readFileSync } = await import('node:fs')
    const real = routeEntries(readFileSync(new URL('../../apps/cockpit/src/app/app.routes.ts', import.meta.url), 'utf8'))
    expect(real.map((entry) => entry.route)).toEqual([
      '/', '/tasks', '/tasks/new', '/tasks/detail', '/repositories', '/repositories/:host/:owner/:name', '/repositories/:id',
      '/worktrees', '/worktrees/:id', '/agents', '/agents/:id', '/machines', '/machines/:id',
    ])
  })
})

describe('budgets', () => {
  it('gives Home 350 kB and every other route 500 kB', () => {
    expect([HOME_BUDGET, ROUTE_BUDGET]).toEqual([350_000, 500_000])
    expect(budgetOf('/')).toBe(HOME_BUDGET)
    expect(budgetOf('/tasks')).toBe(ROUTE_BUDGET)
    expect(budgetOf('/tasks/new')).toBe(ROUTE_BUDGET)
  })
})

describe('routeBudgets', () => {
  it('measures every route the same way: the initial scripts and the page with what it imports statically', () => {
    writeBuild()
    const result = routeBudgets(dist, THREE)
    expect(result.problems).toEqual([])
    // main.js is `import"./chunk-LIB.js";` and 1,000 bytes; chunk-LIB.js is 300.
    expect(result.initialBytes).toBe(23 + 1000 + 300)
    expect(result.rows.map((row) => [row.route, row.bytes, row.budget, row.over])).toEqual([
      ['/', 1323 + 500, HOME_BUDGET, false],
      ['/tasks', 1323 + 23 + 500, ROUTE_BUDGET, false],
      ['/tasks/new', 1323 + 500, ROUTE_BUDGET, false],
    ])
  })

  it('is over for a route above its own budget and not for one above only Home\'s', () => {
    writeBuild({ homeBytes: HOME_BUDGET, tasksBytes: HOME_BUDGET, newTaskBytes: ROUTE_BUDGET })
    const result = routeBudgets(dist, THREE)
    expect(result.rows.map((row) => [row.route, row.over])).toEqual([
      ['/', true],
      ['/tasks', false],
      ['/tasks/new', true],
    ])
    const lines = overBudget(result)
    expect(lines).toHaveLength(2)
    expect(lines[0]).toMatch(/^\/ first-page JavaScript is \d+\.\d\d kB, over the budget of 350\.00 kB \(.*chunk-HOME\.js/)
    expect(lines[1]).toContain('/tasks/new first-page JavaScript')
    expect(lines[1]).toContain('over the budget of 500.00 kB')
  })

  it('fails closed: no Home route, no metafile, no script, a route whose page is not an output', () => {
    writeBuild()
    expect(routeBudgets(dist, "page('tasks', 'Tasks', () => import('./pages/tasks/tasks-page'))").problems.join()).toContain('no page route for Home')
    writeBuild({ omit: ['chunk-TASKS.js'] })
    const result = routeBudgets(dist, THREE)
    expect(result.problems).toEqual(['/tasks: no build output is pages/tasks/tasks-page.ts'])
    expect(result.rows.map((row) => row.route)).toEqual(['/', '/tasks/new'])
    writeFileSync(join(dist, 'index.html'), '<p>no script</p>')
    expect(routeBudgets(dist, THREE).problems.join()).toContain('loads no script')
  })
})

describe('budgetTable', () => {
  it('has a heading, a header and a line per route with its figure, budget, share and verdict', () => {
    writeBuild({ homeBytes: HOME_BUDGET })
    const lines = budgetTable(routeBudgets(dist, THREE))
    expect(lines[0]).toMatch(/^first-page JavaScript per route \(the initial static \d+\.\d\d kB is in every figure\)$/)
    expect(lines[1]).toMatch(/^route +first page +budget +used *$/)
    expect(lines.slice(2).map((line) => line.trim().split(/\s+/))).toEqual([
      ['/', expect.stringMatching(/^\d+\.\d\d$/), 'kB', '350.00', 'kB', expect.stringMatching(/^\d+%$/), 'OVER'],
      ['/tasks', expect.stringMatching(/^\d+\.\d\d$/), 'kB', '500.00', 'kB', expect.stringMatching(/^\d+%$/), 'ok'],
      ['/tasks/new', expect.stringMatching(/^\d+\.\d\d$/), 'kB', '500.00', 'kB', expect.stringMatching(/^\d+%$/), 'ok'],
    ])
  })
})
