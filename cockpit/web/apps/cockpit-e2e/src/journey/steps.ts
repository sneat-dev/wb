import { expect, type Locator, type Page } from '@playwright/test'

// The steps of the whole journey over the pages of the Cockpit (cockpit-views: Home, the five tabs, a list's filter,
// chip, panel and detail route, the palette, New task and a machine's metrics), written once and run twice: by the
// real-daemon journey on Linux CI (journey.e2e.ts, which cannot run on a developer's machine) and by the stubbed
// suite against a fleet shaped like that daemon's (journey-steps.e2e.ts), so a selector or a flow that is wrong
// fails where it can be seen. Every expectation is read from the daemon's own answers (the fleet document, the
// metrics route) or from the fixture the journey builds; none assumes a number the daemon decides.

/** What a step needs of the fleet document: the collections it counts. */
export interface JourneyFleet {
  machines: { id: string; machine: string; route: string }[]
  repositories: { id: string; name: string; host?: string }[]
  worktrees: { id: string; task: string }[]
  agents: unknown[]
}

/** One metrics sample as the route serves it: every measurement is optional. */
export interface JourneySample {
  cpu_percent?: number
  memory_total_bytes?: number
  sampled_at: string
}

export interface JourneyMetrics {
  route: string
  samples: JourneySample[]
  reason?: string
}

const rows = (page: Page): Locator => page.locator('[role=row][data-index]')
const tabs = (page: Page): Locator => page.getByRole('navigation', { name: 'Pages' })
const exact = (text: string): RegExp => new RegExp(`^\\s*${text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}`)

/** The tasks of a fleet: one per task name. */
export const taskNames = (fleet: JourneyFleet): string[] => [...new Set(fleet.worktrees.map((worktree) => worktree.task))].sort()
/** The repositories of a fleet merged by identity: one per lower-cased name. */
export const repositoryNames = (fleet: JourneyFleet): string[] => [...new Set(fleet.repositories.map((repository) => repository.name.toLowerCase()))].sort()

/** Home loads and holds its sections, "Needs you" first, each one a heading of the second level. */
export async function homeLoads(page: Page, origin: string, fleet: JourneyFleet): Promise<void> {
  await page.goto(`${origin}/cockpit/`)
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Home')
  const sections = page.locator('h2.home-h')
  await expect(sections.first()).toHaveText(exact('Needs you'))
  // The sections after the first arrive with the first complete scan, as one lazy chunk.
  for (const name of ['Ready to land', 'In flight', 'Resume', 'Cleanup', 'Throughput']) {
    await expect(sections.filter({ hasText: exact(name) }), name).toHaveCount(1, { timeout: 30_000 })
  }
  // The tasks of the fleet are what Resume offers, and the machine strip has one tile for each machine.
  const resume = page.getByRole('region', { name: /^Resume/ })
  for (const task of taskNames(fleet)) await expect(resume, task).toContainText(task)
  await expect(page.getByRole('region', { name: /^In flight/ }).getByLabel('Machines').locator('.home-tile')).toHaveCount(fleet.machines.length)
}

/** Each tab lists the rows of the real fleet: as many as its collection, with the count line to match. */
export async function everyTabListsRows(page: Page, fleet: JourneyFleet): Promise<void> {
  const expected: [label: string, path: string, count: number][] = [
    ['Tasks', 'tasks', taskNames(fleet).length],
    ['Repositories', 'repositories', repositoryNames(fleet).length],
    ['Worktrees', 'worktrees', fleet.worktrees.length],
    ['Agents', 'agents', fleet.agents.length],
    ['Machines', 'machines', fleet.machines.length],
  ]
  for (const [label, path, count] of expected) {
    await tabs(page).getByRole('link', { name: exact(label) }).click()
    await expect(page).toHaveURL(new RegExp(`/cockpit/${path}(\\?|$)`))
    await expect(tabs(page).getByRole('link', { name: exact(label) })).toHaveAttribute('aria-current', 'page')
    await expect(page.getByRole('heading', { level: 1 })).toHaveText(label)
    if (count === 0) {
      await expect(page.getByText('Nothing has been observed'), label).toBeVisible()
      await expect(rows(page)).toHaveCount(0)
    } else {
      await expect(rows(page), label).toHaveCount(count)
      await expect(page.locator('.count'), label).toHaveText(`${count} of ${count}`)
    }
  }
}

