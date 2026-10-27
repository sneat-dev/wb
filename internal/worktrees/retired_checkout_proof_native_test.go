//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// TestE2EProveRetiredCheckoutVerifiesCompletedPreservedRetirement exercises the
// production proof facade end to end: real bare source and archive remotes, a
// supported preserved retirement that removes the checkout, then the
// read-only evidence a merger acknowledgement binds.
//
//nolint:paralleltest // newGitFixture sets process-wide WB and Git configuration variables.
func TestE2EProveRetiredCheckoutVerifiesCompletedPreservedRetirement(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "retired-proof", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	gitTest(t, worktree, "push", "-u", "origin", "retired-proof")
	archive := filepath.Join(t.TempDir(), "archive.git")
	gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
	testenv.ConfigureGitAutoMaintenanceOff(t, archive)
	result, err := Retire(context.Background(), RetireOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "retired-proof", ArchiveRemote: archive, Apply: true,
		RemoteOwnership: retireAllowRemoteOwner,
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
		},
		OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != "complete" {
		t.Fatalf("retirement = %+v", result)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("retired checkout remains: %v", err)
	}
	options := RetiredCheckoutProofOptions{
		ProjectsRoot: fixture.projectsRoot, Task: result.Task, Repository: result.Repository,
		CandidateWorktree: result.Worktree, CandidateBranch: result.Branch,
		CandidateSHA: result.SourceSHA, PublishedCandidateSHA: result.SourceSHA, ArchiveRemote: archive,
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
		},
	}
	evidence, err := proveRetiredCheckoutWith(context.Background(), options, productionRetiredCheckoutProofPorts())
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Retirement.SourceSHA != result.SourceSHA || evidence.Retirement.ArchiveSHA != result.ArchiveSHA ||
		evidence.Retirement.RetiredRef != result.RetiredRef || evidence.Retirement.Phase != "complete" ||
		evidence.ReceiptPath != result.ReportPath || evidence.Canonical != result.Canonical ||
		len(evidence.ReceiptSHA256) != 64 || len(evidence.ProofSHA256) != 64 {
		t.Fatalf("evidence = %+v", evidence)
	}

	if err := os.MkdirAll(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := proveRetiredCheckoutWith(context.Background(), options, productionRetiredCheckoutProofPorts()); err == nil || !strings.Contains(err.Error(), "still exists") {
		t.Fatalf("recreated checkout proof = %v", err)
	}
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatal(err)
	}
	options.Task = "other-task"
	if _, err := proveRetiredCheckoutWith(context.Background(), options, productionRetiredCheckoutProofPorts()); err == nil || !strings.Contains(err.Error(), "read retirement receipt") {
		t.Fatalf("missing retirement receipt proof = %v", err)
	}
}
