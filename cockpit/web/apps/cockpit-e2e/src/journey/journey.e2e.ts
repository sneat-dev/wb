import { execFileSync, spawnSync } from 'node:child_process'
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, realpathSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import { assertInside, assertSafePort, freePort, isolatedEnv, portInUse, portInUseSync, recordedLivePid } from './isolation'
import { otherConsoleErrors, unexplainedViolations, type Violation } from '../violations'

// cockpit#ac:whole-journey-e2e. The whole chain with no stub: the production web
// build embedded in a wb binary built here, a daemon that `wb cockpit` itself
// starts, a real repository with two WB worktrees, a real browser.
//
// The daemon is isolated from the machine: a free port chosen now, a projects
// root, a unix socket, a state directory, a config and a home that are all
// under one temporary directory, and an environment built from nothing (see
// isolation.ts). The default daemon port, 8766, is never used.

// The script runs from cockpit/web (pnpm test:journey), two directories below the repository root.
const repoRoot = resolve(process.cwd(), '../..')
const SECRET = 'JOURNEY-SECRET-OUTSIDE-THE-CHECKOUT'
const README_BODY = 'Journey fixture repository, committed as a real README.'

let tmp = ''
let wb = ''
let projects = ''
let port = 0
let env: Record<string, string> = {}
let loginUrl = ''
let daemonStarted = false

function wbRun(...args: string[]): string {
  return execFileSync(wb, ['--projects-root', projects, ...args], { env, encoding: 'utf8', cwd: tmp, timeout: 120_000 })
}

function git(cwd: string, ...args: string[]): void {
  execFileSync('git', args, { cwd, env, stdio: 'pipe' })
}

/** Stops the daemon and removes the temporary directory. Synchronous, idempotent, safe in an exit handler. */
function cleanup(): void {
  if (daemonStarted && wb) {
    spawnSync(wb, ['--projects-root', projects, 'daemon', 'stop'], { env, timeout: 60_000 })
    daemonStarted = false
  }
  if (tmp) {
    // A daemon that ignored `stop` must not outlive the test. Its pid is killed only when its
    // record under the temporary root still says it is running and its port is still busy:
    // a stopped daemon's pid may belong to an unrelated process by now.
    const state = join(projects, '.wb', 'runtime', 'daemon-state.json')
    if (existsSync(state) && port > 0) {
      try {
        const pid = recordedLivePid(readFileSync(state, 'utf8'), portInUseSync(port))
        if (pid) process.kill(pid, 'SIGKILL')
      } catch {
        // already gone
      }
    }
    // Keep the daemon's log for a red run: the temporary directory is about to go.
    const log = join(projects, '.wb', 'runtime', 'daemon.log')
    if (existsSync(log)) {
      mkdirSync(join(process.cwd(), 'test-results'), { recursive: true })
      copyFileSync(log, join(process.cwd(), 'test-results', 'journey-daemon.log'))
    }
    rmSync(tmp, { recursive: true, force: true })
    tmp = ''
  }
}

function createFixture(): void {
  mkdirSync(join(tmp, 'home'), { recursive: true })
  mkdirSync(join(tmp, 'tmp'), { recursive: true })
  writeFileSync(join(tmp, 'outside-secret.txt'), `${SECRET}\n`)
  writeFileSync(join(tmp, 'prompt.txt'), 'journey fixture\n')
  const origins = join(tmp, 'origins')
  for (const [name, seed] of [
    ['shop', (dir: string) => writeFileSync(join(dir, 'README.md'), `# Shop\n\n${README_BODY}\n`)],
    ['links', (dir: string) => symlinkSync(join(tmp, 'outside-secret.txt'), join(dir, 'README.md'))],
  ] as const) {
    const bare = join(origins, `${name}.git`)
    const clone = join(projects, 'github.com', 'acme', name)
    mkdirSync(join(clone, '..'), { recursive: true })
    mkdirSync(origins, { recursive: true })
    git(tmp, 'init', '--bare', '-b', 'main', bare)
    git(tmp, 'clone', bare, clone)
    git(clone, 'checkout', '-B', 'main')
    seed(clone)
    git(clone, 'add', 'README.md')
    git(clone, 'commit', '-m', 'Add README')
    git(clone, 'push', 'origin', 'main')
  }
  for (const task of ['journey-one', 'journey-two']) {
    wbRun('create', task, 'acme/shop', '--model', 'unknown', '--original-prompt-file', join(tmp, 'prompt.txt'),
      '--agent', 'journey', '--agent-runtime', 'fixture', '--initiator', 'journey', '--mode', 'manual', '--no-claim', '--non-interactive')
  }
}

