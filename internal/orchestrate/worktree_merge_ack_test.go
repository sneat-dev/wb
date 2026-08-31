package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestAcknowledgeLandedValidationFailureLeavesHistoricalReceiptUntouched(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "ack-source", "feature/ack", "ack.txt", "ack\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeValidationFailed
	receipt.Failure = "candidate validation failed"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}

	// The candidate is the exact commit that the current target will contain.
	runEngineGit(t, fixture.canonical, "update-ref", "refs/heads/main", receipt.Candidate.SHA)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	dryRun, err := AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.Status != "landed_failure_acknowledged" || dryRun.CurrentTargetSHA != receipt.Candidate.SHA {
		t.Fatalf("dry-run acknowledgement = %+v", dryRun)
	}
	if _, err := os.Stat(dryRun.AcknowledgementPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote acknowledgement: %v", err)
	}

	ack, err := AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "verified candidate landed before validation receipt became terminal",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ack.ID == "" || ack.AcknowledgementPath == "" {
		t.Fatalf("applied acknowledgement lacks identity: %+v", ack)
	}
	unchanged, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != string(original) {
		t.Fatal("historical merge receipt was rewritten")
	}
	if _, err := os.Stat(ack.AcknowledgementPath); err != nil {
		t.Fatalf("acknowledgement missing: %v", err)
	}
	ackAgain, err := AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "retry",
	})
	if err != nil || ackAgain.ID != ack.ID {
		t.Fatalf("idempotent acknowledgement = %+v err=%v", ackAgain, err)
	}
	if _, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	}); err == nil || !strings.Contains(err.Error(), "acknowledged as a historical landed failure") {
		t.Fatalf("acknowledged receipt was replayable: %v", err)
	}

	// The old non-terminal receipt no longer owns the lane, so a new source
	// revision can pass normal prepare preflight without branch-only evidence.
	newSource := createMergeSource(t, fixture, "ack-source-new", "feature/ack-new", "new.txt", "new\n")
	next, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{newSource.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Status != WorktreeMergePrepared || next.Candidate.SHA == "" {
		t.Fatalf("new merge preflight = %+v", next)
	}
}

func TestAcknowledgeLandedPostTargetCIFailureLeavesReceiptForFreshForwardRepair(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "contactus-source", "feature/contactus", "contactus.txt", "contactus\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce Contactus: the candidate was directly landed, then the exact
	// target CI receipt failed and left its immutable land receipt nonterminal.
	runEngineGit(t, fixture.canonical, "update-ref", "refs/heads/main", receipt.Candidate.SHA)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	receipt.Phase = WorktreeMergePhaseLand
	receipt.Status = WorktreeMergePostTargetCIFailed
	receipt.LandingSHA = receipt.Candidate.SHA
	receipt.Checks = PullRequestWaitResult{Status: PullRequestWaitFailed, Head: receipt.LandingSHA, Reason: "required target check failed"}
	receipt.Failure = "post-target CI failed"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}

	ack, err := AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "Contactus candidate is in remote main and its post-target CI failure remains historical",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ack.ReceiptStatus != WorktreeMergePostTargetCIFailed || ack.ReceiptLandingSHA != receipt.LandingSHA || ack.CurrentTargetSHA != receipt.LandingSHA {
		t.Fatalf("post-target acknowledgement = %+v", ack)
	}
	unchanged, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil || string(unchanged) != string(original) {
		t.Fatalf("post-target receipt changed: err=%v", err)
	}

	// A repair starts from an additive source revision on a different receipt;
	// it cannot replay or overwrite the historical failed landing.
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "repair.txt"), "repair\n")
	runEngineGit(t, source.WorktreeDir, "add", "repair.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "fix: repair Contactus target CI")
	repair, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if repair.ReceiptPath == receipt.ReceiptPath || repair.Candidate.SHA == receipt.Candidate.SHA {
		t.Fatalf("forward repair reused immutable historical receipt: old=%+v new=%+v", receipt, repair)
	}
}

