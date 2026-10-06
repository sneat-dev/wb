package cmdsession

import (
	"testing"

	"github.com/sneat-dev/wb/internal/session"
)

// sessionLabel presentation belongs to the session command family.
// sessionLabel picks the most specific human-readable name it can from a
// Record: Runtime+native ID, Runtime alone, native ID alone (falling back
// from NativeHarnessID to the legacy AgentID), or a fixed placeholder when
// the record carries neither.
func TestSessionLabelPrefersRuntimeAndNativeHarnessID(t *testing.T) {
	t.Parallel()
	got := sessionLabel(session.Record{Runtime: "claude-code", NativeHarnessID: "abc123"})
	if want := "claude-code/abc123"; got != want {
		t.Errorf("sessionLabel = %q, want %q", got, want)
	}
}

func TestSessionLabelFallsBackToLegacyAgentIDWhenNativeHarnessIDIsEmpty(t *testing.T) {
	t.Parallel()
	got := sessionLabel(session.Record{Runtime: "codex", AgentID: "legacy-9"})
	if want := "codex/legacy-9"; got != want {
		t.Errorf("sessionLabel = %q, want %q", got, want)
	}
}

func TestSessionLabelUsesRuntimeAloneWhenNoNativeIDIsKnown(t *testing.T) {
	t.Parallel()
	got := sessionLabel(session.Record{Runtime: "copilot"})
	if want := "copilot"; got != want {
		t.Errorf("sessionLabel = %q, want %q", got, want)
	}
}

func TestSessionLabelUsesNativeIDAloneWhenRuntimeIsEmpty(t *testing.T) {
	t.Parallel()
	got := sessionLabel(session.Record{NativeHarnessID: "xyz"})
	if want := "xyz"; got != want {
		t.Errorf("sessionLabel = %q, want %q", got, want)
	}
}

func TestSessionLabelIsAnUnnamedSessionWithNeitherRuntimeNorID(t *testing.T) {
	t.Parallel()
	got := sessionLabel(session.Record{})
	if want := "an unnamed session"; got != want {
		t.Errorf("sessionLabel = %q, want %q", got, want)
	}
}
