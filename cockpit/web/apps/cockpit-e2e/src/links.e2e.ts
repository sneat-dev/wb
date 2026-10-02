import { expect, test } from '@playwright/test'
import { BUSY_ROUTES, stubBusy, watch } from './support'

// cockpit-views#ac:every-number-is-a-link: the enumerated count cells of REQ:every-number-is-a-link on Home, Tasks,
// Repositories and Machines, each a link to the list that produced it, using only the vocabulary; and the declared
// exceptions (branch counts, the throughput numbers, a pull request's checks over total), each not a link and
// carrying its reason in its title. Against the fixture of every state, with counts on its repositories.

const withCounts = <T extends { repositories: { worktree_count: number }[] }>(document: T): T => ({
  ...document,
  repositories: document.repositories.map((repository) => ({ ...repository, worktree_count: 3, active_agent_count: 2, open_pull_request_count: 4, local_branch_count: 5, remote_branch_count: 7 })),
})

const hrefOf = async (locator: ReturnType<import('@playwright/test').Page['locator']>) => new URL((await locator.getAttribute('href')) as string, 'http://x')

test('Home: every count it enumerates opens the list it counted', async ({ page }) => {
  await stubBusy(page, 'anonymous-local', withCounts)
  const expectClean = await watch(page)
  await page.goto('/cockpit/')
  await expect(page.getByRole('heading', { level: 2, name: 'Cleanup' })).toBeVisible()
  const link = (name: string | RegExp, scope = page.locator('body')) => scope.getByRole('link', { name })

  // "+n more" of Needs you: Tasks with the chip needs-you; the badge of Home too.
  expect((await hrefOf(link(/^\+\d+ more$/))).search).toBe('?chips=needs-you')
  expect((await hrefOf(link(/^\+\d+ more$/))).pathname).toBe('/cockpit/tasks')
  // The Agents tab badge: Agents with the chip running.
  const agentsBadge = page.getByRole('navigation', { name: 'Pages' }).getByRole('link', { name: /agents running/ })
  expect((await hrefOf(agentsBadge)).pathname + (await hrefOf(agentsBadge)).search).toBe('/cockpit/agents?chips=running')
  // The cleanup counts: Worktrees with the chips safe and look.
  const cleanup = page.getByRole('region', { name: /^Cleanup/ })
  const counts = cleanup.locator('.cleanup-text a')
  await expect(counts).toHaveCount(2)
  expect((await hrefOf(counts.nth(0))).search).toBe('?chips=safe')
  expect((await hrefOf(counts.nth(1))).search).toBe('?chips=look')
  expect((await hrefOf(counts.nth(0))).pathname).toBe('/cockpit/worktrees')
  // The age bars of the cleanup chart: each is a button in the chart's table that opens Worktrees with its age term.
  await cleanup.getByRole('button', { name: 'Ages' }).click()
  const bars = cleanup.locator('app-chart .data button')
  await expect(bars).toHaveCount(5)
  const terms: string[] = []
  for (let index = 0; index < 5; index++) {
    // The table is for assistive technology and clipped from view: press its button as a screen reader's user does.
    await bars.nth(index).dispatchEvent('click')
    await expect(page).toHaveURL(/\/cockpit\/worktrees\?q=age:/)
    terms.push(decodeURIComponent(new URL(page.url()).searchParams.get('q') ?? ''))
    await page.goBack()
    await expect(page.getByRole('heading', { level: 2, name: 'Cleanup' })).toBeVisible()
    if (index < 4 && (await bars.count()) === 0) await cleanup.getByRole('button', { name: 'Ages' }).click()
  }
  expect(terms).toEqual(['age:<1d', 'age:1-7d', 'age:8-30d', 'age:31-90d', 'age:>90d'])
  // A stale or outdated machine in Fleet health: Machines with the chip.
  const health = page.getByRole('region', { name: /^Fleet health/ })
  expect((await hrefOf(health.getByRole('link', { name: /has not published/ }))).search).toBe('?chips=stale')
  expect((await hrefOf(health.getByRole('link', { name: /older WB/ }))).search).toBe('?chips=outdated')

  // The throughput numbers are declared non-linking: no link in the charts, and the reason in the title of each.
  await page.getByRole('heading', { level: 2, name: 'Throughput' }).scrollIntoViewIfNeeded()
  const charts = page.getByRole('region', { name: /^Throughput/ })
  await expect(charts.locator('.chart-card')).toHaveCount(2)
  await expect(charts.locator('a')).toHaveCount(0)
  for (const card of await charts.locator('.chart-card').all()) await expect(card).toHaveAttribute('title', /^Not a link: .*no list can reproduce them$/)
  await expectClean()
})

test('Tasks, Repositories and Machines: the counts are links with only vocabulary terms, and the branch counts and checks over total say why they are not', async ({ page }) => {
  await stubBusy(page, 'anonymous-local', withCounts)
  const expectClean = await watch(page)
  const first = () => page.locator('[role=row][data-index]').first()

  await page.goto('/cockpit/tasks')
  // The default order is by state, so the first row may have no pull request: take the first that has one.
  const withPr = page.locator('[role=row][data-index]').filter({ has: page.locator('app-task-pr-cell app-state-badge [title*="Not a link"]') }).first()
  const taskName = ((await withPr.locator('a.name').textContent()) ?? '').trim()
  const worktrees = withPr.locator('a[href*="/cockpit/worktrees?"]')
  expect(decodeURIComponent((await hrefOf(worktrees)).search)).toBe(`?q=task:"${taskName}"`)
  // A pull request's checks over total is a fact of one pull request: not a link, with its reason.
  const checks = withPr.locator('app-task-pr-cell app-state-badge [title*="Not a link"]').first()
  await expect(checks).toContainText(/\d+\/\d+/)

  await page.goto('/cockpit/repositories')
  const repository = ((await first().locator('span').filter({ hasText: /^[\w.-]+\/[\w.-]+$/ }).first().textContent()) ?? '').trim()
  const query = (locator: ReturnType<typeof first>) => hrefOf(locator).then((url) => `${url.pathname.replace('/cockpit', '')}${decodeURIComponent(url.search)}`)
  expect(await query(first().locator('a[href^="/cockpit/worktrees?"]'))).toBe(`/worktrees?q=repo:"${repository}"`)
  expect(await query(first().locator('a[href^="/cockpit/agents?"]'))).toBe(`/agents?q=repo:"${repository}"`)
  expect(await query(first().locator('a[href^="/cockpit/tasks?"]'))).toBe(`/tasks?q=repo:"${repository}"&chips=pr`)
  // The branch counts: no branches list page, so plain text with the reason in the title.
  const branches = first().locator('[title^="5 local, 7 remote"]')
  await expect(branches).toHaveAttribute('title', '5 local, 7 remote. Not a link: there is no branches list page')
  await expect(first().locator('a', { hasText: '5 / 7' })).toHaveCount(0)

  await page.goto('/cockpit/machines')
  const row = first().locator('a[href*="machine=mach-mac"]')
  await expect(row).toHaveCount(3)
  expect(await row.evaluateAll((links) => links.map((link) => link.getAttribute('href')?.replace('/cockpit', '')))).toEqual(['/repositories?machine=mach-mac', '/worktrees?machine=mach-mac', '/agents?machine=mach-mac'])
  await expectClean()
})

test('every route still loads after the counts became links', async ({ page }) => {
  await stubBusy(page, 'anonymous-local', withCounts)
  for (const route of BUSY_ROUTES) {
    await page.goto(`/cockpit/${route.path}`)
    await expect(page.getByRole('heading', { level: 1 })).not.toHaveText('')
  }
})
