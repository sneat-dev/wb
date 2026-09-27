package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCloneMoveRelocationLetsClaimResolveAfterMove covers reviewer BLOCKING
// #2: a live claim's Worktree field is an immutable path frozen at claim
// creation, so after a clone-placement migration physically moves the
// checkout, activeWorkLogClaim (and everything built on it, including `wb
// worktree land`'s "authoritative active Work Log claim" check via
// LoadWorkLogView) must still resolve the claim at its new location. It must
// do so through the same relocation-receipt journal RelocateRepository and
// `wb worktree relocate` use, recorded here by
// RecordCloneMoveRelocationIntents/FinalizeCloneMoveRelocationReceipts around
// ApplyCloneMove.
func TestCloneMoveRelocationLetsClaimResolveAfterMove(t *testing.T) {
	projectsRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(projectsRoot, ".wb")

	canonical := filepath.Join(projectsRoot, "acme", "app")
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, canonical, "init", "-b", "main")
	gitTest(t, canonical, "config", "user.email", "wb@example.test")
	gitTest(t, canonical, "config", "user.name", "WB Test")
	if err := os.WriteFile(filepath.Join(canonical, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, canonical, "add", ".")
	gitTest(t, canonical, "commit", "-m", "init")
	baseSHA := gitTestOutput(t, canonical, "rev-parse", "HEAD")

	nested := filepath.Join(canonical, ".worktrees", "t1")
	gitTest(t, canonical, "worktree", "add", "-b", "t1", nested)

	ctx := context.Background()
	outcome, err := recordWorkLogWithHooks(home, "t1", CreateResult{
		Repository: "acme/app", WorktreeDir: nested, Branch: "t1", Base: "main", BaseSHA: baseSHA,
	}, WorkLogOptions{
		EffortID: "t1", RunID: "run", AgentID: "codex-1", Model: "unknown",
		TaskSummary: "Prove claim resolution survives clone migration", WBSessionID: "wbs-clone-move",
	}, workLogPublicationHooks{})
	if err != nil {
		t.Fatal(err)
	}

	if _, _, _, err := activeWorkLogClaim(home, nested); err != nil {
		t.Fatalf("claim must resolve before the move: %v", err)
	}

	destination := filepath.Join(projectsRoot, "github.com", "acme", "app")
	plan, err := PlanCloneMove(ctx, canonical, destination)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	pending, err := RecordCloneMoveRelocationIntents(projectsRoot, plan.Worktrees, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) == 0 {
		t.Fatal("expected a relocation intent recorded for the claimed worktree")
	}
	if _, err := ApplyCloneMove(ctx, canonical, destination); err != nil {
		t.Fatal(err)
	}
	if err := FinalizeCloneMoveRelocationReceipts(pending, now); err != nil {
		t.Fatal(err)
	}

	movedNested := filepath.Join(destination, ".worktrees", "t1")
	claim, projection, _, err := activeWorkLogClaim(home, movedNested)
	if err != nil {
		t.Fatalf("claim must resolve after the move: %v", err)
	}
	if claim.ClaimID != outcome.ClaimID {
		t.Fatalf("resolved claim = %+v, want claim ID %s", claim, outcome.ClaimID)
	}
	if projection.Lifecycle != "active" {
		t.Fatalf("resolved projection lifecycle = %q, want active", projection.Lifecycle)
	}

	// The claim's un-migrated frozen path must no longer resolve: it is gone.
	if _, _, _, err := activeWorkLogClaim(home, nested); err == nil {
		t.Fatal("claim must not resolve at its now-nonexistent original path")
	}
}

func TestReconcileClonePlacementRepairsManuallyMovedNestedWorktree(t *testing.T) {
	projectsRoot := t.TempDir()
	legacy := filepath.Join(projectsRoot, "acme", "app")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, legacy, "init", "-b", "main")
	gitTest(t, legacy, "config", "user.email", "wb@example.test")
	gitTest(t, legacy, "config", "user.name", "WB Test")
	if err := os.WriteFile(filepath.Join(legacy, "README.md"), []byte("nested checkout\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, legacy, "add", "README.md")
	gitTest(t, legacy, "commit", "-m", "initial")
	nested := filepath.Join(legacy, ".worktrees", "feature")
	gitTest(t, legacy, "worktree", "add", "-b", "feature", nested)
	destination := filepath.Join(projectsRoot, "github.com", "acme", "app")
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(legacy, destination); err != nil {
		t.Fatal(err)
	}
	movedNested := filepath.Join(destination, ".worktrees", "feature")
	beforePointer, err := readWorktreeCommonDir(movedNested)
	if err != nil || filepath.Clean(beforePointer) == filepath.Join(destination, ".git") {
		t.Fatalf("manual move unexpectedly repaired linked pointer: %q, %v", beforePointer, err)
	}
	ctx := context.Background()
	status, _, err := ReconcileClonePlacement(ctx, destination, legacy, false)
	if err != nil || status != "needs_repair" {
		t.Fatalf("dry-run reconciliation = (%q, %v), want needs_repair", status, err)
	}
	stillStale, err := readWorktreeCommonDir(movedNested)
	if err != nil || stillStale != beforePointer {
		t.Fatalf("dry run changed linked pointer: before=%q after=%q err=%v", beforePointer, stillStale, err)
	}
	status, _, err = ReconcileClonePlacement(ctx, destination, legacy, true)
	if err != nil || status != "repaired" {
		t.Fatalf("applied reconciliation = (%q, %v), want repaired", status, err)
	}
	if err := VerifyClonePlacement(ctx, destination, []string{movedNested}); err != nil {
		t.Fatalf("repaired checkout did not pass registration and Git status verification: %v", err)
	}
	status, _, err = ReconcileClonePlacement(ctx, destination, legacy, false)
	if err != nil || status != "verified" {
		t.Fatalf("recheck after repair = (%q, %v), want verified", status, err)
	}
}
