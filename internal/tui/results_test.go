package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/gitops"
)

func TestNewResultsModelMakesSummaryCategoriesNavigable(t *testing.T) {
	results := []fleetsync.Result{
		{Repo: discover.Repo{Org: "a", Name: "clean"}, Status: fleetsync.Pulled, PullAttempted: true, PullSucceeded: true},
		{Repo: discover.Repo{Org: "a", Name: "broken"}, Status: fleetsync.Failed},
	}
	m := NewResultsModel(results)
	if got := len(m.summary.Items()); got != 16 {
		t.Fatalf("list items = %d, want all 16 summary categories", got)
	}
	assertResultGroupCount(t, m, "Pulled", 1)
	assertResultGroupCount(t, m, "Pull succeeded", 1)
	assertResultGroupCount(t, m, "Errors", 1)
	assertResultGroupCount(t, m, "Needs attention", 0)
	if got := m.summary.Title; got != "Summary" {
		t.Fatalf("list title = %q, want Summary", got)
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	m = updated.(ResultsModel)
	view := m.View().Content
	if !strings.Contains(view, "Not owned/fork") || !strings.Contains(view, "Errors") {
		t.Fatalf("compact summary does not expose first and last categories together: %q", view)
	}
}

func TestResultsModelNavigatesRepositoriesAndDetails(t *testing.T) {
	results := []fleetsync.Result{
		{
			Repo:   discover.Repo{Org: "a", Name: "first"},
			Status: fleetsync.SkippedDirty,
			Detail: gitops.RepoStatus{Modified: []string{"first.txt"}},
		},
		{
			Repo:   discover.Repo{Org: "a", Name: "second"},
			Status: fleetsync.SkippedDirty,
			Detail: gitops.RepoStatus{
				Modified: []string{"second.txt"},
				Unpushed: []string{"abc1234 linked work"},
				UnpushedBranches: []gitops.UnpushedBranch{{
					Branch: "feature", Worktree: "/wt/second", Commits: []string{"abc1234 linked work"},
				}},
			},
		},
		{Repo: discover.Repo{Org: "a", Name: "broken"}, Status: fleetsync.Failed, Err: fmt.Errorf("network unavailable")},
	}
	m := NewResultsModel(results)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = updated.(ResultsModel)
	selectResultGroup(t, &m, "Skipped (dirty)")
	view := m.View().Content
	if !strings.Contains(view, "Summary") || !strings.Contains(view, "Skipped (dirty) (2)") || !strings.Contains(view, "a/first") || !strings.Contains(view, "first.txt") || !strings.Contains(view, "a/second") {
		t.Fatalf("split view missing list or selected details: %q", view)
	}
	if strings.Contains(view, "Worktree: /wt/second") {
		t.Fatalf("details advanced before repository selection: %q", view)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = updated.(ResultsModel)
	if m.focus != focusRepositories {
		t.Fatal("tab did not focus the repository list")
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(ResultsModel)
	view = m.View().Content
	if !strings.Contains(view, "second.txt") || !strings.Contains(view, "Branch: feature") || !strings.Contains(view, "Worktree: /wt/second") {
		t.Fatalf("details did not follow repository selection: %q", view)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	m = updated.(ResultsModel)
	if m.focus != focusSummary {
		t.Fatal("left did not return focus to summary")
	}
	selectResultGroup(t, &m, "Errors")
	view = m.View().Content
	if !strings.Contains(view, "Errors (1)") || !strings.Contains(view, "a/broken") || !strings.Contains(view, "network unavailable") {
		t.Fatalf("detail panel did not follow summary category selection: %q", view)
	}
	if !strings.Contains(view, "   ") {
		t.Fatalf("split view has no spacing between panels: %q", view)
	}
	assertViewBounds(t, view, 120, 30)
}

func TestResultsModelStacksOnNarrowTerminalAndScrollsDetails(t *testing.T) {
	commits := make([]string, 40)
	for i := range commits {
		commits[i] = fmt.Sprintf("%07x commit %d with a subject long enough to wrap", i, i)
	}
	m := NewResultsModel([]fleetsync.Result{{
		Repo: discover.Repo{Org: "a", Name: "many"}, Status: fleetsync.Unpushed,
		Detail: gitops.RepoStatus{Unpushed: commits},
	}})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	m = updated.(ResultsModel)
	selectResultGroup(t, &m, "Needs attention")
	if !m.stacked() {
		t.Fatal("60-column terminal should use stacked panels")
	}
	before := m.detail.YOffset()
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	m = updated.(ResultsModel)
	if after := m.detail.YOffset(); after <= before {
		t.Fatalf("detail offset after pgdown = %d, want > %d", after, before)
	}
	assertViewBounds(t, m.View().Content, 60, 20)
}

func assertResultGroupCount(t *testing.T, m ResultsModel, label string, want int) {
	t.Helper()
	for _, item := range m.summary.Items() {
		group := item.(summaryItem).SummaryGroup
		if group.Label == label {
			if got := len(group.Results); got != want {
				t.Fatalf("%s count = %d, want %d", label, got, want)
			}
			return
		}
	}
	t.Fatalf("summary category %q not found", label)
}

func selectResultGroup(t *testing.T, m *ResultsModel, label string) {
	t.Helper()
	for index, item := range m.summary.Items() {
		if item.(summaryItem).Label == label {
			m.summary.Select(index)
			m.syncGroup(false)
			return
		}
	}
	t.Fatalf("summary category %q not found", label)
}

func TestResultsModelLetsFocusedFilterConsumeQ(t *testing.T) {
	m := NewResultsModel([]fleetsync.Result{
		{Repo: discover.Repo{Org: "acme", Name: "queue"}, Status: fleetsync.Failed},
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(ResultsModel)
	updated, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m = updated.(ResultsModel)
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	m = updated.(ResultsModel)
	if got := m.summary.FilterValue(); got != "q" {
		t.Fatalf("filter value = %q, want q", got)
	}
}

func TestResultsModelFiltersRepositoriesInTheRightPane(t *testing.T) {
	m := NewResultsModel([]fleetsync.Result{
		{Repo: discover.Repo{Org: "acme", Name: "alpha"}, Status: fleetsync.Pulled},
		{Repo: discover.Repo{Org: "acme", Name: "queue"}, Status: fleetsync.Pulled},
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = updated.(ResultsModel)
	selectResultGroup(t, &m, "Pulled")
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	m = updated.(ResultsModel)
	updated, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m = updated.(ResultsModel)
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	m = updated.(ResultsModel)
	if got := m.repositories.FilterValue(); got != "q" {
		t.Fatalf("repository filter value = %q, want q", got)
	}
}

func TestResultsModelNeverExceedsTerminalBounds(t *testing.T) {
	for _, size := range []struct{ width, height int }{{120, 24}, {60, 20}, {40, 10}, {20, 6}} {
		m := NewResultsModel([]fleetsync.Result{{Repo: discover.Repo{Org: "long-owner", Name: "long-repository-name"}, Status: fleetsync.Pulled}})
		updated, _ := m.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
		m = updated.(ResultsModel)
		assertViewBounds(t, m.View().Content, size.width, size.height)
	}
}

func assertViewBounds(t *testing.T, view string, width, height int) {
	t.Helper()
	if got := lipgloss.Height(view); got > height {
		t.Fatalf("view height = %d, want <= %d:\n%s", got, height, view)
	}
	for _, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("view line width = %d, want <= %d: %q", got, width, line)
		}
	}
}
