//go:build e2e

package orchestrate

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// A source can receive a later clean commit and be retired after its earlier
// candidate landed. Its historical failed-CI receipt still needs an audited
// acknowledgement before a new lane can start.
func cleanedDescendantFailureFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt, string) {
	t.Helper()
	fixture, source, receipt, _ := landedTerminalCleanupFixture(t)
	runEngineGit(t, source.WorktreeDir, "commit", "--allow-empty", "-m", "later source work")
	finalSource := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	runEngineGit(t, source.WorktreeDir, "push", "origin", source.Branch)
	runEngineGit(t, fixture.canonical, "merge", "--no-ff", "--no-edit", source.Branch)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	receipt.Status = WorktreeMergePostTargetCIFailed
	receipt.Checks.Status = githubchecks.PullRequestWaitFailed
	receipt.Checks.Head = receipt.LandingSHA
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	for _, task := range sortedUniqueMergeTasks(receipt) {
		outcome, err := worktrees.Cleanup(context.Background(), worktrees.CleanupOptions{
			ProjectsRoot: fixture.githubDir, Task: task, Base: receipt.Target,
			ExactRepository: receipt.Repository, Apply: true, DeleteRemote: true,
			OlderThan: 0, Workers: 1,
		})
		if err != nil || len(outcome.Results) != 1 || !outcome.Results[0].Applied {
			t.Fatalf("clean up %s: %+v, error %v", task, outcome, err)
		}
	}
	return fixture, receipt, finalSource
}

//nolint:paralleltest // The real Git fixture configures process environment with t.Setenv.
func TestE2EAcknowledgeCleanedPostTargetFailureWithLaterSourceDescendant(t *testing.T) {
	fixture, receipt, finalSource := cleanedDescendantFailureFixture(t)
	before, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	options := WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath}
	preview, err := AcknowledgeLandedMergeFailure(context.Background(), options)
	if err != nil || len(preview.ObservedSources) != 1 || preview.ObservedSources[0].SHA != finalSource || preview.ReceiptSHA256 == "" {
		t.Fatalf("descendant preview = %+v, error %v", preview, err)
	}
	if _, err := os.Stat(preview.AcknowledgementPath); !os.IsNotExist(err) {
		t.Fatalf("preview wrote acknowledgement: %v", err)
	}
	options.Apply, options.Actor, options.Reason = true, "reviewer", "clean terminal descendant and fetched target corroborate the old landing"
	ack, err := AcknowledgeLandedMergeFailure(context.Background(), options)
	if err != nil || ack.ID == "" || ack.ObservedSources[0].SHA != finalSource {
		t.Fatalf("descendant apply = %+v, error %v", ack, err)
	}
	if _, err := readLandedFailureAcknowledgement(ack.AcknowledgementPath, receipt); err != nil {
		t.Fatalf("read descendant acknowledgement: %v", err)
	}
	after, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("historical receipt changed: %v", err)
	}
}

//nolint:paralleltest // The real Git fixture configures process environment with t.Setenv.
func TestE2EAcknowledgeCleanedPostTargetFailureRefusesUnreadableReceiptDigest(t *testing.T) {
	fixture, receipt, _ := cleanedDescendantFailureFixture(t)
	sentinel := errors.New("digest read failed")
	options := WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath}
	_, err := acknowledgeCleanedPostTargetFailure(context.Background(), options, receipt, receipt.ReceiptPath,
		defaultRunner, persistLandedFailureAcknowledgement, false,
		func(string) (string, error) { return "", sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("digest failure = %v, want sentinel", err)
	}
	if _, err := os.Stat(landedFailureAcknowledgementPath(receipt.ReceiptPath)); !os.IsNotExist(err) {
		t.Fatalf("digest failure wrote acknowledgement: %v", err)
	}
}

//nolint:paralleltest // The real Git fixture configures process environment with t.Setenv.
func TestE2EAcknowledgeCleanedPostTargetFailureRejectsDivergentSource(t *testing.T) {
	fixture, source, receipt, _ := landedTerminalCleanupFixture(t)
	// In this disposable Git fixture the source branch is replaced after its
	// receipted commit. Both histories reach main, but the final source head
	// does not descend from the historical source/candidate SHA.
	runEngineGit(t, source.WorktreeDir, "reset", "--hard", receipt.TargetSHA)
	runEngineGit(t, source.WorktreeDir, "commit", "--allow-empty", "-m", "divergent source work")
	runEngineGit(t, source.WorktreeDir, "push", "--force-with-lease", "origin", source.Branch)
	runEngineGit(t, fixture.canonical, "merge", "--no-ff", "--no-edit", source.Branch)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	receipt.Status = WorktreeMergePostTargetCIFailed
	receipt.Checks.Status = githubchecks.PullRequestWaitFailed
	receipt.Checks.Head = receipt.LandingSHA
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	for _, task := range sortedUniqueMergeTasks(receipt) {
		outcome, err := worktrees.Cleanup(context.Background(), worktrees.CleanupOptions{
			ProjectsRoot: fixture.githubDir, Task: task, Base: receipt.Target,
			ExactRepository: receipt.Repository, Apply: true, DeleteRemote: true,
			OlderThan: 0, Workers: 1,
		})
		if err != nil || len(outcome.Results) != 1 || !outcome.Results[0].Applied {
			t.Fatalf("clean up %s: %+v, error %v", task, outcome, err)
		}
	}
	_, err := AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath,
	})
	if err == nil || !strings.Contains(err.Error(), "not a proven descendant") {
		t.Fatalf("divergent source accepted: %v", err)
	}
}

//nolint:paralleltest // The real Git fixture configures process environment with t.Setenv.
func TestE2EAcknowledgeCleanedPostTargetFailureRejectsAdvancedCandidate(t *testing.T) {
	fixture, _, receipt, _ := landedTerminalCleanupFixture(t)
	// A later candidate commit can be clean and present on the target, but it
	// is never the receipted candidate whose failed landing is being audited.
	runEngineGit(t, receipt.Candidate.Worktree, "commit", "--allow-empty", "-m", "later candidate work")
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", receipt.Candidate.Branch)
	runEngineGit(t, fixture.canonical, "merge", "--no-ff", "--no-edit", receipt.Candidate.Branch)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	receipt.Status = WorktreeMergePostTargetCIFailed
	receipt.Checks.Status = githubchecks.PullRequestWaitFailed
	receipt.Checks.Head = receipt.LandingSHA
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	for _, task := range sortedUniqueMergeTasks(receipt) {
		outcome, err := worktrees.Cleanup(context.Background(), worktrees.CleanupOptions{
			ProjectsRoot: fixture.githubDir, Task: task, Base: receipt.Target,
			ExactRepository: receipt.Repository, Apply: true, DeleteRemote: true,
			OlderThan: 0, Workers: 1,
		})
		if err != nil || len(outcome.Results) != 1 || !outcome.Results[0].Applied {
			t.Fatalf("clean up %s: %+v, error %v", task, outcome, err)
		}
	}
	_, err := AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath,
	})
	if err == nil || !strings.Contains(err.Error(), "terminal cleanup receipt does not exactly match") {
		t.Fatalf("advanced candidate accepted: %v", err)
	}
}