func TestAcknowledgeLandedFailureAcceptsOlderClaimBaseOnlyWhenItIsAnAncestor(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "ack-older-base", "feature/ack-older-base", "source.txt", "source\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: receipt.Candidate.Worktree})
	if err != nil || claim.Claim == nil {
		t.Fatalf("load candidate claim: %+v err=%v", claim, err)
	}
	olderBase := claim.Claim.BaseSHA

	writeEngineFile(t, filepath.Join(fixture.canonical, "target-after-claim.txt"), "target\n")
	runEngineGit(t, fixture.canonical, "add", "target-after-claim.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: advance receipt target")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	laterTarget := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	runEngineGit(t, receipt.Candidate.Worktree, "fetch", "origin")
	runEngineGit(t, receipt.Candidate.Worktree, "merge", "--no-edit", "origin/main")
	receipt.TargetSHA = laterTarget
	receipt.Candidate.SHA = strings.TrimSpace(runEngineGit(t, receipt.Candidate.Worktree, "rev-parse", "HEAD"))
	receipt.Status = WorktreeMergeValidationFailed
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, fixture.canonical, "update-ref", "refs/heads/main", receipt.Candidate.SHA)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")

	ack, err := AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "candidate contains older immutable claim base and later receipt target",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ack.ClaimBaseSHA != olderBase || ack.ReceiptTargetSHA != laterTarget || ack.ClaimBaseSHA == ack.ReceiptTargetSHA {
		t.Fatalf("acknowledgement did not retain distinct ancestry roots: %+v", ack)
	}
}

func TestAcknowledgeLandedFailureRefusesNonAncestorClaimBaseAndIdentityMismatch(t *testing.T) {
	t.Run("non-ancestor claim base", func(t *testing.T) {
		fixture := newEngineFixture(t)
		source := createMergeSource(t, fixture, "ack-bad-base", "feature/ack-bad-base", "source.txt", "source\n")
		receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
		if err != nil {
			t.Fatal(err)
		}
		receipt.Status = WorktreeMergeValidationFailed
		if err := persistWorktreeMergeReceipt(receipt); err != nil {
			t.Fatal(err)
		}
		runEngineGit(t, fixture.canonical, "update-ref", "refs/heads/main", receipt.Candidate.SHA)
		runEngineGit(t, fixture.canonical, "push", "origin", "main")
		writeEngineFile(t, filepath.Join(fixture.canonical, "unrelated.txt"), "unrelated\n")
		runEngineGit(t, fixture.canonical, "add", "unrelated.txt")
		runEngineGit(t, fixture.canonical, "commit", "-m", "test: unrelated claim base")
		unrelated := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
		view, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: receipt.Candidate.Worktree})
		if err != nil || view.Claim == nil {
			t.Fatalf("load candidate claim: %+v err=%v", view, err)
		}
		contents, err := os.ReadFile(view.Claim.ClaimPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(view.Claim.ClaimPath, []byte(strings.Replace(string(contents), view.Claim.BaseSHA, unrelated, 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "unsafe claim base"})
		if err == nil || !strings.Contains(err.Error(), "does not contain immutable claim base") {
			t.Fatalf("non-ancestor claim base error = %v", err)
		}
	})

	t.Run("candidate identity mismatch", func(t *testing.T) {
		fixture := newEngineFixture(t)
		source := createMergeSource(t, fixture, "ack-identity", "feature/ack-identity", "source.txt", "source\n")
		receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
		if err != nil {
			t.Fatal(err)
		}
		receipt.Status = WorktreeMergeValidationFailed
		receipt.Candidate.Task = "different-task"
		if err := persistWorktreeMergeReceipt(receipt); err != nil {
			t.Fatal(err)
		}
		runEngineGit(t, fixture.canonical, "update-ref", "refs/heads/main", receipt.Candidate.SHA)
		runEngineGit(t, fixture.canonical, "push", "origin", "main")
		_, err = AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "unsafe identity"})
		if err == nil || !strings.Contains(err.Error(), "matching the immutable receipt target and identity") {
			t.Fatalf("identity mismatch error = %v", err)
		}
	})
}

