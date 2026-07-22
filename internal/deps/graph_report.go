package deps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Markdown renders canonical counts and every manifest evidence row.
func (graph Graph) Markdown() string {
	var output strings.Builder
	output.WriteString("# WB dependency graph\n\n")
	fmt.Fprintf(&output, "- Ecosystem: `%s`\n", graph.Ecosystem)
	fmt.Fprintf(&output, "- Base ref: `%s`\n", graph.BaseRef)
	fmt.Fprintf(&output, "- Repositories: `%d`\n", graph.Summary.Repositories)
	fmt.Fprintf(&output, "- Modules: `%d`\n", graph.Summary.Modules)
	fmt.Fprintf(&output, "- Requirements: `%d` (`%d` internal)\n", graph.Summary.Requirements, graph.Summary.InternalRequirements)
	fmt.Fprintf(&output, "- External dependencies: `%d`\n", graph.Summary.ExternalDependencies)
	fmt.Fprintf(&output, "- Ambiguous internal providers: `%d`\n", graph.Summary.AmbiguousProviders)
	fmt.Fprintf(&output, "- Observed dependency/version selections: `%d`\n", graph.Summary.Selections)
	if len(graph.Filters.Dependencies) > 0 {
		fmt.Fprintf(&output, "- Dependency filters: `%s`\n", strings.Join(graph.Filters.Dependencies, "`, `"))
	}
	output.WriteString("\n## Requirement evidence\n\n")
	output.WriteString("| Dependency | Version | Consumer repository | Consumer module | Manifest | Kind | Provider repository | Code graph |\n")
	output.WriteString("|---|---|---|---|---|---|---|---|\n")
	for _, requirement := range graph.Requirements {
		kind := "direct"
		if requirement.Indirect {
			kind = "indirect"
		}
		provider := requirement.ProviderRepository
		if len(requirement.ProviderCandidates) > 0 {
			if provider == "" {
				provider = "ambiguous: " + strings.Join(requirement.ProviderCandidates, ", ")
			} else {
				provider += "; declarations: " + strings.Join(requirement.ProviderCandidates, ", ")
			}
		} else if provider == "" {
			provider = "external"
		}
		_, codeGrapherURL := graphRepositoryLinks(requirement.ConsumerRepository)
		codeGraph := "—"
		if codeGrapherURL != "" {
			codeGraph = "[inspect consumer](" + codeGrapherURL + ")"
		}
		fmt.Fprintf(&output, "| `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | %s |\n",
			requirement.Dependency, requirement.Version, requirement.ConsumerRepository,
			requirement.ConsumerModule, requirement.Manifest, kind, provider, codeGraph)
	}
	return output.String()
}

// YAML serializes the deterministic canonical evidence model.
func (graph Graph) YAML() ([]byte, error) { return yaml.Marshal(graph) }

// JSON serializes the deterministic canonical evidence model.
func (graph Graph) JSON() ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(true)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(graph); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// Output renders the requested stdout format.
func (graph Graph) Output(format string, view GraphView) ([]byte, error) {
	switch format {
	case "markdown":
		return []byte(graph.Markdown()), nil
	case "yaml":
		return graph.YAML()
	case "json":
		return graph.JSON()
	case "svg":
		return graph.SVG(view)
	case "html":
		return graph.HTML(view)
	default:
		return nil, fmt.Errorf("unknown --format %q (want markdown, yaml, json, svg, or html)", format)
	}
}

// WriteGraphReports atomically writes every human, machine, and visual artifact.
func WriteGraphReports(directory string, graph Graph, view GraphView) (GraphReportPaths, error) {
	paths := GraphReportPaths{
		Markdown: filepath.Join(directory, "deps-graph.md"),
		YAML:     filepath.Join(directory, "deps-graph.yaml"),
		JSON:     filepath.Join(directory, "deps-graph.json"),
		SVG:      filepath.Join(directory, "deps-graph.svg"),
		HTML:     filepath.Join(directory, "deps-graph.html"),
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return paths, err
	}
	yamlContents, err := graph.YAML()
	if err != nil {
		return paths, err
	}
	jsonContents, err := graph.JSON()
	if err != nil {
		return paths, err
	}
	svgContents, err := graph.SVG(view)
	if err != nil {
		return paths, err
	}
	htmlContents, err := graph.HTML(view)
	if err != nil {
		return paths, err
	}
	artifacts := []struct {
		path     string
		contents []byte
	}{
		{paths.Markdown, []byte(graph.Markdown())},
		{paths.YAML, yamlContents},
		{paths.JSON, jsonContents},
		{paths.SVG, svgContents},
		{paths.HTML, htmlContents},
	}
	for _, artifact := range artifacts {
		if err := writeAtomic(artifact.path, artifact.contents, 0o644); err != nil {
			return paths, err
		}
	}
	return paths, nil
}

