package daemon

import (
	"testing"
	"time"
)

// TestProcessGenerationMatchesToleratesObservedStartBeforeRecorded drives the
// delta-negation branch: the platform's observed start time can land before
// the recorded one by a small amount (clock/measurement skew in either
// direction), and the comparison must use the absolute difference.
func TestProcessGenerationMatchesToleratesObservedStartBeforeRecorded(t *testing.T) {
	t.Parallel()
	recorded := time.Unix(1000, 0).UTC()
	state := State{PID: 123, ProcessStartedAt: recorded}
	observed := recorded.Add(-500 * time.Millisecond)
	match, known := state.ProcessGenerationMatches(observed, true)
	if !known {
		t.Fatal("ProcessGenerationMatches reported unknown despite a recorded start and an observed one")
	}
	if !match {
		t.Fatal("ProcessGenerationMatches rejected an observed start half a second earlier than recorded, within tolerance")
	}
}

// TestReportedSupervisorNormalizesUnrecognizedValue drives the invalid-value
// branch of ReportedSupervisor: a recorded value this build does not
// recognize (e.g. written by a newer build, or corrupted) must be reported
// as SupervisorNone rather than passed through.
func TestReportedSupervisorNormalizesUnrecognizedValue(t *testing.T) {
	t.Parallel()
	state := State{Supervisor: Supervisor("some-future-supervisor")}
	if got := state.ReportedSupervisor(); got != SupervisorNone {
		t.Fatalf("ReportedSupervisor() = %q, want %q for an unrecognized recorded value", got, SupervisorNone)
	}
}

// TestReportedSupervisorPassesThroughARecognizedValue drives the valid-value
// passthrough branch of ReportedSupervisor: a recorded value this build does
// recognize is reported as-is, not normalized away.
func TestReportedSupervisorPassesThroughARecognizedValue(t *testing.T) {
	t.Parallel()
	state := State{Supervisor: SupervisorSystemd}
	if got := state.ReportedSupervisor(); got != SupervisorSystemd {
		t.Fatalf("ReportedSupervisor() = %q, want %q for a recognized recorded value", got, SupervisorSystemd)
	}
}
