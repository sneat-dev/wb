package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/trakhimenok/workbench/wb/internal/discover"
	"github.com/trakhimenok/workbench/wb/internal/fleetsync"
)

func TestProgressModelTracksCounts(t *testing.T) {
	m := NewProgressModel(map[string]int{"acme": 2, "beta": 1}, 4)

	updated, _ := m.Update(RepoStarted{Org: "acme", Name: "widgets"})
	m = updated.(ProgressModel)
	if len(m.inFlight) != 1 {
		t.Fatalf("inFlight = %d, want 1", len(m.inFlight))
	}

	res := fleetsync.Result{Repo: discover.Repo{Org: "acme", Name: "widgets"}, Status: fleetsync.Pulled}
	updated, _ = m.Update(RepoDone{Result: res})
	m = updated.(ProgressModel)

	if m.done != 1 {
		t.Errorf("done = %d, want 1", m.done)
	}
	if m.orgDone["acme"] != 1 {
		t.Errorf("orgDone[acme] = %d, want 1", m.orgDone["acme"])
	}
	if len(m.inFlight) != 0 {
		t.Errorf("inFlight = %d, want 0 after RepoDone", len(m.inFlight))
	}
	if len(m.Results) != 1 || m.Results[0].Status != fleetsync.Pulled {
		t.Errorf("Results = %+v, want [Pulled]", m.Results)
	}
}

func TestProgressModelInFlightBounded(t *testing.T) {
	m := NewProgressModel(map[string]int{"acme": 5}, 2)
	for i := 0; i < 5; i++ {
		updated, _ := m.Update(RepoStarted{Org: "acme", Name: fmt.Sprintf("repo%d", i)})
		m = updated.(ProgressModel)
	}
	view := m.View()
	if got := strings.Count(view, "…"); got != 2 {
		t.Errorf("live-tail rows in view = %d, want 2 (bounded by maxInFlight)", got)
	}
}

func TestProgressModelSyncDoneQuits(t *testing.T) {
	m := NewProgressModel(nil, 4)
	updated, cmd := m.Update(SyncDone{})
	m = updated.(ProgressModel)
	if !m.quitting {
		t.Error("expected quitting=true after SyncDone")
	}
	if cmd == nil {
		t.Error("expected a non-nil tea.Cmd (tea.Quit) after SyncDone")
	}
}
