package deps

import (
	"bytes"
	"fmt"
	"html"
	"sort"
	"strings"
	"unicode/utf8"
)

type graphPoint struct{ x, y float64 }

// SVG renders one deterministic projection as an accessible standalone SVG.
func (graph Graph) SVG(view GraphView) ([]byte, error) {
	projection, err := graph.Project(view)
	if err != nil {
		return nil, err
	}
	levels := graphProjectionLevels(projection)
	columns := map[int][]GraphProjectionNode{}
	maxLevel := 0
	for _, node := range projection.Nodes {
		level := levels[node.ID]
		columns[level] = append(columns[level], node)
		if level > maxLevel {
			maxLevel = level
		}
	}
	maxRows := 1
	positions := map[string]graphPoint{}
	for level, nodes := range columns {
		sort.Slice(nodes, func(i, j int) bool {
			if nodes[i].Label == nodes[j].Label {
				return nodes[i].ID < nodes[j].ID
			}
			return nodes[i].Label < nodes[j].Label
		})
		columns[level] = nodes
		if len(nodes) > maxRows {
			maxRows = len(nodes)
		}
		for index, node := range nodes {
			positions[node.ID] = graphPoint{x: 50 + float64(level)*410, y: 72 + float64(index)*104}
		}
	}
	width := 380 + float64(maxLevel)*410
	if width < 960 {
		width = 960
	}
	height := 160 + float64(maxRows)*104
	if height < 520 {
		height = 520
	}
	viewID := strings.ReplaceAll(string(view), "-", "_")
	var output bytes.Buffer
	fmt.Fprintf(&output, `<svg xmlns="http://www.w3.org/2000/svg" role="img" aria-labelledby="title_%s desc_%s" viewBox="0 0 %.0f %.0f" data-view="%s">`, viewID, viewID, width, height, html.EscapeString(string(view)))
	fmt.Fprintf(&output, `<title id="title_%s">WB dependency graph — %s view</title>`, viewID, html.EscapeString(string(view)))
	fmt.Fprintf(&output, `<desc id="desc_%s">Providers flow from left to consuming repositories on the right. The graph contains %d nodes and %d edges.</desc>`, viewID, len(projection.Nodes), len(projection.Edges))
	output.WriteString(`<defs><marker id="arrow_` + viewID + `" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M 0 0 L 10 5 L 0 10 z" fill="#64748b"/></marker></defs>`)
	output.WriteString(`<style>
svg{background:#f8fafc;color:#0f172a;font-family:Inter,ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}.edge{fill:none;stroke:#94a3b8;stroke-width:1.7;marker-end:url(#arrow_` + viewID + `)}.edge.indirect{stroke-dasharray:7 6}.edge.behind{stroke:#d97706;stroke-width:2.3}.edge-hit{fill:none;stroke:transparent;stroke-width:14}.node{cursor:pointer;outline:none}.node rect{fill:#fff;stroke:#cbd5e1;stroke-width:1.4;rx:12}.node.repository rect{fill:#eff6ff;stroke:#93c5fd}.node.dependency rect{fill:#f8fafc;stroke:#94a3b8}.node.selection rect{fill:#f5f3ff;stroke:#c4b5fd}.node.behind rect{fill:#fffbeb;stroke:#f59e0b}.node.fleet-highest rect{fill:#ecfdf5;stroke:#34d399}.node:focus rect,.node:hover rect,.node.selected rect{stroke:#2563eb;stroke-width:3}.label{font-size:13px;font-weight:650;fill:#0f172a}.subtitle{font-size:10.5px;fill:#64748b}.edge-label{font-size:10px;fill:#475569}.wave-label{font-size:10px;font-weight:700;letter-spacing:.12em;text-transform:uppercase;fill:#64748b}.legend{font-size:11px;fill:#475569}.dim{opacity:.15}.highlight{opacity:1!important}
</style>`)
	output.WriteString(`<g class="waves" aria-hidden="true">`)
	for level := 0; level <= maxLevel; level++ {
		label := fmt.Sprintf("Layer %02d", level)
		if view == GraphViewRepositories {
			label = fmt.Sprintf("Release wave %02d", level)
		}
		fmt.Fprintf(&output, `<text class="wave-label" x="%.0f" y="34" text-anchor="middle">%s</text>`,
			50+float64(level)*410+140, html.EscapeString(label))
	}
	output.WriteString(`</g>`)
	output.WriteString(`<g class="edges">`)
	for _, edge := range projection.Edges {
		from, fromOK := positions[edge.From]
		to, toOK := positions[edge.To]
		if !fromOK || !toOK {
			continue
		}
		path := graphEdgePath(from, to)
		class := "edge"
		if edge.DirectCount == 0 && edge.IndirectCount > 0 {
			class += " indirect"
		}
		if edge.Status == "behind" {
			class += " behind"
		}
		evidence := html.EscapeString(strings.Join(edge.Evidence, " | "))
		fmt.Fprintf(&output, `<g data-edge-id="%s" data-from="%s" data-to="%s" data-search="%s"><path class="edge-hit" d="%s"/><path class="%s" d="%s"><title>%s</title></path>`,
			html.EscapeString(edge.ID), html.EscapeString(edge.From), html.EscapeString(edge.To), evidence,
			path, class, path, evidence)
		if count := edge.DirectCount + edge.IndirectCount; count > 1 {
			fmt.Fprintf(&output, `<text class="edge-label" x="%.0f" y="%.0f">%d</text>`, (from.x+to.x+280)/2, (from.y+to.y+42)/2-5, count)
		}
		output.WriteString(`</g>`)
	}
	output.WriteString(`</g><g class="nodes">`)
	for _, node := range projection.Nodes {
		point := positions[node.ID]
		class := "node " + node.Kind
		if node.Status != "" && node.Status != "normal" && node.Status != "selected" {
			class += " " + node.Status
		}
		search := strings.Join([]string{node.Label, node.Subtitle, node.Repository, node.Dependency, node.Version}, " ")
		fmt.Fprintf(&output, `<g id="%s_%s" class="%s" transform="translate(%.0f %.0f)" tabindex="0" role="button" aria-label="%s, %s" data-node-id="%s" data-kind="%s" data-status="%s" data-repository="%s" data-organization="%s" data-dependency="%s" data-version="%s" data-github-url="%s" data-codegrapher-url="%s" data-search="%s">`,
			viewID, html.EscapeString(node.ID), html.EscapeString(class), point.x, point.y,
			html.EscapeString(node.Label), html.EscapeString(node.Subtitle), html.EscapeString(node.ID),
			html.EscapeString(node.Kind), html.EscapeString(node.Status), html.EscapeString(node.Repository),
			html.EscapeString(node.Organization), html.EscapeString(node.Dependency), html.EscapeString(node.Version),
			html.EscapeString(node.GitHubURL), html.EscapeString(node.CodeGrapherURL), html.EscapeString(strings.ToLower(search)))
		fmt.Fprintf(&output, `<title>%s — %s</title><rect width="280" height="74"/><text class="label" x="14" y="29">%s</text><text class="subtitle" x="14" y="52">%s</text></g>`,
			html.EscapeString(node.Label), html.EscapeString(node.Subtitle), html.EscapeString(graphTruncate(node.Label, 38)), html.EscapeString(graphTruncate(node.Subtitle, 46)))
	}
	output.WriteString(`</g>`)
	legendY := height - 38
	fmt.Fprintf(&output, `<g class="legend" transform="translate(50 %.0f)"><rect x="0" y="-16" width="16" height="16" rx="4" fill="#ecfdf5" stroke="#34d399"/><text x="24" y="-4">fleet-highest observed</text><rect x="185" y="-16" width="16" height="16" rx="4" fill="#fffbeb" stroke="#f59e0b"/><text x="209" y="-4">behind fleet-highest</text><line x1="390" x2="430" y1="-8" y2="-8" stroke="#94a3b8" stroke-width="2"/><text x="438" y="-4">direct</text><line x1="500" x2="540" y1="-8" y2="-8" stroke="#94a3b8" stroke-width="2" stroke-dasharray="7 6"/><text x="548" y="-4">indirect</text></g>`, legendY)
	output.WriteString(`</svg>`)
	return output.Bytes(), nil
}