/** The text filter, a chip, the panel, the detail route and Back, on Worktrees: each keeps its state in the address. */
export async function filterChipPanelDetailBack(page: Page, origin: string, fleet: JourneyFleet): Promise<void> {
  const total = fleet.worktrees.length
  const task = fleet.worktrees[0].task
  await page.goto(`${origin}/cockpit/worktrees`)
  await expect(rows(page)).toHaveCount(total)

  // The filter narrows the rows and the address holds it.
  const filter = page.getByRole('textbox', { name: 'Filter worktrees' })
  await filter.fill(task)
  await expect(page).toHaveURL(/[?&]q=/)
  const matching = fleet.worktrees.filter((worktree) => worktree.task.includes(task)).length
  await expect(rows(page)).toHaveCount(matching)
  await expect(page.locator('.count')).toHaveText(`${matching} of ${total}`)
  await filter.fill('')
  await expect(rows(page)).toHaveCount(total)

  // A chip toggles, the address holds it and the rows are the ones the count line says.
  const chip = page.getByRole('button', { name: 'Unpushed', exact: true })
  await chip.click()
  await expect(chip).toHaveAttribute('aria-pressed', 'true')
  await expect(page).toHaveURL(/chips=unpushed/)
  const shown = /^(\d+) of (\d+)$/.exec(((await page.locator('.count').textContent()) ?? '').trim())
  expect(shown, 'the count line of a chip').not.toBeNull()
  await expect(rows(page)).toHaveCount(Number(shown![1]))
  expect(Number(shown![2])).toBe(total)
  await chip.click()
  await expect(chip).toHaveAttribute('aria-pressed', 'false')
  await expect(page).not.toHaveURL(/chips=/)
  await expect(rows(page)).toHaveCount(total)

  // Selecting a row opens its panel beside the list; Escape closes it.
  const first = rows(page).first()
  const name = ((await first.locator('a.name').textContent()) ?? '').trim()
  await first.locator('[role=gridcell]:not(.open-cell)').first().click()
  await expect(page).toHaveURL(/[?&]sel=/)
  const panel = page.getByRole('complementary')
  await expect(panel.getByRole('heading', { level: 2 })).toHaveText(name)
  await page.getByRole('grid').focus()
  await page.keyboard.press('Escape')
  await expect(panel).toBeHidden()
  await expect(page).not.toHaveURL(/sel=/)

  // The detail route is the same content on a page, and Back returns to the list with its selection.
  await first.locator('[role=gridcell]:not(.open-cell)').first().click()
  await expect(panel).toBeVisible()
  await page.getByRole('grid').focus()
  await page.keyboard.press('o')
  await expect(page).toHaveURL(/\/cockpit\/worktrees\/[^/?]+$/)
  await expect(page.locator('app-worktree-panel').getByRole('heading', { level: 2 })).toHaveText(name)
  await page.goBack()
  await expect(page).toHaveURL(/[?&]sel=/)
  await expect(page.getByRole('complementary').getByRole('heading', { level: 2 })).toHaveText(name)
}

/** The palette opens with Control+K, finds a task by name and opens it on Enter. */
export async function paletteOpensATask(page: Page, origin: string, task: string): Promise<void> {
  await page.goto(`${origin}/cockpit/`)
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Home')
  await expect(page.locator('app-overlays')).toBeAttached()
  await page.keyboard.press('Control+k')
  const dialog = page.getByRole('dialog', { name: 'Search' })
  await expect(dialog).toBeVisible()
  await dialog.getByRole('combobox').fill(task)
  await expect(dialog.getByRole('option').first()).toContainText(task)
  await expect(dialog.getByRole('option').first()).toHaveAttribute('aria-selected', 'true')
  await page.keyboard.press('Enter')
  await expect(dialog).toBeHidden()
  await expect(page).toHaveURL(new RegExp(`/cockpit/tasks/detail\\?task=${encodeURIComponent(task)}$`))
  await expect(page.locator('app-task-panel').getByRole('heading', { level: 2 })).toHaveText(task)
}

