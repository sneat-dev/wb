package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/sneat-dev/wb/internal/fleetsync"
)

// resultItem adapts a fleetsync.Result to bubbles/list.Item.
type resultItem struct{ fleetsync.Result }

func (i resultItem) Title() string { return i.Repo.Slug() }

func (i resultItem) Description() string {
	if i.Err != nil {
		return "error: " + i.Err.Error()
	}
	if s := i.Detail.Summary(); s != "" {
		return s
	}
	switch i.Status {
	case fleetsync.Diverged, fleetsync.NoUpstream:
		return i.Status.String() + ": " + i.Tracking.Summary()
	}
	return i.Status.String()
}

func (i resultItem) FilterValue() string { return i.Repo.Slug() }

const resultsPanelGap = 3

// ResultsModel is a persistent split-pane review screen: the left panel is a
// navigable summary and the right panel follows its current selection.
type ResultsModel struct {
	list          list.Model
	width, height int
}

// NewResultsModel builds a ResultsModel over the reviewable results —
// Failed, SkippedDirty, KeptArchived, Diverged, NoUpstream, Unpushed, or
// ArchivedUnlandable. Results in other states are omitted; they synced
// cleanly and need no review.
func NewResultsModel(results []fleetsync.Result) ResultsModel {
	items := make([]list.Item, 0, len(results))
	for _, r := range results {
		if Reviewable(r) {
			items = append(items, resultItem{r})
		}
	}
	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = fmt.Sprintf("Needs review (%d)", len(items))
	return ResultsModel{list: l}
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

func (m ResultsModel) Init() tea.Cmd { return nil }

func (m ResultsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		leftWidth, _ := resultsPanelWidths(msg.Width)
		m.list.SetSize(max(1, leftWidth-4), max(1, msg.Height-2))
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m ResultsModel) View() tea.View {
	if m.width <= 0 || m.height <= 0 {
		return tea.NewView(m.list.View())
	}
	leftWidth, rightWidth := resultsPanelWidths(m.width)
	panelHeight := max(1, m.height-2)
	panel := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	left := panel.Width(max(1, leftWidth-4)).Height(panelHeight).Render(m.list.View())
	right := panel.Width(max(1, rightWidth-4)).Height(panelHeight).Render(detailView(m.selectedResult()))
	return tea.NewView(lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", resultsPanelGap), right))
}

func resultsPanelWidths(width int) (left, right int) {
	usable := max(2, width-resultsPanelGap)
	left = usable / 2
	return left, usable - left
}

func (m ResultsModel) selectedResult() fleetsync.Result {
	if item, ok := m.list.SelectedItem().(resultItem); ok {
		return item.Result
	}
	return fleetsync.Result{}
}

// detailView renders the full status breakdown for one result.
func detailView(r fleetsync.Result) string {
	if r.Repo.Org == "" && r.Repo.Name == "" {
		return "No repositories need review.\n\n(q to close)\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %s\n\n", r.Repo.Slug(), r.Status)
	if r.Status == fleetsync.Diverged || r.Status == fleetsync.NoUpstream {
		fmt.Fprintf(&b, "Tracking: %s\n\n", r.Tracking.Summary())
	}
	if summary := r.Detail.Summary(); summary != "" {
		fmt.Fprintf(&b, "%s\n\n", summary)
	}
	section(&b, "Modified", r.Detail.Modified)
	section(&b, "Untracked", r.Detail.Untracked)
	section(&b, "Conflicted", r.Detail.Conflicted)
	section(&b, "Unpushed commits", r.Detail.Unpushed)
	section(&b, "Stashed", r.Detail.Stashed)
	if r.Err != nil {
		fmt.Fprintf(&b, "Error:\n  %s\n", r.Err.Error())
	}
	b.WriteString("\n(↑/↓ select · / filter · q close)\n")
	return b.String()
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
