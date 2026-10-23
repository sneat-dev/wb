import { expect, test, type BrowserContext, type Page } from '@playwright/test'
import { stub, watch } from './support'

// cockpit#req:session-key, in the built application under the daemon's policy. The stubbed daemon is the owner's
// only for a request that carries both halves of the session: the cookie the login sets, and the session key in
// its header. The key reaches the page in the fragment of the login URL alone.

const KEY = 'sEss10n-Key_0123456789-abcdefghijklmnopqrstu'.slice(0, 43)
const HEADER = 'x-wb-cockpit-session-key'
const STORAGE = 'wb-cockpit.session-key'
const COOKIE = 'wb_cockpit_session_stub'

/** What the page asked the daemon: the address, and the session key and cookie the request carried. */
interface Asked {
  url: string
  key: string | undefined
  cookie: boolean
}

/** Stubs the daemon for `page`: the routes every page reads, the login exchange, and a session route that answers by cookie and key. */
async function daemon(page: Page): Promise<Asked[]> {
  const asked: Asked[] = []
  await stub(page)
  page.on('request', (request) => {
    // A request still out when the test ends has no headers to read.
    void request.allHeaders().then(
      (headers) => asked.push({ url: request.url(), key: headers[HEADER], cookie: (headers['cookie'] ?? '').includes(`${COOKIE}=live`) }),
      () => undefined,
    )
  })
  // The README is content: the daemon serves it to the owner alone.
  await page.route('**/api/v1/cockpit/readme?**', async (route) => {
    const headers = await route.request().allHeaders()
    if (headers[HEADER] === KEY) await route.fulfill({ contentType: 'text/markdown; charset=utf-8', body: '# Owner only\n' })
    else await route.fulfill({ status: 401, json: { error: 'owner_session_required' } })
  })
  // The login exchange: the cookie, and a redirect to the application that names no fragment, as the daemon answers.
  await page.route('**/cockpit/session/login?code=*', (route) =>
    route.fulfill({ status: 303, headers: { Location: '/cockpit/', 'Set-Cookie': `${COOKIE}=live; Path=/; HttpOnly; SameSite=Strict`, 'Cache-Control': 'no-store', 'Referrer-Policy': 'no-referrer' } }),
  )
  await page.route('**/api/v1/cockpit/session', async (route) => {
    const headers = await route.request().allHeaders()
    const owner = headers[HEADER] === KEY && (headers['cookie'] ?? '').includes(`${COOKIE}=live`)
    await route.fulfill({ json: { principal: owner ? 'owner' : 'anonymous-local', capabilities: owner ? ['fleet.read', 'repo.content.read'] : ['fleet.read'], code_browser_url: 'https://codegrapher.dev/' } })
  })
  return asked
}

const chip = (page: Page) => page.locator('.session .session-text')
const storedKey = (page: Page) => page.evaluate((name) => window.localStorage.getItem(name), STORAGE)
const holdCookie = (context: BrowserContext, baseURL: string | undefined) => context.addCookies([{ name: COOKIE, value: 'live', url: `${baseURL}/`, httpOnly: true, sameSite: 'Strict' }])

/** Follows the login URL `wb cockpit` prints and waits for the owner session. */
async function signIn(page: Page): Promise<void> {
  await page.goto(`/cockpit/session/login?code=single-use-code#key=${KEY}`)
  await expect(chip(page)).toHaveText('owner')
}