/** New task produces the commands to copy, and runs nothing. `pattern` matches the repositories to pick (a wildcard). */
export async function newTaskProducesCommands(page: Page, origin: string, repositories: string[], pattern: string): Promise<void> {
  const requests: string[] = []
  const record = (request: { method(): string; url(): string }) => requests.push(`${request.method()} ${new URL(request.url()).pathname}`)
  await page.goto(`${origin}/cockpit/`)
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Home')
  page.on('request', record)
  await page.getByRole('link', { name: 'New task' }).click()
  await expect(page).toHaveURL(/\/cockpit\/tasks\/new$/)
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('New task')

  await page.getByRole('combobox', { name: 'Repository' }).fill(pattern)
  await page.getByRole('button', { name: `Add all ${repositories.length} matches` }).click()
  await expect(page.locator('.chip-name')).toHaveCount(repositories.length)
  expect((await page.locator('.chip-name').allInnerTexts()).map((text) => text.trim()).sort()).toEqual([...repositories].sort())
  await page.getByLabel('Task name').fill('journey-three')
  await page.getByLabel('Brief', { exact: true }).fill('Journey brief.')

  // With a brief the commands are the dispatches alone: dispatch creates the worktree itself, so no creation comes first.
  const commands = page.locator('app-copy-command-list li')
  await expect(commands).toHaveCount(repositories.length)
  await expect(page.locator('app-copy-command-list')).not.toContainText('wb worktree create')
  for (const repository of repositories) {
    const dispatch = commands.filter({ hasText: `--repo='${repository}'` })
    await expect(dispatch).toHaveCount(1)
    await expect(dispatch.locator('code')).toContainText("--task='Journey brief.' ")
    await expect(dispatch.locator('code')).toContainText("--new-worktree='journey-three'")
  }
  // The form ran nothing and wrote nothing: it made no request but the page's own reads.
  expect(requests.filter((request) => !request.startsWith('GET '))).toEqual([])
  page.off('request', record)
}

/**
 * The local machine's page. Where the daemon reports metrics for it (the route says `local` with samples) its history
 * is drawn as four charts, each with its table of the numbers; where it does not, the page says so. A CPU reading is
 * a percentage or "not reported" (the first sample after a start has none), never a zero invented for it.
 */
export async function machineMetrics(page: Page, origin: string, fleet: JourneyFleet, metrics: JourneyMetrics): Promise<void> {
  const local = fleet.machines.find((machine) => machine.route === 'local')
  expect(local, 'the fleet has a local machine').toBeDefined()
  await page.goto(`${origin}/cockpit/machines`)
  const row = rows(page).filter({ hasText: local!.machine })
  await expect(row).toHaveCount(1)
  await expect(row).toContainText('local')
  await row.locator('a.name').click()
  await expect(page).toHaveURL(new RegExp(`/cockpit/machines/${encodeURIComponent(local!.id)}$`))
  const content = page.locator('app-machine-panel')
  await expect(content.getByRole('heading', { level: 2 })).toHaveText(local!.machine)
  const source = content.locator('.source')
  if (metrics.route === 'local' && metrics.samples.length > 0) {
    await expect(source).toContainText("local: this machine's own history")
    await expect(content.locator('app-machine-charts canvas')).toHaveCount(4)
    await expect(content.locator('app-machine-charts .title')).toHaveText(['CPU', 'Load (1 minute)', 'Memory used', 'Disk free'])
    // Each chart is a canvas with a text alternative and a table of the same numbers.
    await expect(content.locator('app-machine-charts canvas[role=img][aria-label]')).toHaveCount(4)
    await expect(content.locator('app-machine-charts .data table')).toHaveCount(4)
    const cpu = content.locator('dl.latest dt', { hasText: /^CPU$/ }).locator('+ dd')
    await expect(cpu).toHaveText(/^(\d+%|not reported)$/)
  } else {
    await expect(source).toHaveText('Metrics are not reported for this machine.')
    await expect(content.locator('app-machine-charts, canvas')).toHaveCount(0)
    await expect(content.locator('dl.latest')).toHaveCount(0)
  }
}

/** A count is a link: the local machine's Worktrees count opens exactly the worktrees of the fleet on it. */
export async function countOpensItsList(page: Page, origin: string, fleet: JourneyFleet): Promise<void> {
  const local = fleet.machines.find((machine) => machine.route === 'local')
  expect(local, 'the fleet has a local machine').toBeDefined()
  await page.goto(`${origin}/cockpit/machines`)
  await rows(page).filter({ hasText: local!.machine }).locator('a[href*="/cockpit/worktrees?"]').click()
  await expect(page).toHaveURL(new RegExp(`/cockpit/worktrees\\?.*machine=${encodeURIComponent(local!.id)}`))
  await expect(rows(page)).toHaveCount(fleet.worktrees.length)
  for (const worktree of fleet.worktrees) await expect(rows(page).filter({ hasText: worktree.task })).not.toHaveCount(0)
}
