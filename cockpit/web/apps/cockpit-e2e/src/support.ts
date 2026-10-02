import { expect, type Page } from '@playwright/test'
import type { FleetDocument } from '@cockpit/fleet-data'
// The fixtures of Home's states are the fleets the application is photographed against; the stubbed suite serves them too.
// eslint-disable-next-line @nx/enforce-module-boundaries
import { homeStates } from '../../cockpit/src/app/pages/home/home-fixtures'
import { otherConsoleErrors, unexplainedViolations, type Violation } from './violations'

// What the stubbed end-to-end tests share: a fleet document and the stubs of the
// daemon's routes, and the watcher that fails a test on a console error or a
// policy violation.

export const observed = new Date(Date.now() - 12 * 60_000).toISOString()
export const now = new Date().toISOString()

export const alpha = { machine: 'alpha', machine_id: 'mach-alpha', route: 'local', observed_at: now }
export const beta = { machine: 'beta', machine_id: 'mach-beta', route: 'cached', observed_at: observed }

export const fleet = {
  schema_version: 2,
  snapshot_at: now,
  warming_up: false,
  repositories_total: 3,
  repositories_scanned: 3,
  diagnostics: 0,
  machines: [
    { id: 'mach-alpha', ...alpha, wb_version: '1.0.0', repository_count: 2, worktree_count: 2 },
    { id: 'mach-beta', ...beta, repository_count: 1, worktree_count: 1 },
  ],
  repositories: [
    { id: 'repo-cli', ...alpha, host: 'github.com', name: 'specscore/specscore-cli', default_branch: 'main', worktree_count: 2, active_agent_count: 1, code_index: [{ indexer: 'codegrapher', state: 'stale', behind: 3 }] },
    { id: 'repo-web', ...alpha, host: 'github.com', name: 'acme/web', default_branch: 'main', worktree_count: 0, active_agent_count: 0 },
    { id: 'repo-far', ...beta, name: 'acme/far', worktree_count: 1 },
  ],
  worktrees: [
    { id: 'wt-1', ...alpha, repository: 'repo-cli', task: 'add-search', branch: 'task/add-search', owner_state: 'active', last_activity_at: now, code_index: [{ indexer: 'codegrapher', state: 'fresh' }] },
    { id: 'wt-2', ...alpha, repository: 'repo-cli', task: 'fix-index', branch: 'task/fix-index', owner_state: 'idle', last_activity_at: observed },
    { id: 'wt-3', ...beta, repository: 'repo-far', task: 'far-task', branch: 'task/far-task' },
  ],
  pull_requests: [],
  agents: [
    { id: 'ag-1', ...alpha, kind: 'session', session_id: 'sess-1', runtime: 'claude', state: 'live', repository: 'repo-cli' },
    { id: 'ag-2', ...beta, kind: 'run', run_id: 'run-7', state: 'finished', repository: 'repo-far' },
  ],
}

export async function stub(page: Page, codeBrowserUrl = 'https://codegrapher.dev/') {
  // The snapshot is stamped when it is served, as a daemon does: a slow runner reaches a test minutes after this file loaded.
  await page.route('**/api/v1/cockpit/fleet', (route) =>
    route.fulfill({ json: { ...fleet, snapshot_at: new Date().toISOString() }, headers: { ETag: '"stub"', 'Cache-Control': 'no-cache' } }),
  )
  await page.route('**/api/v1/cockpit/session', (route) =>
    route.fulfill({
      json: { principal: 'anonymous-local', capabilities: ['fleet.read'], code_browser_url: codeBrowserUrl },
    }),
  )
  // A repository's page and panel read its branches when they open (the lazy branches route).
  await page.route('**/api/v1/cockpit/branches?**', (route) => route.fulfill({ json: { branches: [] } }))
  // Home and Machines read the machine metrics every 10 seconds.
  await page.route('**/api/v1/cockpit/machine-metrics?**', (route) => route.fulfill({ json: { machine: 'x', route: 'local', samples: [] } }))
}