// HTML renders all projections into one self-contained interactive document.
func (graph Graph) HTML(defaultView GraphView) ([]byte, error) {
	if _, err := ParseGraphView(string(defaultView)); err != nil {
		return nil, err
	}
	views := []GraphView{GraphViewRepositories, GraphViewDependencies, GraphViewSelections}
	svgs := map[GraphView][]byte{}
	for _, view := range views {
		svg, err := graph.SVG(view)
		if err != nil {
			return nil, err
		}
		svgs[view] = svg
	}
	organizationSet := map[string]bool{}
	for _, repository := range graph.Repositories {
		if repository.Organization != "" {
			organizationSet[repository.Organization] = true
		}
	}
	organizations := make([]string, 0, len(organizationSet))
	for organization := range organizationSet {
		organizations = append(organizations, organization)
	}
	sort.Strings(organizations)
	var output strings.Builder
	output.WriteString(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>WB dependency graph</title><style>
:root{color-scheme:light dark;font-family:Inter,ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#eef2ff;color:#0f172a}*{box-sizing:border-box}body{margin:0;background:linear-gradient(145deg,#eef2ff 0,#f8fafc 42%,#ecfeff 100%);min-height:100vh}.shell{max-width:1680px;margin:auto;padding:28px}.header{display:flex;justify-content:space-between;gap:24px;align-items:end;margin-bottom:18px}.eyebrow{text-transform:uppercase;letter-spacing:.14em;font-size:12px;font-weight:750;color:#4f46e5}h1{font-size:clamp(28px,4vw,48px);margin:5px 0 4px;letter-spacing:-.04em}.lede{margin:0;color:#475569}.summary{display:grid;grid-template-columns:repeat(6,minmax(110px,1fr));gap:10px;margin:20px 0}.metric{background:rgba(255,255,255,.8);border:1px solid #cbd5e1;border-radius:14px;padding:14px;box-shadow:0 6px 24px rgba(15,23,42,.05)}.metric b{display:block;font-size:24px}.metric span{font-size:12px;color:#64748b}.toolbar{display:flex;flex-wrap:wrap;gap:10px;align-items:center;background:#fff;border:1px solid #cbd5e1;border-radius:14px;padding:10px;margin-bottom:12px}.tabs{display:flex;gap:4px}.tabs button,.tool,.organization{border:1px solid #cbd5e1;background:#f8fafc;color:#334155;border-radius:9px;padding:8px 12px;font:inherit;cursor:pointer}.tabs button[aria-selected=true]{background:#1d4ed8;color:#fff;border-color:#1d4ed8}.search{min-width:230px;flex:1;border:1px solid #cbd5e1;border-radius:9px;padding:9px 11px;font:inherit;background:#fff;color:#0f172a}.spacer{flex:1}.workspace{display:grid;grid-template-columns:minmax(0,1fr) 300px;gap:12px}.graph-frame{background:#f8fafc;border:1px solid #cbd5e1;border-radius:18px;box-shadow:0 18px 60px rgba(15,23,42,.10);overflow:auto;min-height:520px;cursor:grab}.graph-frame.dragging{cursor:grabbing;user-select:none}.graph-panel{display:none}.graph-panel.active{display:block}.graph-panel svg{display:block;width:calc(100% * var(--zoom,1));min-width:calc(900px * var(--zoom,1));height:auto}.inspector{background:rgba(255,255,255,.9);border:1px solid #cbd5e1;border-radius:18px;padding:20px;box-shadow:0 18px 60px rgba(15,23,42,.08);align-self:start;position:sticky;top:12px}.inspector h2{font-size:18px;margin:4px 0 16px;overflow-wrap:anywhere}.inspector-label{margin:0;color:#4f46e5;font-size:11px;font-weight:750;text-transform:uppercase;letter-spacing:.12em}.inspector dl{display:grid;gap:10px;margin:0}.inspector dl div{display:grid;grid-template-columns:88px 1fr;gap:8px}.inspector dt{color:#64748b;font-size:11px;text-transform:uppercase}.inspector dd{margin:0;font-size:12px;overflow-wrap:anywhere}.inspector-actions{display:grid;gap:8px;margin-top:18px}.inspector-actions a{display:block;border:1px solid #cbd5e1;border-radius:9px;padding:9px 11px;text-decoration:none;color:#1d4ed8;background:#f8fafc}.inspector-actions a.primary{background:#1d4ed8;color:#fff;border-color:#1d4ed8}.inspector-actions a[hidden]{display:none}.evidence{margin-top:22px;background:rgba(255,255,255,.85);border:1px solid #cbd5e1;border-radius:16px;padding:18px;overflow:auto}table{width:100%;border-collapse:collapse;font-size:13px}th,td{text-align:left;padding:8px;border-bottom:1px solid #e2e8f0;white-space:nowrap}th{color:#475569}.hint{font-size:12px;color:#64748b}.node.dim,.node.filtered,.edges>g.dim,.edges>g.filtered{opacity:.1}.node.highlight,.edges>g.highlight{opacity:1!important}.hide-current .node:not(.behind),.hide-current .edges>g:not(:has(.behind)){opacity:.12}@media(max-width:1100px){.workspace{grid-template-columns:1fr}.inspector{position:static;display:grid;grid-template-columns:1fr 1fr;gap:16px}.inspector-actions{margin-top:0}}@media(max-width:900px){.summary{grid-template-columns:repeat(3,1fr)}}@media(max-width:760px){.shell{padding:16px}.header{display:block}.summary{grid-template-columns:repeat(2,1fr)}.toolbar{align-items:stretch}.tabs{overflow:auto}.graph-frame{min-height:400px}.inspector{display:block}.inspector-actions{margin-top:18px}.evidence{padding:12px}}@media(prefers-color-scheme:dark){:root{background:#0f172a;color:#e2e8f0}body{background:linear-gradient(145deg,#111827,#0f172a 55%,#082f49)}.metric,.toolbar,.evidence,.inspector{background:rgba(15,23,42,.88);border-color:#334155}.lede,.hint,.metric span,th,.inspector dt{color:#94a3b8}.search,.tabs button,.tool,.organization,.inspector-actions a{background:#1e293b;color:#e2e8f0;border-color:#475569}.inspector-actions a.primary{background:#2563eb;color:#fff}.graph-frame{border-color:#334155}}
</style></head><body><main class="shell"><header class="header"><div><div class="eyebrow">WB · Workbench</div><h1>Dependency graph</h1><p class="lede">Providers flow toward consumers. Every edge retains its manifest evidence.</p></div><div class="hint">Base: origin/`)
	output.WriteString(html.EscapeString(graph.BaseRef))
	output.WriteString(` · Ecosystem: `)
	output.WriteString(html.EscapeString(string(graph.Ecosystem)))
	output.WriteString(`</div></header><section class="summary" aria-label="Graph summary">`)
	metrics := []struct {
		value int
		label string
	}{
		{graph.Summary.Repositories, "repositories"}, {graph.Summary.Modules, "modules"},
		{graph.Summary.Requirements, "requirements"}, {graph.Summary.Selections, "version selections"},
		{graph.Summary.ExternalDependencies, "external dependencies"}, {graph.Summary.AmbiguousProviders, "ambiguous providers"},
	}
	for _, metric := range metrics {
		fmt.Fprintf(&output, `<div class="metric"><b>%d</b><span>%s</span></div>`, metric.value, html.EscapeString(metric.label))
	}
	output.WriteString(`</section><section class="toolbar" aria-label="Graph controls"><div class="tabs" role="tablist">`)
	for _, view := range views {
		selected := view == defaultView
		fmt.Fprintf(&output, `<button type="button" role="tab" data-select-view="%s" aria-selected="%t">%s</button>`, view, selected, graphViewLabel(view))
	}
	output.WriteString(`</div><input class="search" type="search" placeholder="Search nodes…" aria-label="Search graph nodes"><label class="hint"><input type="checkbox" class="outdated"> Highlight behind</label><label class="hint">Highlight organization <select class="organization"><option value="">All</option>`)
	for _, organization := range organizations {
		fmt.Fprintf(&output, `<option value="%s">%s</option>`, html.EscapeString(organization), html.EscapeString(organization))
	}
	output.WriteString(`</select></label><span class="spacer"></span><button class="tool zoom-out" type="button" aria-label="Zoom out">−</button><button class="tool fit" type="button">Fit</button><button class="tool reset" type="button">Reset</button><button class="tool zoom-in" type="button" aria-label="Zoom in">+</button></section><div class="workspace"><div class="graph-frame" aria-label="Scrollable dependency graph">`)
	for _, view := range views {
		class := "graph-panel"
		if view == defaultView {
			class += " active"
		}
		fmt.Fprintf(&output, `<section class="%s" data-panel-view="%s">`, class, view)
		output.Write(svgs[view])
		output.WriteString(`</section>`)
	}
	output.WriteString(`</div><aside class="inspector" aria-live="polite"><div><p class="inspector-label">Selected node</p><h2 data-inspector-label>Select a graph node</h2><dl><div><dt>Kind</dt><dd data-inspector-kind>—</dd></div><div><dt>Status</dt><dd data-inspector-status>—</dd></div><div><dt>Repository</dt><dd data-inspector-repository>—</dd></div><div><dt>Dependency</dt><dd data-inspector-dependency>—</dd></div><div><dt>Version</dt><dd data-inspector-version>—</dd></div><div><dt>Connected</dt><dd data-inspector-connected>—</dd></div></dl></div><div class="inspector-actions"><a data-codegrapher-link class="primary" target="_blank" rel="noopener" hidden>Explore code in CodeGrapher ↗</a><a data-github-link target="_blank" rel="noopener" hidden>Open repository on GitHub ↗</a><p class="hint">External links open only after an explicit click. This report never publishes or indexes repository data.</p></div></aside></div><p class="hint">Click or focus a node to highlight its complete upstream and downstream path. Drag or scroll the canvas to pan. “Fleet-highest” compares only versions observed in this report.</p><details class="evidence"><summary><b>Canonical requirement evidence</b></summary><table><thead><tr><th>Dependency</th><th>Version</th><th>Consumer</th><th>Module</th><th>Manifest</th><th>Kind</th><th>Code graph</th></tr></thead><tbody>`)
	for _, requirement := range graph.Requirements {
		kind := "direct"
		if requirement.Indirect {
			kind = "indirect"
		}
		_, codeGrapherURL := graphRepositoryLinks(requirement.ConsumerRepository)
		fmt.Fprintf(&output, `<tr><td><code>%s</code></td><td><code>%s</code></td><td>%s</td><td><code>%s</code></td><td><code>%s</code></td><td>%s</td><td><a href="%s" target="_blank" rel="noopener">inspect consumer ↗</a></td></tr>`,
			html.EscapeString(requirement.Dependency), html.EscapeString(requirement.Version), html.EscapeString(requirement.ConsumerRepository),
			html.EscapeString(requirement.ConsumerModule), html.EscapeString(requirement.Manifest), kind, html.EscapeString(codeGrapherURL))
	}
	output.WriteString(`</tbody></table></details></main><script>
(()=>{
  let scale=1;
  const panels=[...document.querySelectorAll('.graph-panel')];
  const frame=document.querySelector('.graph-frame');
  const search=document.querySelector('.search');
  const organization=document.querySelector('.organization');
  const outdated=document.querySelector('.outdated');
  const active=()=>document.querySelector('.graph-panel.active');
  const details={
    label:document.querySelector('[data-inspector-label]'),kind:document.querySelector('[data-inspector-kind]'),
    status:document.querySelector('[data-inspector-status]'),repository:document.querySelector('[data-inspector-repository]'),
    dependency:document.querySelector('[data-inspector-dependency]'),version:document.querySelector('[data-inspector-version]'),
    connected:document.querySelector('[data-inspector-connected]'),github:document.querySelector('[data-github-link]'),
    codegrapher:document.querySelector('[data-codegrapher-link]')
  };
  function resetHighlight(){active().querySelectorAll('.node,.edges>g').forEach(e=>e.classList.remove('dim','highlight','selected'))}
  function clearInspector(){details.label.textContent='Select a graph node';for(const key of ['kind','status','repository','dependency','version','connected'])details[key].textContent='—';for(const link of [details.github,details.codegrapher]){link.hidden=true;link.removeAttribute('href')}}
  function showDetails(node,connected){details.label.textContent=node.querySelector('.label')?.textContent||'Graph node';details.kind.textContent=node.dataset.kind||'—';details.status.textContent=node.dataset.status||'—';details.repository.textContent=node.dataset.repository||'—';details.dependency.textContent=node.dataset.dependency||'—';details.version.textContent=node.dataset.version||'—';details.connected.textContent=connected==null?'—':String(connected);for(const [link,value] of [[details.github,node.dataset.githubUrl],[details.codegrapher,node.dataset.codegrapherUrl]]){link.hidden=!value;if(value)link.href=value;else link.removeAttribute('href')}}
  function updateZoom(){active().style.setProperty('--zoom',String(scale))}
  function applyFilters(){resetHighlight();const query=search.value.trim().toLowerCase(),org=organization.value;const nodes=[...active().querySelectorAll('.node')],byID=new Map(nodes.map(n=>[n.dataset.nodeId,n]));for(const node of nodes){const searchMismatch=query&&!node.dataset.search.includes(query);const orgMismatch=org&&node.dataset.organization&&node.dataset.organization!==org;node.classList.toggle('filtered',Boolean(searchMismatch||orgMismatch))}active().querySelectorAll('.edges>g').forEach(edge=>edge.classList.toggle('filtered',Boolean(byID.get(edge.dataset.from)?.classList.contains('filtered')||byID.get(edge.dataset.to)?.classList.contains('filtered'))))}
  function choose(view){panels.forEach(p=>p.classList.toggle('active',p.dataset.panelView===view));document.querySelectorAll('[data-select-view]').forEach(b=>b.setAttribute('aria-selected',String(b.dataset.selectView===view)));scale=1;updateZoom();frame.scrollTo({left:0,top:0});clearInspector();applyFilters()}
  function highlight(id){resetHighlight();const panel=active(),nodes=[...panel.querySelectorAll('.node')],edges=[...panel.querySelectorAll('.edges>g')],seen=new Set([id]);let changed=true;while(changed){changed=false;for(const edge of edges){const a=edge.dataset.from,b=edge.dataset.to;if(seen.has(a)||seen.has(b)){if(!seen.has(a)){seen.add(a);changed=true}if(!seen.has(b)){seen.add(b);changed=true}}}}for(const node of nodes)node.classList.add(seen.has(node.dataset.nodeId)?'highlight':'dim');for(const edge of edges)edge.classList.add(seen.has(edge.dataset.from)&&seen.has(edge.dataset.to)?'highlight':'dim');const selected=nodes.find(n=>n.dataset.nodeId===id);if(selected){selected.classList.add('selected');showDetails(selected,seen.size-1)}}
  function bind(){document.querySelectorAll('.node').forEach(node=>{node.addEventListener('focus',()=>highlight(node.dataset.nodeId));node.addEventListener('click',event=>{event.preventDefault();highlight(node.dataset.nodeId)});node.addEventListener('keydown',event=>{if(event.key==='Enter'||event.key===' '){event.preventDefault();highlight(node.dataset.nodeId)}})})}
  document.querySelectorAll('[data-select-view]').forEach(button=>button.addEventListener('click',()=>choose(button.dataset.selectView)));
  search.addEventListener('input',applyFilters);organization.addEventListener('change',applyFilters);
  outdated.addEventListener('change',event=>panels.forEach(panel=>panel.classList.toggle('hide-current',event.target.checked)));
  document.querySelector('.zoom-in').addEventListener('click',()=>{scale=Math.min(2.5,scale+.2);updateZoom()});
  document.querySelector('.zoom-out').addEventListener('click',()=>{scale=Math.max(.6,scale-.2);updateZoom()});
  document.querySelector('.fit').addEventListener('click',()=>{scale=1;updateZoom();frame.scrollTo({left:0,top:0})});
  document.querySelector('.reset').addEventListener('click',()=>{scale=1;search.value='';organization.value='';outdated.checked=false;panels.forEach(panel=>panel.classList.remove('hide-current'));updateZoom();frame.scrollTo({left:0,top:0});clearInspector();applyFilters()});
  let drag=null;
  frame.addEventListener('pointerdown',event=>{if(event.target.closest('.node,a,button,input,select'))return;drag={id:event.pointerId,x:event.clientX,y:event.clientY,left:frame.scrollLeft,top:frame.scrollTop};frame.setPointerCapture(event.pointerId);frame.classList.add('dragging')});
  frame.addEventListener('pointermove',event=>{if(!drag||drag.id!==event.pointerId)return;frame.scrollLeft=drag.left-(event.clientX-drag.x);frame.scrollTop=drag.top-(event.clientY-drag.y)});
  const stopDrag=event=>{if(!drag||drag.id!==event.pointerId)return;drag=null;frame.classList.remove('dragging')};frame.addEventListener('pointerup',stopDrag);frame.addEventListener('pointercancel',stopDrag);
  document.addEventListener('keydown',event=>{if(event.key==='/'&&document.activeElement!==search){event.preventDefault();search.focus()}});
  bind();clearInspector();applyFilters();
})();
</script></body></html>`)
	return []byte(output.String()), nil
}

func graphViewLabel(view GraphView) string {
	switch view {
	case GraphViewRepositories:
		return "Repositories"
	case GraphViewDependencies:
		return "Dependencies"
	case GraphViewSelections:
		return "Versions"
	default:
		return string(view)
	}
}
