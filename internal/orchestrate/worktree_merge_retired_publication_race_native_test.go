//go:build e2e

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

// retiredPublicationNativeJourney prepares the exact native absent-candidate
// journey: a closed-unmerged candidate whose remote branch is gone, a
// supported preserved retirement that removes the checkout and publishes the
// private archive, and the production acknowledgement ports.
func retiredPublicationNativeJourney(t *testing.T, label string) (engineFixture, WorktreeMergeReceipt, worktrees.RetireResult, retiredPublicationPorts, WorktreeMergeRetiredPublicationAcknowledgementOptions) {
	t.Helper()
	fixture := newEngineFixture(t)
	receipt := buildStuckRetiredPublicationReceipt(t, fixture, label+"-source", "feature/"+label, label)
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
	ports := productionRetiredPublicationPorts()
	ports.archiveRemote = archive
	ports.inspect = inspect
	options := WorktreeMergeRetiredPublicationAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "operator", Reason: "supported preserved retirement",
	}
	return fixture, receipt, retired, ports, options
}

// TestE2EAcknowledgeRetiredPublicationRefusesRetirementReceiptChangedBeforePersist
// changes the exact retirement receipt after the proof but immediately before
// publication. The held session must revalidate its bytes and refuse without
// recording an acknowledgement or touching the original receipt again.
//
//nolint:paralleltest // engine and GH fixtures change process-wide environment.
func TestE2EAcknowledgeRetiredPublicationRefusesRetirementReceiptChangedBeforePersist(t *testing.T) {
	_, receipt, retired, ports, options := retiredPublicationNativeJourney(t, "retired-receipt-race")
	mergeReceiptBefore, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	retirementBefore, err := os.ReadFile(retired.ReportPath)
	if err != nil {
		t.Fatal(err)
	}
	ports.beforePersist = func() error {
		// Trailing whitespace keeps the receipt decodable while changing the
		// exact bound bytes, so only the digest fence can refuse it.
		return os.WriteFile(retired.ReportPath, append(append([]byte(nil), retirementBefore...), ' '), 0o600)
	}
	_, err = acknowledgeRetiredPublication(context.Background(), options, ports)
	if err == nil || !strings.Contains(err.Error(), "retirement evidence changed while proving retired publication") {
		t.Fatalf("changed retirement receipt = %v", err)
	}
	if _, statErr := os.Stat(retiredPublicationAcknowledgementPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
		t.Fatalf("changed retirement receipt published an acknowledgement: %v", statErr)
	}
	mergeReceiptAfter, readErr := os.ReadFile(receipt.ReceiptPath)
	if readErr != nil || string(mergeReceiptAfter) != string(mergeReceiptBefore) {
		t.Fatalf("historical merge receipt changed: %v", readErr)
	}
	changedReceipt, readErr := os.ReadFile(retired.ReportPath)
	if readErr != nil || string(changedReceipt) != string(append(append([]byte(nil), retirementBefore...), ' ')) {
		t.Fatalf("retirement receipt was rewritten beyond the injected change: %v", readErr)
	}
}

// TestE2EAcknowledgeRetiredPublicationRefusesCanonicalReboundBeforeFetch proves
// the exact canonical identity survives the proof boundary: replacing the
// canonical root after ProveRetiredCheckoutHeld succeeds refuses the first
// orchestration Git read instead of resolving the newly created path.
//
//nolint:paralleltest // engine and GH fixtures change process-wide environment.
func TestE2EAcknowledgeRetiredPublicationRefusesCanonicalReboundBeforeFetch(t *testing.T) {
	fixture, receipt, retired, ports, options := retiredPublicationNativeJourney(t, "retired-canonical-race")
	mergeReceiptBefore, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	moved := fixture.canonical + "-moved"
	base := ports.proveRetirement
	ports.proveRetirement = func(ctx context.Context, proofOptions worktrees.RetiredCheckoutProofOptions) (heldRetiredCheckout, error) {
		held, proofErr := base(ctx, proofOptions)
		if proofErr != nil {
			return nil, proofErr
		}
		t.Cleanup(func() {
			_ = os.RemoveAll(fixture.canonical)
			_ = os.Rename(moved, fixture.canonical)
		})
		if err := os.Rename(fixture.canonical, moved); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Join(fixture.canonical, ".git"), 0o700); err != nil {
			return nil, err
		}
		return held, nil
	}
	_, err = acknowledgeRetiredPublication(context.Background(), options, ports)
	if err == nil || !strings.Contains(err.Error(), "canonical repository path changed") {
		t.Fatalf("canonical rebound = %v", err)
	}
	if _, statErr := os.Stat(retiredPublicationAcknowledgementPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
		t.Fatalf("canonical rebound published an acknowledgement: %v", statErr)
	}
	mergeReceiptAfter, readErr := os.ReadFile(receipt.ReceiptPath)
	if readErr != nil || string(mergeReceiptAfter) != string(mergeReceiptBefore) {
		t.Fatalf("historical merge receipt changed: %v", readErr)
	}
	remote := strings.TrimSpace(runEngineGit(t, moved, "ls-remote", "--heads", "origin", "refs/heads/"+retired.RetiredRef))
	if !strings.HasPrefix(remote, retired.SourceSHA+"\t") {
		t.Fatalf("retired source ref changed: %q", remote)
	}
}
