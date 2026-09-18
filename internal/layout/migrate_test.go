package layout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
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

// snapshotGitAdminFiles checksums every Git administrative file under root —
// anything inside a `.git` directory, or a linked worktree's own `.git`
// pointer file — so a caller can prove a run touched none of them. It
// deliberately does not look at ordinary working-tree content: the contract
// under test is "no Git administration changed," not "the working tree is
// byte-identical."
func snapshotGitAdminFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	sums := map[string]string{}
	sep := string(filepath.Separator)
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.Contains(path, sep+".git"+sep) && !strings.HasSuffix(path, sep+".git") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		sum := sha256.Sum256(data)
		sums[path] = hex.EncodeToString(sum[:])
		return nil
	})
	return sums
}

// TestMigrateDryRunNeverRepairsAStrandedClone covers BLOCKING #A of the
// second review round: without --apply, a clone already at its host-level
// path whose worktree registration was left stranded by an earlier or
// partial move must only be DETECTED, reported `needs_repair` (the
// dry-run analogue of `planned`), and left entirely untouched — not
// silently repaired, which is exactly what a plain reconcile-and-fix scan
// running on every dry run would otherwise do. Every Git administrative
// file's checksum is snapshotted before and after the dry run and must not
// differ by a single byte. --apply then performs the actual repair, and a
// subsequent dry run sees it as genuinely `already_done`.
func TestMigrateDryRunNeverRepairsAStrandedClone(t *testing.T) {
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

	before := snapshotGitAdminFiles(t, root)
	report, err := Migrate(context.Background(), root, MigrateOptions{Repositories: []string{"dal-go/dalgo"}})
	if err != nil {
		t.Fatal(err)
	}
	after := snapshotGitAdminFiles(t, root)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("dry run must not change any Git administrative file\nbefore=%v\nafter=%v", before, after)
	}
	clone, found := findMigrateClone(report, "dal-go/dalgo")
	if !found || clone.Status != "needs_repair" {
		t.Fatalf("stranded clone dry run = %+v, want status needs_repair", clone)
	}
	if MigrateFailed(report) {
		t.Fatal("needs_repair, like planned, must not be reported as a finding")
	}
	if err := worktrees.VerifyClonePlacement(context.Background(), destination, []string{external}); err == nil {
		t.Fatal("dry run must not have actually repaired the stranded worktree")
	}

	applied, err := Migrate(context.Background(), root, MigrateOptions{Repositories: []string{"dal-go/dalgo"}, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	clone, found = findMigrateClone(applied, "dal-go/dalgo")
	if !found || clone.Status != "repaired" {
		t.Fatalf("apply on stranded clone = %+v, want status repaired", clone)
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

// TestMigrateRepairsANestedStrandedWorktree covers SERIOUS #B of the second
// review round: a clone whose linked worktree lives INSIDE it (nested under
// `.worktrees/`) can be stranded the same way — a manual `mv`, or an
// interrupted earlier migration, renamed the whole clone directory tree
// (nested worktree included) without ever running `git worktree repair`.
// The nested worktree's directory genuinely exists at its new, rebased
// location; only its Git registration is stale, still naming the pre-move
// legacy path. Passing that stale, now-nonexistent path straight to `git
// worktree repair` is exactly what used to fail with "invalid path" — the
// fix is to rebase it onto the clone's current location first, which is a
// path that actually exists, before calling repair.
func TestMigrateRepairsANestedStrandedWorktree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	canonical := initRemoteClone(t, root, "dal-go", "dalgo", "dal-go/dalgo")
	nested := filepath.Join(canonical, ".worktrees", "t1")
	run(t, canonical, "git", "worktree", "add", "-b", "t1", nested)

	destination := filepath.Join(root, "github.com", "dal-go", "dalgo")
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	// The whole tree — clone and its nested worktree together — moves in one
	// rename, exactly as a manual `mv` or an interrupted migration would
	// leave it: the nested worktree's directory now lives at its correctly
	// rebased new location, but neither side's Git registration was updated.
	if err := os.Rename(canonical, destination); err != nil {
		t.Fatal(err)
	}
	rebasedNested := filepath.Join(destination, ".worktrees", "t1")
	if _, err := os.Stat(rebasedNested); err != nil {
		t.Fatal("nested worktree fixture must have moved along with the clone")
	}

	dry, err := Migrate(context.Background(), root, MigrateOptions{Repositories: []string{"dal-go/dalgo"}})
	if err != nil {
		t.Fatal(err)
	}
	clone, found := findMigrateClone(dry, "dal-go/dalgo")
	if !found || clone.Status != "needs_repair" {
		t.Fatalf("nested-stranded clone dry run = %+v, want status needs_repair", clone)
	}

	applied, err := Migrate(context.Background(), root, MigrateOptions{Repositories: []string{"dal-go/dalgo"}, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	clone, found = findMigrateClone(applied, "dal-go/dalgo")
	if !found || clone.Status != "repaired" {
		t.Fatalf("apply on nested-stranded clone = %+v, want status repaired (not failed with an invalid-path error)", clone)
	}
	if err := worktrees.VerifyClonePlacement(context.Background(), destination, []string{rebasedNested}); err != nil {
		t.Fatalf("nested worktree must be genuinely repaired: %v", err)
	}
}

// TestMigrateUndoRejectsPathTraversalID covers the `--undo <id>` path-segment
// validation: an id must never be interpolated into the manifest path
// unchecked, or a value like "../../etc" could make undo reach outside
// <root>/.wb/layout-migrations/ entirely.
func TestMigrateUndoRejectsPathTraversalID(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// An empty UndoID is not exercised here: Migrate treats it as "not an
	// undo" and dispatches to migrateApply instead, so validateMigrationID
	// never sees it through this entry point.
	for _, id := range []string{"../../etc", "..", ".", "a/b", "a\\b"} {
		if _, err := Migrate(context.Background(), root, MigrateOptions{UndoID: id, Apply: true}); err == nil {
			t.Fatalf("undo id %q must be rejected", id)
		}
		if _, err := Migrate(context.Background(), root, MigrateOptions{UndoID: id}); err == nil {
			t.Fatalf("dry-run undo id %q must be rejected", id)
		}
	}
}

// TestMigrateUndoReportsKeptHostOwnerDirectory covers MINOR #9's undo-side
// reporting requirement: when reversing one of several clones that share a
// host-level owner directory, the directory that is intentionally left in
// place (because a sibling clone still lives there) must be named in
// MigrateReport.KeptOwners, not silently skipped.
func TestMigrateUndoReportsKeptHostOwnerDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_ = initRemoteClone(t, root, "acme", "one", "acme/one")
	_ = initRemoteClone(t, root, "acme", "two", "acme/two")

	// Apply each repository in its own run, so each gets its own manifest and
	// undo can reverse exactly one of them: migrateUndo reverses every
	// completed entry in the manifest it is given, with no repository filter
	// of its own.
	appliedOne, err := Migrate(context.Background(), root, MigrateOptions{Repositories: []string{"acme/one"}, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if clone, found := findMigrateClone(appliedOne, "acme/one"); !found || clone.Status != "done" {
		t.Fatalf("acme/one = %+v, want done", clone)
	}
	appliedTwo, err := Migrate(context.Background(), root, MigrateOptions{Repositories: []string{"acme/two"}, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if clone, found := findMigrateClone(appliedTwo, "acme/two"); !found || clone.Status != "done" {
		t.Fatalf("acme/two = %+v, want done", clone)
	}
	hostOrgDir := filepath.Join(root, "github.com", "acme")
	if _, err := os.Stat(hostOrgDir); err != nil {
		t.Fatalf("host owner directory must still hold both clones: %v", err)
	}

	undone, err := Migrate(context.Background(), root, MigrateOptions{UndoID: appliedOne.ManifestID, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	clone, found := findMigrateClone(undone, "acme/one")
	if !found || clone.Status != "reversed" {
		t.Fatalf("acme/one undo = %+v, want reversed", clone)
	}
	if _, err := os.Stat(hostOrgDir); err != nil {
		t.Fatalf("host owner directory must be kept while acme/two still lives there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hostOrgDir, "two")); err != nil {
		t.Fatalf("acme/two must still be at the host level: %v", err)
	}
	found = false
	for _, kept := range undone.KeptOwners {
		if kept.Path == hostOrgDir {
			found = true
			if kept.Reason == "" {
				t.Fatal("kept owner must carry a reason")
			}
		}
	}
	if !found {
		t.Fatalf("kept owners = %+v, want %s reported", undone.KeptOwners, hostOrgDir)
	}
}

// TestMigrateBusyProcessRefusesAClone covers SERIOUS #4's third bullet: a
// live process whose current working directory sits inside a clone (or one
// of its linked worktrees) must refuse the move, naming the PID and command,
// even though neither a Git-operation marker nor a Work Log claim would ever
// observe a bare shell sitting there. Linux-only: /proc has no equivalent on
// other kernels, which is exactly what busyProcessCheckSupported signals.
func TestMigrateBusyProcessRefusesAClone(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("busy-process detection only runs on linux")
	}
	t.Parallel()
	root := t.TempDir()
	canonical := initRemoteClone(t, root, "acme", "busy", "acme/busy")

	cmd := exec.Command("sleep", "60")
	cmd.Dir = canonical
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	report, err := Migrate(context.Background(), root, MigrateOptions{Repositories: []string{"acme/busy"}})
	if err != nil {
		t.Fatal(err)
	}
	clone, found := findMigrateClone(report, "acme/busy")
	if !found || clone.Status != "skipped" {
		t.Fatalf("acme/busy = %+v, want skipped", clone)
	}
	if !strings.Contains(clone.Reason, "process") || !strings.Contains(clone.Reason, "working directory") {
		t.Fatalf("acme/busy reason = %q, want it to name the busy process", clone.Reason)
	}
}

// TestMigrateApplyRejectsAConcurrentRun covers SERIOUS #4's lock requirement:
// a second concurrent `--apply` (migrate or undo) against the same root must
// fail immediately with a clear message rather than blocking or interleaving
// its moves with the first run's.
func TestMigrateApplyRejectsAConcurrentRun(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	lock, err := acquireMigrationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.release()

	if _, err := Migrate(context.Background(), root, MigrateOptions{Apply: true}); err == nil {
		t.Fatal("a concurrent --apply must fail while the lock is held")
	} else if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("error = %v, want it to name a concurrent run", err)
	}
}

// TestMigrateIncludesDotNamedClones covers MINOR #9's second bullet: a clone
// directory named with a leading dot (such as the "acme/.github" convention
// GitHub uses for an org's default community-health repository) must be
// discovered and migrated exactly like any other clone, at both the
// legacy-candidate scan and the host-level already-done scan — not silently
// excluded by a directory-listing filter that treats a leading dot as
// "hidden".
func TestMigrateIncludesDotNamedClones(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	_ = initRemoteClone(t, root, "acme", ".github", "acme/.github")

	dry, err := Migrate(context.Background(), root, MigrateOptions{Repositories: []string{"acme/.github"}})
	if err != nil {
		t.Fatal(err)
	}
	clone, found := findMigrateClone(dry, "acme/.github")
	if !found || clone.Status != "planned" {
		t.Fatalf("dot-named clone dry run = %+v, want planned", clone)
	}

	applied, err := Migrate(context.Background(), root, MigrateOptions{Repositories: []string{"acme/.github"}, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	clone, found = findMigrateClone(applied, "acme/.github")
	if !found || clone.Status != "done" {
		t.Fatalf("dot-named clone apply = %+v, want done", clone)
	}
	destination := filepath.Join(root, "github.com", "acme", ".github")
	if _, err := os.Stat(destination); err != nil {
		t.Fatalf("dot-named clone must have moved to the host level: %v", err)
	}

	// Re-running now must find it at the host level via the already-done
	// scan, which also must not filter it out for its leading dot.
	again, err := Migrate(context.Background(), root, MigrateOptions{Repositories: []string{"acme/.github"}})
	if err != nil {
		t.Fatal(err)
	}
	clone, found = findMigrateClone(again, "acme/.github")
	if !found || clone.Status != "already_done" {
		t.Fatalf("dot-named clone re-scan = %+v, want already_done", clone)
	}
}

// TestMigrateReportsUnrelatedWorktreeBreakageAsInformational covers SERIOUS
// #C of the second review round: a host-level clone's linked worktree can be
// broken for a reason a clone migration cannot possibly have caused — here,
// its directory was simply deleted, never having been nested under any
// legacy pre-migration path. That must be reported informationally at most:
// the clone itself stays `already_done`, the run is not reported as a
// finding, and no automated repair is attempted for a path this migration
// has no rebasing to justify.
func TestMigrateReportsUnrelatedWorktreeBreakageAsInformational(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	hostLevel := filepath.Join(root, "github.com", "acme", "app")
	seedRemoteClone(t, root, "app", "acme/app", hostLevel)
	external := filepath.Join(root, "worktrees", "t1", "acme", "app")
	if err := os.MkdirAll(filepath.Dir(external), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, hostLevel, "git", "worktree", "add", "-b", "t1", external)
	// Delete the worktree directory outright: this is not something a clone
	// migration could have caused (the worktree was never nested under any
	// legacy path this clone might have moved from), so it must be treated
	// as breakage unrelated to migration.
	if err := os.RemoveAll(external); err != nil {
		t.Fatal(err)
	}

	report, err := Migrate(context.Background(), root, MigrateOptions{Repositories: []string{"acme/app"}})
	if err != nil {
		t.Fatal(err)
	}
	clone, found := findMigrateClone(report, "acme/app")
	if !found || clone.Status != "already_done" {
		t.Fatalf("clone with unrelated worktree breakage = %+v, want already_done", clone)
	}
	if MigrateFailed(report) {
		t.Fatal("breakage a migration did not cause must never be reported as a finding")
	}
	foundNote := false
	for _, note := range report.Notes {
		if strings.Contains(note, "acme/app") && strings.Contains(note, "not caused by migration") {
			foundNote = true
		}
	}
	if !foundNote {
		t.Fatalf("notes = %+v, want one naming acme/app's unrelated worktree breakage", report.Notes)
	}

	applied, err := Migrate(context.Background(), root, MigrateOptions{Repositories: []string{"acme/app"}, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	clone, found = findMigrateClone(applied, "acme/app")
	if !found || clone.Status != "already_done" {
		t.Fatalf("apply on unrelated breakage = %+v, want already_done (never auto-repaired)", clone)
	}
	if MigrateFailed(applied) {
		t.Fatal("apply must not turn unrelated breakage into a finding either")
	}
}

// TestMigrateKeptOwnersReflectFinalDiskStateNotPerCloneObservation covers
// MINOR #E of the second review round: whether a legacy owner directory is
// reported kept must reflect what is actually still on disk once the WHOLE
// run finishes, not what one clone observed while a sibling under the same
// owner directory had not yet been migrated in the same run. Two clones
// share one legacy owner directory; migrating both in the same run empties
// and removes it, so it must not appear in KeptOwners at all.
func TestMigrateKeptOwnersReflectFinalDiskStateNotPerCloneObservation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	owner := filepath.Join(root, "acme")
	// A bare, seedless clone per repository — unlike initRemoteClone, this
	// leaves nothing else behind under the shared owner directory, so it
	// genuinely becomes empty once both clones below have moved.
	for _, name := range []string{"one", "two"} {
		canonical := filepath.Join(owner, name)
		if err := os.MkdirAll(canonical, 0o755); err != nil {
			t.Fatal(err)
		}
		run(t, canonical, "git", "init", "-b", "main")
		run(t, canonical, "git", "config", "user.email", "wb@example.test")
		run(t, canonical, "git", "config", "user.name", "WB Test")
		if err := os.WriteFile(filepath.Join(canonical, "README.md"), []byte("acme/"+name+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		run(t, canonical, "git", "add", ".")
		run(t, canonical, "git", "commit", "-m", "init")
		run(t, canonical, "git", "remote", "add", "origin", "git@github.com:acme/"+name+".git")
	}

	report, err := Migrate(context.Background(), root, MigrateOptions{Repositories: []string{"acme/one", "acme/two"}, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, repository := range []string{"acme/one", "acme/two"} {
		if clone, found := findMigrateClone(report, repository); !found || clone.Status != "done" {
			t.Fatalf("%s = %+v, want done", repository, clone)
		}
	}
	if _, err := os.Stat(owner); !os.IsNotExist(err) {
		t.Fatalf("legacy owner directory must be gone once both siblings moved, err=%v", err)
	}
	for _, kept := range report.KeptOwners {
		if kept.Path == owner {
			t.Fatalf("kept owners = %+v, must not list %s: it was actually removed by the end of the run", report.KeptOwners, owner)
		}
	}
}

// TestBusyProcessSelfAncestorGivesCdOutGuidance covers MINOR #D of the second
// review round: when the busy-process refusal is triggered by this very
// process's own working directory (or an ancestor's, such as its parent
// shell) rather than some unrelated agent or user session, the reason must
// say to `cd` out of the clone and re-run — a completely different, and much
// more actionable, remedy than naming an unrelated PID to go investigate.
//
// Not run in parallel: it changes this process's own working directory,
// which every other test in this package assumes is stable. It restores the
// original directory before returning, and Go only runs t.Parallel() tests
// concurrently with each other, never with a non-parallel test still
// executing — see busyProcessReason's own doc for why this is exactly the
// process-tree case it must recognise.
func TestBusyProcessSelfAncestorGivesCdOutGuidance(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("busy-process detection only runs on linux")
	}
	root := t.TempDir()
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(original); err != nil {
			t.Fatal(err)
		}
	}()

	reason := worktrees.BusyProcessReason([]string{root})
	if reason == "" {
		t.Fatal("want a busy-process reason for this process's own cwd")
	}
	if !strings.Contains(reason, "cd out") {
		t.Fatalf("reason = %q, want self-ancestor cd-out guidance", reason)
	}
}

// TestMigrateLeavesFinishedCheckoutInPlaceWhenCloneIsRefused covers Fix #2 of
// the founder's 2026-09-18 redesign: only checkouts of clones that were NOT
// refused are relocation candidates. A clone with a Git operation in
// progress (a rebase, here) is refused whole at the clone level
// (refuseClone) before relocateClones ever runs, so a finished checkout
// inside it -- normally the safe, relocate-on-sight case -- must be left
// exactly where it is, with no relocation recorded for it at all.
func TestMigrateLeavesFinishedCheckoutInPlaceWhenCloneIsRefused(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	canonical := initRemoteClone(t, root, "acme", "rebasing-finished", "acme/rebasing-finished")
	// worktrees.Create fetches and verifies against the clone's real origin,
	// so the origin must stay reachable until after the claim exists.
	run(t, canonical, "git", "remote", "set-url", "origin", filepath.Join(root, "acme", "rebasing-finished.git"))
	run(t, canonical, "git", "push", "-u", "origin", "main")
	created, err := worktrees.Create(context.Background(), []string{"acme/rebasing-finished"}, worktrees.CreateOptions{
		ProjectsRoot: root, Operation: "t1", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create t1: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("create t1 returned %d results", len(created))
	}
	nested := created[0].WorktreeDir
	run(t, canonical, "git", "remote", "set-url", "origin", "git@github.com:acme/rebasing-finished.git")

	if _, err := worktrees.LogFinalize(context.Background(), worktrees.LogFinalizeOptions{
		ProjectsRoot: root, Worktree: nested, Result: "success", Apply: true,
	}); err != nil {
		t.Fatalf("finalize t1: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(canonical, ".git", "rebase-merge"), 0o755); err != nil {
		t.Fatal(err)
	}

	report, err := Migrate(context.Background(), root, MigrateOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if !MigrateFailed(report) {
		t.Fatal("a run with a refused clone must be reported as failed")
	}
	clone, found := findMigrateClone(report, "acme/rebasing-finished")
	if !found || clone.Status != "skipped" || !strings.Contains(strings.ToLower(clone.Reason), "rebase") {
		t.Fatalf("clone = %+v, want skipped naming the rebase", clone)
	}
	if len(clone.Relocations) != 0 {
		t.Fatalf("a refused clone must record no relocations at all: %+v", clone.Relocations)
	}
	if _, err := os.Stat(nested); err != nil {
		t.Fatalf("the finished checkout must be left exactly in place: %v", err)
	}
	if _, err := os.Stat(canonical); err != nil {
		t.Fatalf("the refused clone must be left exactly in place: %v", err)
	}
}

// TestMigrateRelocatesFinishedCheckoutAndLeavesActiveTaskInPlace covers
// relocateOneCheckout's task/active/destination branches directly, mirroring
// projects-root-layout's migrate-relocates-managed-worktrees fixture at the
// package level: clone A is legacy and holds a finished, in-clone checkout,
// which relocates to the central store alongside clone A's own move; clone B
// is already host-level (a live claim on any of clone A's own linked
// worktrees would refuse clone A's move entirely, so the active checkout
// must live in a clone that never needs to move) and holds an active
// checkout, which is left exactly where it is, with a finding, not an error.
func TestMigrateRelocatesFinishedCheckoutAndLeavesActiveTaskInPlace(t *testing.T) {
	root := t.TempDir()

	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "wb")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "worktrees.yaml")
	// repository-local while creating both claims: each lands nested inside
	// its own clone, matching the real machine's legacy layout, exactly as
	// the equivalent cmd/wb AC fixture does.
	if err := os.WriteFile(configPath, []byte("version: 1\nworktrees:\n  store: repository-local\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	canonical := initRemoteClone(t, root, "dal-go", "relocate-me", "dal-go/relocate-me")
	run(t, canonical, "git", "remote", "set-url", "origin", filepath.Join(root, "dal-go", "relocate-me.git"))
	run(t, canonical, "git", "push", "-u", "origin", "main")
	finished, err := worktrees.Create(context.Background(), []string{"dal-go/relocate-me"}, worktrees.CreateOptions{
		ProjectsRoot: root, Operation: "finished-task", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create finished: %v", err)
	}
	finishedWorktree := finished[0].WorktreeDir
	run(t, canonical, "git", "remote", "set-url", "origin", "git@github.com:dal-go/relocate-me.git")
	if _, err := worktrees.LogFinalize(context.Background(), worktrees.LogFinalizeOptions{
		ProjectsRoot: root, Worktree: finishedWorktree, Result: "success", Apply: true,
	}); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	hostLevel := filepath.Join(root, "github.com", "dal-go", "already-host")
	seedRemoteClone(t, root, "already-host", "dal-go/already-host", hostLevel)
	run(t, hostLevel, "git", "remote", "set-url", "origin", filepath.Join(root, "already-host.git"))
	run(t, hostLevel, "git", "push", "-u", "origin", "main")
	active, err := worktrees.Create(context.Background(), []string{"dal-go/already-host"}, worktrees.CreateOptions{
		ProjectsRoot: root, Operation: "active-task", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create active: %v", err)
	}
	activeWorktree := active[0].WorktreeDir
	run(t, hostLevel, "git", "remote", "set-url", "origin", "git@github.com:dal-go/already-host.git")

	// Reset to today's machine config: default central store, so both
	// nested checkouts above no longer match where migrate would place them.
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}

	report, err := Migrate(context.Background(), root, MigrateOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	clone, found := findMigrateClone(report, "dal-go/relocate-me")
	if !found || clone.Status != "done" || len(clone.Relocations) != 1 {
		t.Fatalf("clone A = %+v, want done with one relocation", clone)
	}
	finishedReloc := clone.Relocations[0]
	if finishedReloc.Task != "finished-task" || finishedReloc.Status != "done" {
		t.Fatalf("finished-task relocation = %+v, want done", finishedReloc)
	}
	if _, err := os.Stat(finishedReloc.Destination); err != nil {
		t.Fatalf("finished task checkout must exist at its relocated destination: %v", err)
	}
	if _, err := os.Stat(finishedWorktree); !os.IsNotExist(err) {
		t.Fatalf("finished task checkout must no longer sit at its pre-migration path: err=%v", err)
	}
	// Regression: a relocated checkout's own .worktree.md marker must name
	// its final relocated path, not the intermediate (post-clone-move,
	// pre-relocation) path -- found on a real machine on 2026-09-18.
	markerContents, err := os.ReadFile(filepath.Join(finishedReloc.Destination, ".worktree.md"))
	if err != nil {
		t.Fatalf("relocated checkout marker missing: %v", err)
	}
	if !strings.Contains(string(markerContents), finishedReloc.Destination) {
		t.Fatalf("relocated checkout marker does not name its final path %s:\n%s", finishedReloc.Destination, markerContents)
	}
	if strings.Contains(string(markerContents), clone.Destination+string(filepath.Separator)+".worktrees") {
		t.Fatalf("relocated checkout marker still names its intermediate pre-relocation path:\n%s", markerContents)
	}

	hostClone, found := findMigrateClone(report, "dal-go/already-host")
	if !found || hostClone.Status != "already_done" || len(hostClone.Relocations) != 1 {
		t.Fatalf("clone B = %+v, want already_done with one relocation", hostClone)
	}
	activeReloc := hostClone.Relocations[0]
	if activeReloc.Task != "active-task" || activeReloc.Status != "skipped" || !strings.Contains(activeReloc.Reason, "active task") {
		t.Fatalf("active-task relocation = %+v, want skipped naming the active task", activeReloc)
	}
	if _, err := os.Stat(activeWorktree); err != nil {
		t.Fatalf("active task checkout must be left exactly in place: %v", err)
	}
}

// TestMigrateRelocationManifestWriteFailureReturnsPartialReport covers the
// relocation persist closure's own manifest-write error path: an already
// host-level clone whose managed checkout still needs relocating, with its
// manifest directory made unwritable so the relocation's outcome cannot be
// recorded. The physical relocation itself (RelocateCheckout's own journal)
// still completes; only migrate's mirrored manifest bookkeeping fails, and
// Migrate reports that failure while still listing the clone's own status.
func TestMigrateRelocationManifestWriteFailureReturnsPartialReport(t *testing.T) {
	root := t.TempDir()

	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "wb")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "worktrees.yaml")
	// repository-local while creating the claim: it lands nested inside the
	// clone, matching the real machine's legacy layout, so relocating it to
	// the default central store below is a genuine move, not a no-op.
	if err := os.WriteFile(configPath, []byte("version: 1\nworktrees:\n  store: repository-local\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	hostLevel := filepath.Join(root, "github.com", "acme", "already-host")
	seedRemoteClone(t, root, "already-host", "acme/already-host", hostLevel)
	run(t, hostLevel, "git", "remote", "set-url", "origin", filepath.Join(root, "already-host.git"))
	run(t, hostLevel, "git", "push", "-u", "origin", "main")

	created, err := worktrees.Create(context.Background(), []string{"acme/already-host"}, worktrees.CreateOptions{
		ProjectsRoot: root, Operation: "relocate-me", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	nested := created[0].WorktreeDir
	run(t, hostLevel, "git", "remote", "set-url", "origin", "git@github.com:acme/already-host.git")
	if _, err := worktrees.LogFinalize(context.Background(), worktrees.LogFinalizeOptions{
		ProjectsRoot: root, Worktree: nested, Result: "success", Apply: true,
	}); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	// Reset to today's machine config: default central store, so the nested
	// checkout above no longer matches where migrate would place it.
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}

	migrationsDir := filepath.Join(root, ".wb", migrationsDirName)
	if err := os.MkdirAll(migrationsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Pre-create the migration lock file itself: acquireMigrationLock opens
	// it with O_CREATE, which is a no-op (and needs no directory write
	// permission) once the file already exists, so the run's own lock
	// acquisition still succeeds once migrationsDir is read-only below --
	// isolating the failure to the relocation manifest write this test means
	// to exercise.
	if err := os.WriteFile(filepath.Join(migrationsDir, migrationLockName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(migrationsDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(migrationsDir, 0o755); err != nil {
			t.Fatal(err)
		}
	})

	report, err := Migrate(context.Background(), root, MigrateOptions{Apply: true})
	if err == nil {
		t.Fatalf("a relocation manifest write failure must be reported as an error; report=%+v", report)
	}
	clone, found := findMigrateClone(report, "acme/already-host")
	if !found {
		t.Fatal("the partial report must still list the clone despite the relocation manifest write failure")
	}
	if clone.Status != "already_done" {
		t.Fatalf("clone = %+v, want already_done (the relocation failure must not corrupt the clone's own status)", clone)
	}
	// RelocateCheckout's own move-then-repair-then-verify-then-receipt
	// journal already completed the physical relocation before persist's
	// manifest write was even attempted -- exactly as an interrupted clone
	// move's own manifest write leaves the real move already done. What
	// failed is only migrate's own mirrored bookkeeping of that outcome.
	if _, statErr := os.Stat(nested); !os.IsNotExist(statErr) {
		t.Fatalf("the checkout must no longer sit at its pre-relocation path: err=%v", statErr)
	}
}

// TestMigrateUndoReversesRelocationAndRemovesEmptyTaskDirectory covers
// removeEmptyRelocationDirectories: undoing a relocated, finished-task
// checkout puts it back at its pre-migration (in-clone) path and removes the
// emptied <root>/.worktrees/<task> directory the forward relocation left
// behind. The clone itself is already host-level (never moves), isolating
// the relocation reversal from a clone-move reversal happening at the same
// time.
func TestMigrateUndoReversesRelocationAndRemovesEmptyTaskDirectory(t *testing.T) {
	root := t.TempDir()

	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configDir := filepath.Join(configHome, "wb")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "worktrees.yaml")
	// repository-local while creating the claim: it lands nested inside the
	// clone, matching the real machine's legacy layout.
	if err := os.WriteFile(configPath, []byte("version: 1\nworktrees:\n  store: repository-local\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	hostLevel := filepath.Join(root, "github.com", "dal-go", "undo-relocate")
	seedRemoteClone(t, root, "undo-relocate", "dal-go/undo-relocate", hostLevel)
	run(t, hostLevel, "git", "remote", "set-url", "origin", filepath.Join(root, "undo-relocate.git"))
	run(t, hostLevel, "git", "push", "-u", "origin", "main")

	created, err := worktrees.Create(context.Background(), []string{"dal-go/undo-relocate"}, worktrees.CreateOptions{
		ProjectsRoot: root, Operation: "undo-me", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	originalWorktree := created[0].WorktreeDir
	run(t, hostLevel, "git", "remote", "set-url", "origin", "git@github.com:dal-go/undo-relocate.git")
	if _, err := worktrees.LogFinalize(context.Background(), worktrees.LogFinalizeOptions{
		ProjectsRoot: root, Worktree: originalWorktree, Result: "success", Apply: true,
	}); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	// Reset to today's machine config: default central store, so the nested
	// checkout above no longer matches where migrate would place it.
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}

	applied, err := Migrate(context.Background(), root, MigrateOptions{Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	clone, found := findMigrateClone(applied, "dal-go/undo-relocate")
	if !found || clone.Status != "already_done" || len(clone.Relocations) != 1 || clone.Relocations[0].Status != "done" {
		t.Fatalf("apply clone = %+v, want already_done with one done relocation", clone)
	}
	relocatedDestination := clone.Relocations[0].Destination
	taskDir := filepath.Join(root, ".worktrees", "undo-me")
	if _, err := os.Stat(taskDir); err != nil {
		t.Fatalf("relocated task directory must exist after apply: %v", err)
	}
	if _, err := os.Stat(originalWorktree); !os.IsNotExist(err) {
		t.Fatalf("the checkout must no longer sit at its pre-migration path after apply: err=%v", err)
	}

	undone, err := Migrate(context.Background(), root, MigrateOptions{UndoID: applied.ManifestID, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	undoClone, found := findMigrateClone(undone, "dal-go/undo-relocate")
	if !found || undoClone.Status != "reversed" {
		t.Fatalf("undo clone = %+v, want reversed", undoClone)
	}
	if _, err := os.Stat(originalWorktree); err != nil {
		t.Fatalf("the checkout must be back at its pre-migration path: %v", err)
	}
	if _, err := os.Stat(relocatedDestination); !os.IsNotExist(err) {
		t.Fatalf("the relocated destination must no longer exist after undo: err=%v", err)
	}
	if _, err := os.Stat(taskDir); !os.IsNotExist(err) {
		t.Fatalf("the emptied <root>/.worktrees/<task> directory must be removed after undo: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".worktrees")); err != nil {
		t.Fatalf("<root>/.worktrees itself must survive: %v", err)
	}
}

// TestMigrateIncludeTaskLiftsOnlyTheLiveClaimRefusal covers
// REQ: clone-migration-include-active-tasks at the package level: an unknown
// --include-task name is a usage error before anything moves, and a known
// one lifts only the live-claim refusal for that task, planning the clone
// with a reason naming the inclusion.
func TestMigrateIncludeTaskLiftsOnlyTheLiveClaimRefusal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	canonical := initRemoteClone(t, root, "acme", "included", "acme/included")
	run(t, canonical, "git", "remote", "set-url", "origin", filepath.Join(root, "acme", "included.git"))
	run(t, canonical, "git", "push", "-u", "origin", "main")
	if _, err := worktrees.Create(context.Background(), []string{"acme/included"}, worktrees.CreateOptions{
		ProjectsRoot: root, Operation: "t-included", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	run(t, canonical, "git", "remote", "set-url", "origin", "git@github.com:acme/included.git")

	if _, err := Migrate(context.Background(), root, MigrateOptions{IncludeTasks: []string{"no-such-task"}}); err == nil {
		t.Fatal("an unknown --include-task name must fail the run before anything moves")
	}
	var unknownTask *UnknownIncludeTaskError
	if _, err := Migrate(context.Background(), root, MigrateOptions{IncludeTasks: []string{"no-such-task"}}); !errors.As(err, &unknownTask) || unknownTask.Task != "no-such-task" {
		t.Fatalf("err = %v, want *UnknownIncludeTaskError naming no-such-task", err)
	}
	if _, err := os.Stat(canonical); err != nil {
		t.Fatalf("an unknown --include-task must move nothing: %v", err)
	}

	report, err := Migrate(context.Background(), root, MigrateOptions{IncludeTasks: []string{"t-included"}})
	if err != nil {
		t.Fatal(err)
	}
	clone, found := findMigrateClone(report, "acme/included")
	if !found || clone.Status != "planned" || !strings.Contains(clone.Reason, "included: active task t-included") {
		t.Fatalf("clone = %+v, want planned naming the inclusion", clone)
	}

	applied, err := Migrate(context.Background(), root, MigrateOptions{IncludeTasks: []string{"t-included"}, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	appliedClone, found := findMigrateClone(applied, "acme/included")
	if !found || appliedClone.Status != "done" {
		t.Fatalf("apply clone = %+v, want done", appliedClone)
	}
}

// TestMigrateUndoHonoursManifestRecordedInclusions covers the fix for a
// reported bug: undo of an included clone was refused for the very live
// claim the migration included, because both refuseClone call sites in
// migrateUndo passed an empty include set instead of the manifest's own
// recorded inclusions. --apply now records, per clone, which tasks an
// inclusion covered; --undo reads those back and honours exactly them (a
// task still active at undo time is still not otherwise refused). Passing
// --include-task/--include-active-tasks together with --undo is a usage
// error instead of being silently ignored.
func TestMigrateUndoHonoursManifestRecordedInclusions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	canonical := initRemoteClone(t, root, "acme", "undo", "acme/undo")
	run(t, canonical, "git", "remote", "set-url", "origin", filepath.Join(root, "acme", "undo.git"))
	run(t, canonical, "git", "push", "-u", "origin", "main")
	if _, err := worktrees.Create(context.Background(), []string{"acme/undo"}, worktrees.CreateOptions{
		ProjectsRoot: root, Operation: "t-undo", WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	run(t, canonical, "git", "remote", "set-url", "origin", "git@github.com:acme/undo.git")

	applied, err := Migrate(context.Background(), root, MigrateOptions{IncludeTasks: []string{"t-undo"}, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	appliedClone, found := findMigrateClone(applied, "acme/undo")
	if !found || appliedClone.Status != "done" {
		t.Fatalf("apply clone = %+v, want done", appliedClone)
	}
	destination := appliedClone.Destination

	// --include-task/--include-active-tasks together with --undo is a usage
	// error: undo honours exactly the manifest's own recorded inclusions.
	var undoIncludeFlags *UndoIncludeFlagsError
	if _, err := Migrate(context.Background(), root, MigrateOptions{UndoID: applied.ManifestID, IncludeTasks: []string{"t-undo"}}); !errors.As(err, &undoIncludeFlags) {
		t.Fatalf("err = %v, want *UndoIncludeFlagsError", err)
	}
	if _, err := Migrate(context.Background(), root, MigrateOptions{UndoID: applied.ManifestID, IncludeActiveTasks: true}); !errors.As(err, &undoIncludeFlags) {
		t.Fatalf("err = %v, want *UndoIncludeFlagsError", err)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatalf("a rejected undo call must move nothing: %v", err)
	}

	// The task's claim is still live (never finished): without honouring
	// the manifest's recorded inclusion, undo's own refuseClone call would
	// refuse to move the clone back, citing the very claim the original
	// --apply included.
	undone, err := Migrate(context.Background(), root, MigrateOptions{UndoID: applied.ManifestID, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	undoneClone, found := findMigrateClone(undone, "acme/undo")
	if !found || undoneClone.Status != "reversed" {
		t.Fatalf("undo clone = %+v, want reversed (not skipped for the included live claim)", undoneClone)
	}
	if _, err := os.Stat(canonical); err != nil {
		t.Fatalf("undo must restore the clone at its original path: %v", err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("undo must remove the host-level destination: err=%v", err)
	}
	status := gitOutput(t, canonical, "status", "--porcelain=v1")
	if strings.TrimSpace(status) != "" {
		t.Fatalf("git status after undo = %q, want clean", status)
	}
	listed, err := worktrees.List(context.Background(), worktrees.ListOptions{ProjectsRoot: root, Task: "t-undo"})
	if err != nil {
		t.Fatalf("list t-undo after undo: %v", err)
	}
	if len(listed) != 1 || listed[0].CanonicalDir != canonical {
		t.Fatalf("t-undo resolution after undo = %+v, want exactly one result naming the restored path %s", listed, canonical)
	}
}
