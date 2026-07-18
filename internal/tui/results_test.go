package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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

func TestResultsModelEnterShowsDetailAndEscReturns(t *testing.T) {
	results := []fleetsync.Result{
		{
			Repo:   discover.Repo{Org: "a", Name: "broken"},
			Status: fleetsync.SkippedDirty,
			Detail: gitops.RepoStatus{Modified: []string{"f.txt"}},
		},
	}
	m := NewResultsModel(results)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(ResultsModel)
	if !m.showDetail {
		t.Fatal("expected showDetail=true after enter")
	}
	view := m.View()
	if !strings.Contains(view, "a/broken") || !strings.Contains(view, "f.txt") {
		t.Fatalf("detail view missing expected content: %q", view)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(ResultsModel)
	if m.showDetail {
		t.Fatal("expected showDetail=false after esc")
	}
}
