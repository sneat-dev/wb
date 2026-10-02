// The metrics page (/metrics). It is a file of the daemon's own origin, so the page needs no inline script.
//
// What this page shows is stored data: metric and coverage records that a client posted to the hub, and the
// machine's worktrees. None of it is trusted as markup, whatever validated it on the way in, because records
// written before the validation existed are still in the store. So:
//   - the page is built with createElement and textContent only; no data is ever concatenated into HTML;
//   - a class name is chosen from a fixed set, never taken from a record;
//   - a link's address is built by the page (https:// and a repository name) or must parse as https;
//   - there is no inline event handler: one listener per region reads a data- attribute.
(() => {
  'use strict'

  let currentType = new URLSearchParams(window.location.search).get('type') || 'summary'
  let types = []
  let allMetrics = []
  let coverageMetrics = []
  let worktreesList = []
  let filterText = ''
  const expanded = new Set()

  /** The pill classes the stylesheet has. A status outside the set is shown as neutral. */
  const PILL_CLASSES = new Set(['passed', 'warning', 'failed', 'neutral', 'active', 'abandoned'])
  const pillClass = (status) => (typeof status === 'string' && PILL_CLASSES.has(status) ? status : 'neutral')
  const FILL_CLASSES = { passed: 'fill-passed', warning: 'fill-warning' }
  const fillClass = (status) => (typeof status === 'string' && Object.hasOwn(FILL_CLASSES, status) ? FILL_CLASSES[status] : 'fill-failed')

  const text = (value) => (value === undefined || value === null ? '' : String(value))
  const number = (value) => (typeof value === 'number' && Number.isFinite(value) ? value : 0)
  /** The records of an answer: the objects of a list. An answer of another shape has none. */
  const list = (value) => (Array.isArray(value) ? value.filter((item) => typeof item === 'object' && item !== null) : [])
  const lower = (value) => text(value).toLowerCase()

  /** An element with a class and text, and children appended. */
  function el(tag, className, content, children) {
    const node = document.createElement(tag)
    if (className) node.className = className
    if (content !== undefined && content !== null) node.textContent = text(content)
    for (const child of children || []) node.append(child)
    return node
  }

  function table(className, headings, rows) {
    return el('table', className, null, [
      el('thead', '', null, [el('tr', '', null, headings.map((heading) => el('th', '', heading)))]),
      el('tbody', '', null, rows),
    ])
  }

  function formatAge(d) {
    if (!d) return '—'
    const s = Math.floor((Date.now() - new Date(d)) / 1000)
    if (Number.isNaN(s)) return '—'
    if (s < 60) return 'just now'
    if (s < 3600) return Math.floor(s / 60) + 'm ago'
    if (s < 86400) return Math.floor(s / 3600) + 'h ago'
    return Math.floor(s / 86400) + 'd ago'
  }

  const renderPill = (status, content) => el('span', 'pill ' + pillClass(status), content)

  function renderBar(value, status) {
    const fill = el('span', 'progress-bar-fill ' + fillClass(status))
    fill.style.width = Math.max(0, Math.min(100, number(value))) + '%'
    return el('span', 'progress-bar-bg', null, [fill])
  }

  /** "covered / statements", from numbers only; anything else in the record is not shown. */
  function statements(record, suffix) {
    if (!record || typeof record.statements !== 'number' || !record.statements) return null
    return number(record.covered) + ' / ' + record.statements + suffix
  }

  /** The address of a repository on its forge: always https, whatever the name holds. */
  function repositoryLink(repository) {
    const link = el('a', '', text(repository).replace(/^github\.com\//, ''))
    link.href = 'https://' + text(repository)
    link.target = '_blank'
    link.rel = 'noopener'
    return el('td', 'repo', null, [link])
  }

  /** A link to an address a record names, only when it parses as https; otherwise its text alone. */
  function externalLink(address, label) {
    let parsed = null
    try {
      parsed = new URL(text(address))
    } catch {
      parsed = null
    }
    if (parsed === null || parsed.protocol !== 'https:') return el('span', 'sub', label)
    const link = el('a', '', label)
    link.href = parsed.href
    link.target = '_blank'
    link.rel = 'noopener'
    link.style.color = 'var(--accent)'
    return link
  }

  function shaCell(sha) {
    if (!sha) return el('td', '', '—')
    const short = el('span', 'branch', text(sha).substring(0, 7))
    short.title = text(sha)
    return el('td', '', null, [short])
  }

  /** A button that names what it acts on in data- attributes; the region's listener reads them. */
  function actionButton(className, action, repository, label) {
    const button = el('button', className, label)
    button.type = 'button'
    button.dataset.action = action
    button.dataset.repository = text(repository)
    return button
  }

  function renderDimensions(dimensions, label) {
    const rows = list(dimensions)
    if (!rows.length) return el('div', 'empty', 'No granular ' + (text(label) || 'breakdown') + ' available.')
    const filter = filterText.toLowerCase()
    const shown = rows.filter((d) => !filter || lower(d.name).includes(filter))
    if (!shown.length) return el('div', 'empty', 'No packages match the search query.')
    return table(
      'dim-table',
      [text(label) || 'Dimension', 'Value', 'Status', 'Details'],
      shown.map((d) => {
        const details = d.details ? (statements(d.details, ' stmts') ?? JSON.stringify(d.details)) : '—'
        return el('tr', '', null, [
          el('td', 'dim-path', d.name),
          el('td', '', null, [renderBar(d.value, d.status), document.createTextNode(text(d.formatted_value || d.value))]),
          el('td', '', null, [renderPill(d.status, d.status)]),
          el('td', 'sub', details),
        ])
      }),
    )
  }

  function renderSummary() {
    const filter = filterText.toLowerCase()

    // 1. Least coverage repos
    const sortedCov = coverageMetrics.filter((m) => !filter || lower(m.repository).includes(filter)).sort((a, b) => number(a.value) - number(b.value))
    document.querySelector('#leastCoverageTable').replaceChildren(
      !sortedCov.length
        ? el('div', 'empty', 'No coverage metrics recorded yet.')
        : table(
            '',
            ['Repository', 'Coverage', 'Statements', 'Action'],
            sortedCov.slice(0, 8).map((m) =>
              el('tr', '', null, [
                repositoryLink(m.repository),
                el('td', '', null, [renderBar(m.value, m.status), renderPill(m.status, m.formatted_value || number(m.value).toFixed(1) + '%')]),
                el('td', 'sub', statements(m.metadata, '') ?? '—'),
                el('td', '', null, [actionButton('btn-drill', 'drill', m.repository, 'Breakdown')]),
              ]),
            ),
          ),
    )

    // 2. Most active repos (sorted by reported_at desc)
    const sortedActive = allMetrics.filter((m) => !filter || lower(m.repository).includes(filter)).sort((a, b) => new Date(b.reported_at) - new Date(a.reported_at))
    document.querySelector('#mostActiveTable').replaceChildren(
      !sortedActive.length
        ? el('div', 'empty', 'No repository activity recorded yet.')
        : table(
            '',
            ['Repository', 'Metric', 'Last Activity', 'Commit'],
            sortedActive.slice(0, 8).map((m) =>
              el('tr', '', null, [repositoryLink(m.repository), el('td', '', null, [renderPill(m.status, m.formatted_value || m.value)]), el('td', 'sub', formatAge(m.reported_at)), shaCell(m.sha)]),
            ),
          ),
    )

    // 3. Active & Abandoned Worktrees
    const filteredWt = worktreesList.filter((w) => !filter || lower(w.repository).includes(filter) || lower(w.task).includes(filter) || lower(w.branch).includes(filter))
    document.querySelector('#worktreesTable').replaceChildren(
      !filteredWt.length
        ? el('div', 'empty', 'No managed worktrees found.')
        : table(
            '',
            ['Repository', 'Task', 'Branch', 'Owner', 'Status', 'Activity'],
            filteredWt.map((w) => {
              const isActive = w.owner_state === 'active'
              return el('tr', '', null, [
                el('td', 'repo', text(w.repository).replace(/^github\.com\//, '')),
                el('td', '', w.task),
                el('td', 'branch', w.branch),
                el('td', '', w.owner || '—'),
                el('td', '', null, [el('span', 'pill ' + (isActive ? 'active' : 'abandoned'), null, [el('span', isActive ? 'dot' : 'dot bad'), document.createTextNode(isActive ? 'active' : text(w.owner_state) || 'abandoned')])]),
                el('td', 'sub', number(w.age_seconds) ? Math.floor(number(w.age_seconds) / 60) + 'm ago' : '—'),
              ])
            }),
          ),
    )
  }

  function renderTable() {
    const container = document.querySelector('#tableContainer')
    const typeDef = types.find((t) => t.type === currentType) || { dimension_label: 'Package' }
    const filter = filterText.toLowerCase()
    const shown = allMetrics.filter((m) => !filter || lower(m.repository).includes(filter) || list(m.dimensions).some((d) => lower(d.name).includes(filter)))

    if (!shown.length) {
      container.replaceChildren(el('div', 'empty', null, [document.createTextNode('No repositories found for metric: '), el('b', '', currentType), document.createTextNode('.')]))
      return
    }

    const rows = []
    for (const m of shown) {
      const isExp = expanded.has(text(m.repository))
      const metadata = m.metadata || {}
      let details = el('td', '', '—')
      if (statements(metadata, ' statements') !== null) {
        details = el('td', '', statements(metadata, ' statements'))
      } else if (metadata.workflow_run_url) {
        details = el('td', '', null, [externalLink(metadata.workflow_run_url, 'Run #' + text(metadata.workflow_run_id))])
      }
      const dimCount = list(m.dimensions).length
      const expand =
        dimCount > 0
          ? actionButton('btn-expand', 'expand', m.repository, isExp ? '▲ Hide ' + dimCount : '▼ ' + dimCount + ' ' + (text(typeDef.dimension_label) || 'Packages'))
          : el('span', 'sub', '—')

      rows.push(
        el('tr', '', null, [
          repositoryLink(m.repository),
          el('td', '', null, [renderBar(m.value, m.status), renderPill(m.status, m.formatted_value || m.value)]),
          details,
          shaCell(m.sha),
          el('td', 'sub', formatAge(m.reported_at)),
          el('td', '', null, [expand]),
        ]),
      )
      if (isExp && dimCount > 0) {
        const cell = el('td', '', null, [renderDimensions(m.dimensions, typeDef.dimension_label)])
        cell.colSpan = 6
        rows.push(el('tr', 'dimensions-row', null, [cell]))
      }
    }
    container.replaceChildren(table('', ['Repository', text(typeDef.title) || 'Value', 'Details', 'Commit', 'Reported', 'Breakdown'], rows))
  }

  /** Sets the text of an element the page has; a card the page does not show is skipped. */
  function show(selector, value) {
    const node = document.querySelector(selector)
    if (node) node.textContent = text(value)
  }

  function updateKPIs() {
    const source = coverageMetrics.length ? coverageMetrics : allMetrics
    show('#kpiRepos', source.length)
    let attention = 0
    let totalStmts = 0
    let coveredStmts = 0
    for (const m of source) {
      if (m.status === 'warning' || m.status === 'failed') attention++
      if (m.metadata && typeof m.metadata.statements === 'number') {
        totalStmts += m.metadata.statements
        coveredStmts += number(m.metadata.covered)
      }
    }
    show('#kpiAttention', attention)
    show('#kpiFleet', !source.length || totalStmts === 0 ? '—' : ((coveredStmts / totalStmts) * 100).toFixed(1) + '%')

    let activeWt = 0
    let abandonedWt = 0
    for (const w of worktreesList) {
      if (w.owner_state === 'active') activeWt++
      else abandonedWt++
    }
    show('#kpiActiveWt', activeWt)
    show('#kpiAbandonedWt', abandonedWt)
  }

  function renderTabs() {
    document.querySelector('#tabs').replaceChildren(
      ...[{ type: 'summary', title: '📊 Summary' }, ...types].map((t) => {
        const tab = el('div', t.type === currentType ? 'tab active' : 'tab', t.title)
        tab.dataset.type = text(t.type)
        return tab
      }),
    )
  }

  function selectType(t) {
    currentType = t
    const url = new URL(window.location)
    if (t === 'summary') url.searchParams.delete('type')
    else url.searchParams.set('type', t)
    window.history.replaceState({}, '', url)
    renderTabs()
    applyView()
  }

  function applyView() {
    const summary = currentType === 'summary'
    document.querySelector('#summaryView').style.display = summary ? 'block' : 'none'
    document.querySelector('#tableView').style.display = summary ? 'none' : 'block'
    if (summary) renderSummary()
    else renderTable()
  }

  /** The one listener of the page's buttons: what a button acts on is in its data- attributes, as text. */
  function onAction(event) {
    const button = event.target instanceof Element ? event.target.closest('button[data-action]') : null
    if (button === null) return
    const repository = button.dataset.repository
    if (button.dataset.action === 'drill') {
      expanded.add(repository)
      selectType('test_coverage')
      return
    }
    if (expanded.has(repository)) expanded.delete(repository)
    else expanded.add(repository)
    renderTable()
  }

  async function fetchOverview() {
    try {
      const res = await fetch('/api/v1/overview')
      if (res.ok) {
        const data = (await res.json()) || {}
        worktreesList = list(data.worktrees)
        if (data.machine) show('#machine', text(data.machine.name) + ' · ' + text(data.machine.wb_version))
      }
    } catch {
      // The worktrees are shown as none.
    }
  }

  async function fetchHealth() {
    try {
      const res = await fetch('/api/v1/health')
      if (res.ok) {
        const data = (await res.json()) || {}
        show('#machine', text(data.machine) + ' · ' + text(data.wb_version))
      }
    } catch {
      // The machine chip keeps its text.
    }
  }

  /**
   * The records of an answer, or none when it is not JSON: a daemon with no hub has no metrics routes, and
   * answers their addresses with its index page.
   */
  async function records(res) {
    if (!res.ok) return null
    try {
      return list(await res.json())
    } catch {
      return []
    }
  }

  const DEFAULT_TYPES = [{ type: 'test_coverage', title: 'Test Coverage', dimension_label: 'Package' }]

  async function fetchTypes() {
    try {
      const answered = await records(await fetch('/v0/workbench/metrics/types'))
      types = answered !== null && answered.length ? answered : DEFAULT_TYPES
    } catch {
      types = DEFAULT_TYPES
    }
    renderTabs()
  }

  async function fetchMetrics() {
    try {
      document.querySelector('#error').style.display = 'none'
      await fetchOverview()

      // The coverage metrics are always read: the cards and the summary show them.
      coverageMetrics = (await records(await fetch('/v0/workbench/metrics?type=test_coverage'))) ?? coverageMetrics

      if (currentType !== 'summary' && currentType !== 'test_coverage') {
        allMetrics = (await records(await fetch('/v0/workbench/metrics?type=' + encodeURIComponent(currentType)))) ?? allMetrics
      } else {
        allMetrics = coverageMetrics
      }

      updateKPIs()
      applyView()
      show('#updated', 'Updated at ' + new Date().toLocaleTimeString())
    } catch (err) {
      const errEl = document.querySelector('#error')
      errEl.textContent = 'Failed to load metrics: ' + err.message
      errEl.style.display = 'block'
    }
  }

  document.querySelector('#tabs').addEventListener('click', (event) => {
    const tab = event.target instanceof Element ? event.target.closest('[data-type]') : null
    if (tab !== null) selectType(tab.dataset.type)
  })
  document.querySelector('#summaryView').addEventListener('click', onAction)
  document.querySelector('#tableView').addEventListener('click', onAction)
  document.querySelector('#filter').addEventListener('input', (event) => {
    filterText = event.target.value
    if (currentType === 'summary') renderSummary()
    else renderTable()
  })

  async function init() {
    await fetchHealth()
    await fetchTypes()
    await fetchMetrics()
  }

  init()
  setInterval(fetchMetrics, 15000)
})()
