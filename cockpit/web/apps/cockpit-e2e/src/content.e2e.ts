import { expect, test, type Page } from '@playwright/test'
import { otherConsoleErrors, unexplainedViolations, type Violation } from './violations'

// Repository content and the code-index panel, in the built application served
// under the daemon's content security policy, against a stubbed API. Every test
// ends by checking that no policy violation and no console error occurred,
// beyond the PrimeUI licence banner this build is known to show.

const now = new Date().toISOString()
const alpha = { machine: 'alpha', machine_id: 'mach-alpha', route: 'local', observed_at: now }

const stats = (files: number) => ({
  indexed: true,
  files,
  symbols: 40,
  edges: 90,
  kinds: [
    { kind: 'function', count: 30 },
    { kind: 'struct', count: 10 },
  ],
})
const notIndexed = { indexed: false, files: 0, symbols: 0, edges: 0, kinds: [] }

function fleet(files: number, provider: string | null = 'codegrapher') {
  // A daemon with no provider configured reports no statistics at all.
  const withStats = (statistics: object) => (provider ? { statistics } : {})
  return {
    schema_version: 2,
    snapshot_at: now,
    warming_up: false,
    repositories_total: 1,
    repositories_scanned: 1,
    diagnostics: 0,
    ...(provider ? { code_index_provider: provider } : {}),
    machines: [{ id: 'mach-alpha', ...alpha, repository_count: 1, worktree_count: 2 }],
    repositories: [
      {
        id: 'repo-cli',
        ...alpha,
        host: 'github.com',
        name: 'specscore/specscore-cli',
        default_branch: 'main',
        worktree_count: 2,
        active_agent_count: 0,
        code_index: [{ indexer: 'codegrapher', state: 'fresh', receipt_at: now, ...withStats(stats(files)) }],
      },
    ],
    worktrees: [
      { id: 'wt-indexed', ...alpha, repository: 'repo-cli', task: 'add-search', branch: 'task/add-search', code_index: [{ indexer: 'codegrapher', state: 'fresh', receipt_at: now, ...withStats(stats(files)) }] },
      { id: 'wt-bare', ...alpha, repository: 'repo-cli', task: 'fix-index', branch: 'task/fix-index', code_index: [{ indexer: 'codegrapher', state: 'never', ...withStats(notIndexed) }] },
    ],
    pull_requests: [],
    agents: [],
  }
}

const OWNER = { principal: 'owner', capabilities: ['fleet.read', 'repo.content.read'], code_browser_url: 'https://codegrapher.dev/' }
const ANONYMOUS = { principal: 'anonymous-local', capabilities: ['fleet.read'], code_browser_url: 'https://codegrapher.dev/' }

/** Stubs the API and records every request the page makes, and every call of the README route. */
async function stub(page: Page, options: { session: typeof OWNER; document?: () => unknown; readme?: { status?: number; body: string; json?: boolean } }) {
  const requests: string[] = []
  const readmeCalls: string[] = []
  page.on('request', (request) => requests.push(request.url()))
  await page.route('**/api/v1/cockpit/fleet', (route) =>
    route.fulfill({ json: (options.document ?? (() => fleet(12)))(), headers: { ETag: `"${Math.random()}"`, 'Cache-Control': 'no-cache' } }),
  )
  await page.route('**/api/v1/cockpit/session', (route) => route.fulfill({ json: options.session }))
  await page.route('**/api/v1/cockpit/readme?*', (route) => {
    readmeCalls.push(route.request().url())
    const readme = options.readme ?? { body: '' }
    return route.fulfill({ status: readme.status ?? 200, body: readme.body, contentType: readme.json ? 'application/json' : 'text/markdown; charset=utf-8' })
  })
  return { requests, readmeCalls }
}

/**
 * Records console errors, page errors, dialogs and policy violations; call the
 * result last. `expectedStatus` names a status the test provokes on purpose: the
 * browser logs a failed request as a console error, which is not one then.
 */