/** Records console errors and policy violations; call `expectClean` last. */
export async function watch(page: Page) {
  const consoleErrors: string[] = []
  page.on('console', (message) => {
    if (message.type() === 'error') consoleErrors.push(message.text())
  })
  page.on('pageerror', (error) => consoleErrors.push(`page error: ${error.message}`))
  await page.addInitScript(() => {
    const store = window as unknown as { __violations: unknown[] }
    store.__violations = []
    document.addEventListener('securitypolicyviolation', (event) => {
      store.__violations.push({ directive: event.violatedDirective, blockedURI: event.blockedURI })
    })
  })
  return async () => {
    const violations = await page.evaluate(() => (window as unknown as { __violations: unknown[] }).__violations)
    expect(unexplainedViolations(violations as Violation[])).toEqual([])
    expect(otherConsoleErrors(consoleErrors)).toEqual([])
  }
}

/** The busy fleet of Home's fixtures (every state the lists show), the routes of every page with the ids of its entries. */
export const BUSY_ROUTES: { name: string; path: string }[] = [
  { name: 'home', path: '' },
  { name: 'tasks', path: 'tasks' },
  { name: 'tasks-new', path: 'tasks/new' },
  { name: 'task-detail', path: 'tasks/detail?task=refactor-cache' },
  { name: 'repositories', path: 'repositories' },
  { name: 'repository-by-name', path: 'repositories/github.com/sneat-dev/wb' },
  { name: 'repository-by-id', path: 'repositories/r-wb' },
  { name: 'worktrees', path: 'worktrees' },
  { name: 'worktree-detail', path: 'worktrees/wt-1' },
  { name: 'agents', path: 'agents' },
  { name: 'agent-detail', path: 'agents/vm-blocked' },
  { name: 'machines', path: 'machines' },
  { name: 'machine-detail-local', path: 'machines/mach-mac' },
  { name: 'machine-detail-live', path: 'machines/mach-vm' },
  { name: 'machine-detail-stale', path: 'machines/mach-old' },
]

/**
 * Stubs the daemon with the busy fleet of Home's fixtures and its machine metrics, for a session of `principal`
 * (`machine_routes` are put in the session response of any principal, so a page that believed them for an anonymous
 * one would show). Returns every address the page asked, as `METHOD /path`.
 */
export async function stubBusy(page: Page, principal: 'anonymous-local' | 'owner' = 'anonymous-local', change: (document: FleetDocument) => FleetDocument = (document) => document): Promise<string[]> {
  const { metrics } = homeStates()['busy']
  const document = change(homeStates()['busy'].document)
  const requests: string[] = []
  page.on('request', (request) => requests.push(`${request.method()} ${new URL(request.url()).pathname}${new URL(request.url()).search}`))
  const routes = document.machines.filter((machine) => machine.route !== 'local').map((machine) => ({ machine_id: machine.id, ssh: { host: 'secret-host.example', user: 'secretuser', wb_path: '/opt/secret/wb' } }))
  await page.route('**/api/v1/cockpit/fleet', (route) => route.fulfill({ json: { ...document, snapshot_at: new Date().toISOString() }, headers: { ETag: '"busy"', 'Cache-Control': 'no-cache' } }))
  await page.route('**/api/v1/cockpit/session', (route) =>
    route.fulfill({
      json: {
        principal,
        capabilities: principal === 'owner' ? ['fleet.read', 'repo.content.read'] : ['fleet.read'],
        code_browser_url: 'https://codegrapher.dev/',
        machine_routes: routes,
      },
    }),
  )
  await page.route('**/api/v1/cockpit/branches?**', (route) => route.fulfill({ json: { branches: [] } }))
  await page.route('**/api/v1/cockpit/machine-metrics?**', (route) => {
    const id = new URL(route.request().url()).searchParams.get('machine') ?? ''
    const answer = metrics.get(id)
    // The sample times are those of the fixture's snapshot: moved to now, so the charts and the load verdict are current.
    const shift = answer === undefined ? 0 : Date.now() - Date.parse(answer.samples[answer.samples.length - 1]?.sampled_at ?? new Date().toISOString())
    return route.fulfill({
      json: answer === undefined ? { machine: id, route: 'none', samples: [], reason: 'no_source' } : { ...answer, samples: answer.samples.map((sample) => ({ ...sample, sampled_at: new Date(Date.parse(sample.sampled_at) + shift).toISOString() })) },
    })
  })
  return requests
}
