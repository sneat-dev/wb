import { expect, test, type Response } from '@playwright/test'
import { otherConsoleErrors, unexplainedViolations, type Violation } from './violations'

test('the built shell loads under /cockpit/ with no console errors and no CSP violations', async ({ page }) => {
  const consoleErrors: string[] = []
  page.on('console', (message) => {
    if (message.type() === 'error') consoleErrors.push(message.text())
  })
  page.on('pageerror', (error) => consoleErrors.push(`page error: ${error.message}`))
  // The shell reads the daemon's API; this static server has none, so stub it.
  await page.route('**/api/v1/cockpit/fleet', (route) => route.fulfill({ json: { schema_version: 2, warming_up: false, repositories_total: 0, repositories_scanned: 0, diagnostics: 0, machines: [], repositories: [], worktrees: [], pull_requests: [], agents: [] } }))
  await page.route('**/api/v1/cockpit/session', (route) => route.fulfill({ json: { principal: 'anonymous-local', capabilities: [], code_browser_url: 'https://codegrapher.dev/' } }))
  await page.addInitScript(() => {
    const store = window as unknown as { __violations: unknown[] }
    store.__violations = []
    document.addEventListener('securitypolicyviolation', (event) => {
      // The licence banner lives in a closed shadow root, so the event is
      // retargeted to its host, which is in the composed path.
      const inLicenseBanner = event
        .composedPath()
        .some((node) => (node as Element).id === 'p-license-host')
      store.__violations.push({
        directive: event.violatedDirective,
        blockedURI: event.blockedURI,
        inLicenseBanner,
      })
    })
  })

  const response = await page.goto('/cockpit/')
  const headers = await (response as Response).allHeaders()
  const policy = headers['content-security-policy']
  expect(policy).toContain("script-src 'self';")
  expect(policy).not.toContain('unsafe-')

  await expect(page.getByRole('heading', { name: 'WB Cockpit' })).toBeVisible()
  expect(await page.locator('style').count()).toBeGreaterThan(0)
  await page.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))))
  const violations = await page.evaluate(() => (window as unknown as { __violations: unknown[] }).__violations)
  expect(unexplainedViolations(violations as Violation[])).toEqual([])
  expect(otherConsoleErrors(consoleErrors)).toEqual([])
})

test('the shell follows the browser colour scheme', async ({ page }) => {
  await page.route('**/api/v1/cockpit/**', (route) => route.fulfill({ status: 401, json: { error: 'stubbed' } }))
  await page.emulateMedia({ colorScheme: 'dark' })
  await page.goto('/cockpit/')
  const dark = await page.evaluate(() => getComputedStyle(document.body).backgroundColor)
  await page.emulateMedia({ colorScheme: 'light' })
  const light = await page.evaluate(() => getComputedStyle(document.body).backgroundColor)
  expect(dark).not.toBe(light)
})
