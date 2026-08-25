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

func TestReviewable(t *testing.T) {
	cases := []struct {
		status fleetsync.Status
		want   bool
	}{
		{fleetsync.Failed, true},
		{fleetsync.SkippedDirty, true},
		{fleetsync.Diverged, true},
		{fleetsync.NoUpstream, true},
		{fleetsync.EmptyRemote, false},
		{fleetsync.KeptArchived, true},
		{fleetsync.Cloned, false},
		{fleetsync.Pulled, false},
		{fleetsync.NoOp, false},
	}
	for _, c := range cases {
		got := Reviewable(fleetsync.Result{Status: c.status})
		if got != c.want {
			t.Errorf("Reviewable(%v) = %v, want %v", c.status, got, c.want)
		}
	}
}

func TestNewResultsModelMakesSummaryCategoriesNavigable(t *testing.T) {
	results := []fleetsync.Result{
		{Repo: discover.Repo{Org: "a", Name: "clean"}, Status: fleetsync.Pulled, PullAttempted: true, PullSucceeded: true},
		{Repo: discover.Repo{Org: "a", Name: "broken"}, Status: fleetsync.Failed},
	}
	m := NewResultsModel(results)
	if got := len(m.list.Items()); got != 16 {
		t.Fatalf("list items = %d, want all 16 summary categories", got)
	}
	assertResultGroupCount(t, m, "Pulled", 1)
	assertResultGroupCount(t, m, "Pull succeeded", 1)
	assertResultGroupCount(t, m, "Errors", 1)
	assertResultGroupCount(t, m, "Needs attention", 0)
	if got := m.list.Title; got != "Summary" {
		t.Fatalf("list title = %q, want Summary", got)
	}
}

func TestResultsModelRendersNavigableSplitPane(t *testing.T) {
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
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(ResultsModel)
	selectResultGroup(t, &m, "Skipped (dirty)")
	view := m.View().Content
	if !strings.Contains(view, "Summary") || !strings.Contains(view, "Skipped (dirty)") || !strings.Contains(view, "a/first") || !strings.Contains(view, "first.txt") || !strings.Contains(view, "a/second") || !strings.Contains(view, "Worktree: /wt/second") {
		t.Fatalf("split view missing list or selected details: %q", view)
	}
	selectResultGroup(t, &m, "Errors")
	view = m.View().Content
	if !strings.Contains(view, "Errors — 1 repository") || !strings.Contains(view, "a/broken") || !strings.Contains(view, "network unavailable") {
		t.Fatalf("detail panel did not follow summary category selection: %q", view)
	}
	if !strings.Contains(view, "   ") {
		t.Fatalf("split view has no spacing between panels: %q", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if width := lipgloss.Width(line); width > 100 {
			t.Fatalf("split view line width = %d, want <= 100: %q", width, line)
		}
	}
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
	for _, line := range strings.Split(m.View().Content, "\n") {
		if width := lipgloss.Width(line); width > 60 {
			t.Fatalf("stacked view line width = %d, want <= 60: %q", width, line)
		}
	}
}

func assertResultGroupCount(t *testing.T, m ResultsModel, label string, want int) {
	t.Helper()
	for _, item := range m.list.Items() {
		group := item.(summaryItem).resultGroup
		if group.label == label {
			if got := len(group.results); got != want {
				t.Fatalf("%s count = %d, want %d", label, got, want)
			}
			return
		}
	}
	t.Fatalf("summary category %q not found", label)
}

func selectResultGroup(t *testing.T, m *ResultsModel, label string) {
	t.Helper()
	for index, item := range m.list.Items() {
		if item.(summaryItem).label == label {
			m.list.Select(index)
			m.syncDetail(false)
			return
		}
	}
	t.Fatalf("summary category %q not found", label)
}

func TestResultsModelLetsFilterInputConsumeQ(t *testing.T) {
	m := NewResultsModel([]fleetsync.Result{
		{Repo: discover.Repo{Org: "acme", Name: "queue"}, Status: fleetsync.Failed},
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(ResultsModel)
	updated, _ = m.Update(tea.KeyPressMsg{Code: '/', Text: "/"})
	m = updated.(ResultsModel)
	updated, _ = m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	m = updated.(ResultsModel)
	if got := m.list.FilterValue(); got != "q" {
		t.Fatalf("filter value = %q, want q", got)
	}
}
