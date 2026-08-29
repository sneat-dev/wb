package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCleanupSupersessionRefusesAnUnclassifiedResidual is the issue #97
// acceptance fixture's fail-closed half. The source branch is intentionally
// split: one source commit is accounted for, while the second is omitted from
// the machine-readable inventory. A trusted-looking receipt must not turn that
// omission into deletion authority.
func TestCleanupSupersessionRefusesAnUnclassifiedResidual(t *testing.T) {
	fixture, result, sourceCommits, targetHead := prepareSupersessionTask(t, "supersession-unclassified")
	receiptPath := writeSupersessionReceipt(t, fixture, SupersessionReceipt{
		Version: 1, Repository: result.Repository, Task: "supersession-unclassified", Branch: result.Branch,
		OriginalHead: sourceCommits[len(sourceCommits)-1], Target: "main", TargetHead: targetHead,
		Replacements:      []SupersessionReplacement{{Kind: "commit", Ref: "replacement", SHA: targetHead}},
		Residuals:         []SupersessionResidual{{Commit: sourceCommits[0], Classification: "replaced", Reason: "replacement slice landed", ReplacementRef: "replacement", Reviewed: true}},
		ResidualsComplete: true,
		Approval:          SupersessionApproval{Actor: "reviewer@example.test", Trusted: true, Decision: "approved", ReceiptID: "review-1", ApprovedAt: time.Now().UTC()},
	})
	installMergedPullRequestFixtures(t, nil, time.Time{})

	outcome, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "supersession-unclassified", SupersededBy: receiptPath,
		Apply: true, DeleteRemote: true, OlderThan: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].Eligible || outcome.Results[0].Applied {
		t.Fatalf("unclassified residual was accepted: %#v", outcome.Results)
	}
	if !strings.Contains(outcome.Results[0].Reason, "unclassified") {
		t.Fatalf("refusal does not identify the incomplete residual inventory: %q", outcome.Results[0].Reason)
	}
	if _, err := os.Stat(result.WorktreeDir); err != nil {
		t.Fatalf("refused supersession removed worktree: %v", err)
	}
	if got := remoteBranchForTest(t, fixture.canonical, result.Branch); got == "" {
		t.Fatal("refused supersession removed remote branch")
	}
}