async function watch(page: Page, expectedStatus?: number) {
  const consoleErrors: string[] = []
  const dialogs: string[] = []
  page.on('console', (message) => {
    if (message.type() === 'error') consoleErrors.push(message.text())
  })
  page.on('pageerror', (error) => consoleErrors.push(`page error: ${error.message}`))
  page.on('dialog', (dialog) => {
    dialogs.push(dialog.message())
    void dialog.dismiss()
  })
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
    expect(otherConsoleErrors(consoleErrors).filter((message) => !(expectedStatus && message.includes(`status of ${expectedStatus}`)))).toEqual([])
    expect(dialogs).toEqual([])
  }
}

// cockpit#ac:readme-needs-owner
test('the README renders for an owner session, and without one the page asks for it and never calls the route', async ({ page }) => {
  const expectClean = await watch(page)
  const owner = await stub(page, { session: OWNER, readme: { body: '# Widgets\n\nA **small** library.\n' } })
  await page.goto('/cockpit/repositories/repo-cli')
  await expect(page.getByRole('heading', { name: 'github.com/specscore/specscore-cli' })).toBeVisible()
  await expect(page.locator('.readme-content h4')).toHaveText('Widgets')
  await expect(page.locator('.readme-content strong')).toHaveText('small')
  expect(owner.readmeCalls).toHaveLength(1)
  expect(owner.readmeCalls[0]).toContain('repository=repo-cli')

  await page.unrouteAll()
  const anonymous = await stub(page, { session: ANONYMOUS, readme: { body: '# Never shown\n' } })
  await page.goto('/cockpit/repositories/repo-cli')
  const section = page.locator('app-readme-section')
  await expect(section).toContainText('An owner session is needed to read the README')
  await expect(section.locator('code')).toHaveText('wb cockpit')
  await expect(page.locator('.readme-content')).toHaveCount(0)
  expect(anonymous.readmeCalls).toEqual([])
  // The lists still load without a session.
  await expect(page.getByRole('heading', { name: 'github.com/specscore/specscore-cli' })).toBeVisible()
  await expectClean()
})

// cockpit#ac:hostile-readme-is-inert
test('a hostile README shows as text: no script runs, the injected content makes no request and no policy violation is needed', async ({ page }) => {
  const hostile = [
    '# Hostile',
    '',
    '<script>window.__pwned = "script"; alert("script")</script>',
    '',
    'Before <img src="https://evil.example/pixel.png" onerror="window.__pwned = \'onerror\'; alert(\'onerror\')"> after.',
    '',
    '[click me](javascript:window.__pwned=\'link\';alert(\'link\'))',
    '',
    '![tracker](https://evil.example/tracker.png)',
    '',
    '<a href="javascript:alert(1)" onclick="alert(2)" style="position:fixed;inset:0">cover</a>',
    '',
    '<iframe src="https://evil.example/frame"></iframe><form action="https://evil.example/post"><input name="x"></form>',
    '',
    '[docs](docs/guide.md) [ok](https://example.com/page)',
  ].join('\n')
  const expectClean = await watch(page)
  const evil: string[] = []
  await page.route('**/evil.example/**', (route) => {
    evil.push(route.request().url())
    return route.abort()
  })
  const { requests } = await stub(page, { session: OWNER, readme: { body: hostile } })
  const response = await page.goto('/cockpit/repositories/repo-cli')
  const policy = (await response!.allHeaders())['content-security-policy']
  expect(policy).toContain("script-src 'self';")
  expect(policy).not.toContain('unsafe-')
  expect(policy).toContain("img-src 'self';")

  const readme = page.locator('.readme-content')
  await expect(readme.locator('h4')).toHaveText('Hostile')
  await expect(readme).toContainText('<script>window.__pwned')
  await expect(readme).toContainText('onerror=')

  // Nothing the README named is an element, an attribute or a loading resource.
  await expect(readme.locator('script, img, iframe, form, input, style, object, embed, svg, base, meta, link')).toHaveCount(0)
  expect(await readme.locator('*').evaluateAll((elements) => elements.flatMap((element) => element.getAttributeNames().filter((name) => name === 'style' || name.startsWith('on'))))).toEqual([])
  const hrefs = await readme.locator('a').evaluateAll((links) => links.map((link) => [link.getAttribute('href'), link.getAttribute('rel')]))
  expect(hrefs).toEqual([
    ['https://evil.example/tracker.png', 'noopener noreferrer'],
    ['https://example.com/page', 'noopener noreferrer'],
  ])
  expect(await page.evaluate(() => (window as unknown as { __pwned?: string }).__pwned)).toBeUndefined()

  // The injected content made no request: the page only ever spoke to its own origin.
  expect(evil).toEqual([])
  const origin = new URL(page.url()).origin
  expect(requests.filter((url) => new URL(url).origin !== origin)).toEqual([])
  // No policy violation was needed to achieve that: the renderer never asked.
  await expectClean()
})

