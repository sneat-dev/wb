package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// TestAcknowledgeRetiredPublicationAfterSupportedPreservedRetirement is the
// native red->green journey: the candidate PR was closed unmerged, the
// supported preserved-retirement transaction verified the retired source and
// private archive and removed the checkout, and only then can the retired
// publication acknowledgement prove the exact lane is free.
//
//nolint:paralleltest // engine fixture and GH fixture change process-wide environment and use one private repository.
func TestAcknowledgeRetiredPublicationAfterSupportedPreservedRetirement(t *testing.T) {
	fixture := newEngineFixture(t)
	receipt := buildStuckRetiredPublicationReceipt(t, fixture, "retired-absent-source", "feature/retired-absent", "absent")
	original, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	installRetiredPublicationGH(t, fixture.repository.Slug, receipt.PullRequest)
	t.Setenv("WB_TEST_PR_STATE", "CLOSED")
	t.Setenv("WB_TEST_PR_CLOSED_AT", "2026-09-01T00:00:00Z")
	t.Setenv("WB_TEST_PR_MERGED_AT", "")
	t.Setenv("WB_TEST_PR_HEAD_REF", receipt.Candidate.Branch)
	t.Setenv("WB_TEST_PR_HEAD_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_PR_MERGE_COMMIT_SHA", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", "--delete", receipt.Candidate.Branch)

	archive := filepath.Join(t.TempDir(), "archive.git")
	runEngineGit(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
	testenv.ConfigureGitAutoMaintenanceOff(t, archive)
	inspect := func(_ context.Context, repository string) (worktrees.RetiredArchiveInspection, error) {
		return worktrees.RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
	}
	retired, err := worktrees.Retire(context.Background(), worktrees.RetireOptions{
		ProjectsRoot: fixture.githubDir, Task: receipt.Candidate.Task, Repository: fixture.repository.Slug, Apply: true,
		ArchiveRemote: archive, RemoteOwnership: func(context.Context, string) error { return nil },
		Inspect: inspect, OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if retired.Phase != "complete" {
		t.Fatalf("retirement = %+v", retired)
	}
	if _, err := os.Stat(receipt.Candidate.Worktree); !os.IsNotExist(err) {
		t.Fatalf("retired checkout remains: %v", err)
	}

	options := WorktreeMergeRetiredPublicationAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "operator", Reason: "supported preserved retirement",
	}
	ports := productionRetiredPublicationPorts()
	ports.archiveRemote = archive
	ports.inspect = inspect
	dryRunPorts := ports
	dryRunOptions := options
	dryRunOptions.Apply = false
	dryRun, err := acknowledgeRetiredPublication(context.Background(), dryRunOptions, dryRunPorts)
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.RetirementPhase != "complete" || dryRun.RetirementSourceSHA != retired.SourceSHA {
		t.Fatalf("dry-run acknowledgement = %+v", dryRun)
	}
	if _, err := os.Stat(dryRun.AcknowledgementPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote acknowledgement: %v", err)
	}

	ack, err := acknowledgeRetiredPublication(context.Background(), options, ports)
	if err != nil {
		t.Fatal(err)
	}
	if ack.RetirementReceiptPath != retired.ReportPath || ack.RetirementSourceSHA != retired.SourceSHA ||
		ack.RetirementRetiredRef != retired.RetiredRef || ack.RetirementArchiveRef != retired.ArchiveRef ||
		ack.RetirementArchiveSHA != retired.ArchiveSHA || ack.RetirementPhase != "complete" ||
		ack.RetirementCanonical != retired.Canonical || len(ack.RetirementReceiptSHA256) != 64 || len(ack.RetirementProofSHA256) != 64 {
		t.Fatalf("retirement evidence = %+v", ack)
	}
	if unchanged, err := os.ReadFile(receipt.ReceiptPath); err != nil || string(unchanged) != string(original) {
		t.Fatalf("historical merge receipt changed: %v", err)
	}
	if _, err := os.Stat(ack.AcknowledgementPath); err != nil {
		t.Fatal(err)
	}
	ackAgain, err := acknowledgeRetiredPublication(context.Background(), options, ports)
	if err != nil || ackAgain.ID != ack.ID || !sameRetiredPublicationAcknowledgement(ackAgain, ack) {
		t.Fatalf("idempotent acknowledgement = %+v err=%v", ackAgain, err)
	}

	if len(receipt.Sources) != 1 {
		t.Fatalf("expected exactly one receipted source, got %+v", receipt.Sources)
	}
	next, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{receipt.Sources[0].Worktree}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Status != WorktreeMergePrepared || next.PullRequest != "" || next.ReceiptPath == receipt.ReceiptPath {
		t.Fatalf("new merge preflight = %+v", next)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.canonical, "ls-remote", "--heads", "origin", "refs/heads/"+retired.RetiredRef)); !strings.HasPrefix(remote, retired.SourceSHA+"\t") {
		t.Fatalf("retired source ref changed: %q", remote)
	}
}