/** Records console errors and policy violations; call the result last. */
async function watch(page: Page, expectedStatus?: number) {
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
    // The browser logs a failed request as a console error; the symlink README's refusal is the one provoked on purpose.
    expect(otherConsoleErrors(consoleErrors).filter((message) => !(expectedStatus && message.includes(`status of ${expectedStatus}`)))).toEqual([])
  }
}

test.describe.configure({ mode: 'serial' })

test.beforeAll(() => {
  test.setTimeout(600_000)
  // First, before anything is built or created (pnpm test:journey also runs this guard before its build).
  execFileSync(process.execPath, [join(process.cwd(), 'tools/journey-guard.mjs')], { stdio: 'inherit' })
  // The temporary directory is under /tmp, not os.tmpdir(): a unix socket path must stay under 104 bytes on macOS.
  tmp = realpathSync(mkdtempSync('/tmp/wbj-'))
  for (const signal of ['SIGINT', 'SIGTERM'] as const) {
    process.once(signal, () => {
      cleanup()
      process.exit(1)
    })
  }
  process.once('exit', cleanup)
  projects = join(tmp, 'projects')
  env = isolatedEnv(tmp, process.env)
  expect(existsSync(join(repoRoot, 'cockpit/web/dist/index.html')), 'run the production web build first (pnpm build)').toBe(true)
  wb = join(tmp, 'bin', 'wb')
  // The binary embeds cockpit/web/dist, exactly as the release build does.
  execFileSync('go', ['build', '-o', wb, './cmd/wb'], { cwd: repoRoot, stdio: 'pipe', timeout: 400_000 })
  createFixture()
})

test.afterAll(() => cleanup())

