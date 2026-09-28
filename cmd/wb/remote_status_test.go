package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/remotestate"
)

// TestOpenRemoteRejectsUnsupportedProvider drives the default branch of
// openRemote's provider switch: a provider name that is neither "git" nor
// "hub" is refused as a usage error rather than reaching either backend.
func TestOpenRemoteRejectsUnsupportedProvider(t *testing.T) {
	t.Parallel()
	_, err := openRemote(remotestate.Config{Provider: "ftp"}, t.TempDir())
	if err == nil {
		t.Fatal("openRemote(ftp) returned nil error, want a refusal")
	}
	exitErr, ok := err.(*exitError)
	if !ok {
		t.Fatalf("openRemote(ftp) error type = %T, want *exitError", err)
	}
	if exitErr.code != exitUsage {
		t.Fatalf("openRemote(ftp) exit code = %d, want %d", exitErr.code, exitUsage)
	}
	const want = "remote.provider ftp is not supported"
	if exitErr.message != want {
		t.Fatalf("openRemote(ftp) message = %q, want %q", exitErr.message, want)
	}
}

// TestWriteRemoteStatusDiagnosticsRendersEachMismatch drives the range loop
// over diagnostics.Mismatches: every mismatch gets its own warning line.
func TestWriteRemoteStatusDiagnosticsRendersEachMismatch(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	writeRemoteStatusDiagnostics(&out, remoteStatusDiagnostics{
		Provider: "hub", Store: "memory",
		Mismatches: []string{"laptop-a", "laptop-b"},
	})
	rendered := out.String()
	for _, want := range []string{
		"warning: remote provider mismatch: laptop-a; configured store is memory",
		"warning: remote provider mismatch: laptop-b; configured store is memory",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("output missing %q: %q", want, rendered)
		}
	}
}

// TestWriteClaimOutcomeSkipsEmptyText drives the "text == \"\"" early-return
// branch in text mode: nothing is written and no error is returned.
func TestWriteClaimOutcomeSkipsEmptyText(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	err := writeClaimOutcome(&out, false, remotestate.ClaimOutcome{}, "")
	if err != nil {
		t.Fatalf("writeClaimOutcome returned %v, want nil", err)
	}
	if out.Len() != 0 {
		t.Fatalf("writeClaimOutcome wrote %q, want nothing", out.String())
	}
}
