import { expect, test } from '@playwright/test'
import { BUSY_ROUTES, stubBusy, watch } from './support'

// cockpit#req:anonymous-local-reads-metadata-only and cockpit-views#req:owner-gating-is-visible, on every route in the
// built application: an anonymous session asks only the metadata routes (the fleet, the session, the lazy branches and
// machine metrics), never the README, the action registry or an operation, and nothing that carries an SSH route is
// rendered, even though the stubbed session response carries `machine_routes` (which the daemon never sends to an
// anonymous reader, and the page must not believe). No action button is shown, not even a disabled one; the one
// affordance is the session chip's "Sign in as owner".

const METADATA = /^GET \/api\/v1\/cockpit\/(fleet|session|branches|machine-metrics)(\?|$)/
const SECRETS = ['secret-host.example', 'secretuser', '/opt/secret/wb']

for (const route of BUSY_ROUTES) {
  test(`${route.name}: asks only the metadata routes and shows no owner-only text`, async ({ page }) => {
    const requests = await stubBusy(page)
    const expectClean = await watch(page)
    await page.goto(`/cockpit/${route.path}`)
    await expect(page.getByRole('heading', { level: 1 })).not.toHaveText('')
    // Home's rest, the panels and the lazy chunks arrive after the first paint: let them in before judging.
    await page.locator('app-overlays').waitFor({ state: 'attached' })
    if (route.path === '') await expect(page.getByRole('heading', { level: 2, name: 'Cleanup' })).toBeVisible()
    await page.waitForTimeout(300)

    expect(requests.filter((request) => request.startsWith('GET /api/') && !METADATA.test(request))).toEqual([])
    expect(requests.filter((request) => request.startsWith('GET /api/')).length).toBeGreaterThan(0)
    expect(requests.filter((request) => !request.startsWith('GET '))).toEqual([])

    const html = await page.content()
    for (const secret of SECRETS) expect(html, secret).not.toContain(secret)
    await expect(page.locator('body')).not.toContainText(/\bssh \S+@|\bssh secret/)
    // No action it cannot run, not even disabled: the affordance is the session chip alone.
    await expect(page.getByRole('button', { name: /^(Push|Land|Discard|Commit|Create pull request|Stop|Reply)\b/ })).toHaveCount(0)
    await expect(page.getByText('anonymous', { exact: true })).toBeVisible()
    await expectClean()
  })
}

test('the session chip offers the one way in: Sign in as owner, run wb cockpit', async ({ page }) => {
  await stubBusy(page)
  await page.goto('/cockpit/')
  await page.getByText('anonymous', { exact: true }).click()
  await expect(page.getByRole('dialog', { name: 'Sign in as owner' })).toContainText('wb cockpit')
})

test('the same routes for an owner session do show the SSH form of a command the owner can run', async ({ page }) => {
  await stubBusy(page, 'owner')
  await page.goto('/cockpit/machines/mach-old')
  await expect(page.locator('app-machine-panel')).toContainText('ssh secretuser@secret-host.example')
})