test('wb cockpit starts its own daemon and the whole journey works on the real chain', async ({ page, playwright }) => {
  port = await freePort()
  assertSafePort(port)
  for (const path of [projects, env['HOME'] ?? '', env['XDG_CONFIG_HOME'] ?? '', env['XDG_STATE_HOME'] ?? '']) assertInside(path, tmp)
  expect(env['WB_HOME']).toBeUndefined()
  expect(await portInUse(port)).toBe(false)

  // No daemon is running for this projects root: wb cockpit starts it.
  expect(existsSync(join(projects, '.wb', 'runtime', 'daemon-state.json'))).toBe(false)
  daemonStarted = true
  const printed = wbRun('cockpit', '--listen', `127.0.0.1:${port}`)
  const match = /^cockpit: (http:\/\/127\.0\.0\.1:\d+\/cockpit\/session\/login\?code=\S+)$/m.exec(printed)
  expect(match, printed).not.toBeNull()
  loginUrl = match![1]!
  const origin = new URL(loginUrl).origin
  expect(origin).toBe(`http://127.0.0.1:${port}`)
  // The unix socket this daemon serves is inside the temporary directory.
  const runtime = join(projects, '.wb', 'runtime')
  const sockets = readdirSync(runtime).filter((name) => statSync(join(runtime, name)).isSocket())
  expect(sockets).toHaveLength(1)
  assertInside(join(runtime, sockets[0]!), tmp)

  const expectClean = await watch(page, 403)
  const failures: string[] = []
  page.on('response', (response) => {
    if (response.status() >= 400 && response.url().startsWith(origin) && !response.url().includes('/readme')) failures.push(`${response.status()} ${response.url()}`)
  })

  // Following the printed URL signs in as owner and lands on Home.
  await page.goto(loginUrl)
  await expect(page).toHaveURL(new RegExp(`^${origin.replace(/[.]/g, '\\.')}/cockpit/(dashboard)?$`))
  expect(page.url()).not.toContain('code=')
  await page.goto(`${origin}/cockpit/dashboard`)
  // The old address of Home still works and shows it.
  await expect(page.getByRole('heading', { level: 1 })).toHaveText('Home')
  const session = await (await page.request.get(`${origin}/api/v1/cockpit/session`)).json()
  expect(session.principal).toBe('owner')

  // Hover the worktree count: the card names both worktrees.
  const tile = page.locator('.tile').filter({ hasText: 'Worktrees' }).first()
  const count = tile.locator('app-count')
  await count.locator('a').hover()
  const card = count.locator('.count-card')
  await expect(card).toBeVisible()
  await expect(card.locator('li')).toHaveCount(2)
  await expect(card).toContainText('journey-one')
  await expect(card).toContainText('journey-two')

  // Click it: the Worktrees table shows exactly those two rows.
  await count.locator('a').click()
  await expect(page).toHaveURL(/\/cockpit\/worktrees(\?|$)/)
  const rows = page.locator('tbody tr')
  await expect(rows).toHaveCount(2)
  await expect(rows.filter({ hasText: 'journey-one' })).toHaveCount(1)
  await expect(rows.filter({ hasText: 'journey-two' })).toHaveCount(1)

  // The repository page shows the real README.
  await page.goto(`${origin}/cockpit/repositories`)
  await page.locator('tbody tr').filter({ hasText: 'acme/shop' }).getByRole('link', { name: /acme\/shop/ }).first().click()
  await expect(page.getByRole('heading', { name: 'github.com/acme/shop' })).toBeVisible()
  await expect(page.locator('.readme-content')).toContainText(README_BODY)
  await expect(page.locator('.readme-content h4')).toHaveText('Shop')

  // A README committed as a symbolic link shows nothing from outside the checkout.
  await page.goto(`${origin}/cockpit/repositories`)
  await page.locator('tbody tr').filter({ hasText: 'acme/links' }).getByRole('link', { name: /acme\/links/ }).first().click()
  await expect(page.getByRole('heading', { name: 'github.com/acme/links' })).toBeVisible()
  await expect(page.locator('app-readme-section')).toContainText(/not a regular file|could not/i)
  await expect(page.locator('.readme-content')).toHaveCount(0)
  await expect(page.locator('body')).not.toContainText(SECRET)
  const linksId = new URL(page.url()).pathname.split('/').pop()!
  const linksReadme = await page.request.get(`${origin}/api/v1/cockpit/readme?repository=${linksId}`)
  expect(linksReadme.status()).toBeGreaterThanOrEqual(400)
  expect(await linksReadme.text()).not.toContain(SECRET)

  // The fleet document's collections are lists, never null.
  const fleet = await (await page.request.get(`${origin}/api/v1/cockpit/fleet`)).json()
  for (const name of ['machines', 'repositories', 'worktrees', 'branches', 'pull_requests', 'agents']) {
    expect(Array.isArray(fleet[name]), `${name} is a list`).toBe(true)
  }
  expect(fleet.worktrees).toHaveLength(2)

  // The README route refuses a request that carries no cookie.
  const shopId = fleet.repositories.find((repository: { name: string }) => repository.name === 'acme/shop').id
  const anonymousApi = await playwright.request.newContext()
  const refused = await anonymousApi.get(`${origin}/api/v1/cockpit/readme?repository=${shopId}`)
  expect(refused.status()).toBeGreaterThanOrEqual(401)
  expect(refused.status()).toBeLessThan(404)
  expect(await refused.text()).not.toContain(README_BODY)
  await anonymousApi.dispose()

  // Clear the cookie and reload: the lists still load, the README asks for an owner session.
  await page.goto(`${origin}/cockpit/repositories/${shopId}`)
  await expect(page.locator('.readme-content')).toContainText(README_BODY)
  await page.context().clearCookies()
  await page.reload()
  await expect(page.getByRole('heading', { name: 'github.com/acme/shop' })).toBeVisible()
  const section = page.locator('app-readme-section')
  await expect(section).toContainText('An owner session is needed to read the README')
  await expect(section.locator('code')).toHaveText('wb cockpit')
  await expect(page.locator('.readme-content')).toHaveCount(0)
  await page.getByRole('navigation', { name: 'Pages' }).getByRole('link', { name: 'Worktrees' }).click()
  await expect(rows).toHaveCount(2)
  const anonymousSession = await (await page.request.get(`${origin}/api/v1/cockpit/session`)).json()
  expect(anonymousSession.principal).not.toBe('owner')

  await expectClean()
  expect(failures).toEqual([])

  // Stop the daemon now and prove nothing is left listening.
  cleanup()
  expect(await portInUse(port)).toBe(false)
})
