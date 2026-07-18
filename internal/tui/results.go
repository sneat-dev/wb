package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

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
	return i.Status.String()
}

func (i resultItem) FilterValue() string { return i.Repo.Slug() }

// ResultsModel is the interactive drill-down: a list of repos that need
// review, and a detail view for the currently selected one.
type ResultsModel struct {
	list       list.Model
	showDetail bool
	selected   fleetsync.Result
}

// NewResultsModel builds a ResultsModel over the reviewable results —
// Failed, SkippedDirty, or KeptArchived. Results in other states are
// omitted; they synced cleanly and need no review.
func NewResultsModel(results []fleetsync.Result) ResultsModel {
	items := make([]list.Item, 0, len(results))
	for _, r := range results {
		if Reviewable(r) {
			items = append(items, resultItem{r})
		}
	}
	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Needs review"
	return ResultsModel{list: l}
}

// Reviewable reports whether a result belongs in the drill-down list.
func Reviewable(r fleetsync.Result) bool {
	switch r.Status {
	case fleetsync.Failed, fleetsync.SkippedDirty, fleetsync.KeptArchived:
		return true
	default:
		return false
	}
}

func (m ResultsModel) Init() tea.Cmd { return nil }

func (m ResultsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, msg.Height)
		return m, nil
	case tea.KeyMsg:
		if m.showDetail {
			switch msg.String() {
			case "esc", "q":
				m.showDetail = false
			}
			return m, nil
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "enter":
			if it, ok := m.list.SelectedItem().(resultItem); ok {
				m.selected = it.Result
				m.showDetail = true
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m ResultsModel) View() string {
	if m.showDetail {
		return detailView(m.selected)
	}
	return m.list.View()
}

// detailView renders the full status breakdown for one result.
func detailView(r fleetsync.Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s — %s\n\n", r.Repo.Slug(), r.Status)
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
	b.WriteString("\n(esc to go back)\n")
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
