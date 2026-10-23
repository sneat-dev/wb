package cmdworktree

import (
	"bytes"
	"errors"
	"github.com/sneat-dev/wb/internal/worktreerun"
	"strings"
	"testing"
)

type activeLimitedWriter struct{ Allow, Writes int }

func (writer *activeLimitedWriter) Write(raw []byte) (int, error) {
	if writer.Writes >= writer.Allow {
		return 0, errors.New("active output write failed")
	}
	writer.Writes++
	return len(raw), nil
}
func TestOriginalActiveTextBranches(t *testing.T) {
	t.Parallel()
	report := worktreerun.ActiveReport{
		SchemaVersion: 1,
		Local:         worktreerun.ActiveLocalStatus{Status: "incomplete", OmittedUnresolvedClaims: 2},
		Remote:        worktreerun.ActiveRemoteStatus{Status: "stale", Error: "one snapshot is stale"},
		Worktrees: []worktreerun.ActiveRow{
			{Locality: "local", Repository: "acme/app", Task: "alpha", Branch: "b", OwnerState: "active", Lifecycle: "working", Summary: "local one"},
			{Locality: "remote", Machine: "them/machine-b", Repository: "acme/app", Task: "beta", Branch: "b", OwnerState: "unknown", Lifecycle: "working", SnapshotStale: true},
		},
	}
	var out bytes.Buffer
	if err := writeActiveWorktreeText(&out, report); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"local: incomplete (2 unresolved claims omitted; inspect wb worktree list)",
		"remote: stale (one snapshot is stale)",
		"local local acme/app alpha b [active/working] — local one",
		"remote them/machine-b acme/app beta b [unknown/working] (STALE SNAPSHOT)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("active text missing %q:\n%s", want, text)
		}
	}

	// A clean report writes just the two status lines.
	out.Reset()
	if err := writeActiveWorktreeText(&out, worktreerun.ActiveReport{Local: worktreerun.ActiveLocalStatus{Status: "available"}, Remote: worktreerun.ActiveRemoteStatus{Status: "local_only"}}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "local: available\nremote: local_only\n" {
		t.Fatalf("clean report text = %q", got)
	}

	// Every write failure is propagated.
	for allow := 0; allow < 7; allow++ {
		if err := writeActiveWorktreeText(&activeLimitedWriter{Allow: allow}, report); err == nil {
			t.Fatalf("writeActiveWorktreeText with %d writes allowed returned nil", allow)
		}
	}
}

// This assertion preserves the active half of the original compound root sweep.
func TestOriginalActiveCompoundWriterSweep(t *testing.T) {
	t.Parallel()
	active := worktreerun.ActiveReport{
		SchemaVersion: 1,
		Local:         worktreerun.ActiveLocalStatus{Status: "incomplete", OmittedUnresolvedClaims: 2},
		Remote:        worktreerun.ActiveRemoteStatus{Status: "stale", Error: "stale snapshot"},
		Worktrees: []worktreerun.ActiveRow{
			{Locality: "local", Repository: "acme/a", Task: "t", Branch: "b", OwnerState: "active", Lifecycle: "working", Summary: "s"},
			{Locality: "remote", Machine: "m", Repository: "acme/b", Task: "t2", Branch: "b", OwnerState: "unknown", Lifecycle: "working", SnapshotStale: true},
		},
	}
	activeSweepWrites(t, 14, func(writer *activeLimitedWriter) error {
		return writeActiveWorktreeText(writer, active)
	})
}

func activeSweepWrites(t *testing.T, max int, run func(writer *activeLimitedWriter) error) {
	t.Helper()
	for allow := 0; allow <= max; allow++ {
		if err := run(&activeLimitedWriter{Allow: allow}); err == nil {
			return
		}
	}
	t.Fatalf("no write budget up to %d let the renderer finish", max)
}
