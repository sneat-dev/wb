package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionpark"
)

// TestWithParkedRemoteResumeCustodyResolvesMemberAfterCloneMigration covers
// park-and-resume-agent-sessions#req:resume-resolves-members-by-identity for
// the remote (cross-machine) resume path: the source-side member validation
// in WithParkedRemoteResumeCustody must resolve each member by identity --
// the same resolver acquire() (session_park_local.go) uses for local resume
// -- not by its recorded, possibly-stale CanonicalDir/WorktreesRoot/
// WorktreeDir. This exercises exactly the source-side validation after a
// `wb layout migrate`-style clone move, using the same lower-level
// clone-move primitives migrate.go itself calls (PlanCloneMove,
// RecordCloneMoveRelocationIntents, ApplyCloneMove,
// FinalizeCloneMoveRelocationReceipts); no real SSH courier is needed.
func TestWithParkedRemoteResumeCustodyResolvesMemberAfterCloneMigration(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "park-remote-migrated")
	useIdentityRemote(t, fixture, worktree)
	branch := gitTestOutput(t, worktree, "branch", "--show-current")
	gitTest(t, worktree, "push", "origin", branch)
	guard, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot, Admission: AdmissionEnforce})
	if err != nil {
		t.Fatal(err)
	}
	member, err := CaptureParkedSessionWorktree(context.Background(), fixture.projectsRoot, ListResult{
		Repository: "acme/app", CanonicalDir: guard.CanonicalDir, WorktreeDir: worktree,
		WorktreesRoot: guard.WorktreesRoot, Branch: branch,
	}, source)
	if err != nil {
		t.Fatal(err)
	}

	// Move the canonical clone exactly like `wb layout migrate` does: plan,
	// record relocation intents for every linked worktree, apply the move,
	// then finalize the receipts.
	destination := filepath.Join(fixture.projectsRoot, "github.com", "acme", "app")
	plan, err := PlanCloneMove(context.Background(), guard.CanonicalDir, destination)
	if err != nil {
		t.Fatal(err)
	}
	// A monotonic-clock time.Time (a raw time.Now()) round-trips through the
	// intent's JSON persistence losing that reading, so the on-disk record
	// compares unequal to the in-memory one at Finalize -- exactly the
	// pitfall migrate.go itself avoids with `time.Now().UTC()`.
	now := time.Now().UTC()
	pending, err := RecordCloneMoveRelocationIntents(fixture.projectsRoot, plan.Worktrees, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyCloneMove(context.Background(), guard.CanonicalDir, destination); err != nil {
		t.Fatal(err)
	}
	if err := FinalizeCloneMoveRelocationReceipts(pending, now); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(guard.CanonicalDir); !os.IsNotExist(statErr) {
		t.Fatalf("canonical must have moved away from its recorded path %s: err=%v", guard.CanonicalDir, statErr)
	}

	bundle := sessionpark.Bundle{
		SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: "park-remote-migrated",
		Source: source, Continuation: "private continuation", ParkedAt: time.Now().UTC(),
		Worktrees: []sessionpark.Worktree{member},
	}
	reached := false
	if err := WithParkedRemoteResumeCustody(context.Background(), fixture.projectsRoot, bundle, func() error {
		reached = true
		return nil
	}); err != nil {
		t.Fatalf("remote resume source-side validation after migration: %v", err)
	}
	if !reached {
		t.Fatal("delivery callback never reached")
	}
}
