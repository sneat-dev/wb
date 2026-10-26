//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// TestE2ERetirementRefusesLateIgnoredFilesAndCustodyReadFailures drives the real
// retirement facade: ignored material that appears after admission but before
// the deletion fence refuses and leaves the checkout, source and archive in
// place, and a custody inspection that fails at local removal refuses without
// removing anything.
//
//nolint:paralleltest // newGitFixture sets process-wide WB and Git configuration variables.
func TestE2ERetirementRefusesLateIgnoredFilesAndCustodyReadFailures(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "retire-late-ignored", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	gitTest(t, worktree, "push", "-u", "origin", "retire-late-ignored")
	if err := os.WriteFile(filepath.Join(worktree, ".gitignore"), []byte("build/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", ".gitignore")
	gitTest(t, worktree, "commit", "-m", "test: ignore build output")
	gitTest(t, worktree, "push", "origin", "retire-late-ignored")
	archive := filepath.Join(t.TempDir(), "archive.git")
	gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
	testenv.ConfigureGitAutoMaintenanceOff(t, archive)
	_, err = Retire(context.Background(), RetireOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "retire-late-ignored", ArchiveRemote: archive, Apply: true,
		RemoteOwnership: retireAllowRemoteOwner,
		afterPhase: func(string) error {
			if err := os.MkdirAll(filepath.Join(worktree, "build"), 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(worktree, "build", "late-secret.bin"), []byte("safe fake secret\n"), 0o600)
		},
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
		},
		OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "ignored worktree files would be lost") {
		t.Fatalf("late ignored material retirement = %v", err)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("late ignored material removed the checkout: %v", err)
	}
	if branch := gitTestOutput(t, fixture.canonical, "for-each-ref", "--format=%(objectname)", "refs/heads/retire-late-ignored"); strings.TrimSpace(branch) == "" {
		t.Fatal("late ignored material deleted the local branch")
	}

	custodyErr := errors.New("custody evidence unreadable")
	operation := retireTransactionOperation(RetireOptions{}, "", "", ListResult{}, nil, nil, retireCustodyFence{},
		func(context.Context, string, string, ListResult) (retireCustodyFence, error) {
			return retireCustodyFence{}, custodyErr
		})
	if err := operation.Apply.RemoveLocal(context.Background(), &RetireResult{}); err == nil || !errors.Is(err, custodyErr) {
		t.Fatalf("custody inspection failure at local removal = %v", err)
	}
}

// TestE2ERetiredCheckoutProofRefusesChangedCanonicalAndUnpublishedHead exercises
// the held proof against a real preserved retirement: a canonical namespace
// changed after the proof is held refuses every later Git read, and a published
// pull-request head that is a valid distinct object but not a commit (or is
// missing) refuses before source binding succeeds.
//
//nolint:paralleltest // newGitFixture sets process-wide WB and Git configuration variables.
func TestE2ERetiredCheckoutProofRefusesChangedCanonicalAndUnpublishedHead(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "retired-proof-head", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	gitTest(t, worktree, "push", "-u", "origin", "retired-proof-head")
	archive := filepath.Join(t.TempDir(), "archive.git")
	gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
	testenv.ConfigureGitAutoMaintenanceOff(t, archive)
	result, err := Retire(context.Background(), RetireOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "retired-proof-head", ArchiveRemote: archive, Apply: true,
		RemoteOwnership: retireAllowRemoteOwner,
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
		},
		OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
	})
	if err != nil || result.Phase != "complete" {
		t.Fatalf("retirement = %+v, %v", result, err)
	}
	options := RetiredCheckoutProofOptions{
		ProjectsRoot: fixture.projectsRoot, Task: result.Task, Repository: result.Repository,
		CandidateWorktree: result.Worktree, CandidateBranch: result.Branch,
		CandidateSHA: result.SourceSHA, PublishedCandidateSHA: result.SourceSHA, ArchiveRemote: archive,
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
		},
	}
	treeSHA := strings.TrimSpace(gitTestOutput(t, result.Canonical, "rev-parse", result.SourceSHA+"^{tree}"))
	if treeSHA == "" || treeSHA == result.SourceSHA {
		t.Fatalf("tree object identity = %q", treeSHA)
	}
	options.PublishedCandidateSHA = treeSHA
	if _, err := ProveRetiredCheckoutHeld(context.Background(), options); err == nil || !strings.Contains(err.Error(), "published pull-request head object") {
		t.Fatalf("noncommit published head = %v", err)
	}
	options.PublishedCandidateSHA = strings.Repeat("d", 40)
	if _, err := ProveRetiredCheckoutHeld(context.Background(), options); err == nil || !strings.Contains(err.Error(), "published pull-request head object") {
		t.Fatalf("missing published head = %v", err)
	}

	options.PublishedCandidateSHA = result.SourceSHA
	proof, err := ProveRetiredCheckoutHeld(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proof.Close() }()
	if err := os.Rename(result.Canonical, result.Canonical+"-held"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(result.Canonical, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := proof.Git(context.Background(), "rev-parse", "HEAD"); err == nil || !strings.Contains(err.Error(), "canonical repository") {
		t.Fatalf("held proof after canonical change = %v", err)
	}
}
