import { expect, test, type Page } from '@playwright/test'
import { countOpensItsList, everyTabListsRows, filterChipPanelDetailBack, homeLoads, machineMetrics, newTaskProducesCommands, paletteOpensATask, type JourneyFleet, type JourneyMetrics } from './journey/steps'
import { checkedAt, watch } from './support'

// The steps of the real-daemon journey (journey/steps.ts), run here against a stubbed daemon whose answers are shaped
// like the real one's for the journey's fixture: one Linux machine that is this one, two repositories, two WB
// worktrees of two tasks, no pull request and no agent, and a metrics history whose first sample has no CPU
// reading. The journey itself (journey/journey.e2e.ts) runs only on Linux CI against a real daemon; this keeps its
// selectors and flows honest where a developer can run them.

const ago = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString()
const local = { machine: 'runner', machine_id: 'mach-runner', route: 'local', observed_at: ago(0) }

const fleet = {
  schema_version: 2,
  snapshot_at: ago(0),
  refresh_interval_seconds: 30,
  warming_up: false,
  repositories_total: 2,
  repositories_scanned: 2,
  diagnostics: 0,
  machines: [{ id: 'mach-runner', ...local, wb_version: '1.0.0', os: 'linux', arch: 'amd64', cpu_count: 4, repository_count: 2, worktree_count: 2 }],
  repositories: [
    { id: 'repo-shop', ...local, host: 'github.com', name: 'acme/shop', default_branch: 'main', worktree_count: 2 },
    { id: 'repo-links', ...local, host: 'github.com', name: 'acme/links', default_branch: 'main', worktree_count: 0 },
  ],
  worktrees: ['journey-one', 'journey-two'].map((task, index) => ({
    id: `wt-${index + 1}`,
    ...local,
    repository: 'repo-shop',
    task,
    branch: `task/${task}`,
    owner_state: 'unknown',
    last_activity_at: ago(index + 1),
    ahead: 0,
    behind: 0,
    has_upstream: false,
  })),
  pull_requests: [],
  agents: [],
}
const journeyFleet: JourneyFleet = fleet

const sample = (minutesAgo: number, withCpu: boolean) => ({
  ...(withCpu ? { cpu_percent: 12.5 } : {}),
  load1: 0.4,
  memory_used_bytes: 2 * 2 ** 30,
  memory_total_bytes: 8 * 2 ** 30,
  disk_free_bytes: 50 * 2 ** 30,
  disk_total_bytes: 100 * 2 ** 30,
  sampled_at: ago(minutesAgo),
})

async function serve(page: Page, metrics: JourneyMetrics & { machine: string }) {
  const requests: string[] = []
  page.on('request', (request) => requests.push(`${request.method()} ${new URL(request.url()).pathname}`))
  await page.route('**/api/v1/cockpit/fleet', (route) => route.fulfill({ json: { ...fleet, snapshot_at: ago(0) }, headers: { ETag: '"journey"', 'Cache-Control': 'no-cache', ...checkedAt() } }))
  await page.route('**/api/v1/cockpit/session', (route) => route.fulfill({ json: { principal: 'owner', capabilities: ['fleet.read', 'repo.content.read'], code_browser_url: 'https://codegrapher.dev/' } }))
  await page.route('**/api/v1/cockpit/branches?**', (route) => route.fulfill({ json: { branches: [] } }))
  await page.route('**/api/v1/cockpit/machine-metrics?**', (route) => route.fulfill({ json: metrics }))
  return requests
}

const origin = () => new URL(test.info().project.use.baseURL as string).origin
// Read when served (a getter), so a sample is as old as the page believes however long after the file loaded the test starts.
const withCpu = {
  machine: 'mach-runner',
  route: 'local',
  get samples() {
    return [sample(2, false), sample(1.8, true), sample(1.5, true)]
  },
}
const withoutCpu = {
  machine: 'mach-runner',
  route: 'local',
  get samples() {
    return [sample(0.2, false)]
  },
}

// cockpit-views#ac:every-tab-lists-its-collection
test('Home holds its sections and every tab lists the rows of the fleet', async ({ page }) => {
  await serve(page, withCpu)
  const expectClean = await watch(page)
  await homeLoads(page, origin(), journeyFleet)
  await everyTabListsRows(page, journeyFleet)
  await expectClean()
})

// cockpit-views#ac:every-number-is-a-link
test('a count on Machines opens the list it counts', async ({ page }) => {
  await serve(page, withCpu)
  const expectClean = await watch(page)
  await countOpensItsList(page, origin(), journeyFleet)
  await expectClean()
})

test('a filter, a chip, the panel, the detail route and Back keep their state in the address', async ({ page }) => {
  await serve(page, withCpu)
  const expectClean = await watch(page)
  await filterChipPanelDetailBack(page, origin(), journeyFleet)
  await expectClean()
})

// cockpit-views#ac:palette-searches-in-memory
test('the palette finds a task and opens it, and New task produces its commands and runs nothing', async ({ page }) => {
  await serve(page, withCpu)
  const expectClean = await watch(page)
  await paletteOpensATask(page, origin(), 'journey-one')
  await newTaskProducesCommands(page, origin(), ['acme/links', 'acme/shop'], 'acme/*')
  await expectClean()
})

// cockpit-views#ac:machine-detail-metrics-charts
test('the local machine draws its history as four charts with tables, and a first sample without a CPU says "not reported"', async ({ page }) => {
  await serve(page, withCpu)
  const expectClean = await watch(page)
  await machineMetrics(page, origin(), journeyFleet, withCpu)
  await expectClean()

  // The first sample after a daemon start has no CPU: the page says so and the machine's load is unknown, never free and never busy.
  const first = await page.context().newPage()
  await serve(first, withoutCpu)
  await machineMetrics(first, origin(), journeyFleet, withoutCpu)
  await expect(first.locator('app-machine-panel dl.latest dt', { hasText: /^CPU$/ }).locator('+ dd')).toHaveText('not reported')
  await first.goto(`${origin()}/cockpit/machines`)
  await expect(first.locator('[role=row][data-index]').first()).toContainText('load unknown')
  await expect(first.locator('[role=row][data-index]').first().locator('.meter')).toHaveCount(0)
})

test('a platform with no metrics says so, and draws nothing', async ({ page }) => {
  const none = { machine: 'mach-runner', route: 'none', samples: [], reason: 'unsupported' }
  await serve(page, none)
  const expectClean = await watch(page)
  await machineMetrics(page, origin(), journeyFleet, none)
  await expectClean()
})
