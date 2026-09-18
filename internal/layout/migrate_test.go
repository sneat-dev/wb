package layout

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func findMigrateClone(report MigrateReport, repository string) (MigrateClone, bool) {
	for _, clone := range report.Clones {
		if clone.Repository == repository {
			return clone, true
		}
	}
	return MigrateClone{}, false
}

// TestMigratePlansThenMovesAndRepoints encodes
// projects-root-layout#ac:migrate-plans-then-moves-and-repoints at the
// package level: a dry run changes nothing, apply moves the clone with
// uncommitted changes intact and repoints a nested and an external worktree,
// and a repeated apply is a no-op.
func TestMigratePlansThenMovesAndRepoints(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	canonical := initRemoteClone(t, root, "dal-go", "dalgo", "dal-go/dalgo")
	if err := os.WriteFile(filepath.Join(canonical, "uncommitted.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(canonical, ".worktrees", "t1")
	run(t, canonical, "git", "worktree", "add", "-b", "t1", nested)
	external := filepath.Join(root, "worktrees", "t2", "dal-go", "dalgo")
	if err := os.MkdirAll(filepath.Dir(external), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, canonical, "git", "worktree", "add", "-b", "t2", external)

	destination := filepath.Join(root, "github.com", "dal-go", "dalgo")

	dry, err := Migrate(context.Background(), root, MigrateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(canonical); err != nil {
		t.Fatal("dry run must not move the clone")
	}
	clone, found := findMigrateClone(dry, "dal-go/dalgo")
	if !found || clone.Status != "planned" || clone.Destination != destination {
		t.Fatalf("dry-run clone = %+v", clone)
	}
	if len(clone.Worktrees) != 2 {
		t.Fatalf("dry-run worktrees = %+v, want 2", clone.Worktrees)
	}
	applied, err := Migrate(context.Background(), root, MigrateOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	clone, found = findMigrateClone(applied, "dal-go/dalgo")
	if !found || clone.Status != "done" {
		t.Fatalf("apply clone = %+v, want done", clone)
	}
	if applied.ManifestID == "" || applied.ManifestPath == "" {
		t.Fatal("apply must record a manifest")
	}
	if _, err := os.Stat(applied.ManifestPath); err != nil {
		t.Fatalf("manifest file missing: %v", err)
	}
	// initRemoteClone leaves a sibling "-seed" checkout under the same legacy
	// owner directory, so the owner directory is correctly kept (it is not
	// empty) even though the migrated clone itself is gone from it.
	if _, err := os.Stat(canonical); !os.IsNotExist(err) {
		t.Fatalf("migrated clone must be gone from its legacy path, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "dal-go")); err != nil {
		t.Fatalf("legacy owner directory holding another checkout must be kept: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "uncommitted.txt")); err != nil {
		t.Fatal("uncommitted change must survive the move")
	}
	for _, marker := range []string{destination, filepath.Join(destination, ".worktrees", "t1"), external} {
		contents, err := os.ReadFile(filepath.Join(marker, ".worktree.md"))
		if err != nil {
			t.Fatalf("marker missing at %s: %v", marker, err)
		}
		if !strings.Contains(string(contents), destination) {
			t.Fatalf("marker at %s does not name %s:\n%s", marker, destination, contents)
		}
	}
	auditReport, err := Audit(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range auditReport.Findings {
		if finding.PathSlug == "dal-go/dalgo" && finding.Kind != KindOK {
			t.Fatalf("layout audit reports the migrated clone as %s: %+v", finding.Kind, finding)
		}
	}

	again, err := Migrate(context.Background(), root, MigrateOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	clone, found = findMigrateClone(again, "dal-go/dalgo")
	if !found || clone.Status != "already_done" {
		t.Fatalf("second apply clone = %+v, want already_done", clone)
	}
}

// TestMigrateSkipsUnsafeClones encodes
// projects-root-layout#ac:migrate-skips-unsafe-clones at the package level.
func TestMigrateSkipsUnsafeClones(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	_ = initRemoteClone(t, root, "acme", "clean", "acme/clean")
	_ = initRemoteClone(t, root, "acme", "mismatch", "other/mismatch")

	occupied := initRemoteClone(t, root, "acme", "occupied", "acme/occupied")
	_ = occupied
	if err := os.MkdirAll(filepath.Join(root, "github.com", "acme", "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}

	rebasing := initRemoteClone(t, root, "acme", "rebasing", "acme/rebasing")
	if err := os.MkdirAll(filepath.Join(rebasing, ".git", "rebase-merge"), 0o755); err != nil {
		t.Fatal(err)
	}

	claimed := initRemoteClone(t, root, "acme", "claimed", "acme/claimed")
	// worktrees.Create fetches and verifies against the clone's real origin,
	// so the origin must stay reachable until after the claim exists.
	run(t, claimed, "git", "remote", "set-url", "origin", filepath.Join(root, "acme", "claimed.git"))
	run(t, claimed, "git", "push", "-u", "origin", "main")
	created, err := worktrees.Create(context.Background(), []string{"acme/claimed"}, worktrees.CreateOptions{
		ProjectsRoot: root, Operation: "claim-task",
		WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create claimed worktree: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("create claimed worktree returned %d results", len(created))
	}
	run(t, claimed, "git", "remote", "set-url", "origin", "git@github.com:acme/claimed.git")

	report, err := Migrate(context.Background(), root, MigrateOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if !MigrateFailed(report) {
		t.Fatal("a run with refusals must be reported as failed")
	}
	expect := map[string]string{
		"acme/mismatch": "owner",
		"acme/occupied": "destination",
		"acme/rebasing": "rebase",
		"acme/claimed":  "claim",
	}
	for repository, wantSubstring := range expect {
		clone, found := findMigrateClone(report, repository)
		if !found || clone.Status != "skipped" {
			t.Fatalf("%s = %+v, want skipped", repository, clone)
		}
		if !strings.Contains(strings.ToLower(clone.Reason), wantSubstring) {
			t.Fatalf("%s reason = %q, want it to mention %q", repository, clone.Reason, wantSubstring)
		}
		if _, err := os.Stat(clone.Source); err != nil {
			t.Fatalf("%s must be left in place: %v", repository, err)
		}
	}
	clone, found := findMigrateClone(report, "acme/clean")
	if !found || clone.Status != "done" {
		t.Fatalf("clean clone = %+v, want done", clone)
	}
}

// TestMigrateIsReversible encodes
// projects-root-layout#ac:migrate-is-reversible at the package level.
func TestMigrateIsReversible(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	canonical := initRemoteClone(t, root, "dal-go", "dalgo", "dal-go/dalgo")
	nested := filepath.Join(canonical, ".worktrees", "t1")
	run(t, canonical, "git", "worktree", "add", "-b", "t1", nested)

	applied, err := Migrate(context.Background(), root, MigrateOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "github.com", "dal-go", "dalgo")
	if _, err := os.Stat(destination); err != nil {
		t.Fatal("apply must have moved the clone")
	}

	// Dry-run undo: reports the reversal plan without moving anything.
	dryUndo, err := Migrate(context.Background(), root, MigrateOptions{UndoID: applied.ManifestID})
	if err != nil {
		t.Fatal(err)
	}
	clone, found := findMigrateClone(dryUndo, "dal-go/dalgo")
	if !found || clone.Status != "planned" {
		t.Fatalf("dry-run undo clone = %+v, want planned", clone)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatal("dry-run undo must not move anything")
	}

	undone, err := Migrate(context.Background(), root, MigrateOptions{UndoID: applied.ManifestID, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	clone, found = findMigrateClone(undone, "dal-go/dalgo")
	if !found || clone.Status != "reversed" {
		t.Fatalf("undo clone = %+v, want reversed", clone)
	}
	if _, err := os.Stat(canonical); err != nil {
		t.Fatal("undo must restore the legacy clone path")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("undo must remove the host-level path")
	}
	for _, checkout := range []string{canonical, nested} {
		run(t, checkout, "git", "status", "--porcelain=v1")
	}

	// Re-running the same undo is a no-op that reports the manifest entry as
	// already reversed rather than skipped.
	again, err := Migrate(context.Background(), root, MigrateOptions{UndoID: applied.ManifestID, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	clone, found = findMigrateClone(again, "dal-go/dalgo")
	if !found || clone.Status != "skipped" {
		t.Fatalf("repeated undo clone = %+v, want skipped (already reversed)", clone)
	}
}

// TestMigrateRemovesAnEmptiedLegacyOwnerDirectory covers the branch where the
// legacy owner directory holds nothing else once its one clone has moved.
func TestMigrateRemovesAnEmptiedLegacyOwnerDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	owner := filepath.Join(root, "acme")
	canonical := filepath.Join(owner, "solo")
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, canonical, "git", "init", "-b", "main")
	run(t, canonical, "git", "config", "user.email", "wb@example.test")
	run(t, canonical, "git", "config", "user.name", "WB Test")
	if err := os.WriteFile(filepath.Join(canonical, "README.md"), []byte("acme/solo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(t, canonical, "git", "add", ".")
	run(t, canonical, "git", "commit", "-m", "init")
	run(t, canonical, "git", "remote", "add", "origin", "git@github.com:acme/solo.git")

	if _, err := Migrate(context.Background(), root, MigrateOptions{Apply: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(owner); !os.IsNotExist(err) {
		t.Fatalf("emptied legacy owner directory must be removed, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "github.com", "acme", "solo")); err != nil {
		t.Fatal("clone must have moved to the host level")
	}
}

// TestMigrateRepositoryFilterRestrictsToNamedRepositories exercises the
// owner/repository argument filter.
func TestMigrateRepositoryFilterRestrictsToNamedRepositories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_ = initRemoteClone(t, root, "acme", "only", "acme/only")
	_ = initRemoteClone(t, root, "acme", "other", "acme/other")

	report, err := Migrate(context.Background(), root, MigrateOptions{Repositories: []string{"acme/only"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Clones) != 1 {
		t.Fatalf("clones = %+v, want exactly acme/only", report.Clones)
	}
	if _, found := findMigrateClone(report, "acme/only"); !found {
		t.Fatalf("filtered report missing acme/only: %+v", report.Clones)
	}
}

// TestMigrateReportsExistingHostLevelClonesAsAlreadyDone covers the branch
// where discovery finds a clone that never needed to move.
func TestMigrateReportsExistingHostLevelClonesAsAlreadyDone(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	hostLevel := filepath.Join(root, "github.com", "acme", "app")
	seedRemoteClone(t, root, "app", "acme/app", hostLevel)

	report, err := Migrate(context.Background(), root, MigrateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	clone, found := findMigrateClone(report, "acme/app")
	if !found || clone.Status != "already_done" || clone.Source != hostLevel || clone.Destination != hostLevel {
		t.Fatalf("host-level clone = %+v, want already_done at %s", clone, hostLevel)
	}
	if MigrateFailed(report) {
		t.Fatal("an already-migrated clone must not be reported as a finding")
	}
}

// TestMigrateUndoUnknownManifestFails covers the manifest read-error branch.
func TestMigrateUndoUnknownManifestFails(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(context.Background(), root, MigrateOptions{UndoID: "does-not-exist", Apply: true}); err == nil {
		t.Fatal("undo of an unknown manifest id must fail")
	}
}

// TestDaemonRunningReflectsRecordedState covers both branches of the
// daemon-restart-required signal: absent state and a ready daemon.
func TestDaemonRunningReflectsRecordedState(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if daemonRunning(root) {
		t.Fatal("no daemon state recorded must report not running")
	}
	statePath, err := daemon.StatePath(root)
	if err != nil {
		t.Fatal(err)
	}
	state := daemon.State{
		SchemaVersion: daemon.StateSchemaVersion,
		Status:        daemon.StatusReady,
		Listen:        "127.0.0.1:0",
		Queue:         daemon.Queue{SchemaVersion: daemon.QueueSchemaVersion},
		UpdatedAt:     time.Now().UTC(),
	}
	if err := (daemon.Store{Path: statePath}).Save(state); err != nil {
		t.Fatal(err)
	}
	if !daemonRunning(root) {
		t.Fatal("a recorded ready daemon must report running")
	}
}

// TestGitOperationInProgressDetectsEveryMarker exercises every marker
// GitOperationInProgress checks, directly against the package it lives in.
func TestGitOperationInProgressDetectsEveryMarker(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	canonical := initRemoteClone(t, root, "acme", "state", "acme/state")

	if reason, err := worktrees.GitOperationInProgress(context.Background(), canonical); err != nil || reason != "" {
		t.Fatalf("clean clone reason = %q, err = %v", reason, err)
	}
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "index.lock"} {
		path := filepath.Join(canonical, ".git", marker)
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		reason, err := worktrees.GitOperationInProgress(context.Background(), canonical)
		if err != nil || reason == "" {
			t.Fatalf("marker %s: reason = %q, err = %v", marker, reason, err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	for _, marker := range []string{"rebase-merge", "rebase-apply"} {
		path := filepath.Join(canonical, ".git", marker)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		reason, err := worktrees.GitOperationInProgress(context.Background(), canonical)
		if err != nil || reason == "" {
			t.Fatalf("marker %s: reason = %q, err = %v", marker, reason, err)
		}
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
}

// TestMigrateSkipsACloneWithAnUnPickedUpParkedSession covers reviewer
// BLOCKING #2's second refusal: a parked session (`wb session park`) that has
// not yet been resumed binds one of its member worktrees to an exact active
// Work Log claim and custody record. Migrating that worktree's clone out from
// under it before the session resumes would leave that binding pointing at a
// location it no longer describes, so it must refuse instead.
func TestMigrateSkipsACloneWithAnUnPickedUpParkedSession(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	canonical := initRemoteClone(t, root, "acme", "parked", "acme/parked")

	parkID, err := sessionpark.NewID()
	if err != nil {
		t.Fatal(err)
	}
	store := sessionpark.NewStore(filepath.Join(root, ".wb", "parked-sessions"))
	now := time.Now().UTC()
	bundle := sessionpark.Bundle{
		SchemaVersion:   sessionpark.SchemaVersion,
		ParkedSessionID: parkID,
		Source: session.Record{
			PID: 1234, WBSessionID: "wbs-source", Machine: "machine-1", Runtime: "codex", StartedAt: now,
		},
		Continuation: "continue here",
		ParkedAt:     now,
		Worktrees: []sessionpark.Worktree{
			{
				Repository: "acme/parked", CanonicalDir: canonical, WorktreeDir: canonical,
				Branch: "main", Head: strings.Repeat("a", 40),
			},
		},
	}
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}

	report, err := Migrate(context.Background(), root, MigrateOptions{Repositories: []string{"acme/parked"}})
	if err != nil {
		t.Fatal(err)
	}
	clone, found := findMigrateClone(report, "acme/parked")
	if !found || clone.Status != "skipped" || !strings.Contains(clone.Reason, "parked session") {
		t.Fatalf("clone with an un-picked-up parked session = %+v, want skipped naming the parked session", clone)
	}
}

// TestMigrateRepairsAStrandedAlreadyAtDestinationClone covers reviewer
// BLOCKING #3: a clone that reached its host-level path outside a full,
// verified ApplyCloneMove (a manual rename, or an earlier migration
// interrupted after the directory move but before `git worktree repair`)
// must not be silently reported already_done while its linked worktree is
// unusable. Migrate must detect the broken registration, repair it, and
// report the clone as `repaired` — and a subsequent run must then see it as
// genuinely `already_done`.
func TestMigrateRepairsAStrandedAlreadyAtDestinationClone(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	canonical := initRemoteClone(t, root, "dal-go", "dalgo", "dal-go/dalgo")
	external := filepath.Join(root, "worktrees", "t1", "dal-go", "dalgo")
	if err := os.MkdirAll(filepath.Dir(external), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, canonical, "git", "worktree", "add", "-b", "t1", external)

	destination := filepath.Join(root, "github.com", "dal-go", "dalgo")
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	// Simulate a half-done move: the clone directory itself was renamed to its
	// host-level destination, but the worktree registry was never repaired, so
	// the clone's own records still point at the old (now nonexistent) path
	// and the external worktree's Git administration still resolves to it.
	if err := os.Rename(canonical, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(external); err != nil {
		t.Fatal("stranded worktree fixture must still exist on disk")
	}

	report, err := Migrate(context.Background(), root, MigrateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	clone, found := findMigrateClone(report, "dal-go/dalgo")
	if !found || clone.Status != "repaired" {
		t.Fatalf("stranded clone = %+v, want status repaired", clone)
	}
	if err := worktrees.VerifyClonePlacement(context.Background(), destination, []string{external}); err != nil {
		t.Fatalf("clone must be genuinely repaired: %v", err)
	}

	// initRemoteClone leaves a sibling "-seed" checkout under the same legacy
	// owner directory with no usable origin, which the scan (correctly)
	// reports skipped; restrict to the repository under test so that
	// unrelated fixture noise doesn't fail this assertion.
	again, err := Migrate(context.Background(), root, MigrateOptions{Repositories: []string{"dal-go/dalgo"}})
	if err != nil {
		t.Fatal(err)
	}
	clone, found = findMigrateClone(again, "dal-go/dalgo")
	if !found || clone.Status != "already_done" {
		t.Fatalf("re-run after repair = %+v, want already_done", clone)
	}
	if MigrateFailed(again) {
		t.Fatal("a repaired-then-verified clone must not be reported as a finding")
	}
}