func graphEdgePath(from, to graphPoint) string {
	startX, startY := from.x+280, from.y+37
	endX, endY := to.x, to.y+37
	if endX > startX {
		control := (startX + endX) / 2
		return fmt.Sprintf("M %.0f %.0f C %.0f %.0f, %.0f %.0f, %.0f %.0f", startX, startY, control, startY, control, endY, endX, endY)
	}
	loopX := startX + 70
	return fmt.Sprintf("M %.0f %.0f C %.0f %.0f, %.0f %.0f, %.0f %.0f", startX, startY, loopX, startY-52, loopX, endY+52, endX, endY)
}

func graphTruncate(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "…"
}

func graphProjectionLevels(projection GraphProjection) map[string]int {
	adjacency := map[string][]string{}
	for _, node := range projection.Nodes {
		adjacency[node.ID] = nil
	}
	for _, edge := range projection.Edges {
		adjacency[edge.From] = append(adjacency[edge.From], edge.To)
	}
	for node := range adjacency {
		sort.Strings(adjacency[node])
	}
	index := 0
	indexes := map[string]int{}
	lowLink := map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	var components [][]string
	var connect func(string)
	connect = func(node string) {
		indexes[node] = index
		lowLink[node] = index
		index++
		stack = append(stack, node)
		onStack[node] = true
		for _, next := range adjacency[node] {
			if _, visited := indexes[next]; !visited {
				connect(next)
				if lowLink[next] < lowLink[node] {
					lowLink[node] = lowLink[next]
				}
			} else if onStack[next] && indexes[next] < lowLink[node] {
				lowLink[node] = indexes[next]
			}
		}
		if lowLink[node] != indexes[node] {
			return
		}
		var component []string
		for {
			last := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[last] = false
			component = append(component, last)
			if last == node {
				break
			}
		}
		sort.Strings(component)
		components = append(components, component)
	}
	nodeIDs := make([]string, 0, len(adjacency))
	for node := range adjacency {
		nodeIDs = append(nodeIDs, node)
	}
	sort.Strings(nodeIDs)
	for _, node := range nodeIDs {
		if _, visited := indexes[node]; !visited {
			connect(node)
		}
	}
	componentByNode := map[string]int{}
	for component, nodes := range components {
		for _, node := range nodes {
			componentByNode[node] = component
		}
	}
	componentEdges := map[int]map[int]bool{}
	indegree := make([]int, len(components))
	for from, nextNodes := range adjacency {
		fromComponent := componentByNode[from]
		for _, to := range nextNodes {
			toComponent := componentByNode[to]
			if fromComponent == toComponent {
				continue
			}
			if componentEdges[fromComponent] == nil {
				componentEdges[fromComponent] = map[int]bool{}
			}
			if !componentEdges[fromComponent][toComponent] {
				componentEdges[fromComponent][toComponent] = true
				indegree[toComponent]++
			}
		}
	}
	levels := make([]int, len(components))
	var ready []int
	for component := range components {
		if indegree[component] == 0 {
			ready = append(ready, component)
		}
	}
	sort.Ints(ready)
	for len(ready) > 0 {
		component := ready[0]
		ready = ready[1:]
		var targets []int
		for target := range componentEdges[component] {
			targets = append(targets, target)
		}
		sort.Ints(targets)
		for _, target := range targets {
			if levels[target] < levels[component]+1 {
				levels[target] = levels[component] + 1
			}
			indegree[target]--
			if indegree[target] == 0 {
				ready = append(ready, target)
				sort.Ints(ready)
			}
		}
	}
	result := map[string]int{}
	for node, component := range componentByNode {
		result[node] = levels[component]
	}
	return result
}
