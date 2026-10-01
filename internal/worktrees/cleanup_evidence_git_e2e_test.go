//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

//nolint:paralleltest // newGitFixture configures process-wide Git and WB fixture state.
func TestE2EDiscardedProofCountsEveryExactRecordAndRechecksGit(t *testing.T) {
	fixture := newGitFixture(t)
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	root := filepath.Join(t.TempDir(), "worktrees")
	worktree := filepath.Join(root, "discard-task", "acme", "app")
	entry := ListResult{Task: "discard-task", Repository: "acme/app", CanonicalDir: fixture.canonical,
		WorktreesRoot: root, WorktreeDir: worktree, Branch: "wb/discard", Base: "main", HeadSHA: head}
	discarded := newLifecycleBacklogRecord(fixture.projectsRoot, entry, string(AbortDiscarded))
	if err := persistLifecycleBacklog(fixture.home, &discarded, lifecycleStageComplete); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(lifecycleBacklogDirectory(fixture.home), "ignored.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lifecycleBacklogDirectory(fixture.home), "ignored.txt"), []byte("unrelated"), 0o600); err != nil {
		t.Fatal(err)
	}
	lookup := func() (*DiscardedLifecycleBacklogProof, error) {
		t.Helper()
		return FindDiscardedLifecycleBacklogProof(context.Background(), fixture.projectsRoot, entry.Repository,
			entry.Base, entry.Task, entry.WorktreeDir, entry.Branch, entry.HeadSHA)
	}
	proof, err := lookup()
	if err != nil || proof == nil || proof.Path != lifecycleBacklogPath(fixture.home, discarded.ID) {
		t.Fatalf("exact discarded proof = %+v, %v", proof, err)
	}
	removed := newLifecycleBacklogRecord(fixture.projectsRoot, entry, "removed")
	if err := persistLifecycleBacklog(fixture.home, &removed, lifecycleStageComplete); err != nil {
		t.Fatal(err)
	}
	if proof, err := lookup(); proof != nil || err == nil || !strings.Contains(err.Error(), "found 2") {
		t.Fatalf("two exact durable records must refuse proof: %+v, %v", proof, err)
	}
	if err := os.Remove(lifecycleBacklogPath(fixture.home, removed.ID)); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "worktree", "add", "-b", entry.Branch, entry.WorktreeDir, head)
	if proof, err := lookup(); proof != nil || err == nil || !strings.Contains(err.Error(), "still exists") {
		t.Fatalf("present checkout accepted: %+v, %v", proof, err)
	}
	gitTest(t, fixture.canonical, "worktree", "remove", "--force", entry.WorktreeDir)
	if proof, err := lookup(); proof != nil || err == nil || !strings.Contains(err.Error(), "still exists locally") {
		t.Fatalf("surviving local branch accepted: %+v, %v", proof, err)
	}
	gitTest(t, fixture.canonical, "branch", "-D", entry.Branch)
	gitTest(t, fixture.canonical, "worktree", "add", "-b", entry.Branch, entry.WorktreeDir, head)
	if err := os.RemoveAll(entry.WorktreeDir); err != nil {
		t.Fatal(err)
	}
	if proof, err := lookup(); proof != nil || err == nil || !strings.Contains(err.Error(), "remains registered") {
		t.Fatalf("still-registered absent checkout accepted: %+v, %v", proof, err)
	}
}

//nolint:paralleltest // Each real Git fixture configures process-wide test environment.
func TestE2EVacantBacklogRequiresLiveCanonicalAndClaimAuthority(t *testing.T) {
	for _, scenario := range []string{"invalid canonical", "cancelled registration", "missing failed-create claim"} {
		//nolint:paralleltest // newGitFixture configures process-wide test environment.
		t.Run(scenario, func(t *testing.T) {
			fixture := newGitFixture(t)
			head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
			root := filepath.Join(t.TempDir(), "worktrees")
			worktree := filepath.Join(root, "vacant-task", "acme", "app")
			entry := ListResult{Task: "vacant-task", Repository: "acme/app", CanonicalDir: fixture.canonical,
				WorktreesRoot: root, WorktreeDir: worktree, Branch: "wb/vacant", Base: "main", HeadSHA: head}
			record := newLifecycleBacklogRecord(fixture.projectsRoot, entry, "removed")
			ctx := context.Background()
			lockErr := errors.New("namespace unavailable")
			switch scenario {
			case "invalid canonical":
				record.CanonicalDir = filepath.Join(t.TempDir(), "not-a-repository")
				if err := os.Mkdir(record.CanonicalDir, 0o700); err != nil {
					t.Fatal(err)
				}
			case "cancelled registration":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			case "missing failed-create claim":
				record.RecoveryKind = "create_work_log_failed"
				record.WorkLogEffort = "effort"
				record.WorkLogRun = "run"
				record.WorkLogClaim = strings.Repeat("a", 64)
			}
			err := completeVacantLifecycleBacklog(ctx, fixture.home, &record, lockErr)
			if scenario == "missing failed-create claim" {
				if err == nil || errors.Is(err, lockErr) || !strings.Contains(err.Error(), "Work Log") {
					t.Fatalf("missing immutable claim was hidden by lock error: %v", err)
				}
			} else if !errors.Is(err, lockErr) {
				t.Fatalf("%s returned %v, want original lock refusal", scenario, err)
			}
		})
	}
}

//nolint:paralleltest // newGitFixture configures process-wide Git and WB fixture state.
func TestE2EDiscardedProofRefusesChangedRegistrationAuthority(t *testing.T) {
	fixture := newGitFixture(t)
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	root := filepath.Join(t.TempDir(), "worktrees")
	worktree := filepath.Join(root, "discard-task", "acme", "app")
	entry := ListResult{Task: "discard-task", Repository: "acme/app", CanonicalDir: fixture.canonical,
		WorktreesRoot: root, WorktreeDir: worktree, Branch: "wb/discard", Base: "main", HeadSHA: head}
	record := newLifecycleBacklogRecord(fixture.projectsRoot, entry, string(AbortDiscarded))
	if err := persistLifecycleBacklog(fixture.home, &record, lifecycleStageComplete); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fixture.canonical, fixture.canonical+"-moved"); err != nil {
		t.Fatal(err)
	}
	proof, err := FindDiscardedLifecycleBacklogProof(context.Background(), fixture.projectsRoot,
		entry.Repository, entry.Base, entry.Task, entry.WorktreeDir, entry.Branch, entry.HeadSHA)
	if proof != nil || err == nil || !strings.Contains(err.Error(), "registration") {
		t.Fatalf("missing canonical registration authority accepted: %+v, %v", proof, err)
	}
}
