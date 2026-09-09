package dashboard

const indexHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>WB operations</title>
  <style>
    :root{color-scheme:light dark;--bg:#f3f5f7;--panel:#fff;--ink:#17202a;--muted:#667085;--line:#dce2e8;--accent:#2457d6;--good:#16794b;--bad:#c33d3d;--shadow:0 10px 30px rgba(20,31,50,.08)}
    @media(prefers-color-scheme:dark){:root{--bg:#101419;--panel:#171d24;--ink:#edf2f7;--muted:#9aa7b5;--line:#2b3541;--accent:#7fa2ff;--good:#5bd49b;--bad:#ff8585;--shadow:0 12px 32px rgba(0,0,0,.25)}}
    *{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.45 ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}main{width:min(1180px,calc(100% - 32px));margin:34px auto 64px}header{display:flex;justify-content:space-between;align-items:flex-end;gap:20px;margin-bottom:22px}h1{font-size:30px;line-height:1;margin:0 0 8px;letter-spacing:-.03em}h2{font-size:16px;margin:0 0 14px}.sub{color:var(--muted)}.machine{padding:7px 11px;border:1px solid var(--line);border-radius:999px;background:var(--panel);white-space:nowrap}.cards{display:grid;grid-template-columns:repeat(5,1fr);gap:12px;margin-bottom:18px}.card,.panel{background:var(--panel);border:1px solid var(--line);border-radius:14px;box-shadow:var(--shadow)}.card{padding:16px}.label{color:var(--muted);font-size:12px;text-transform:uppercase;letter-spacing:.07em}.value{font-size:25px;font-weight:700;margin-top:5px}.grid{display:grid;grid-template-columns:1.25fr .75fr;gap:18px}.panel{padding:18px;min-width:0}table{width:100%;border-collapse:collapse}th{text-align:left;color:var(--muted);font-size:11px;text-transform:uppercase;letter-spacing:.06em;font-weight:600}th,td{padding:10px 8px;border-bottom:1px solid var(--line)}tr:last-child td{border-bottom:0}.repo{font-weight:650}.branch{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;font-size:12px}.pill{display:inline-flex;align-items:center;gap:6px}.dot{width:7px;height:7px;border-radius:50%;background:var(--good)}.dot.bad{background:var(--bad)}.empty{padding:24px 8px;color:var(--muted);text-align:center}.error{display:none;background:#7b1f1f;color:#fff;padding:11px 14px;border-radius:10px;margin-bottom:15px}footer{color:var(--muted);margin-top:16px;font-size:12px}@media(max-width:900px){.cards{grid-template-columns:repeat(2,1fr)}.grid{grid-template-columns:1fr}}@media(max-width:560px){header{align-items:flex-start;flex-direction:column}.cards{grid-template-columns:1fr 1fr}main{width:min(100% - 20px,1180px)}}
  </style>
</head>
<body><main>
  <header><div><h1>WB operations</h1><div class="sub">Worktrees, governed command cost, and machine health</div></div><div id="machine" class="machine">Connecting…</div></header>
  <div id="error" class="error"></div>
  <section class="cards">
    <div class="card"><div class="label">Worktrees</div><div id="worktrees" class="value">—</div></div>
    <div class="card"><div class="label">Operations · 14d</div><div id="operations" class="value">—</div></div>
    <div class="card"><div class="label">Failures</div><div id="failures" class="value">—</div></div>
    <div class="card"><div class="label">Wall time</div><div id="wall" class="value">—</div></div>
    <div class="card"><div class="label">CPU time</div><div id="cpu" class="value">—</div></div>
  </section>
  <section class="grid">
    <div class="panel"><h2>Active worktrees</h2><div id="worktreeTable"></div></div>
    <div class="panel"><h2>Command cost</h2><div id="kindTable"></div></div>
  </section>
  <footer id="updated">Waiting for the first snapshot.</footer>
</main>
<script>
const esc=v=>String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
const duration=ms=>{if(!ms)return '0s';if(ms<1000)return ms+'ms';if(ms<60000)return (ms/1000).toFixed(ms<10000?1:0)+'s';return (ms/60000).toFixed(1)+'m'};
const age=s=>{if(!s)return 'now';if(s<3600)return Math.floor(s/60)+'m';if(s<86400)return Math.floor(s/3600)+'h';return Math.floor(s/86400)+'d'};
function worktrees(rows){if(!rows.length)return '<div class="empty">No managed worktrees found.</div>';return '<table><thead><tr><th>Repository</th><th>Task</th><th>Owner</th><th>Activity</th><th>Age</th></tr></thead><tbody>'+rows.map(w=>'<tr><td class="repo">'+esc(w.repository)+'</td><td><div>'+esc(w.task)+'</div><div class="branch">'+esc(w.branch)+'</div></td><td>'+esc(w.owner||'—')+'</td><td><span class="pill"><span class="dot '+(w.owner_state==='active'?'':'bad')+'"></span>'+esc(w.owner_state)+'</span></td><td>'+age(w.age_seconds)+'</td></tr>').join('')+'</tbody></table>'}
function kinds(rows){if(!rows.length)return '<div class="empty">Run commands through <span class="branch">wb run -- …</span> to collect cost.</div>';return '<table><thead><tr><th>Kind</th><th>Runs</th><th>P50</th><th>P95</th></tr></thead><tbody>'+rows.map(k=>'<tr><td class="repo">'+esc(k.kind)+'</td><td>'+k.operations+(k.failed?' · <span style="color:var(--bad)">'+k.failed+' failed</span>':'')+'</td><td>'+duration(k.p50_ms)+'</td><td>'+duration(k.p95_ms)+'</td></tr>').join('')+'</tbody></table>'}
async function refresh(){try{const r=await fetch('/api/v1/overview',{cache:'no-store'});if(!r.ok)throw new Error((await r.json()).message||r.statusText);const d=await r.json(),o=d.operations;document.querySelector('#error').style.display='none';document.querySelector('#machine').textContent=d.machine.name+' · '+d.machine.wb_version;document.querySelector('#worktrees').textContent=d.worktrees.length;document.querySelector('#operations').textContent=o.operations;document.querySelector('#failures').textContent=o.failed;document.querySelector('#wall').textContent=duration(o.wall_ms);document.querySelector('#cpu').textContent=duration(o.user_cpu_ms+o.system_cpu_ms);document.querySelector('#worktreeTable').innerHTML=worktrees(d.worktrees);document.querySelector('#kindTable').innerHTML=kinds(o.kinds);document.querySelector('#updated').textContent='Updated '+new Date(d.generated_at).toLocaleTimeString()+(d.diagnostics?' · '+d.diagnostics+' inventory diagnostics':'')}catch(e){const n=document.querySelector('#error');n.textContent='Dashboard refresh failed: '+e.message;n.style.display='block'}}
refresh();setInterval(refresh,10000);
</script></body></html>`
