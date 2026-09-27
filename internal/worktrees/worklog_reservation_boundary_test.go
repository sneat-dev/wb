package worktrees

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreApplyRenameReservationKeepsExactPromptAndTerminalHistory(t *testing.T) {
	home := t.TempDir()
	if found, err := findPreApplyRenameReservations(home, "destination"); err != nil || len(found) != 0 {
		t.Fatalf("missing Work Log unexpectedly has reservations: %+v, %v", found, err)
	}
	options, err := (WorkLogOptions{EffortID: "destination", RunID: "run", Model: "unknown"}).WithOriginalPromptFromStdin([]byte("rename request\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reservePreApplyRenameWorkLog(home, "source", "destination", options); err != nil {
		t.Fatal(err)
	}
	if err := reservePreApplyRenameWorkLog(home, "source", "destination", options); err != nil {
		t.Fatalf("same immutable reservation was not idempotent: %v", err)
	}
	found, err := findPreApplyRenameReservations(home, "destination")
	if err != nil || len(found) != 1 || found[0].OldTask != "source" || found[0].terminalized {
		t.Fatalf("reserved prompt not discoverable: %+v, %v", found, err)
	}
	if err := terminalizePreApplyRenameReservation(home, found[0]); err != nil {
		t.Fatal(err)
	}
	if err := terminalizePreApplyRenameReservation(home, found[0]); err != nil {
		t.Fatalf("terminalization was not idempotent: %v", err)
	}
	found, err = findPreApplyRenameReservations(home, "destination")
	if err != nil || len(found) != 1 || !found[0].terminalized {
		t.Fatalf("terminalized reservation no longer discoverable: %+v, %v", found, err)
	}
	conflicting := options
	if err := reservePreApplyRenameWorkLog(home, "other-source", "destination", conflicting); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting source was allowed to reuse reservation: %v", err)
	}
	archive := filepath.Join(home, "worklogs", "destination", "runs", "run", "original-prompt.txt")
	if err := os.WriteFile(archive, []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := findPreApplyRenameReservations(home, "destination"); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("changed prompt archive accepted as terminal evidence: %v", err)
	}
}

func TestPreApplyRenameReservationDoesNotAuthorizeClaimedRun(t *testing.T) {
	home := t.TempDir()
	options, err := (WorkLogOptions{EffortID: "destination", RunID: "run", Model: "unknown"}).WithOriginalPromptFromStdin([]byte("rename request\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reservePreApplyRenameWorkLog(home, "source", "destination", options); err != nil {
		t.Fatal(err)
	}
	runDir, _, err := openWorkLogRun(home, "destination", "run", false)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := openPrivateChild(runDir, "claims", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONImmutableAt(claims, "owned.json", map[string]any{"version": 1}, false); err != nil {
		t.Fatal(err)
	}
	_ = claims.Close()
	_ = runDir.Close()
	found, err := findPreApplyRenameReservations(home, "destination")
	if err != nil || len(found) != 0 {
		t.Fatalf("claimed run remained abortable as a prompt-only reservation: %+v, %v", found, err)
	}
}
