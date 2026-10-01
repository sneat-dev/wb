import { expect, type Page } from '@playwright/test'
import { otherConsoleErrors, unexplainedViolations, type Violation } from './violations'

// What the stubbed end-to-end tests share: a fleet document and the stubs of the
// daemon's routes, and the watcher that fails a test on a console error or a
// policy violation beyond the PrimeUI licence banner this build is known to show.

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
  await page.route('**/api/v1/cockpit/fleet', (route) =>
    route.fulfill({ json: fleet, headers: { ETag: '"stub"', 'Cache-Control': 'no-cache' } }),
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
      const inLicenseBanner = event.composedPath().some((node) => (node as Element).id === 'p-license-host')
      store.__violations.push({ directive: event.violatedDirective, blockedURI: event.blockedURI, inLicenseBanner })
    })
  })
  return async () => {
    const violations = await page.evaluate(() => (window as unknown as { __violations: unknown[] }).__violations)
    expect(unexplainedViolations(violations as Violation[])).toEqual([])
    expect(otherConsoleErrors(consoleErrors)).toEqual([])
  }
}
