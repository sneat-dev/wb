package console

import (
	"bytes"
	"testing"
)

// TestTailCovInteractiveRefusesOptOutsWithoutATerminal covers the two ways a
// caller is told "not interactive" before the stream is even inspected: an
// explicit --non-interactive flag and the WB_NON_INTERACTIVE environment
// opt-out. The terminal-owning variant of this test cannot run on a machine
// with no controlling terminal, so the decision itself is pinned here.
func TestTailCovInteractiveRefusesOptOutsWithoutATerminal(t *testing.T) {
	stream := &bytes.Buffer{}

	if Interactive(stream, true) {
		t.Error("Interactive(stream, forced=true) = true, want false: an explicit opt-out wins over everything")
	}

	t.Setenv(EnvDisable, "1")
	if Interactive(stream, false) {
		t.Errorf("Interactive(stream, forced=false) with %s=1 = true, want false", EnvDisable)
	}
	if Interactive(stream, true) {
		t.Errorf("Interactive(stream, forced=true) with %s=1 = true, want false", EnvDisable)
	}
}

// TestTailCovDisabledReadsTheLiveEnvironment proves Disabled consults the
// process environment under the documented name with the documented polarity:
// any non-false value is an opt-out, and "0"/"false" opt back in.
func TestTailCovDisabledReadsTheLiveEnvironment(t *testing.T) {
	t.Setenv(EnvDisable, "0")
	if Disabled() {
		t.Errorf("Disabled() with %s=0 = true, want false", EnvDisable)
	}
	t.Setenv(EnvDisable, "true")
	if !Disabled() {
		t.Errorf("Disabled() with %s=true = false, want true", EnvDisable)
	}
}
