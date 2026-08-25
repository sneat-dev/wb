package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/gitops"
)

// summaryItem adapts one row from the printed sync summary to a navigable
// list item. The result set behind the row is rendered in the detail panel.
type summaryItem struct{ fleetsync.SummaryGroup }

func (i summaryItem) Title() string {
	return fmt.Sprintf("%-9s %-20s%d", summarySectionLabel(i.Section), i.Label, len(i.Results))
}

func (i summaryItem) Description() string { return pluralRepositories(len(i.Results)) }

func (i summaryItem) FilterValue() string { return i.Label + " " + string(i.Section) }

func summarySectionLabel(section fleetsync.SummarySection) string {
	switch section {
	case fleetsync.SummaryFinalOutcomes:
		return "Outcome"
	case fleetsync.SummaryPullActions:
		return "Pull"
	case fleetsync.SummaryAttention:
		return "Attention"
	case fleetsync.SummaryErrors:
		return "Error"
	default:
		return ""
	}
}

type repositoryItem struct{ fleetsync.Result }

func (i repositoryItem) Title() string {
	detail := i.Status.String()
	if pull := i.PullSummary(); pull != "" {
		detail += ", pull " + pull
	}
	return i.Repo.Slug() + " — " + detail
}

func (i repositoryItem) Description() string { return i.Detail.Summary() }

func (i repositoryItem) FilterValue() string {
	var errorText string
	if i.Err != nil {
		errorText = i.Err.Error()
	}
	return strings.Join([]string{i.Repo.Slug(), i.Status.String(), i.PullSummary(), i.Detail.Summary(), errorText}, " ")
}

type resultsFocus uint8

const (
	focusSummary resultsFocus = iota
	focusRepositories
)

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
	summary            list.Model
	repositories       list.Model
	detail             viewport.Model
	focus              resultsFocus
	width, height      int
	selectedGroup      string
	selectedRepository string
}

