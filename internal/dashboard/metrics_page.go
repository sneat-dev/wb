package dashboard

const metricsHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>WB Metrics · Fleet Quality &amp; Velocity</title>
  <style>
    :root{color-scheme:light dark;--bg:#f3f5f7;--panel:#fff;--ink:#17202a;--muted:#667085;--line:#dce2e8;--accent:#2457d6;--good:#16794b;--warn:#b58105;--bad:#c33d3d;--shadow:0 10px 30px rgba(20,31,50,.08);--bar-bg:#e9edf2}
    @media(prefers-color-scheme:dark){:root{--bg:#101419;--panel:#171d24;--ink:#edf2f7;--muted:#9aa7b5;--line:#2b3541;--accent:#7fa2ff;--good:#5bd49b;--warn:#e5ad35;--bad:#ff8585;--shadow:0 12px 32px rgba(0,0,0,.25);--bar-bg:#232d38}}
    *{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.45 ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}main{width:min(1180px,calc(100% - 32px));margin:34px auto 64px}header{display:flex;justify-content:space-between;align-items:flex-end;gap:20px;margin-bottom:22px}h1{font-size:30px;line-height:1;margin:0 0 8px;letter-spacing:-.03em}h2{font-size:16px;margin:0 0 14px}.sub{color:var(--muted)}
    nav.nav-links{display:flex;gap:12px;margin-top:10px;font-size:13px}nav.nav-links a{color:var(--accent);text-decoration:none}nav.nav-links a.active{color:var(--ink);font-weight:600}
    .machine{padding:7px 11px;border:1px solid var(--line);border-radius:999px;background:var(--panel);white-space:nowrap}
    .cards{display:grid;grid-template-columns:repeat(5,1fr);gap:12px;margin-bottom:20px}.card,.panel{background:var(--panel);border:1px solid var(--line);border-radius:14px;box-shadow:var(--shadow)}.card{padding:16px}.label{color:var(--muted);font-size:12px;text-transform:uppercase;letter-spacing:.07em}.value{font-size:25px;font-weight:700;margin-top:5px}
    .grid{display:grid;grid-template-columns:1fr 1fr;gap:18px;margin-bottom:20px}
    .panel{padding:18px;min-width:0;margin-bottom:20px}
    .tabs-bar{display:flex;justify-content:space-between;align-items:center;gap:12px;margin-bottom:16px;flex-wrap:wrap}
    .tabs{display:flex;gap:8px}.tab{padding:7px 14px;border:1px solid var(--line);border-radius:999px;background:var(--panel);color:var(--muted);cursor:pointer;font-size:13px;font-weight:600}.tab.active{background:var(--accent);color:#fff;border-color:var(--accent)}
    .filter-input{padding:7px 12px;border:1px solid var(--line);border-radius:8px;background:var(--bg);color:var(--ink);font-size:13px;min-width:240px}
    table{width:100%;border-collapse:collapse}th{text-align:left;color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.06em;font-weight:600}th,td{padding:11px 10px;border-bottom:1px solid var(--line)}tr:last-child td{border-bottom:0}
    .repo{font-weight:650}.repo a{color:inherit;text-decoration:none}.repo a:hover{text-decoration:underline}
    .branch{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12px}
    .pill{display:inline-flex;align-items:center;gap:6px;padding:3px 9px;border-radius:999px;font-size:12px;font-weight:600}
    .pill.passed,.pill.active{background:rgba(22,121,75,.12);color:var(--good)}
    .pill.warning{background:rgba(181,129,5,.15);color:var(--warn)}
    .pill.failed,.pill.abandoned{background:rgba(195,61,61,.12);color:var(--bad)}
    .pill.neutral{background:rgba(102,112,133,.12);color:var(--muted)}
    .dot{width:7px;height:7px;border-radius:50%;background:var(--good)}.dot.bad{background:var(--bad)}
    .progress-bar-bg{width:100%;max-width:140px;height:7px;border-radius:999px;background:var(--bar-bg);overflow:hidden;display:inline-block;vertical-align:middle;margin-right:8px}
    .progress-bar-fill{height:100%;border-radius:999px}
    .fill-passed{background:var(--good)}.fill-warning{background:var(--warn)}.fill-failed{background:var(--bad)}
    .btn-expand,.btn-drill{background:none;border:1px solid var(--line);border-radius:6px;padding:4px 8px;cursor:pointer;color:var(--ink);font-size:12px}
    .btn-expand:hover,.btn-drill:hover{background:var(--bg)}
    .dimensions-row{background:rgba(0,0,0,.02);border-top:1px dashed var(--line)}
    @media(prefers-color-scheme:dark){.dimensions-row{background:rgba(255,255,255,.02)}}
    .dim-table{margin:10px 0 14px 20px;width:calc(100% - 20px);font-size:13px}
    .dim-table th{font-size:10px}
    .dim-table td{padding:6px 8px}
    .dim-path{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12px}
    .empty{padding:24px 8px;color:var(--muted);text-align:center}
    .error{display:none;background:#7b1f1f;color:#fff;padding:11px 14px;border-radius:10px;margin-bottom:15px}
    footer{color:var(--muted);margin-top:16px;font-size:12px}
    @media(max-width:960px){.cards{grid-template-columns:repeat(3,1fr)}.grid{grid-template-columns:1fr}}
    @media(max-width:560px){header{align-items:flex-start;flex-direction:column}.cards{grid-template-columns:1fr 1fr}main{width:min(100% - 20px,1180px)}}
  </style>
</head>
<body><main>
  <header>
    <div>
      <h1>WB Metrics</h1>
      <div class="sub">Fleet test coverage, worktree health, and engineering metrics</div>
      <nav class="nav-links">
        <a href="/">Operations</a>
        <span>·</span>
        <a href="/metrics" class="active">Metrics &amp; Coverage</a>
      </nav>
    </div>
    <div id="machine" class="machine">Connecting…</div>
  </header>
  <div id="error" class="error"></div>
  <section class="cards">
    <div class="card"><div class="label">Repositories</div><div id="kpiRepos" class="value">—</div></div>
    <div class="card"><div class="label">Fleet Coverage</div><div id="kpiFleet" class="value">—</div></div>
    <div class="card"><div class="label">Active Worktrees</div><div id="kpiActiveWt" class="value">—</div></div>
    <div class="card"><div class="label">Abandoned Worktrees</div><div id="kpiAbandonedWt" class="value">—</div></div>
    <div class="card"><div class="label">Needs Attention</div><div id="kpiAttention" class="value">—</div></div>
  </section>

  <div class="tabs-bar">
    <div id="tabs" class="tabs"></div>
    <input type="text" id="filter" class="filter-input" placeholder="Filter repositories, packages, or worktrees…" oninput="onFilterChange(this.value)">
  </div>

  <div id="summaryView" style="display:none">
    <div class="grid">
      <div class="panel">
        <h2>⚠️ Repositories with Least Coverage</h2>
        <div id="leastCoverageTable"></div>
      </div>
      <div class="panel">
        <h2>🔥 Most Active Repositories</h2>
        <div id="mostActiveTable"></div>
      </div>
    </div>
    <div class="panel">
      <h2>🛠️ Active &amp; Abandoned Worktrees</h2>
      <div id="worktreesTable"></div>
    </div>
  </div>

  <section id="tableView" class="panel">
    <div id="tableContainer"></div>
  </section>

  <footer id="updated">Waiting for metrics data.</footer>
</main>
<script>
const esc=v=>String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
let currentType = new URLSearchParams(window.location.search).get('type') || 'summary';
let types = [];
let allMetrics = [];
let coverageMetrics = [];
let worktreesList = [];
let filterText = '';
const expanded = new Set();

function formatAge(d){if(!d)return'—';const s=Math.floor((Date.now()-new Date(d))/1000);if(s<60)return'just now';if(s<3600)return Math.floor(s/60)+'m ago';if(s<86400)return Math.floor(s/3600)+'h ago';return Math.floor(s/86400)+'d ago';}

function fillClass(status){if(status==='passed')return'fill-passed';if(status==='warning')return'fill-warning';return'fill-failed';}

function renderPill(status, text){
  return '<span class="pill '+(status||'neutral')+'">'+esc(text)+'</span>';
}

function renderBar(val, status){
  const clamped = Math.max(0, Math.min(100, val));
  return '<span class="progress-bar-bg"><span class="progress-bar-fill '+fillClass(status)+'" style="width:'+clamped+'%"></span></span>';
}

function renderDimensions(dimList, dimLabel){
  if(!dimList || !dimList.length) return '<div class="empty">No granular '+esc(dimLabel||'breakdown')+' available.</div>';
  const filter = filterText.toLowerCase();
  const rows = dimList.filter(d => !filter || d.name.toLowerCase().includes(filter));
  if(!rows.length) return '<div class="empty">No packages match the search query.</div>';
  return '<table class="dim-table"><thead><tr><th>'+esc(dimLabel||'Dimension')+'</th><th>Value</th><th>Status</th><th>Details</th></tr></thead><tbody>'+
    rows.map(d => {
      const details = d.details ? (d.details.statements ? d.details.covered+' / '+d.details.statements+' stmts' : JSON.stringify(d.details)) : '—';
      return '<tr><td class="dim-path">'+esc(d.name)+'</td><td>'+renderBar(d.value, d.status)+esc(d.formatted_value||d.value)+'</td><td>'+renderPill(d.status, d.status)+'</td><td class="sub">'+esc(details)+'</td></tr>';
    }).join('')+'</tbody></table>';
}

function toggleExpand(repoKey){
  if(expanded.has(repoKey)) expanded.delete(repoKey);
  else expanded.add(repoKey);
  renderTable();
}

function drillIntoRepo(repoKey){
  expanded.add(repoKey);
  selectType('test_coverage');
}

function renderSummary(){
  const leastEl = document.querySelector('#leastCoverageTable');
  const activeEl = document.querySelector('#mostActiveTable');
  const wtEl = document.querySelector('#worktreesTable');
  const filter = filterText.toLowerCase();

  // 1. Least coverage repos
  const sortedCov = [...coverageMetrics].filter(m => !filter || m.repository.toLowerCase().includes(filter)).sort((a,b) => a.value - b.value);
  if(!sortedCov.length){
    leastEl.innerHTML = '<div class="empty">No coverage metrics recorded yet.</div>';
  } else {
    const rows = sortedCov.slice(0, 8);
    leastEl.innerHTML = '<table><thead><tr><th>Repository</th><th>Coverage</th><th>Statements</th><th>Action</th></tr></thead><tbody>' +
      rows.map(m => {
        const repoSlug = m.repository.replace(/^github\.com\//, '');
        const details = m.metadata && m.metadata.statements ? m.metadata.covered+' / '+m.metadata.statements : '—';
        return '<tr>' +
          '<td class="repo"><a href="https://'+esc(m.repository)+'" target="_blank">'+esc(repoSlug)+'</a></td>' +
          '<td>' + renderBar(m.value, m.status) + renderPill(m.status, m.formatted_value || (m.value.toFixed(1)+'%')) + '</td>' +
          '<td class="sub">' + details + '</td>' +
          '<td><button class="btn-drill" onclick="drillIntoRepo(\''+esc(m.repository)+'\')">Breakdown</button></td>' +
        '</tr>';
      }).join('') + '</tbody></table>';
  }

  // 2. Most active repos (sorted by reported_at desc)
  const sortedActive = [...allMetrics].filter(m => !filter || m.repository.toLowerCase().includes(filter)).sort((a,b) => new Date(b.reported_at) - new Date(a.reported_at));
  if(!sortedActive.length){
    activeEl.innerHTML = '<div class="empty">No repository activity recorded yet.</div>';
  } else {
    const rows = sortedActive.slice(0, 8);
    activeEl.innerHTML = '<table><thead><tr><th>Repository</th><th>Metric</th><th>Last Activity</th><th>Commit</th></tr></thead><tbody>' +
      rows.map(m => {
        const repoSlug = m.repository.replace(/^github\.com\//, '');
        const sha = m.sha ? '<span class="branch" title="'+esc(m.sha)+'">'+esc(m.sha.substring(0, 7))+'</span>' : '—';
        return '<tr>' +
          '<td class="repo"><a href="https://'+esc(m.repository)+'" target="_blank">'+esc(repoSlug)+'</a></td>' +
          '<td>' + renderPill(m.status, m.formatted_value || m.value) + '</td>' +
          '<td class="sub">' + formatAge(m.reported_at) + '</td>' +
          '<td>' + sha + '</td>' +
        '</tr>';
      }).join('') + '</tbody></table>';
  }

  // 3. Active & Abandoned Worktrees
  const filteredWt = worktreesList.filter(w => {
    if(!filter) return true;
    return w.repository.toLowerCase().includes(filter) || w.task.toLowerCase().includes(filter) || w.branch.toLowerCase().includes(filter);
  });
  if(!filteredWt.length){
    wtEl.innerHTML = '<div class="empty">No managed worktrees found.</div>';
  } else {
    wtEl.innerHTML = '<table><thead><tr><th>Repository</th><th>Task</th><th>Branch</th><th>Owner</th><th>Status</th><th>Activity</th></tr></thead><tbody>' +
      filteredWt.map(w => {
        const isActive = w.owner_state === 'active';
        const statusClass = isActive ? 'active' : 'abandoned';
        const statusLabel = isActive ? 'active' : (w.owner_state || 'abandoned');
        const repoSlug = w.repository.replace(/^github\.com\//, '');
        return '<tr>' +
          '<td class="repo">'+esc(repoSlug)+'</td>' +
          '<td>'+esc(w.task)+'</td>' +
          '<td class="branch">'+esc(w.branch)+'</td>' +
          '<td>'+esc(w.owner || '—')+'</td>' +
          '<td><span class="pill '+statusClass+'"><span class="dot '+(isActive?'':'bad')+'"></span>'+esc(statusLabel)+'</span></td>' +
          '<td class="sub">'+(w.age_seconds ? Math.floor(w.age_seconds/60)+'m ago' : '—')+'</td>' +
        '</tr>';
      }).join('') + '</tbody></table>';
  }
}

function renderTable(){
  const container = document.querySelector('#tableContainer');
  const typeDef = types.find(t => t.type === currentType) || {dimension_label: 'Package'};
  const filter = filterText.toLowerCase();
  const list = allMetrics.filter(m => {
    if(!filter) return true;
    if(m.repository.toLowerCase().includes(filter)) return true;
    if(m.dimensions && m.dimensions.some(d => d.name.toLowerCase().includes(filter))) return true;
    return false;
  });

  if(!list.length){
    container.innerHTML = '<div class="empty">No repositories found for metric: <b>'+esc(currentType)+'</b>.</div>';
    return;
  }

  let html = '<table><thead><tr><th>Repository</th><th>'+esc(typeDef.title||'Value')+'</th><th>Details</th><th>Commit</th><th>Reported</th><th>Breakdown</th></tr></thead><tbody>';
  for(const m of list){
    const repoSlug = m.repository.replace(/^github\.com\//, '');
    const isExp = expanded.has(m.repository);
    let details = '—';
    if(m.metadata && m.metadata.statements !== undefined){
      details = m.metadata.covered + ' / ' + m.metadata.statements + ' statements';
    } else if(m.metadata && m.metadata.workflow_run_url){
      details = '<a href="'+esc(m.metadata.workflow_run_url)+'" target="_blank" style="color:var(--accent)">Run #'+esc(m.metadata.workflow_run_id)+'</a>';
    }
    const sha = m.sha ? '<span class="branch" title="'+esc(m.sha)+'">'+esc(m.sha.substring(0, 7))+'</span>' : '—';
    const dimCount = m.dimensions ? m.dimensions.length : 0;
    const expandBtn = dimCount > 0
      ? '<button class="btn-expand" onclick="toggleExpand(\''+esc(m.repository)+'\')">'+(isExp ? '▲ Hide '+dimCount : '▼ '+dimCount+' '+esc(typeDef.dimension_label||'Packages'))+'</button>'
      : '<span class="sub">—</span>';

    html += '<tr>' +
      '<td class="repo"><a href="https://'+esc(m.repository)+'" target="_blank">'+esc(repoSlug)+'</a></td>' +
      '<td>' + renderBar(m.value, m.status) + renderPill(m.status, m.formatted_value || m.value) + '</td>' +
      '<td>' + details + '</td>' +
      '<td>' + sha + '</td>' +
      '<td class="sub">' + formatAge(m.reported_at) + '</td>' +
      '<td>' + expandBtn + '</td>' +
    '</tr>';

    if(isExp && dimCount > 0){
      html += '<tr class="dimensions-row"><td colspan="6">' + renderDimensions(m.dimensions, typeDef.dimension_label) + '</td></tr>';
    }
  }
  html += '</tbody></table>';
  container.innerHTML = html;
}

function updateKPIs(){
  const source = coverageMetrics.length ? coverageMetrics : allMetrics;
  document.querySelector('#kpiRepos').textContent = source.length;
  let passing = 0, attention = 0, totalStmts = 0, coveredStmts = 0;
  for(const m of source){
    if(m.status === 'passed') passing++;
    else if(m.status === 'warning' || m.status === 'failed') attention++;
    if(m.metadata && m.metadata.statements){
      totalStmts += m.metadata.statements;
      coveredStmts += m.metadata.covered;
    }
  }
  document.querySelector('#kpiPassing').textContent = passing;
  document.querySelector('#kpiAttention').textContent = attention;
  if(!source.length || totalStmts === 0){
    document.querySelector('#kpiFleet').textContent = '—';
  } else {
    document.querySelector('#kpiFleet').textContent = (coveredStmts / totalStmts * 100).toFixed(1) + '%';
  }

  // Worktree KPIs
  let activeWt = 0, abandonedWt = 0;
  for(const w of worktreesList){
    if(w.owner_state === 'active') activeWt++;
    else abandonedWt++;
  }
  document.querySelector('#kpiActiveWt').textContent = activeWt;
  document.querySelector('#kpiAbandonedWt').textContent = abandonedWt;
}

function renderTabs(){
  const tabsContainer = document.querySelector('#tabs');
  const tabsList = [{type: 'summary', title: '📊 Summary'}, ...types];
  tabsContainer.innerHTML = tabsList.map(t =>
    '<div class="tab '+(t.type === currentType ? 'active' : '')+'" onclick="selectType(\''+esc(t.type)+'\')">'+esc(t.title)+'</div>'
  ).join('');
}

function selectType(t){
  currentType = t;
  const url = new URL(window.location);
  if(t === 'summary'){
    url.searchParams.delete('type');
  } else {
    url.searchParams.set('type', t);
  }
  window.history.replaceState({}, '', url);
  renderTabs();
  applyView();
}

function applyView(){
  const summaryEl = document.querySelector('#summaryView');
  const tableEl = document.querySelector('#tableView');
  if(currentType === 'summary'){
    summaryEl.style.display = 'block';
    tableEl.style.display = 'none';
    renderSummary();
  } else {
    summaryEl.style.display = 'none';
    tableEl.style.display = 'block';
    renderTable();
  }
}

function onFilterChange(v){
  filterText = v;
  if(currentType === 'summary'){
    renderSummary();
  } else {
    renderTable();
  }
}

async function fetchOverview(){
  try {
    const res = await fetch('/api/v1/overview');
    if(res.ok){
      const data = await res.json();
      if(data.worktrees){
        worktreesList = data.worktrees;
      }
      if(data.machine){
        document.querySelector('#machine').textContent = data.machine.name + ' · ' + data.machine.wb_version;
      }
    }
  } catch(e){}
}

async function fetchHealth(){
  try {
    const res = await fetch('/api/v1/health');
    if(res.ok){
      const data = await res.json();
      document.querySelector('#machine').textContent = data.machine + ' · ' + data.wb_version;
    }
  } catch(e){}
}

async function fetchTypes(){
  try {
    const res = await fetch('/v0/workbench/metrics/types');
    if(res.ok){
      types = await res.json();
    } else {
      types = [{type: 'test_coverage', title: 'Test Coverage', dimension_label: 'Package'}];
    }
  } catch(e){
    types = [{type: 'test_coverage', title: 'Test Coverage', dimension_label: 'Package'}];
  }
  renderTabs();
}

async function fetchMetrics(){
  try {
    document.querySelector('#error').style.display = 'none';

    // Fetch worktrees and machine health from overview
    await fetchOverview();

    // Always fetch coverage metrics for KPIs and Summary
    const covRes = await fetch('/v0/workbench/metrics?type=test_coverage');
    if(covRes.ok){
      coverageMetrics = await covRes.json();
    }

    if(currentType !== 'summary' && currentType !== 'test_coverage'){
      const res = await fetch('/v0/workbench/metrics?type=' + encodeURIComponent(currentType));
      if(res.ok){
        allMetrics = await res.json();
      }
    } else {
      allMetrics = coverageMetrics;
    }

    updateKPIs();
    applyView();
    document.querySelector('#updated').textContent = 'Updated at ' + new Date().toLocaleTimeString();
  } catch(err){
    const errEl = document.querySelector('#error');
    errEl.textContent = 'Failed to load metrics: ' + err.message;
    errEl.style.display = 'block';
  }
}

async function init(){
  await fetchHealth();
  await fetchTypes();
  await fetchMetrics();
}

init();
setInterval(fetchMetrics, 15000);
</script></body></html>`
