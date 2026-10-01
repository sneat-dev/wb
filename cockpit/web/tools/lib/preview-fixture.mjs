// The preview harness: the production build served with the fleet-data fixtures
// as the stubbed daemon API, so the Cockpit can be looked at (and screenshotted)
// without a daemon. Nobody may start a real daemon on a Mac: its launchd job is
// one fixed label per user, so the preview never talks to one. It answers only
// what the application reads, on a loopback port taken from an environment
// variable, and never on the daemon's port (8766) or the end-to-end port (4300).
import { createServer } from 'node:http'
import { createHandler } from './serve-dist.mjs'

// Ports this harness refuses: the wb daemon's, and the one the stubbed end-to-end run uses by default.
export const REFUSED_PORTS = [8766, 4300]

const API = '/api/v1/cockpit/'
const DAY = 86_400_000

// The port named by COCKPIT_PREVIEW_PORT: an unprivileged port that is neither the daemon's nor the end-to-end server's.
export function previewPort(env) {
  const text = env.COCKPIT_PREVIEW_PORT
  if (text === undefined || text === '') throw new Error('preview: set COCKPIT_PREVIEW_PORT to the loopback port to serve on')
  const port = Number(text)
  if (!Number.isInteger(port) || port < 1024 || port > 65_535) throw new Error(`preview: COCKPIT_PREVIEW_PORT must be a port from 1024 to 65535, not ${text}`)
  if (REFUSED_PORTS.includes(port)) throw new Error(`preview: port ${port} is refused: it belongs to the wb daemon or to the end-to-end run`)
  return port
}

const TIME = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/
const DATE = /^\d{4}-\d{2}-\d{2}$/

// A copy of `value` with every time moved by `delta` milliseconds, and every
// plain date by the nearest whole number of days.
export function shiftTimes(value, delta) {
  if (typeof value === 'string') {
    if (TIME.test(value)) return new Date(Date.parse(value) + delta).toISOString()
    if (DATE.test(value)) return new Date(Date.parse(value) + Math.round(delta / DAY) * DAY).toISOString().slice(0, 10)
    return value
  }
  if (Array.isArray(value)) return value.map((item) => shiftTimes(item, delta))
  if (value !== null && typeof value === 'object') return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, shiftTimes(item, delta)]))
  return value
}

// The states the preview can show: a normal fleet, a daemon still scanning, and
// either side of a schema mismatch.
export const STATES = ['ok', 'warming', 'daemon-older', 'page-older']

// The document the daemon would answer in `state`.
export function documentFor(document, state) {
  switch (state) {
    case 'warming':
      return { ...document, warming_up: true, repositories_total: 438, repositories_scanned: 120, repositories: document.repositories.slice(0, 120), worktrees: document.worktrees.slice(0, 60) }
    case 'daemon-older':
      return { ...document, schema_version: 1 }
    case 'page-older':
      return { ...document, schema_version: 3 }
    default:
      return document
  }
}

const SESSIONS = {
  anonymous: { principal: 'anonymous-local', capabilities: ['fleet.read'], code_browser_url: 'https://codegrapher.dev/' },
  owner: { principal: 'owner', capabilities: ['fleet.read', 'repo.content.read'], code_browser_url: 'https://codegrapher.dev/' },
}

// What the preview's action registry offers (REQ:actions-are-discovered): the push of a worktree and the
// landing of a pull request, the two actions Home puts in a row's slot.
export function registryActions(target) {
  const [type] = target.split(':')
  if (type === 'worktree') return [{ id: 'branch.push', title: 'Push', target_types: ['worktree'], applicable: true, parameters: [], capability: 'branch.push', safety: 'guarded', permitted: true }]
  if (type === 'pull_request') return [{ id: 'pr.land', title: 'Land', target_types: ['pull_request'], applicable: true, parameters: [], capability: 'pr.land', safety: 'guarded', permitted: true }]
  return []
}

// The capabilities an owner session carries once the daemon has an action registry.
const REGISTRY_CAPABILITIES = ['branch.push', 'pr.land']

function sendJson(response, status, body) {
  response.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-cache' }).end(JSON.stringify(body))
}

// A request handler: the daemon's API from `data`, everything else the built
// application under /cockpit/. `data` is {document, metrics: Map, branches: Map}
// as performanceFixture() returns it. Times move with the clock: every answer
// is shifted so the snapshot is `snapshotAgeMs` old when it is read.
export function createPreviewHandler({ distRoot, data, state = 'ok', session = 'anonymous', registry, snapshotAgeMs = 8000, clock = Date.now }) {
  const statics = createHandler(distRoot)
  const snapshotAt = Date.parse(data.document.snapshot_at)
  const delta = () => clock() - snapshotAgeMs - snapshotAt
  return (request, response) => {
    const { pathname, searchParams } = new URL(request.url, 'http://localhost')
    if (!pathname.startsWith(API)) {
      statics(request, response)
      return
    }
    if (request.method !== 'GET') {
      response.writeHead(405, { Allow: 'GET' }).end('method not allowed')
      return
    }
    const route = pathname.slice(API.length)
    if (route === 'fleet') return sendJson(response, 200, shiftTimes(documentFor(data.document, state), delta()))
    if (route === 'session') {
      const known = SESSIONS[session] ?? SESSIONS.anonymous
      return sendJson(response, 200, registry && session === 'owner' ? { ...known, capabilities: [...known.capabilities, ...REGISTRY_CAPABILITIES] } : known)
    }
    if (route === 'machine-metrics') {
      const metrics = data.metrics.get(searchParams.get('machine') ?? '')
      return metrics ? sendJson(response, 200, shiftTimes(metrics, delta())) : sendJson(response, 200, { machine: searchParams.get('machine'), route: 'none', samples: [], reason: 'not_reported' })
    }
    // With `registry` the daemon has an action registry: the owner session carries its capabilities and the route answers. Without, the route is absent (404).
    if (route === 'actions' && registry) return sendJson(response, 200, { actions: registryActions(searchParams.get('target') ?? '') })
    if (route === 'branches') return sendJson(response, 200, shiftTimes({ branches: data.branches.get(searchParams.get('repository') ?? '') ?? [] }, delta()))
    return sendJson(response, 404, { error: 'not served by the preview' })
  }
}

// Serves on `port`, loopback only; resolves with the server once it listens.
export function startPreview(options, port) {
  // Whoever calls it, the daemon's port and the end-to-end port are never served on; 0 is a free port.
  if (REFUSED_PORTS.includes(port)) return Promise.reject(new Error(`preview: port ${port} is refused: it belongs to the wb daemon or to the end-to-end run`))
  return new Promise((resolve, reject) => {
    const server = createServer(createPreviewHandler(options))
    server.once('error', reject)
    server.listen(port, '127.0.0.1', () => resolve(server))
  })
}
