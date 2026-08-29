package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// externalWorktree creates a linked worktree at an arbitrary path outside
// every WB worktrees root, mirroring how `git worktree add` or a tool that
// predates WB would have made it. Unlike orphanFixture.addWorktree, this
// stays on a *gitFixture so it can be combined with the merge/push helpers
// the rest of this package's Cleanup/Abort integration tests already use.
//
// The path shape is deliberately <root>/external/<branch-slug>/acme/app —
// three real segments below "external", mirroring a real external worktree
// such as .worktrees/<name>/<repo> closely enough that ReconstructManifest's
// positional path-derived effort_id (parts[len-3], see effortFromWorktreePath)
// gives each branch its own distinct effort, exactly as it would for two
// differently named checkouts on the founder's fleet. A flatter path (for
// example .../external/<branch>, where the branch's own "/" already supplies
// the nesting) would make every worktree here collide on the same segment.
func (fixture *gitFixture) externalWorktree(t *testing.T, branch string) string {
	t.Helper()
	slug := strings.ReplaceAll(branch, "/", "-")
	path := filepath.Join(filepath.Dir(fixture.projectsRoot), "external", slug, "acme", "app")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "worktree", "add", "-b", branch, path, "main")
	configureGitUser(t, path)
	return path
}

func TestAdoptRecordsLiveBranchWhenFolderNameDiffers(t *testing.T) {
	fixture := newGitFixture(t)
	path := filepath.Join(filepath.Dir(fixture.projectsRoot), "external", "infallible-herschel-414857", "acme", "app")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const liveBranch = "claude/recursing-pare-f391aa"
	gitTest(t, fixture.canonical, "worktree", "add", "-b", liveBranch, path, "main")
	configureGitUser(t, path)

	results, err := Adopt(context.Background(), AdoptOptions{
		ProjectsRoot: fixture.projectsRoot, Base: "main", Path: path, Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Action != AdoptAdopted {
		t.Fatalf("adopt result = %#v", results)
	}
	manifest, err := ReadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Branch != liveBranch {
		t.Fatalf("manifest branch = %q, want live branch %q", manifest.Branch, liveBranch)
	}
	claim, _, _, err := activeWorkLogClaim(fixture.home, path)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Branch != liveBranch {
		t.Fatalf("claim branch = %q, want live branch %q", claim.Branch, liveBranch)
	}
}

// TestAdoptDryRunDoesNotMutate proves adopt is dry-run by default and, like
// backfill, additive even for a worktree holding uncommitted changes: no
// manifest, no Work Log claim, and no change to the working tree.
func TestAdoptDryRunDoesNotMutate(t *testing.T) {
	fixture := newGitFixture(t)
	path := fixture.externalWorktree(t, "feature/adopt-dry-run")
	if err := os.WriteFile(filepath.Join(path, "wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirtyBefore := mustGitOutput(t, path, "status", "--porcelain")

	results, err := Adopt(context.Background(), AdoptOptions{ProjectsRoot: fixture.projectsRoot, Base: "main", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Action != AdoptWouldAdopt || results[0].Task == "" {
		t.Fatalf("dry run must only plan: %+v", results)
	}
	if _, err := ReadManifest(path); err == nil {
		t.Fatal("a dry run must not write a manifest")
	}
	if after := mustGitOutput(t, path, "status", "--porcelain"); after != dirtyBefore {
		t.Fatalf("adopt changed the working tree: %q -> %q", dirtyBefore, after)
	}
	if _, _, _, err := activeWorkLogClaim(fixture.home, path); err == nil {
		t.Fatal("a dry run must not write a Work Log claim")
	} else if !errors.Is(err, errWorkLogProjectionNotFound) {
		t.Fatalf("unexpected Work Log state after a dry run: %v", err)
	}
}

// TestAdoptIsIdempotentAcrossInterruptedSweeps proves re-running --apply
// after a worktree is already adopted is a safe no-op, and that the
// registration it wrote makes the worktree discoverable through the same
// List() walk Cleanup/Abort use — not a parallel inventory.
func TestAdoptIsIdempotentAcrossInterruptedSweeps(t *testing.T) {
	fixture := newGitFixture(t)
	path := fixture.externalWorktree(t, "feature/adopt-idempotent")

	applied, err := Adopt(context.Background(), AdoptOptions{ProjectsRoot: fixture.projectsRoot, Base: "main", Path: path, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0].Action != AdoptAdopted || applied[0].Task == "" {
		t.Fatalf("apply must adopt: %+v", applied)
	}
	task := applied[0].Task
	manifest, err := ReadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Provenance != ProvenanceReconstructed {
		t.Fatalf("an adopted manifest is an inference and must say so: %+v", manifest)
	}
	if prompts, err := ListPrompts(path); err != nil || len(prompts) != 0 {
		t.Fatalf("adopt must not fabricate a prompt: %+v (%v)", prompts, err)
	}

	again, err := Adopt(context.Background(), AdoptOptions{ProjectsRoot: fixture.projectsRoot, Base: "main", Path: path, Apply: true})
	if err != nil {
		t.Fatalf("re-running after an interruption must be safe: %v", err)
	}
	if len(again) != 1 || again[0].Action != AdoptAlreadyAdopted {
		t.Fatalf("a second sweep must recognize existing state: %+v", again)
	}

	listed, err := List(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Task: task, Base: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].WorktreeDir != path || !listed[0].External {
		t.Fatalf("adopted worktree not discoverable via the ordinary List() walk: %+v", listed)
	}
}

// TestAdoptSkipsMissingWorktree mirrors TestBackfillSkipsRegistrationsWithNoWorkingTree:
// a registration Git still knows about but whose directory is gone is
// nothing to adopt.
func TestAdoptSkipsMissingWorktree(t *testing.T) {
	fixture := newGitFixture(t)
	path := fixture.externalWorktree(t, "feature/adopt-missing")
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}

	results, err := Adopt(context.Background(), AdoptOptions{ProjectsRoot: fixture.projectsRoot, Base: "main", Path: path, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Action != AdoptSkipped {
		t.Fatalf("a vanished working tree cannot be adopted: %+v", results)
	}
	if !strings.Contains(results[0].Reason, "prune") {
		t.Fatalf("the reason must name the remedy: %q", results[0].Reason)
	}
}

// adoptAndCommit externally creates, commits to, and adopts one worktree,
// returning its task and branch.
func adoptAndCommit(t *testing.T, fixture *gitFixture, branch, file string) (task, path string) {
	t.Helper()
	path = fixture.externalWorktree(t, branch)
	if err := os.WriteFile(filepath.Join(path, file), []byte(branch+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, path, "add", file)
	gitTest(t, path, "commit", "-m", "work on "+branch)
	applied, err := Adopt(context.Background(), AdoptOptions{ProjectsRoot: fixture.projectsRoot, Base: "main", Path: path, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0].Action != AdoptAdopted {
		t.Fatalf("adopt %s = %+v", branch, applied)
	}
	return applied[0].Task, path
}

// TestAdoptedWorktreeCleanupAppliesExistingSafetyChecks is the proof
// requirement from the brief: after adoption, wb worktree cleanup applies
// its full existing safety machinery to an external worktree exactly as it
// does to one wb worktree create made directly — not a second, separate
// removal path. It proves dirty refusal, lock refusal, and a genuine
// zero-loss removal (landed branch, integrated worktree) all still work.
func TestAdoptedWorktreeCleanupAppliesExistingSafetyChecks(t *testing.T) {
	fixture := newGitFixture(t)

	// Dirty: refused, and Cleanup --apply must not touch it.
	dirtyTask, dirtyPath := adoptAndCommit(t, fixture, "feature/adopt-cleanup-dirty", "feature.txt")
	if err := os.WriteFile(filepath.Join(dirtyPath, "wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	installMergedPullRequestFixtures(t, nil, time.Time{})
	dirtyCleanup, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: dirtyTask, Base: "main", Apply: true,
		Now: func() time.Time { return time.Date(2026, time.July, 3, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(dirtyCleanup.Results) != 1 || dirtyCleanup.Results[0].Eligible || dirtyCleanup.Results[0].Applied ||
		!strings.Contains(dirtyCleanup.Results[0].Reason, "local changes") {
		t.Fatalf("dirty adopted cleanup result = %#v", dirtyCleanup.Results)
	}
	if !dirtyCleanup.Results[0].External {
		t.Fatal("cleanup lost the adopted worktree's External marker")
	}
	if _, err := os.Stat(dirtyPath); err != nil {
		t.Fatalf("dirty adopted worktree was removed: %v", err)
	}

	// Locked: refused. A bare .lock file is exactly what the ordinary
	// listing walk treats as locked (see listLayout); no live process is
	// needed to prove eligibility refuses it.
	lockedTask, lockedPath := adoptAndCommit(t, fixture, "feature/adopt-cleanup-locked", "feature.txt")
	lockPath := filepath.Join(fixture.home, "worktrees", lockedTask, ".lock")
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lockedCleanup, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: lockedTask, Base: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lockedCleanup.Results) != 1 || lockedCleanup.Results[0].Eligible || !lockedCleanup.Results[0].Locked {
		t.Fatalf("locked adopted cleanup plan = %#v", lockedCleanup.Results)
	}
	if _, err := os.Stat(lockedPath); err != nil {
		t.Fatalf("locked adopted worktree was touched by a dry run: %v", err)
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}

	// Clean and landed: a genuine zero-loss removal, same as any worktree WB
	// created directly. Push and merge into the canonical clone's main so the
	// adopted worktree's head is a real ancestor of a freshly fetched
	// origin/main.
	landedTask, landedPath := adoptAndCommit(t, fixture, "feature/adopt-cleanup-landed", "feature.txt")
	landedBranch := "feature/adopt-cleanup-landed"
	landedHead := gitTestOutput(t, landedPath, "rev-parse", "HEAD")
	gitTest(t, landedPath, "push", "-u", "origin", landedBranch)
	gitTest(t, fixture.canonical, "merge", "--no-ff", landedBranch, "-m", "merge "+landedBranch)
	gitTest(t, fixture.canonical, "push", "origin", "main")

	landedCleanup, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: landedTask, Base: "main", Apply: true, DeleteRemote: true,
		Now: func() time.Time { return time.Date(2026, time.July, 3, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(landedCleanup.Results) != 1 || !landedCleanup.Results[0].Eligible || !landedCleanup.Results[0].Applied ||
		!landedCleanup.Results[0].WorktreeGone || !landedCleanup.Results[0].BranchDeleted {
		t.Fatalf("landed adopted cleanup result = %#v", landedCleanup.Results)
	}
	if landedCleanup.Results[0].HeadSHA != landedHead {
		t.Fatalf("landed cleanup head = %s, want %s", landedCleanup.Results[0].HeadSHA, landedHead)
	}
	if _, err := os.Stat(landedPath); !os.IsNotExist(err) {
		t.Fatalf("adopted worktree still exists after cleanup: %v", err)
	}
	if gitRefExists(fixture.canonical, "refs/heads/"+landedBranch) {
		t.Fatal("adopted worktree's local branch still exists after cleanup")
	}
	// The registration entry — task/owner/repository directory and pointer
	// file — is retired exactly like an ordinary task's, leaving nothing
	// behind in WB's own home.
	if _, err := os.Stat(filepath.Join(fixture.home, "worktrees", landedTask)); !os.IsNotExist(err) {
		t.Fatalf("adopted worktree registration was not retired: %v", err)
	}
}

// TestAdoptedWorktreeAbortAppliesExistingSafetyChecks proves wb worktree
// abort also applies its full existing safety machinery to an adopted external
// worktree: dirty bytes are captured before discard, and a clean, unlanded
// discarded abort removes the checkout and its branch exactly as it does for
// a worktree wb worktree create made directly.
func TestAdoptedWorktreeAbortAppliesExistingSafetyChecks(t *testing.T) {
	fixture := newGitFixture(t)

	dirtyTask, dirtyPath := adoptAndCommit(t, fixture, "feature/adopt-abort-dirty", "feature.txt")
	if err := os.WriteFile(filepath.Join(dirtyPath, "wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	discardedDirty, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: dirtyTask, Base: "main",
		Disposition: AbortDiscarded, DeleteRemote: true, Apply: true,
	})
	if err != nil || len(discardedDirty) != 1 || !discardedDirty[0].Applied || discardedDirty[0].DirtyCapture == nil {
		t.Fatalf("dirty adopted abort = %#v, err=%v, want a sealed capture", discardedDirty, err)
	}
	if _, err := os.Stat(dirtyPath); !os.IsNotExist(err) {
		t.Fatalf("dirty adopted worktree remains after discard: %v", err)
	}

	cleanTask, cleanPath := adoptAndCommit(t, fixture, "feature/adopt-abort-clean", "feature.txt")
	cleanBranch := "feature/adopt-abort-clean"
	discarded, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: cleanTask, Base: "main",
		Disposition: AbortDiscarded, DeleteRemote: true, Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(discarded) != 1 || !discarded[0].Applied || !discarded[0].WorktreeGone || !discarded[0].BranchDeleted || !discarded[0].External {
		t.Fatalf("discarded adopted abort result = %+v", discarded)
	}
	if _, err := os.Stat(cleanPath); !os.IsNotExist(err) {
		t.Fatalf("adopted worktree still exists after a discarded abort: %v", err)
	}
	if gitRefExists(fixture.canonical, "refs/heads/"+cleanBranch) {
		t.Fatal("adopted worktree's local branch still exists after a discarded abort")
	}
	// Abort never retires the task directory itself even for a worktree WB
	// created directly (see TestAbortDiscardedUnusedWorktreesIsAudited); what
	// it must retire here is the registration entry removeAdoptedRegistration
	// owns — the owner/repository directory holding the adoption pointer.
	if _, err := os.Stat(filepath.Join(fixture.home, "worktrees", cleanTask, "acme")); !os.IsNotExist(err) {
		t.Fatalf("adopted worktree registration was not retired by abort: %v", err)
	}
}
