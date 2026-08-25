package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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

func TestNewResultsModelFiltersToReviewable(t *testing.T) {
	results := []fleetsync.Result{
		{Repo: discover.Repo{Org: "a", Name: "clean"}, Status: fleetsync.Pulled},
		{Repo: discover.Repo{Org: "a", Name: "broken"}, Status: fleetsync.Failed},
	}
	m := NewResultsModel(results)
	if got := len(m.list.Items()); got != 1 {
		t.Fatalf("list items = %d, want 1", got)
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
			Detail: gitops.RepoStatus{Modified: []string{"second.txt"}},
		},
	}
	m := NewResultsModel(results)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = updated.(ResultsModel)
	view := m.View().Content
	if !strings.Contains(view, "Needs review (2)") || !strings.Contains(view, "a/first") || !strings.Contains(view, "first.txt") {
		t.Fatalf("split view missing list or selected details: %q", view)
	}
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = updated.(ResultsModel)
	view = m.View().Content
	if !strings.Contains(view, "a/second") || !strings.Contains(view, "second.txt") {
		t.Fatalf("detail panel did not follow selection: %q", view)
	}
	if !strings.Contains(view, "   ") {
		t.Fatalf("split view has no spacing between panels: %q", view)
	}
}
