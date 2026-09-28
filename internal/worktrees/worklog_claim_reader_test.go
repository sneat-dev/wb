package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadWorkLogRecordsRejectsMissingAndMalformedRecords(t *testing.T) {
	t.Parallel()
	run, runPath, err := openWorkLogRun(t.TempDir(), "effort", "run", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	claimID := strings.Repeat("c", 64)
	if _, err := readWorkLogClaimAt(run, claimID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing claims directory: %v", err)
	}
	claims, err := openPrivateChild(run, "claims", true)
	if err != nil {
		t.Fatal(err)
	}
	want := workLogClaim{ClaimID: claimID, Task: "read-claim"}
	if err := writeJSONImmutableAt(claims, claimID+".json", want, false); err != nil {
		_ = claims.Close()
		t.Fatal(err)
	}
	_ = claims.Close()
	got, err := readWorkLogClaimAt(run, claimID)
	if err != nil || got.ClaimID != want.ClaimID || got.Task != want.Task {
		t.Fatalf("immutable claim: got=%+v, error=%v", got, err)
	}
	if err := os.WriteFile(filepath.Join(runPath, "claims", "malformed.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorkLogClaimAt(run, "malformed"); err == nil {
		t.Fatal("malformed immutable claim was accepted")
	}
	if _, err := readWorkLogTerminalAt(run, claimID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing terminal directory: %v", err)
	}
	terminals, err := openPrivateChild(run, "terminals", true)
	if err != nil {
		t.Fatal(err)
	}
	wantTerminal := workLogTerminalRecord{workLogClaim: want, Disposition: "landed"}
	if err := writeJSONImmutableAt(terminals, claimID+".json", wantTerminal, false); err != nil {
		_ = terminals.Close()
		t.Fatal(err)
	}
	_ = terminals.Close()
	gotTerminal, err := readWorkLogTerminalAt(run, claimID)
	if err != nil || gotTerminal.ClaimID != claimID || gotTerminal.Disposition != "landed" {
		t.Fatalf("immutable terminal: got=%+v, error=%v", gotTerminal, err)
	}
}
