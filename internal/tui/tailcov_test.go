package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestTailCovProgressModelInitAndUnhandledMessage pins that the model starts no
// command of its own (the sync drives every message) and passes a message it
// does not own through without mutating state.
func TestTailCovProgressModelInitAndUnhandledMessage(t *testing.T) {
	t.Parallel()
	m := NewProgressModel(map[string]int{"acme": 1}, 1)
	if cmd := m.Init(); cmd != nil {
		t.Fatalf("Init() = %v, want nil while the sync drives its own messages", cmd)
	}

	updated, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if cmd != nil {
		t.Fatalf("unhandled message produced command %v, want nil", cmd)
	}
	got := updated.(ProgressModel)
	if got.done != 0 || len(got.inFlight) != 0 || len(got.Results) != 0 || got.quitting {
		t.Fatalf("unhandled message mutated progress state: %#v", got)
	}
}

// TestTailCovProgressModelQuittingRendersEmptyView asserts a quit renders
// nothing at all, so the alternate-screen restore is not fighting stale rows.
func TestTailCovProgressModelQuittingRendersEmptyView(t *testing.T) {
	t.Parallel()
	m := NewProgressModel(map[string]int{"acme": 2}, 2)
	if m.View().Content == "" {
		t.Fatal("running progress view is empty, so the quit assertion proves nothing")
	}
	updated, _ := m.Update(SyncDone{})
	m = updated.(ProgressModel)
	if !m.quitting {
		t.Fatal("SyncDone did not mark the model quitting")
	}
	if got := m.View().Content; got != "" {
		t.Fatalf("quitting view = %q, want empty", got)
	}
}
