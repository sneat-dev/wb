import { execFileSync, spawnSync } from 'node:child_process'
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, realpathSync, rmSync, statSync, symlinkSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { expect, test, type Page } from '@playwright/test'
import { assertInside, assertSafePort, freePort, isolatedEnv, portInUse, portInUseSync, recordedLivePid } from './isolation'
import { otherConsoleErrors, unexplainedViolations, type Violation } from '../violations'
import { countOpensItsList, everyTabListsRows, filterChipPanelDetailBack, homeLoads, machineMetrics, newTaskProducesCommands, paletteOpensATask } from './steps'

// cockpit#ac:whole-journey-e2e. The whole chain with no stub: the production web
// build embedded in a wb binary built here, a daemon that `wb cockpit` itself
// starts, two real repositories with two WB worktrees of two tasks, a real
// browser. The steps over the pages (Home and its sections, the five tabs, a
// list's filter, chip, panel and detail route, the palette, New task, the local
// machine's metrics) are in steps.ts, which the stubbed suite runs too
// (../journey-steps.e2e.ts) against answers shaped like this daemon's.
//
// It runs only on Linux under GitHub Actions (journey-guard.mjs refuses to run
// anywhere else): the daemon is one fixed launchd label per user on macOS, so
// starting one there would stop the developer's live service.
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
  // stdout is a pipe here, so the login URL, which is a credential, is printed only because it is asked for.
  const printed = wbRun('cockpit', '--print-url', '--listen', `127.0.0.1:${port}`)
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
  // The printed URL carried the session key in its fragment (cockpit#req:session-key): the page took it out of the
  // address and keeps it in the storage of its own origin, and it is what makes the cookie an owner's.
  expect(new URL(loginUrl).hash).toMatch(/^#key=[A-Za-z0-9_-]{43}$/)
  expect(page.url()).not.toContain('#')
  await expect.poll(() => page.evaluate(() => window.localStorage.getItem('wb-cockpit.session-key'))).toBe(new URL(loginUrl).hash.slice('#key='.length))
  const sessionKey = { 'X-Wb-Cockpit-Session-Key': new URL(loginUrl).hash.slice('#key='.length) }
  const session = await (await page.request.get(`${origin}/api/v1/cockpit/session`, { headers: sessionKey })).json()
  expect(session.principal).toBe('owner')
  // The cookie alone, which any other server on the loopback host is sent, is an anonymous reader's: no error, and nothing of the owner's.
  const replayed = await page.request.get(`${origin}/api/v1/cockpit/session`)
  expect(replayed.status()).toBe(200)
  expect((await replayed.json()).principal).toBe('anonymous-local')
  expect((await page.request.get(`${origin}/api/v1/log?tail=1`)).status()).toBe(401)
  expect((await page.request.get(`${origin}/api/v1/log?tail=1`, { headers: sessionKey })).status()).not.toBe(401)

  // The fleet document of the real daemon: collections are lists, never null, and the fixture is what it says.
  const fleet = await (await page.request.get(`${origin}/api/v1/cockpit/fleet`)).json()
  for (const name of ['machines', 'repositories', 'worktrees', 'pull_requests', 'agents']) {
    expect(Array.isArray(fleet[name]), `${name} is a list`).toBe(true)
  }
  expect(fleet.schema_version).toBe(2)
  expect(fleet.worktrees.map((worktree: { task: string }) => worktree.task).sort()).toEqual(['journey-one', 'journey-two'])
  expect(fleet.repositories.map((repository: { name: string }) => repository.name).sort()).toEqual(['acme/links', 'acme/shop'])
  expect(fleet.machines.filter((machine: { route: string }) => machine.route === 'local')).toHaveLength(1)

  // The old address of Home still works and shows it, with its sections.
  await page.goto(`${origin}/cockpit/dashboard`)
  await homeLoads(page, origin, fleet)
  // Every tab lists the rows of the fleet.
  await everyTabListsRows(page, fleet)
  // A filter, a chip, the panel, the detail route and Back.
  await filterChipPanelDetailBack(page, origin, fleet)
  // The palette finds a task and opens it; New task produces its commands and runs nothing.
  await paletteOpensATask(page, origin, 'journey-one')
  await newTaskProducesCommands(page, origin, ['acme/links', 'acme/shop'], 'acme/*')
  // The local machine: its history as charts on a platform that reports metrics (Linux does, through /proc), or "not reported".
  const local = fleet.machines.find((machine: { route: string }) => machine.route === 'local')
  const metrics = await (await page.request.get(`${origin}/api/v1/cockpit/machine-metrics?machine=${encodeURIComponent(local.id)}`)).json()
  await machineMetrics(page, origin, fleet, metrics)

  // A count is a link: the local machine's Worktrees count opens exactly its two worktrees.
  await countOpensItsList(page, origin, fleet)
  const rows = page.locator('[role=row][data-index]')

  // The repository's page shows the real README.
  await page.goto(`${origin}/cockpit/repositories`)
  await page.getByRole('link', { name: 'Open acme/shop', exact: true }).click()
  await expect(page.getByRole('heading', { level: 2, name: /acme\/shop$/ })).toBeVisible()
  await expect(page.locator('.readme-content')).toContainText(README_BODY)
  await expect(page.locator('.readme-content h4')).toHaveText('Shop')

  // A README committed as a symbolic link shows nothing from outside the checkout.
  await page.goto(`${origin}/cockpit/repositories`)
  await page.getByRole('link', { name: 'Open acme/links', exact: true }).click()
  await expect(page.getByRole('heading', { level: 2, name: /acme\/links$/ })).toBeVisible()
  await expect(page.locator('app-readme-section')).toContainText(/not a regular file|could not/i)
  await expect(page.locator('.readme-content')).toHaveCount(0)
  await expect(page.locator('body')).not.toContainText(SECRET)
  const linksId = fleet.repositories.find((repository: { name: string }) => repository.name === 'acme/links').id
  const linksReadme = await page.request.get(`${origin}/api/v1/cockpit/readme?repository=${linksId}`, { headers: sessionKey })
  expect(linksReadme.status()).toBeGreaterThanOrEqual(400)
  expect(await linksReadme.text()).not.toContain(SECRET)

  // The README route refuses a request that carries no cookie.
  const shopId = fleet.repositories.find((repository: { name: string }) => repository.name === 'acme/shop').id
  const anonymousApi = await playwright.request.newContext()
  const refused = await anonymousApi.get(`${origin}/api/v1/cockpit/readme?repository=${shopId}`)
  expect(refused.status()).toBeGreaterThanOrEqual(401)
  expect(refused.status()).toBeLessThan(404)
  expect(await refused.text()).not.toContain(README_BODY)
  await anonymousApi.dispose()

  // Clear the cookie and reload: the lists still load, the README asks for an owner session, and no page shows an SSH route.
  await page.goto(`${origin}/cockpit/repositories?sel=${encodeURIComponent(shopId)}`)
  await expect(page.locator('.readme-content')).toContainText(README_BODY)
  await page.context().clearCookies()
  await page.reload()
  await expect(page.getByRole('heading', { level: 2, name: /acme\/shop$/ })).toBeVisible()
  const section = page.locator('app-readme-section')
  await expect(section).toContainText('An owner session is needed to read the README')
  await expect(section.locator('code')).toHaveText('wb cockpit')
  await expect(page.locator('.readme-content')).toHaveCount(0)
  await page.getByRole('navigation', { name: 'Pages' }).getByRole('link', { name: /^Worktrees/ }).click()
  await expect(rows).toHaveCount(2)
  const anonymousSession = await (await page.request.get(`${origin}/api/v1/cockpit/session`)).json()
  expect(anonymousSession.principal).not.toBe('owner')
  expect(anonymousSession.machine_routes).toBeUndefined()

  await expectClean()
  expect(failures).toEqual([])

  // Stop the daemon now and prove nothing is left listening.
  cleanup()
  expect(await portInUse(port)).toBe(false)
})