// cockpit#ac:hostile-readme-is-inert, the second page: a README committed as a symbolic link.
test('a README committed as a symbolic link shows no content from outside the checkout', async ({ page }) => {
  const expectClean = await watch(page, 403)
  await stub(page, { session: OWNER, readme: { status: 403, json: true, body: JSON.stringify({ error: 'readme_not_a_regular_file', detail: 'SECRET-OUTSIDE-THE-CHECKOUT' }) } })
  await page.goto('/cockpit/repositories/repo-cli')
  await expect(page.locator('app-readme-section')).toContainText('not a regular file')
  await expect(page.locator('.readme-content')).toHaveCount(0)
  // The marker sits in the refusal's own body, where a renderer that showed the response would show it.
  await expect(page.locator('body')).not.toContainText('SECRET-OUTSIDE-THE-CHECKOUT')
  await expectClean()
})

// cockpit#ac:code-index-panel
test('the code-index panel shows the totals and the kinds, follows a new receipt, says not indexed, and says when no provider is configured', async ({ page }) => {
  let files = 12
  const expectClean = await watch(page)
  const { readmeCalls } = await stub(page, { session: ANONYMOUS, document: () => fleet(files) })

  // Opened twice with no owner session: the same numbers, and no content route asked.
  for (let visit = 0; visit < 2; visit++) {
    await page.goto('/cockpit/worktrees/wt-indexed')
    const panel = page.locator('app-code-index-panel')
    await expect(panel.locator('.totals div')).toHaveText(['Files12', 'Symbols40', 'Edges90'])
    await expect(panel.locator('.kinds li')).toHaveText(['function30', 'struct10'])
    await expect(panel.locator('app-count')).toHaveCount(0)
  }

  // A new receipt, the provider now reporting 13 files, and a refresh.
  files = 13
  await page.goto('/cockpit/worktrees/wt-indexed')
  await expect(page.locator('app-code-index-panel .totals div').first()).toHaveText('Files13')

  // The other checkout has no index.
  await page.goto('/cockpit/worktrees/wt-bare')
  await expect(page.locator('app-code-index-panel')).toContainText('Not indexed.')

  // The repository page carries the same panel.
  await page.goto('/cockpit/repositories/repo-cli')
  await expect(page.locator('app-code-index-panel .totals div')).toHaveText(['Files13', 'Symbols40', 'Edges90'])
  expect(readmeCalls).toEqual([])

  // A second daemon with no provider configured.
  await page.unrouteAll()
  await stub(page, { session: ANONYMOUS, document: () => fleet(13, null) })
  await page.goto('/cockpit/worktrees/wt-indexed')
  await expect(page.locator('app-code-index-panel')).toContainText('No code-index provider is configured.')
  await expect(page.locator('app-code-index-panel .totals')).toHaveCount(0)
  await expectClean()
})

test('a row opens its detail page, which links back to the list', async ({ page }) => {
  const expectClean = await watch(page)
  await stub(page, { session: ANONYMOUS })
  await page.goto('/cockpit/repositories')
  await page.getByRole('link', { name: 'github.com/specscore/specscore-cli' }).first().click()
  await expect(page).toHaveURL(/\/cockpit\/repositories\/repo-cli$/)
  await expect(page.locator('dl.detail')).toContainText('main')
  await page.getByRole('link', { name: /Repositories/ }).first().click()
  await expect(page).toHaveURL(/\/cockpit\/repositories$/)
  await page.goto('/cockpit/worktrees')
  await page.getByRole('link', { name: 'Open add-search', exact: true }).click()
  await expect(page).toHaveURL(/\/cockpit\/worktrees\/wt-indexed$/)
  await expect(page.getByRole('heading', { name: 'add-search' })).toBeVisible()
  await expectClean()
})