// TestCleanupSupersessionDryRunAndApplyShareReviewedEvidence proves the
// positive path and the dry-run/apply evidence boundary. Both invocations use
// the same immutable receipt; apply may terminalize only after rechecking the
// exact source and target heads.
func TestCleanupSupersessionDryRunAndApplyShareReviewedEvidence(t *testing.T) {
	fixture, result, sourceCommits, targetHead := prepareSupersessionTask(t, "supersession-reviewed")
	receipt := SupersessionReceipt{
		Version: 1, Repository: result.Repository, Task: "supersession-reviewed", Branch: result.Branch,
		OriginalHead: sourceCommits[len(sourceCommits)-1], Target: "main", TargetHead: targetHead,
		Replacements: []SupersessionReplacement{{Kind: "commit", Ref: "replacement", SHA: targetHead}},
		Residuals: []SupersessionResidual{
			{Commit: sourceCommits[0], Classification: "replaced", Reason: "replacement slice landed", ReplacementRef: "replacement", Reviewed: true},
			{Commit: sourceCommits[1], Classification: "obsolete", Reason: "old harness behavior", Reviewed: true},
		},
		ResidualsComplete: true,
		Approval:          SupersessionApproval{Actor: "reviewer@example.test", Trusted: true, Decision: "approved", ReceiptID: "review-2", ApprovedAt: time.Now().UTC()},
	}
	receiptPath := writeSupersessionReceipt(t, fixture, receipt)
	installMergedPullRequestFixtures(t, nil, time.Time{})

	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "supersession-reviewed", SupersededBy: receiptPath,
		OlderThan: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Results) != 1 || !planned.Results[0].Eligible || !planned.Results[0].SupersededAtOrigin {
		t.Fatalf("reviewed supersession dry-run = %#v", planned.Results)
	}

	applied, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "supersession-reviewed", SupersededBy: receiptPath,
		Apply: true, DeleteRemote: true, OlderThan: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Results) != 1 || !applied.Results[0].Eligible || !applied.Results[0].Applied || !applied.Results[0].SupersededAtOrigin {
		t.Fatalf("reviewed supersession apply = %#v", applied.Results)
	}
	if _, err := os.Stat(result.WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("superseded worktree still exists: %v", err)
	}
	if got := remoteBranchForTest(t, fixture.canonical, result.Branch); got != "" {
		t.Fatalf("superseded remote branch remains at %s", got)
	}
	var archived SupersessionReceipt
	err = filepath.WalkDir(fixture.home, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || !strings.Contains(path, string(filepath.Separator)+"terminals"+string(filepath.Separator)) {
			return nil
		}
		contents, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		var terminal workLogTerminalRecord
		if decodeErr := json.Unmarshal(contents, &terminal); decodeErr != nil {
			return nil
		}
		if terminal.Supersession != nil {
			archived = *terminal.Supersession
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if archived.Approval.ReceiptID != receipt.Approval.ReceiptID || len(archived.Residuals) != len(receipt.Residuals) {
		t.Fatalf("archived Work Log omitted supersession evidence: %#v", archived)
	}
}

func TestCleanupSupersessionRefusesTargetDrift(t *testing.T) {
	fixture, result, sourceCommits, targetHead := prepareSupersessionTask(t, "supersession-target-drift")
	receiptPath := writeSupersessionReceipt(t, fixture, completeSupersessionReceipt(result, "supersession-target-drift", sourceCommits, targetHead, "review-target"))
	installMergedPullRequestFixtures(t, nil, time.Time{})
	writeAndCommit(t, fixture.canonical, "target-drift.txt", "drift\n", "target drift")
	gitTest(t, fixture.canonical, "push", "origin", "main")

	outcome, err := Cleanup(context.Background(), CleanupOptions{ProjectsRoot: fixture.projectsRoot, Task: "supersession-target-drift", SupersededBy: receiptPath, Apply: true, DeleteRemote: true, OlderThan: 0})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Results[0].Eligible || outcome.Results[0].Applied || !strings.Contains(outcome.Results[0].Reason, "target head") {
		t.Fatalf("target drift was accepted: %#v", outcome.Results)
	}
	if _, err := os.Stat(result.WorktreeDir); err != nil {
		t.Fatalf("target drift removed worktree: %v", err)
	}
}

func TestCleanupSupersessionRefusesChangedSourceRef(t *testing.T) {
	fixture, result, sourceCommits, targetHead := prepareSupersessionTask(t, "supersession-source-drift")
	receiptPath := writeSupersessionReceipt(t, fixture, completeSupersessionReceipt(result, "supersession-source-drift", sourceCommits, targetHead, "review-source"))
	installMergedPullRequestFixtures(t, nil, time.Time{})
	writeAndCommit(t, result.WorktreeDir, "source-drift.txt", "drift\n", "source drift")

	outcome, err := Cleanup(context.Background(), CleanupOptions{ProjectsRoot: fixture.projectsRoot, Task: "supersession-source-drift", SupersededBy: receiptPath, Apply: true, DeleteRemote: true, OlderThan: 0})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Results[0].Eligible || outcome.Results[0].Applied || !strings.Contains(outcome.Results[0].Reason, "original head") {
		t.Fatalf("source drift was accepted: %#v", outcome.Results)
	}
	if _, err := os.Stat(result.WorktreeDir); err != nil {
		t.Fatalf("source drift removed worktree: %v", err)
	}
}

func TestCleanupSupersessionRefusesRemoteSourceRefDrift(t *testing.T) {
	fixture, result, sourceCommits, targetHead := prepareSupersessionTask(t, "supersession-remote-source-drift")
	receiptPath := writeSupersessionReceipt(t, fixture, completeSupersessionReceipt(result, "supersession-remote-source-drift", sourceCommits, targetHead, "review-remote-source"))
	installMergedPullRequestFixtures(t, nil, time.Time{})
	writeAndCommit(t, result.WorktreeDir, "remote-source-drift.txt", "drift\n", "remote source drift")
	gitTest(t, result.WorktreeDir, "push", "origin", result.Branch)
	gitTest(t, result.WorktreeDir, "reset", "--hard", sourceCommits[len(sourceCommits)-1])

	outcome, err := Cleanup(context.Background(), CleanupOptions{ProjectsRoot: fixture.projectsRoot, Task: "supersession-remote-source-drift", SupersededBy: receiptPath, Apply: true, DeleteRemote: true, OlderThan: 0})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Results[0].Eligible || outcome.Results[0].Applied || !strings.Contains(outcome.Results[0].Reason, "remote branch advanced") {
		t.Fatalf("remote source drift was accepted: %#v", outcome.Results)
	}
}

func TestCleanupSupersessionRefusesUnreviewedResidual(t *testing.T) {
	fixture, result, sourceCommits, targetHead := prepareSupersessionTask(t, "supersession-unreviewed")
	receipt := completeSupersessionReceipt(result, "supersession-unreviewed", sourceCommits, targetHead, "review-unreviewed")
	receipt.Residuals[1].Reviewed = false
	receiptPath := writeSupersessionReceipt(t, fixture, receipt)
	installMergedPullRequestFixtures(t, nil, time.Time{})

	outcome, err := Cleanup(context.Background(), CleanupOptions{ProjectsRoot: fixture.projectsRoot, Task: "supersession-unreviewed", SupersededBy: receiptPath, Apply: true, DeleteRemote: true, OlderThan: 0})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Results[0].Eligible || outcome.Results[0].Applied || !strings.Contains(outcome.Results[0].Reason, "unreviewed") {
		t.Fatalf("unreviewed residual was accepted: %#v", outcome.Results)
	}
}

func prepareSupersessionTask(t *testing.T, task string) (*gitFixture, CreateResult, []string, string) {
	t.Helper()
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: task, WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := created[0]
	commits := []string{
		writeAndCommit(t, result.WorktreeDir, "replacement.txt", "replacement\n", "replacement slice"),
		writeAndCommit(t, result.WorktreeDir, "obsolete.txt", "obsolete\n", "obsolete harness"),
	}
	gitTest(t, result.WorktreeDir, "push", "-u", "origin", result.Branch)
	targetHead := gitTestOutput(t, fixture.canonical, "rev-parse", "origin/main")
	return fixture, result, commits, targetHead
}

func writeSupersessionReceipt(t *testing.T, fixture *gitFixture, receipt SupersessionReceipt) string {
	t.Helper()
	path := filepath.Join(fixture.home, "supersession.json")
	contents, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(contents, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func completeSupersessionReceipt(result CreateResult, task string, sourceCommits []string, targetHead, receiptID string) SupersessionReceipt {
	return SupersessionReceipt{
		Version: 1, Repository: result.Repository, Task: task, Branch: result.Branch,
		OriginalHead: sourceCommits[len(sourceCommits)-1], Target: "main", TargetHead: targetHead,
		Replacements: []SupersessionReplacement{{Kind: "commit", Ref: "replacement", SHA: targetHead}},
		Residuals: []SupersessionResidual{
			{Commit: sourceCommits[0], Classification: "replaced", Reason: "replacement slice landed", ReplacementRef: "replacement", Reviewed: true},
			{Commit: sourceCommits[1], Classification: "obsolete", Reason: "old harness behavior", Reviewed: true},
		},
		ResidualsComplete: true,
		Approval:          SupersessionApproval{Actor: "reviewer@example.test", Trusted: true, Decision: "approved", ReceiptID: receiptID, ApprovedAt: time.Now().UTC()},
	}
}
