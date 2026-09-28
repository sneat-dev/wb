package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadWorkLogClaimAtRejectsMissingAndMalformedRecords(t *testing.T) {
	t.Parallel()
	run, runPath, err := openWorkLogRun(t.TempDir(), "effort", "run", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run.Close() }()
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
}