// NewResultsModel turns the final sync summary itself into the left-hand
// navigation. Selecting a count shows every repository contributing to it on
// the right, including successful/no-op categories rather than only failures.
func NewResultsModel(results []fleetsync.Result) ResultsModel {
	groups := fleetsync.Summary(results)
	items := make([]list.Item, 0, len(groups))
	for _, group := range groups {
		items = append(items, summaryItem{group})
	}
	summaryDelegate := list.NewDefaultDelegate()
	summaryDelegate.ShowDescription = false
	summaryDelegate.SetSpacing(0)
	summary := list.New(items, summaryDelegate, 0, 0)
	summary.Title = "Summary"
	summary.SetShowStatusBar(false)
	summary.SetShowHelp(false)

	repositoryDelegate := list.NewDefaultDelegate()
	repositoryDelegate.ShowDescription = false
	repositoryDelegate.SetSpacing(0)
	repositories := list.New(nil, repositoryDelegate, 0, 0)
	repositories.Title = "Repositories"
	repositories.SetShowStatusBar(false)
	repositories.SetShowHelp(false)

	detail := viewport.New()
	detail.SoftWrap = true
	detail.FillHeight = true
	m := ResultsModel{summary: summary, repositories: repositories, detail: detail}
	m.syncGroup(true)
	return m
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
		// While the active list's filter input is active, printable keys
		// (including q and tab) belong to that input.
		if !m.filtering() {
			switch msg.String() {
			case "q":
				return m, tea.Quit
			case "tab":
				m.toggleFocus()
				return m, nil
			case "enter", "right", "l":
				if m.focus == focusSummary && len(m.repositories.Items()) > 0 {
					m.focus = focusRepositories
					return m, nil
				}
			case "shift+tab", "left", "h":
				if m.focus == focusRepositories {
					m.focus = focusSummary
					return m, nil
				}
			case "esc":
				if m.focus == focusRepositories && m.repositories.FilterState() == list.Unfiltered {
					m.focus = focusSummary
					return m, nil
				}
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
	if m.focus == focusRepositories {
		m.repositories, cmd = m.repositories.Update(msg)
		m.syncRepository(false)
	} else {
		m.summary, cmd = m.summary.Update(msg)
		m.syncGroup(false)
	}
	return m, cmd
}

func (m ResultsModel) View() tea.View {
	if m.width <= 0 || m.height <= 0 {
		return tea.NewView(m.summary.View())
	}
	help := resultsHelp(m.width)
	bodyHeight := max(1, m.height-lipgloss.Height(help))
	var body string
	if m.stacked() {
		topHeight, bottomHeight := resultsPanelHeights(bodyHeight)
		contentWidth := resultsContentWidth(m.width)
		left := resultsPanel(m.focus == focusSummary).Width(contentWidth).Height(resultsContentHeight(topHeight)).Render(m.summary.View())
		right := resultsPanel(m.focus == focusRepositories).Width(contentWidth).Height(resultsContentHeight(bottomHeight)).Render(m.rightPanel(contentWidth, resultsContentHeight(bottomHeight)))
		body = lipgloss.JoinVertical(lipgloss.Left, left, strings.Repeat("\n", resultsStackedGap), right)
	} else {
		leftWidth, rightWidth := resultsPanelWidths(m.width)
		contentHeight := resultsContentHeight(bodyHeight)
		leftContentWidth := resultsContentWidth(leftWidth)
		rightContentWidth := resultsContentWidth(rightWidth)
		left := resultsPanel(m.focus == focusSummary).Width(leftContentWidth).Height(contentHeight).Render(m.summary.View())
		right := resultsPanel(m.focus == focusRepositories).Width(rightContentWidth).Height(contentHeight).Render(m.rightPanel(rightContentWidth, contentHeight))
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, strings.Repeat(" ", resultsPanelGap), right)
	}
	view := body + "\n" + help
	view = lipgloss.NewStyle().MaxWidth(m.width).MaxHeight(m.height).Render(view)
	return tea.NewView(view)
}

func resultsPanel(focused bool) lipgloss.Style {
	style := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
	if focused {
		style = style.BorderForeground(lipgloss.Color("62"))
	}
	return style
}

func resultsHelp(width int) string {
	help := "tab pane · ↑/↓ select · / filter · pgup/pgdown details · q close"
	if width < 72 {
		help = "tab pane · / filter · pgup/dn detail · q close"
	}
	return dimStyle.MaxWidth(max(1, width)).Render(help)
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

func resultsRightHeights(height int) (repositories, detail int) {
	if height <= 2 {
		return 1, 1
	}
	usable := height - 1 // horizontal divider
	repositories = max(3, usable/3)
	if repositories >= usable {
		repositories = max(1, usable-1)
	}
	return repositories, max(1, usable-repositories)
}

func resultsContentWidth(panelWidth int) int { return max(1, panelWidth-resultsFrameWidth) }
func resultsContentHeight(panelHeight int) int {
	return max(1, panelHeight-resultsFrameHeight)
}

func (m ResultsModel) stacked() bool { return m.width < resultsMinSplitWidth }

func (m *ResultsModel) resize() {
	bodyHeight := max(1, m.height-lipgloss.Height(resultsHelp(m.width)))
	if m.stacked() {
		topHeight, bottomHeight := resultsPanelHeights(bodyHeight)
		contentWidth := resultsContentWidth(m.width)
		m.summary.SetSize(contentWidth, resultsContentHeight(topHeight))
		m.resizeRight(contentWidth, resultsContentHeight(bottomHeight))
		return
	}
	leftWidth, rightWidth := resultsPanelWidths(m.width)
	contentHeight := resultsContentHeight(bodyHeight)
	m.summary.SetSize(resultsContentWidth(leftWidth), contentHeight)
	m.resizeRight(resultsContentWidth(rightWidth), contentHeight)
}

func (m *ResultsModel) resizeRight(width, height int) {
	repositoryHeight, detailHeight := resultsRightHeights(height)
	m.repositories.SetSize(width, repositoryHeight)
	m.detail.SetWidth(width)
	m.detail.SetHeight(detailHeight)
}

func (m ResultsModel) rightPanel(width, height int) string {
	repositoryHeight, detailHeight := resultsRightHeights(height)
	repositoryView := lipgloss.NewStyle().Width(width).Height(repositoryHeight).MaxWidth(width).MaxHeight(repositoryHeight).Render(m.repositories.View())
	divider := dimStyle.Render(strings.Repeat("─", max(1, width)))
	detailView := lipgloss.NewStyle().Width(width).Height(detailHeight).MaxWidth(width).MaxHeight(detailHeight).Render(m.detail.View())
	return lipgloss.JoinVertical(lipgloss.Left, repositoryView, divider, detailView)
}

func (m ResultsModel) filtering() bool {
	if m.focus == focusRepositories {
		return m.repositories.FilterState() == list.Filtering
	}
	return m.summary.FilterState() == list.Filtering
}

func (m *ResultsModel) toggleFocus() {
	if m.focus == focusRepositories {
		m.focus = focusSummary
	} else if len(m.repositories.Items()) > 0 {
		m.focus = focusRepositories
	}
}

func (m *ResultsModel) syncGroup(force bool) {
	group := m.selectedSummaryGroup()
	selected := string(group.Section) + "\x00" + group.Label
	if !force && selected == m.selectedGroup {
		return
	}
	m.selectedGroup = selected
	m.repositories.ResetFilter()
	items := make([]list.Item, 0, len(group.Results))
	for _, result := range group.Results {
		items = append(items, repositoryItem{result})
	}
	m.repositories.SetItems(items)
	m.repositories.ResetSelected()
	m.repositories.Title = fmt.Sprintf("%s (%d)", group.Label, len(group.Results))
	m.selectedRepository = ""
	m.syncRepository(true)
}

func (m *ResultsModel) syncRepository(force bool) {
	result, ok := m.selectedResult()
	selected := ""
	if ok {
		selected = result.Repo.Slug() + "\x00" + result.Status.String()
	}
	if !force && selected == m.selectedRepository {
		return
	}
	m.selectedRepository = selected
	if ok {
		m.detail.SetContent(detailView(result))
	} else if group := m.selectedSummaryGroup(); group.Label != "" {
		m.detail.SetContent(fmt.Sprintf("%s — %s\n\nNo repositories in this category.\n", group.Label, pluralRepositories(len(group.Results))))
	} else {
		m.detail.SetContent("No summary category matches the filter.\n")
	}
	m.detail.GotoTop()
}

func (m ResultsModel) selectedSummaryGroup() fleetsync.SummaryGroup {
	if item, ok := m.summary.SelectedItem().(summaryItem); ok {
		return item.SummaryGroup
	}
	return fleetsync.SummaryGroup{}
}

func (m ResultsModel) selectedResult() (fleetsync.Result, bool) {
	if item, ok := m.repositories.SelectedItem().(repositoryItem); ok {
		return item.Result, true
	}
	return fleetsync.Result{}, false
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
