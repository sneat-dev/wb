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

//nolint:paralleltest // newGitFixture changes process-wide WB and Git environment for native repositories.
func TestE2ERetirementTransactionReplaysAtomicDeletionProof(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "retire-transaction-replay", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	gitTest(t, worktree, "push", "-u", "origin", "retire-transaction-replay")
	archive := filepath.Join(t.TempDir(), "archive.git")
	gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
	testenv.ConfigureGitAutoMaintenanceOff(t, archive)
	options := RetireOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "retire-transaction-replay", Apply: true, ArchiveRemote: archive,
		RemoteOwnership: retireAllowRemoteOwner,
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
		},
		OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
		afterPhase: func(phase string) error {
			if phase == "original_delete_pushed" {
				return errors.New("injected after atomic push")
			}
			return nil
		},
	}
	partial, err := Retire(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "injected after atomic push") {
		t.Fatalf("interruption = (%+v, %v)", partial, err)
	}
	if partial.DeleteIntentSHA != partial.OriginalRemoteSHA || partial.Phase != "archive_published" {
		t.Fatalf("durable deletion intent = %+v", partial)
	}
	report, err := readRetireReport(partial.ReportPath)
	if err != nil || report.DeleteIntentSHA != partial.OriginalRemoteSHA {
		t.Fatalf("stored deletion intent = (%+v, %v)", report, err)
	}
	if proof := gitTestOutput(t, fixture.canonical, "ls-remote", "origin", retireDeletionProofRef(partial)); !strings.HasPrefix(proof, partial.SourceSHA+"\t") {
		t.Fatalf("atomic proof = %q", proof)
	}
	if original := gitTestOutput(t, fixture.canonical, "ls-remote", "origin", "refs/heads/"+partial.Branch); original != "" {
		t.Fatalf("original ref survived atomic deletion: %q", original)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("checkout removed before verified proof: %v", err)
	}
	options.afterPhase = nil
	finished, err := Retire(context.Background(), options)
	if err != nil || finished.Phase != "complete" || finished.SourceSHA != partial.SourceSHA || finished.ArchiveSHA != partial.ArchiveSHA {
		t.Fatalf("replay = (%+v, %v)", finished, err)
	}
	if _, err := os.Lstat(worktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkout remains after proved replay: %v", err)
	}
}