func TestSupersedeValidationFailedWorktreeMergeBindsReplacementWithoutRewritingReceipt(t *testing.T) {
	fixture := newEngineFixture(t)
	sourceA := createMergeSource(t, fixture, "supersede-source-a", "feature/supersede-a", "a.txt", "a\n")
	sourceB := createMergeSource(t, fixture, "supersede-source-b", "feature/supersede-b", "b.txt", "b\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{sourceA.WorktreeDir, sourceB.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeValidationFailed
	receipt.Failure = "historical validation failure"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	originalReceipt, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, sourceA.WorktreeDir, "push", "origin", "feature/supersede-a")
	runEngineGit(t, sourceB.WorktreeDir, "push", "origin", "feature/supersede-b")
	writeEngineFile(t, filepath.Join(fixture.canonical, "target.txt"), "target\n")
	runEngineGit(t, fixture.canonical, "add", "target.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: advance target for replacement")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	currentTarget := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	replacement := createMergeSource(t, fixture, "supersede-replacement", "feature/supersede-replacement", "replacement.txt", "replacement\n")
	runEngineGit(t, replacement.WorktreeDir, "fetch", "origin")
	runEngineGit(t, replacement.WorktreeDir, "merge", "--no-edit", "origin/feature/supersede-a")
	runEngineGit(t, replacement.WorktreeDir, "merge", "--no-edit", "origin/feature/supersede-b")
	replacementHead := strings.TrimSpace(runEngineGit(t, replacement.WorktreeDir, "rev-parse", "HEAD"))
	if contains, err := isMergeAncestor(context.Background(), replacement.WorktreeDir, receipt.Candidate.SHA, replacementHead); err != nil || contains {
		t.Fatalf("replacement unexpectedly contains failed candidate: contains=%t err=%v", contains, err)
	}

	dryRun, err := SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if dryRun.CurrentTargetSHA != currentTarget || dryRun.Replacement.SHA != replacementHead {
		t.Fatalf("dry-run supersession = %+v", dryRun)
	}
	if _, err := os.Stat(dryRun.AcknowledgementPath); !os.IsNotExist(err) {
		t.Fatalf("dry-run wrote acknowledgement: %v", err)
	}
	ack, err := SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir, Apply: true, Actor: "reviewer", Reason: "replacement contains immutable roots but not failed candidate",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ack.ID == "" || ack.ReceiptSHA256 == "" || ack.OriginalCandidate != receipt.Candidate || ack.Replacement.SHA != replacementHead {
		t.Fatalf("supersession acknowledgement = %+v", ack)
	}
	unchanged, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil || string(unchanged) != string(originalReceipt) {
		t.Fatalf("failed receipt changed: err=%v", err)
	}
	if superseded, err := hasValidationFailureSupersession(receipt); err != nil || !superseded {
		t.Fatalf("supersession was not valid: superseded=%t err=%v", superseded, err)
	}
	if _, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath}); err == nil || !strings.Contains(err.Error(), "superseded by an audited replacement") {
		t.Fatalf("superseded receipt was replayable: %v", err)
	}
}