// cockpit#ac:session-key-reaches-the-page-in-the-fragment
test('the login URL with the key in its fragment becomes an owner session, and the fragment leaves the address', async ({ page, baseURL }) => {
  const asked = await daemon(page)
  const expectClean = await watch(page)
  await signIn(page)

  // The address is the application's, with no fragment and no code, and Back does not bring the key back.
  await expect(page).toHaveURL(`${baseURL}/cockpit/`)
  expect(await page.evaluate(() => window.location.hash)).toBe('')
  expect(await storedKey(page)).toBe(KEY)
  await page.getByRole('navigation', { name: 'Pages' }).getByRole('link', { name: /^Tasks/ }).click()
  await expect(page).toHaveURL(`${baseURL}/cockpit/tasks`)
  await page.goBack()
  await expect(page).toHaveURL(`${baseURL}/cockpit/`)
  expect(await page.evaluate(() => window.location.hash)).toBe('')

  // No request was ever sent the key in its address, and the static files of the page are not sent it at all.
  expect(asked.filter((request) => request.url.includes(KEY))).toEqual([])
  expect(asked.filter((request) => !new URL(request.url).pathname.startsWith('/api/') && request.key !== undefined)).toEqual([])
  expect(asked.some((request) => request.url.endsWith('/api/v1/cockpit/session') && request.key === KEY && request.cookie)).toBe(true)

  // Once signed in, every read of the daemon carries the key in its header, and so the owner's content is served.
  asked.length = 0
  await page.getByRole('navigation', { name: 'Pages' }).getByRole('link', { name: /^Repositories/ }).click()
  await page.getByRole('link', { name: 'Open specscore/specscore-cli', exact: true }).click()
  await expect(page.locator('.readme-content')).toContainText('Owner only')
  const api = asked.filter((request) => new URL(request.url).pathname.startsWith('/api/v1/cockpit/'))
  expect(api.some((request) => request.url.includes('/readme?'))).toBe(true)
  expect(api.filter((request) => request.key !== KEY).map((request) => request.url)).toEqual([])
  expect(asked.filter((request) => request.url.includes(KEY))).toEqual([])
  await expectClean()
})

// cockpit#ac:a-reload-and-a-second-tab-keep-the-owner-session
test('a reload keeps the owner session', async ({ page, baseURL }) => {
  await daemon(page)
  await signIn(page)
  await page.reload()
  await expect(chip(page)).toHaveText('owner')
  await expect(page).toHaveURL(`${baseURL}/cockpit/`)
  expect(await storedKey(page)).toBe(KEY)
})

// cockpit#ac:a-reload-and-a-second-tab-keep-the-owner-session
test('a second tab of the same origin is the owner too, with no key in its address', async ({ page, context, baseURL }) => {
  await daemon(page)
  await signIn(page)
  const second = await context.newPage()
  const asked = await daemon(second)
  await second.goto('/cockpit/tasks')
  await expect(chip(second)).toHaveText('owner')
  await expect(second).toHaveURL(`${baseURL}/cockpit/tasks`)
  expect(asked.filter((request) => request.url.includes(KEY))).toEqual([])
})

// cockpit#ac:replayed-cookie-is-not-the-owner
test('a browser that holds the cookie and no key is an anonymous reader, and is told how to sign in', async ({ page, context, baseURL }) => {
  const asked = await daemon(page)
  const expectClean = await watch(page)
  await holdCookie(context, baseURL)
  await page.goto('/cockpit/')
  await expect(chip(page)).toHaveText('anonymous')
  // The page works: the fleet is listed and there is no error.
  await expect(page.getByRole('heading', { level: 1 })).not.toHaveText('')
  const session = asked.filter((request) => request.url.endsWith('/api/v1/cockpit/session'))
  expect(session.length).toBeGreaterThan(0)
  expect(session.every((request) => request.cookie && request.key === undefined)).toBe(true)
  // The one way in is the command, with its copy button.
  await chip(page).click()
  const card = page.getByRole('dialog', { name: 'Sign in as owner' })
  await expect(card).toContainText('wb cockpit')
  await expect(card.getByRole('button', { name: /Copy wb cockpit/ })).toBeVisible()
  await expectClean()
})

test('an address with a key the daemon does not take neither signs in nor signs the owner out', async ({ page, baseURL }) => {
  await daemon(page)
  const made = 'x'.repeat(43)

  // With no session: the fragment leaves the address, nothing is kept, the reader is anonymous.
  await page.goto(`/cockpit/#key=${made}`)
  await expect(chip(page)).toHaveText('anonymous')
  await expect(page).toHaveURL(`${baseURL}/cockpit/`)
  expect(await storedKey(page)).toBeNull()

  // With the owner's session: the owner stays the owner, with the key it had.
  await signIn(page)
  await page.goto(`/cockpit/tasks#key=${made}`)
  await expect(chip(page)).toHaveText('owner')
  await expect(page).toHaveURL(`${baseURL}/cockpit/tasks`)
  expect(await storedKey(page)).toBe(KEY)
})

test('the key ends with the session: once the daemon no longer takes it, the page forgets it', async ({ page, context }) => {
  await daemon(page)
  await signIn(page)
  // The session is over (a logout elsewhere, an expiry, a daemon restart): the cookie is gone.
  await context.clearCookies()
  await page.reload()
  await expect(chip(page)).toHaveText('anonymous')
  await expect.poll(() => storedKey(page)).toBeNull()
})
