package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/gitops"
)

type resultGroup struct {
	label, section string
	results        []fleetsync.Result
}

// summaryItem adapts one row from the printed sync summary to a navigable
// list item. The result set behind the row is rendered in the detail panel.
type summaryItem struct{ resultGroup }

func (i summaryItem) Title() string { return fmt.Sprintf("%-20s%d", i.label, len(i.results)) }

func (i summaryItem) Description() string {
	return i.section + " · " + pluralRepositories(len(i.results))
}

func (i summaryItem) FilterValue() string { return i.label + " " + i.section }

const (
	resultsPanelGap      = 3
	resultsStackedGap    = 1
	resultsMinSplitWidth = 88
	resultsFrameWidth    = 4 // two border columns plus one padding column per side
	resultsFrameHeight   = 2 // top and bottom borders
)

// ResultsModel is a persistent split-pane review screen: the left panel is a
// navigable summary and the right panel follows its current selection.
type ResultsModel struct {
	list          list.Model
	detail        viewport.Model
	width, height int
	selected      string
}

// NewResultsModel turns the final sync summary itself into the left-hand
// navigation. Selecting a count shows every repository contributing to it on
// the right, including successful/no-op categories rather than only failures.
func NewResultsModel(results []fleetsync.Result) ResultsModel {
	groups := resultGroups(results)
	items := make([]list.Item, 0, len(groups))
	for _, group := range groups {
		items = append(items, summaryItem{group})
	}
	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Summary"
	detail := viewport.New()
	detail.SoftWrap = true
	detail.FillHeight = true
	m := ResultsModel{list: l, detail: detail}
	m.syncDetail(true)
	return m
}

// Reviewable reports whether a result belongs in the drill-down list.
func Reviewable(r fleetsync.Result) bool {
	switch r.Status {
	case fleetsync.Failed, fleetsync.SkippedDirty, fleetsync.KeptArchived,
		fleetsync.Diverged, fleetsync.NoUpstream,
		fleetsync.Unpushed, fleetsync.ArchivedUnlandable:
		return true
	default:
		return false
	}
}

func resultGroups(results []fleetsync.Result) []resultGroup {
	group := func(label, section string, match func(fleetsync.Result) bool) resultGroup {
		matched := make([]fleetsync.Result, 0)
		for _, result := range results {
			if match(result) {
				matched = append(matched, result)
			}
		}
		sort.SliceStable(matched, func(i, j int) bool { return matched[i].Repo.Slug() < matched[j].Repo.Slug() })
		return resultGroup{label: label, section: section, results: matched}
	}
	status := func(want fleetsync.Status) func(fleetsync.Result) bool {
		return func(result fleetsync.Result) bool { return result.Status == want }
	}
	return []resultGroup{
		group("Not owned/fork", "Final outcome", status(fleetsync.NoOp)),
		group("Cloned", "Final outcome", status(fleetsync.Cloned)),
		group("Pulled", "Final outcome", status(fleetsync.Pulled)),
		group("Pull planned", "Pull action", func(result fleetsync.Result) bool { return result.PullPlanned }),
		group("Pull attempted", "Pull action", func(result fleetsync.Result) bool { return result.PullAttempted }),
		group("Pull succeeded", "Pull action", func(result fleetsync.Result) bool { return result.PullSucceeded }),
		group("Updated from remote", "Pull action", func(result fleetsync.Result) bool { return result.Updated }),
		group("Already current", "Pull action", func(result fleetsync.Result) bool { return result.PullSucceeded && !result.Updated }),
		group("Skipped (dirty)", "Final outcome", status(fleetsync.SkippedDirty)),
		group("Skipped (ignored)", "Final outcome", status(fleetsync.SkippedIgnored)),
		group("Empty remote", "Final outcome", status(fleetsync.EmptyRemote)),
		group("Archived removed", "Final outcome", status(fleetsync.RemovedArchived)),
		group("Archived kept", "Final outcome", status(fleetsync.KeptArchived)),
		group("Archived absent", "Final outcome", status(fleetsync.AbsentArchived)),
		group("Needs attention", "Attention", needsAttention),
		group("Errors", "Error", status(fleetsync.Failed)),
	}
}

func needsAttention(result fleetsync.Result) bool {
	switch result.Status {
	case fleetsync.Diverged, fleetsync.NoUpstream, fleetsync.Unpushed, fleetsync.ArchivedUnlandable:
		return true
	default:
		return false
	}
}

func pluralRepositories(count int) string {
	if count == 1 {
		return "1 repository"
	}
	return fmt.Sprintf("%d repositories", count)
}

func (m ResultsModel) Init() tea.Cmd { return nil }