func TestSupersedeValidationFailedWorktreeMergeRefusesInvalidEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, fixture engineFixture, receipt *WorktreeMergeReceipt, replacement worktrees.CreateResult)
		want   string
	}{
		{
			name: "wrong receipt status",
			mutate: func(t *testing.T, _ engineFixture, receipt *WorktreeMergeReceipt, _ worktrees.CreateResult) {
				receipt.Status = WorktreeMergePrepared
				if err := persistWorktreeMergeReceipt(*receipt); err != nil {
					t.Fatal(err)
				}
			},
			want: "want prepare validation_failed",
		},
		{
			name: "dirty old candidate",
			mutate: func(t *testing.T, _ engineFixture, receipt *WorktreeMergeReceipt, _ worktrees.CreateResult) {
				writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "dirty.txt"), "dirty\n")
			},
			want: "candidate is not clean",
		},
		{
			name: "dirty replacement",
			mutate: func(t *testing.T, _ engineFixture, _ *WorktreeMergeReceipt, replacement worktrees.CreateResult) {
				writeEngineFile(t, filepath.Join(replacement.WorktreeDir, "dirty.txt"), "dirty\n")
			},
			want: "replacement is not clean",
		},
		{
			name: "drifted old candidate",
			mutate: func(t *testing.T, _ engineFixture, receipt *WorktreeMergeReceipt, _ worktrees.CreateResult) {
				writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "advanced.txt"), "advanced\n")
				runEngineGit(t, receipt.Candidate.Worktree, "add", "advanced.txt")
				runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "test: advance failed candidate")
			},
			want: "does not match receipted candidate",
		},
		{
			name: "drifted receipted source",
			mutate: func(t *testing.T, _ engineFixture, receipt *WorktreeMergeReceipt, _ worktrees.CreateResult) {
				source := receipt.Sources[0]
				writeEngineFile(t, filepath.Join(source.Worktree, "advanced.txt"), "advanced\n")
				runEngineGit(t, source.Worktree, "add", "advanced.txt")
				runEngineGit(t, source.Worktree, "commit", "-m", "test: advance receipted source")
			},
			want: "does not match",
		},
		{
			name: "missing replacement claim",
			mutate: func(t *testing.T, fixture engineFixture, _ *WorktreeMergeReceipt, replacement worktrees.CreateResult) {
				view, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: replacement.WorktreeDir})
				if err != nil || view.Claim == nil {
					t.Fatalf("load replacement claim: %+v err=%v", view, err)
				}
				if err := os.Remove(view.Claim.ClaimPath); err != nil {
					t.Fatal(err)
				}
			},
			want: "load replacement Work Log",
		},
		{
			name: "remote target moved outside replacement",
			mutate: func(t *testing.T, fixture engineFixture, _ *WorktreeMergeReceipt, _ worktrees.CreateResult) {
				writeEngineFile(t, filepath.Join(fixture.canonical, "moved-target.txt"), "moved\n")
				runEngineGit(t, fixture.canonical, "add", "moved-target.txt")
				runEngineGit(t, fixture.canonical, "commit", "-m", "test: move target")
				runEngineGit(t, fixture.canonical, "push", "origin", "main")
			},
			want: "does not contain required immutable root",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, receipt, replacement := supersessionFixture(t)
			test.mutate(t, fixture, &receipt, replacement)
			_, err := SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir, Apply: true, Actor: "reviewer", Reason: "unsafe evidence"})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("supersession error = %v, want %q", err, test.want)
			}
			if _, statErr := os.Stat(validationFailureSupersessionPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
				t.Fatalf("refusal wrote supersession: %v", statErr)
			}
		})
	}
}

