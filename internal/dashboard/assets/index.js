// The operations page (/). It is a file of the daemon's own origin, so the page needs no inline script.
//
// Everything this page shows comes from /api/v1/overview, which lists what the machine holds: repository,
// task, branch and owner names. None of it is trusted as markup. The page is built with createElement and
// textContent only: no data is ever concatenated into HTML, and a class name is chosen from a fixed set.
(() => {
  'use strict'

  /** An element with a class and text, and children appended. */
  function el(tag, className, text, children) {
    const node = document.createElement(tag)
    if (className) node.className = className
    if (text !== undefined && text !== null) node.textContent = String(text)
    for (const child of children || []) node.append(child)
    return node
  }

  const duration = (ms) => {
    if (!ms) return '0s'
    if (ms < 1000) return ms + 'ms'
    if (ms < 60000) return (ms / 1000).toFixed(ms < 10000 ? 1 : 0) + 's'
    return (ms / 60000).toFixed(1) + 'm'
  }
  const age = (s) => {
    if (!s) return 'now'
    if (s < 3600) return Math.floor(s / 60) + 'm'
    if (s < 86400) return Math.floor(s / 3600) + 'h'
    return Math.floor(s / 86400) + 'd'
  }
  /** A count is a number or nothing: a value of any other type is not shown. */
  const count = (value) => (typeof value === 'number' && Number.isFinite(value) ? value : 0)

  function table(headings, rows) {
    return el('table', '', null, [
      el('thead', '', null, [el('tr', '', null, headings.map((heading) => el('th', '', heading)))]),
      el('tbody', '', null, rows),
    ])
  }

  function worktrees(rows) {
    if (!rows.length) return el('div', 'empty', 'No managed worktrees found.')
    return table(
      ['Repository', 'Task', 'Owner', 'Activity', 'Age'],
      rows.map((w) =>
        el('tr', '', null, [
          el('td', 'repo', w.repository),
          el('td', '', null, [el('div', '', w.task), el('div', 'branch', w.branch)]),
          el('td', '', w.owner || '—'),
          el('td', '', null, [el('span', 'pill', null, [el('span', w.owner_state === 'active' ? 'dot' : 'dot bad'), document.createTextNode(String(w.owner_state ?? ''))])]),
          el('td', '', age(count(w.age_seconds))),
        ]),
      ),
    )
  }

  function kinds(rows) {
    if (!rows.length) {
      return el('div', 'empty', null, [document.createTextNode('Run commands through '), el('span', 'branch', 'wb run -- …'), document.createTextNode(' to collect cost.')])
    }
    return table(
      ['Kind', 'Runs', 'P50', 'P95'],
      rows.map((k) => {
        const runs = el('td', '', count(k.operations))
        if (count(k.failed)) {
          const failed = el('span', '', count(k.failed) + ' failed')
          failed.style.color = 'var(--bad)'
          runs.append(' · ', failed)
        }
        return el('tr', '', null, [el('td', 'repo', k.kind), runs, el('td', '', duration(count(k.p50_ms))), el('td', '', duration(count(k.p95_ms)))])
      }),
    )
  }

  /** The records of an answer: the objects of a list. An answer of another shape has none. */
  const list = (value) => (Array.isArray(value) ? value.filter((item) => typeof item === 'object' && item !== null) : [])
  const show = (selector, text) => {
    document.querySelector(selector).textContent = String(text)
  }

  async function refresh() {
    try {
      const r = await fetch('/api/v1/overview', { cache: 'no-store' })
      // The page shows its own fixed text, never one the server sent.
      if (!r.ok) throw new Error('the overview is unavailable (status ' + r.status + ')')
      const d = (await r.json()) || {}
      const o = d.operations || {}
      const machine = d.machine || {}
      document.querySelector('#error').style.display = 'none'
      show('#machine', String(machine.name ?? '') + ' · ' + String(machine.wb_version ?? ''))
      show('#worktrees', list(d.worktrees).length)
      show('#operations', count(o.operations))
      show('#failures', count(o.failed))
      show('#wall', duration(count(o.wall_ms)))
      show('#cpu', duration(count(o.user_cpu_ms) + count(o.system_cpu_ms)))
      document.querySelector('#worktreeTable').replaceChildren(worktrees(list(d.worktrees)))
      document.querySelector('#kindTable').replaceChildren(kinds(list(o.kinds)))
      show('#updated', 'Updated ' + new Date(d.generated_at).toLocaleTimeString() + (count(d.diagnostics) ? ' · ' + count(d.diagnostics) + ' inventory diagnostics' : ''))
    } catch (e) {
      const n = document.querySelector('#error')
      n.textContent = 'Dashboard refresh failed: ' + e.message
      n.style.display = 'block'
    }
  }

  refresh()
  setInterval(refresh, 10000)
})()
