package worktrees

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCheckedCleanupClaimRechecksProjectionAndOwnsFence(t *testing.T) {
	t.Parallel()
	home, worktree := t.TempDir(), t.TempDir()
	projection := workLogProjection{Version: 1, EffortID: "effort", RunID: "run", ClaimID: strings.Repeat("a", 64), Lifecycle: "active"}
	projectionDir := filepath.Join(worktree, workLogProjectionDirectory)
	if err := os.Mkdir(projectionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeProjection := func(value workLogProjection) {
		t.Helper()
		content, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(projectionDir, workLogProjectionName), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeProjection(projection)
	locked, _, err := openCheckedCleanupClaim(home, worktree, projection)
	if locked != nil || err == nil || !strings.Contains(err.Error(), "open private work-log run:") {
		t.Fatalf("missing run: owner=%v, error=%v", locked, err)
	}
	run, runPath, err := openWorkLogRun(home, projection.EffortID, projection.RunID, true)
	if err != nil {
		t.Fatal(err)
	}
	locksPath := filepath.Join(runPath, "locks")
	if err := os.WriteFile(locksPath, []byte("not a directory"), 0o600); err != nil {
		_ = run.Close()
		t.Fatal(err)
	}
	locked, _, err = openCheckedCleanupClaim(home, worktree, projection)
	if locked != nil || err == nil || !strings.Contains(err.Error(), "open claim-lock directory:") {
		_ = run.Close()
		t.Fatalf("obstructed claim fence: owner=%v, error=%v", locked, err)
	}
	if err := os.Remove(locksPath); err != nil {
		_ = run.Close()
		t.Fatal(err)
	}
	locked, _, err = openCheckedCleanupClaim(home, worktree, projection)
	if locked != nil || !errors.Is(err, os.ErrNotExist) {
		_ = run.Close()
		t.Fatalf("missing claims directory: owner=%v, error=%v", locked, err)
	}
	claims, err := openPrivateChild(run, "claims", true)
	if err != nil {
		_ = run.Close()
		t.Fatal(err)
	}
	locked, _, err = openCheckedCleanupClaim(home, worktree, projection)
	if locked != nil || err == nil || !strings.Contains(err.Error(), "read immutable work-log claim:") {
		_ = claims.Close()
		_ = run.Close()
		t.Fatalf("missing immutable claim: owner=%v, error=%v", locked, err)
	}
	claim := workLogClaim{Version: 1, EffortID: projection.EffortID, RunID: projection.RunID, ClaimID: projection.ClaimID}
	if err := writeJSONImmutableAt(claims, projection.ClaimID+".json", claim, false); err != nil {
		_ = claims.Close()
		_ = run.Close()
		t.Fatal(err)
	}
	_ = claims.Close()
	_ = run.Close()

	stale := projection
	stale.Lifecycle = "terminal"
	writeProjection(stale)
	locked, _, err = openCheckedCleanupClaim(home, worktree, projection)
	if locked != nil || err == nil || !strings.Contains(err.Error(), "projection changed while waiting for claim fence") {
		t.Fatalf("stale projection: owner=%v, error=%v", locked, err)
	}

	writeProjection(projection)
	locked, got, err := openCheckedCleanupClaim(home, worktree, projection)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClaimID != claim.ClaimID || got.EffortID != claim.EffortID {
		locked.close()
		t.Fatalf("immutable claim = %+v", got)
	}
	locked.close()
	if _, err := locked.directory.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("run directory remains open after cleanup claim release: %v", err)
	}
}