func TestSupersedeValidationFailedWorktreeMergeRefusesMissingSourceAncestryAndTampering(t *testing.T) {
	t.Run("missing receipted source ancestry", func(t *testing.T) {
		fixture := newEngineFixture(t)
		source := createMergeSource(t, fixture, "missing-source", "feature/missing-source", "source.txt", "source\n")
		receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
		if err != nil {
			t.Fatal(err)
		}
		receipt.Status = WorktreeMergeValidationFailed
		if err := persistWorktreeMergeReceipt(receipt); err != nil {
			t.Fatal(err)
		}
		writeEngineFile(t, filepath.Join(fixture.canonical, "target.txt"), "target\n")
		runEngineGit(t, fixture.canonical, "add", "target.txt")
		runEngineGit(t, fixture.canonical, "commit", "-m", "test: target without source")
		runEngineGit(t, fixture.canonical, "push", "origin", "main")
		replacement := createMergeSource(t, fixture, "missing-source-replacement", "feature/missing-source-replacement", "replacement.txt", "replacement\n")
		_, err = SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir, Apply: true, Actor: "reviewer", Reason: "missing source ancestry"})
		if err == nil || !strings.Contains(err.Error(), "does not contain required immutable root") {
			t.Fatalf("missing source ancestry error = %v", err)
		}
	})

	t.Run("tampered acknowledgement and receipt", func(t *testing.T) {
		fixture, receipt, replacement := supersessionFixture(t)
		ack, err := SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir, Apply: true, Actor: "reviewer", Reason: "valid supersession"})
		if err != nil {
			t.Fatal(err)
		}
		ackContents, err := os.ReadFile(ack.AcknowledgementPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ack.AcknowledgementPath, []byte(strings.Replace(string(ackContents), ack.ID, "tampered", 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := hasValidationFailureSupersession(receipt); err == nil || !strings.Contains(err.Error(), "invalid immutable identity") {
			t.Fatalf("tampered acknowledgement error = %v", err)
		}
		if err := os.WriteFile(ack.AcknowledgementPath, ackContents, 0o600); err != nil {
			t.Fatal(err)
		}
		receiptContents, err := os.ReadFile(receipt.ReceiptPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(receipt.ReceiptPath, append(receiptContents, ' '), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := hasValidationFailureSupersession(receipt); err == nil || !strings.Contains(err.Error(), "invalid immutable identity") {
			t.Fatalf("tampered receipt error = %v", err)
		}
	})
}

func supersessionFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt, worktrees.CreateResult) {
	t.Helper()
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "supersession-source", "feature/supersession-source", "source.txt", "source\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeValidationFailed
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, source.WorktreeDir, "push", "origin", "feature/supersession-source")
	writeEngineFile(t, filepath.Join(fixture.canonical, "target.txt"), "target\n")
	runEngineGit(t, fixture.canonical, "add", "target.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: advance target")
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	replacement := createMergeSource(t, fixture, "supersession-replacement", "feature/supersession-replacement", "replacement.txt", "replacement\n")
	runEngineGit(t, replacement.WorktreeDir, "fetch", "origin")
	runEngineGit(t, replacement.WorktreeDir, "merge", "--no-edit", "origin/feature/supersession-source")
	return fixture, receipt, replacement
}

func TestAcknowledgeLandedValidationFailureRefusesMissingTargetContainment(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "ack-refuse", "feature/ack-refuse", "ack.txt", "ack\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeValidationFailed
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	_, err = AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "unsafe test",
	})
	if err == nil || !strings.Contains(err.Error(), "does not contain receipted candidate") {
		t.Fatalf("missing target containment error = %v", err)
	}
	if _, statErr := os.Stat(landedFailureAcknowledgementPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
		t.Fatalf("refusal left acknowledgement: %v", statErr)
	}
}

func TestAcknowledgeLandedValidationFailureRefusesChangedReceiptedSource(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "ack-source-drift", "feature/ack-source-drift", "ack.txt", "ack\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeValidationFailed
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, fixture.canonical, "update-ref", "refs/heads/main", receipt.Candidate.SHA)
	runEngineGit(t, fixture.canonical, "push", "origin", "main")
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "changed-after-receipt.txt"), "changed\n")
	runEngineGit(t, source.WorktreeDir, "add", "changed-after-receipt.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "fix: advance receipted source")

	_, err = AcknowledgeLandedMergeFailure(context.Background(), WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "reviewer", Reason: "unsafe source drift",
	})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("changed receipted source acknowledgement error = %v", err)
	}
}
