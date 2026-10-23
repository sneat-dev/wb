package progress

import "testing"

// TestTailCovIndexHandsOutAStablePointer covers the optional-layer helper: layer
// zero is meaningful, so callers must be able to hold a pointer to it rather
// than rely on a sentinel zero value.
func TestTailCovIndexHandsOutAStablePointer(t *testing.T) {
	t.Parallel()
	zero := Index(0)
	if zero == nil {
		t.Fatal("Index(0) = nil, want a pointer so layer zero survives as a value")
	}
	if *zero != 0 {
		t.Fatalf("*Index(0) = %d, want 0", *zero)
	}

	written := Index(3)
	*written = 7
	if *written != 7 {
		t.Fatalf("*Index(3) after assignment = %d, want 7", *written)
	}
	// Each call owns its own storage: mutating one pointer must not leak into
	// the next event built from another Index call.
	if other := Index(3); *other != 3 {
		t.Fatalf("*Index(3) = %d, want a fresh 3; Index results must not be shared", *other)
	}
}

// TestTailCovReportDeliversToAConfiguredReporterAndToleratesNil covers the two
// observable outcomes of Report: a configured reporter receives the event
// unchanged, and a nil reporter (reporting disabled) is a silent no-op instead
// of a panic.
func TestTailCovReportDeliversToAConfiguredReporterAndToleratesNil(t *testing.T) {
	t.Parallel()
	Report(nil, Event{Operation: "must-not-panic"})

	layer := Index(0)
	event := Event{
		Operation:  "worktree-sweep",
		Phase:      "measure",
		Repository: "sneat-dev/wb",
		Detail:     "measuring",
		State:      Running,
		Completed:  2,
		Total:      5,
		Wave:       1,
		Layer:      layer,
	}

	var received []Event
	Report(func(got Event) { received = append(received, got) }, event)

	if len(received) != 1 {
		t.Fatalf("reporter called %d times, want exactly 1", len(received))
	}
	if received[0] != event {
		t.Fatalf("reporter received %#v, want the event passed to Report %#v", received[0], event)
	}
	if received[0].Layer == nil || *received[0].Layer != 0 {
		t.Fatalf("layer zero did not survive reporting: %#v", received[0].Layer)
	}
}

// TestTailCovStatesAreTheWireValues pins the state strings other packages and
// the JSON/daemon contract write out. Renaming one silently would break every
// consumer that switches on the string.
func TestTailCovStatesAreTheWireValues(t *testing.T) {
	t.Parallel()
	for state, want := range map[State]string{
		Started:   "started",
		Running:   "running",
		Completed: "completed",
		Failed:    "failed",
		Waiting:   "waiting",
	} {
		if string(state) != want {
			t.Errorf("State = %q, want %q", string(state), want)
		}
	}
}
