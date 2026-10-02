// @vitest-environment jsdom
//
// The daemon's dashboard pages (internal/dashboard/assets), loaded as the daemon serves them: the page's
// markup and the script file it names. They share an origin, and so a storage, with Cockpit, which keeps the
// owner's session key there (cockpit#req:session-key). What they render is stored data that a client posted,
// so every field of every record is hostile here, and nothing of it may become an element, an attribute, a
// class or an address that runs script.
import { readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { afterEach, describe, expect, it, vi } from 'vitest'

const assets = join(dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'internal', 'dashboard', 'assets')
const read = (name) => readFileSync(join(assets, name), 'utf8')

/** What an injected value tries: an element with a handler, a break out of a quoted handler, of an attribute, of a class, and a script address. */
const PAYLOADS = [
  '<img src=x onerror=window.__pwned=1>',
  `'");window.__pwned=1;alert(1)//`,
  '" onmouseover="window.__pwned=1" x="',
  'passed" onclick="window.__pwned=1',
  '</td></tr></table><script>window.__pwned=1</script><svg onload=window.__pwned=1>',
  'javascript:window.__pwned=1',
  '&#39;);window.__pwned=1;(&#39;',
]

/** One metric record with `payload` in every field, of every type the field could have. */
function hostileMetric(payload, index, extra = {}) {
  return {
    repository: `github.com/${payload}/${index}`,
    owner: payload,
    name: payload,
    metric_type: payload,
    reported_at: payload,
    ref: payload,
    sha: payload,
    value: payload,
    formatted_value: payload,
    status: payload,
    metadata: { statements: payload, covered: payload, workflow_run_url: payload, workflow_run_id: payload, [payload]: payload },
    dimensions: [
      { name: payload, value: payload, formatted_value: payload, status: payload, details: { statements: payload, covered: payload, [payload]: payload } },
      { name: `pkg-${index}`, value: 50, formatted_value: '', status: payload, details: { statements: 10, covered: payload } },
      { name: payload + index, value: 1, formatted_value: payload, status: 'passed' },
    ],
    ...extra,
  }
}

const hostileMetrics = () => [
  ...PAYLOADS.map((payload, index) => hostileMetric(payload, index)),
  // The same, with the numbers a real record has, so the numeric branches render too.
  ...PAYLOADS.map((payload, index) => hostileMetric(payload, `n${index}`, { value: 42.5, reported_at: new Date().toISOString(), metadata: { statements: 100, covered: 40, workflow_run_url: payload } })),
  // A record with no statements shows its workflow run: an address that is not https is never a link.
  ...PAYLOADS.map((payload, index) => hostileMetric(payload, `w${index}`, { metadata: { workflow_run_url: payload, workflow_run_id: payload } })),
  hostileMetric('ok', 'https', { metadata: { workflow_run_url: 'https://github.com/acme/r/actions/runs/1', workflow_run_id: 1 } }),
]

const hostileOverview = () => ({
  generated_at: PAYLOADS[0],
  diagnostics: PAYLOADS[0],
  machine: { name: PAYLOADS[0], wb_version: PAYLOADS[1] },
  worktrees: PAYLOADS.flatMap((payload) => [
    { repository: payload, task: payload, branch: payload, owner: payload, owner_state: payload, age_seconds: payload },
    { repository: `github.com/${payload}`, task: payload, branch: payload, owner: '', owner_state: 'active', age_seconds: 600 },
  ]),
  operations: {
    operations: PAYLOADS[0],
    failed: PAYLOADS[0],
    wall_ms: PAYLOADS[0],
    user_cpu_ms: PAYLOADS[0],
    system_cpu_ms: PAYLOADS[0],
    kinds: PAYLOADS.flatMap((payload) => [
      { kind: payload, operations: payload, failed: payload, p50_ms: payload, p95_ms: payload },
      { kind: payload, operations: 3, failed: 2, p50_ms: 1500, p95_ms: 90000 },
    ]),
  },
})

const hostileTypes = () => [
  { type: 'test_coverage', title: PAYLOADS[0], dimension_label: PAYLOADS[1] },
  ...PAYLOADS.map((payload) => ({ type: payload, title: payload, dimension_label: payload })),
]

/** Loads a page as the daemon serves it, with the daemon's routes answered by `routes`, and runs the page's script file. */
async function load(page, routes) {
  const html = read(`${page}.html`)
  window.history.replaceState({}, '', '/' + (page === 'index' ? '' : page))
  document.documentElement.innerHTML = html.replace(/^<!doctype html>\s*<html[^>]*>/i, '').replace(/<\/html>\s*$/i, '')
  delete window.__pwned
  const asked = []
  vi.stubGlobal('fetch', async (input) => {
    asked.push(String(input))
    const path = String(input).split('?')[0]
    const answer = routes[String(input)] ?? routes[path]
    if (answer === undefined) return new Response('{}', { status: 404 })
    return new Response(JSON.stringify(answer), { status: 200, headers: { 'Content-Type': 'application/json' } })
  })
  // The script is the file the page names; nothing else on the page is script.
  const scripts = [...document.querySelectorAll('script')]
  expect(scripts.map((script) => [script.getAttribute('src'), script.textContent])).toEqual([[`/dashboard-assets/${page}.js`, '']])
  new Function(read(`${page}.js`))()
  await settle()
  return asked
}

const settle = async () => {
  for (let turn = 0; turn < 20; turn++) await new Promise((resolve) => setTimeout(resolve, 0))
}

/** The classes the two pages' stylesheets and scripts use: a class outside the set came from data. */
const CLASSES = new Set([
  'empty', 'repo', 'branch', 'pill', 'dot', 'bad', 'sub', 'machine', 'cards', 'card', 'label', 'value', 'grid', 'panel', 'error', 'nav-links', 'active',
  'tabs-bar', 'tabs', 'tab', 'filter-input', 'passed', 'warning', 'failed', 'neutral', 'abandoned', 'progress-bar-bg', 'progress-bar-fill', 'fill-passed',
  'fill-warning', 'fill-failed', 'btn-expand', 'btn-drill', 'dimensions-row', 'dim-table', 'dim-path',
])

/** The elements a dashboard page is made of. Anything else was created from data. */
const TAGS = new Set(['HTML', 'HEAD', 'META', 'TITLE', 'STYLE', 'BODY', 'MAIN', 'HEADER', 'NAV', 'A', 'SPAN', 'DIV', 'H1', 'H2', 'SECTION', 'INPUT', 'FOOTER', 'SCRIPT', 'TABLE', 'THEAD', 'TBODY', 'TR', 'TH', 'TD', 'BUTTON', 'B'])

/** The attributes the pages' own markup and scripts set. */
const ATTRIBUTES = new Set(['class', 'id', 'href', 'target', 'rel', 'style', 'title', 'type', 'placeholder', 'colspan', 'src', 'charset', 'name', 'content', 'lang', 'data-action', 'data-repository', 'data-type'])

/** Fails if anything in the document was created from an injected value. */
function expectNothingInjected() {
  expect(window.__pwned).toBeUndefined()
  const problems = []
  for (const node of document.querySelectorAll('*')) {
    if (!TAGS.has(node.tagName)) problems.push(`element <${node.tagName.toLowerCase()}>`)
    for (const attribute of node.attributes) {
      if (!ATTRIBUTES.has(attribute.name)) problems.push(`attribute ${attribute.name} on <${node.tagName.toLowerCase()}>`)
    }
    for (const name of node.classList) {
      if (!CLASSES.has(name)) problems.push(`class "${name}"`)
    }
    if (node.tagName === 'A' && !['https:', 'http:'].includes(new URL(node.href, 'http://127.0.0.1:8766').protocol)) problems.push(`address ${node.getAttribute('href')}`)
    if (node.tagName === 'A' && node.target === '_blank' && new URL(node.href).protocol !== 'https:') problems.push(`external address ${node.getAttribute('href')}`)
    if (node.hasAttribute('style') && /url\(|expression|javascript/i.test(node.getAttribute('style'))) problems.push(`style ${node.getAttribute('style')}`)
  }
  expect([...new Set(problems)]).toEqual([])
  // One script, the page's own file.
  expect(document.querySelectorAll('script').length).toBe(1)
  expect(document.querySelectorAll('img, svg, iframe, object, embed, form, link, base').length).toBe(0)
}

/** Opens every breakdown of the table that is closed; each click renders the table again. */
function openEveryBreakdown() {
  for (;;) {
    const closed = [...document.querySelectorAll('#tableView button[data-action="expand"]')].find((button) => button.textContent.startsWith('▼'))
    if (closed === undefined) return
    closed.click()
  }
}

/** How many times the injected markup is on the page as text, which is where it belongs. */
const shownAsText = (payload) => document.body.textContent.split(payload).length - 1

afterEach(() => vi.unstubAllGlobals())

describe('the metrics page with a hostile value in every field of every record', () => {
  const routes = () => ({
    '/api/v1/health': { machine: PAYLOADS[0], wb_version: PAYLOADS[1] },
    '/api/v1/overview': hostileOverview(),
    '/v0/workbench/metrics/types': hostileTypes(),
    '/v0/workbench/metrics': hostileMetrics(),
  })

  it('shows the summary with every value as text and creates nothing from it', async () => {
    const asked = await load('metrics', routes())
    expect(asked).toContain('/v0/workbench/metrics?type=test_coverage')
    expect(document.querySelector('#error').style.display).toBe('none')
    // The three summary tables rendered, and the cards.
    expect(document.querySelectorAll('#leastCoverageTable tbody tr').length).toBe(8)
    expect(document.querySelectorAll('#mostActiveTable tbody tr').length).toBe(8)
    expect(document.querySelectorAll('#worktreesTable tbody tr').length).toBe(PAYLOADS.length * 2)
    expect(document.querySelector('#kpiRepos').textContent).toBe(String(hostileMetrics().length))
    expect(document.querySelector('#kpiFleet').textContent).toBe('40.0%')
    for (const payload of PAYLOADS) expect(shownAsText(payload), payload).toBeGreaterThan(0)
    // A status outside the closed set is the neutral pill, never a class of its own.
    expect([...document.querySelectorAll('#leastCoverageTable .pill')].every((pill) => pill.className === 'pill neutral')).toBe(true)
    expectNothingInjected()
  })

  it('shows every metric type, with every breakdown open, and creates nothing from it', async () => {
    await load('metrics', routes())
    const tabs = () => [...document.querySelectorAll('#tabs [data-type]')]
    expect(tabs().length).toBe(hostileTypes().length + 1)
    for (let index = 0; index < tabs().length; index++) {
      tabs()[index].click()
      await settle()
      expectNothingInjected()
      openEveryBreakdown()
      expectNothingInjected()
    }
    // The table view rendered with its breakdowns: each record's three dimensions are rows.
    tabs()[1].click()
    openEveryBreakdown()
    expect(document.querySelectorAll('#tableContainer > table > tbody > tr').length).toBe(hostileMetrics().length * 2)
    expect(document.querySelectorAll('#tableContainer .dim-table tbody tr').length).toBe(hostileMetrics().length * 3)
    // An address that is not https is text; the one that is https is a link.
    const runs = [...document.querySelectorAll('#tableContainer a')].filter((link) => link.textContent.startsWith('Run #'))
    expect(runs.map((link) => link.href)).toEqual(['https://github.com/acme/r/actions/runs/1'])
    expect([...document.querySelectorAll('#tableContainer span.sub')].filter((span) => span.textContent.startsWith('Run #')).length).toBe(PAYLOADS.length * 2)
    document.querySelector('#tableView button[data-action="expand"]').click()
    expect(document.querySelectorAll('#tableContainer .dim-table tbody tr').length).toBe(hostileMetrics().length * 3 - 3)
    expectNothingInjected()
  })

  it('opens a repository named by a hostile value from its Breakdown button, by its data attribute alone', async () => {
    await load('metrics', routes())
    const button = document.querySelector('#leastCoverageTable button[data-action="drill"]')
    const repository = button.dataset.repository
    expect(PAYLOADS.some((payload) => repository.includes(payload))).toBe(true)
    button.click()
    await settle()
    expect(new URLSearchParams(window.location.search).get('type')).toBe('test_coverage')
    const open = document.querySelector('#tableContainer .dimensions-row')
    expect(open.previousElementSibling.querySelector('button[data-action="expand"]').dataset.repository).toBe(repository)
    expectNothingInjected()
  })

  it('filters by the text typed, which is never markup either', async () => {
    await load('metrics', routes())
    const filter = document.querySelector('#filter')
    for (const typed of [PAYLOADS[0], 'pkg-0', 'no such thing']) {
      filter.value = typed
      filter.dispatchEvent(new Event('input', { bubbles: true }))
      expectNothingInjected()
    }
    expect(document.querySelector('#leastCoverageTable').textContent).toBe('No coverage metrics recorded yet.')
    ;[...document.querySelectorAll('#tabs [data-type]')][1].click()
    expect(document.querySelector('#tableContainer').textContent).toBe('No repositories found for metric: test_coverage.')
    filter.value = 'pkg-0'
    filter.dispatchEvent(new Event('input', { bubbles: true }))
    document.querySelector('#tableView button[data-action="expand"]').click()
    expect(document.querySelectorAll('#tableContainer .dim-table tbody tr').length).toBe(1)
    expectNothingInjected()
  })

  it('renders answers that are not the shape it expects without creating anything from them', async () => {
    for (const answer of [PAYLOADS[0], { worktrees: PAYLOADS[0] }, [PAYLOADS[0], null, 7], null]) {
      await load('metrics', { '/api/v1/health': answer ?? {}, '/api/v1/overview': answer ?? {}, '/v0/workbench/metrics/types': answer, '/v0/workbench/metrics': answer })
      expectNothingInjected()
    }
    // With no route at all the page says so in its own words.
    await load('metrics', {})
    expect(document.querySelector('#leastCoverageTable').textContent).toBe('No coverage metrics recorded yet.')
    expectNothingInjected()
  })
})

describe('the operations page with a hostile value in every field', () => {
  it('lists the worktrees and the command cost with every value as text and creates nothing from it', async () => {
    const asked = await load('index', { '/api/v1/overview': hostileOverview() })
    expect(asked).toEqual(['/api/v1/overview'])
    expect(document.querySelector('#error').style.display).toBe('none')
    expect(document.querySelectorAll('#worktreeTable tbody tr').length).toBe(PAYLOADS.length * 2)
    expect(document.querySelectorAll('#kindTable tbody tr').length).toBe(PAYLOADS.length * 2)
    expect(document.querySelector('#machine').textContent).toBe(`${PAYLOADS[0]} · ${PAYLOADS[1]}`)
    // A count that is not a number is shown as none, never as what it holds.
    expect(document.querySelector('#operations').textContent).toBe('0')
    expect(document.querySelector('#kindTable tbody tr td:nth-child(2)').textContent).toBe('0')
    expect(document.querySelector('#kindTable tbody tr:nth-child(2) td:nth-child(2)').textContent).toBe('3 · 2 failed')
    for (const payload of PAYLOADS) expect(shownAsText(payload), payload).toBeGreaterThan(0)
    expectNothingInjected()
  })

  it('says in its own words that the overview is unavailable, and shows an empty machine as empty', async () => {
    await load('index', {})
    expect(document.querySelector('#error').textContent).toBe('Dashboard refresh failed: the overview is unavailable (status 404)')
    await load('index', { '/api/v1/overview': {} })
    expect(document.querySelector('#worktreeTable').textContent).toBe('No managed worktrees found.')
    expect(document.querySelector('#kindTable').textContent).toBe('Run commands through wb run -- … to collect cost.')
    expectNothingInjected()
  })
})

// The check itself: the way the pages used to be built does create what the assertions look for.
describe('the injection check', () => {
  it('fails on a page built by concatenating a value into markup', async () => {
    await load('index', { '/api/v1/overview': {} })
    document.querySelector('#worktreeTable').innerHTML = '<span class="pill ' + PAYLOADS[3] + '">' + PAYLOADS[0] + '</span>'
    expect(() => expectNothingInjected()).toThrow()
  })
})