func (m ResultsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		// While the filter input is active, printable keys (including q) belong
		// to that input. Outside it, q closes the review screen.
		if m.list.FilterState() != list.Filtering {
			switch msg.String() {
			case "q":
				return m, tea.Quit
			case "pgup", "ctrl+u":
				m.detail.HalfPageUp()
				return m, nil
			case "pgdown", "ctrl+d":
				m.detail.HalfPageDown()
				return m, nil
			}
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	m.syncDetail(false)
	return m, cmd
}

func (m ResultsModel) View() tea.View {
	if m.width <= 0 || m.height <= 0 {
		return tea.NewView(m.list.View())
	}
	panel := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	if m.stacked() {
		topHeight, bottomHeight := resultsPanelHeights(m.height)
		contentWidth := resultsContentWidth(m.width)
		left := panel.Width(contentWidth).Height(resultsContentHeight(topHeight)).Render(m.list.View())
		right := panel.Width(contentWidth).Height(resultsContentHeight(bottomHeight)).Render(m.detailPanel())
		return tea.NewView(lipgloss.JoinVertical(lipgloss.Left, left, strings.Repeat("\n", resultsStackedGap), right))
	}
	leftWidth, rightWidth := resultsPanelWidths(m.width)
	contentHeight := resultsContentHeight(m.height)
	left := panel.Width(resultsContentWidth(leftWidth)).Height(contentHeight).Render(m.list.View())
	right := panel.Width(resultsContentWidth(rightWidth)).Height(contentHeight).Render(m.detailPanel())
	return tea.NewView(lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", resultsPanelGap), right))
}

func resultsPanelWidths(width int) (left, right int) {
	usable := max(2, width-resultsPanelGap)
	left = usable / 2
	return left, usable - left
}

func resultsPanelHeights(height int) (top, bottom int) {
	usable := max(2, height-resultsStackedGap)
	top = usable / 2
	return top, usable - top
}

func resultsContentWidth(panelWidth int) int { return max(1, panelWidth-resultsFrameWidth) }
func resultsContentHeight(panelHeight int) int {
	return max(1, panelHeight-resultsFrameHeight)
}

func (m ResultsModel) stacked() bool { return m.width < resultsMinSplitWidth }

func (m *ResultsModel) resize() {
	if m.stacked() {
		topHeight, bottomHeight := resultsPanelHeights(m.height)
		contentWidth := resultsContentWidth(m.width)
		m.list.SetSize(contentWidth, resultsContentHeight(topHeight))
		m.detail.SetWidth(contentWidth)
		m.detail.SetHeight(max(1, resultsContentHeight(bottomHeight)-1))
		return
	}
	leftWidth, rightWidth := resultsPanelWidths(m.width)
	contentHeight := resultsContentHeight(m.height)
	m.list.SetSize(resultsContentWidth(leftWidth), contentHeight)
	m.detail.SetWidth(resultsContentWidth(rightWidth))
	m.detail.SetHeight(max(1, contentHeight-1))
}

func (m *ResultsModel) syncDetail(force bool) {
	group := m.selectedGroup()
	selected := group.label
	if !force && selected == m.selected {
		return
	}
	m.selected = selected
	m.detail.SetContent(groupDetailView(group))
	m.detail.GotoTop()
}

func (m ResultsModel) detailPanel() string {
	help := dimStyle.Render("pgup/pgdown details · ↑/↓ category · / filter · q close")
	return m.detail.View() + "\n" + help
}

func (m ResultsModel) selectedGroup() resultGroup {
	if item, ok := m.list.SelectedItem().(summaryItem); ok {
		return item.resultGroup
	}
	return resultGroup{}
}

func groupDetailView(group resultGroup) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %s\n\n", group.label, pluralRepositories(len(group.results)))
	if len(group.results) == 0 {
		b.WriteString("No repositories in this category.\n")
		return b.String()
	}
	for index, result := range group.results {
		if index > 0 {
			b.WriteString("────────────────────────────────────────\n\n")
		}
		b.WriteString(detailView(result))
	}
	return b.String()
}

// detailView renders the full status breakdown for one result.
func detailView(r fleetsync.Result) string {
	if r.Repo.Org == "" && r.Repo.Name == "" {
		return "No repositories need review.\n\n(q to close)\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %s\n\n", r.Repo.Slug(), r.Status)
	if pull := r.PullSummary(); pull != "" {
		fmt.Fprintf(&b, "Pull: %s\n\n", pull)
	}
	if r.Status == fleetsync.Diverged || r.Status == fleetsync.NoUpstream {
		fmt.Fprintf(&b, "Tracking: %s\n\n", r.Tracking.Summary())
	}
	if summary := r.Detail.Summary(); summary != "" {
		fmt.Fprintf(&b, "%s\n\n", summary)
	}
	section(&b, "Modified", r.Detail.Modified)
	section(&b, "Untracked", r.Detail.Untracked)
	section(&b, "Conflicted", r.Detail.Conflicted)
	writeUnpushedDetail(&b, r.Detail)
	section(&b, "Stashed", r.Detail.Stashed)
	if r.Err != nil {
		fmt.Fprintf(&b, "Error:\n  %s\n", r.Err.Error())
	}
	return b.String()
}

func writeUnpushedDetail(b *strings.Builder, status gitops.RepoStatus) {
	if len(status.UnpushedBranches) == 0 {
		section(b, "Unpushed commits", status.Unpushed)
		return
	}
	b.WriteString("Unpushed commits:\n")
	for _, branch := range status.UnpushedBranches {
		fmt.Fprintf(b, "  Branch: %s\n", branch.Branch)
		if branch.Worktree != "" {
			fmt.Fprintf(b, "  Worktree: %s\n", branch.Worktree)
		}
		for _, commit := range branch.Commits {
			fmt.Fprintf(b, "    %s\n", commit)
		}
		b.WriteString("\n")
	}
}

func section(b *strings.Builder, title string, lines []string) {
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(b, "%s:\n", title)
	for _, l := range lines {
		fmt.Fprintf(b, "  %s\n", l)
	}
	b.WriteString("\n")
}
