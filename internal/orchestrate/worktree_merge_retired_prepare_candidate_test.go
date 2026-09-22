package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/retiredcandidateack"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func retiredPrepareCandidateFixture(t *testing.T, diverged bool) (engineFixture, WorktreeMergeReceipt) {
	t.Helper()
	fixture := newEngineFixture(t)
	runEngineGit(t, fixture.repository.CloneURL, "symbolic-ref", "HEAD", "refs/heads/main")
	runEngineGit(t, fixture.canonical, "branch", "deleted-target", "main")
	if diverged {
		runEngineGit(t, fixture.canonical, "checkout", "deleted-target")
		writeEngineFile(t, filepath.Join(fixture.canonical, "only-on-deleted-target.txt"), "target only\n")
		runEngineGit(t, fixture.canonical, "add", "only-on-deleted-target.txt")
		runEngineGit(t, fixture.canonical, "commit", "-m", "target only")
		runEngineGit(t, fixture.canonical, "checkout", "main")
	}
	runEngineGit(t, fixture.canonical, "push", "origin", "deleted-target")
	created, err := worktrees.Create(context.Background(), []string{fixture.repository.Slug}, worktrees.CreateOptions{
		ProjectsRoot: fixture.githubDir, Operation: "retired-prepare-candidate", Base: "deleted-target",
		WorkLog: worktrees.WorkLogOptions{Model: "test-model"},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("create candidate: results=%+v err=%v", created, err)
	}
	candidate := created[0]
	home, err := wbhome.Root(fixture.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	receipt := WorktreeMergeReceipt{
		SchemaVersion: WorktreeMergeSchemaVersion,
		ID:            "retired-prepare-receipt", Lane: worktreeMergeLaneID(fixture.repository.Slug, "deleted-target"),
		Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeConflict,
		Repository: fixture.repository.Slug, Target: "deleted-target", TargetSHA: candidate.BaseSHA,
		Candidate:   WorktreeMergeCandidate{Task: "retired-prepare-candidate", Worktree: candidate.WorktreeDir, Branch: candidate.Branch},
		Sources:     []WorktreeMergeSource{{Task: "source", Worktree: filepath.Join(fixture.githubDir, "source"), Branch: "feature/source", SHA: strings.Repeat("b", 40)}},
		ReceiptPath: filepath.Join(home, "reports", "worktree-merge", "retired-prepare-receipt.json"),
	}
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	return fixture, receipt
}

func TestAcknowledgeRetiredPrepareCandidateWritesBoundCandidateOnlyProof(t *testing.T) {
	fixture, receipt := retiredPrepareCandidateFixture(t, false)
	options := WorktreeMergeRetiredPrepareCandidateAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true,
		Actor: "reviewer", Reason: "candidate reached default branch and recorded target was deleted",
	}
	ackPath := retiredcandidateack.Path(receipt.ReceiptPath)
	original, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcknowledgeRetiredPrepareCandidate(context.Background(), options); err == nil || !strings.Contains(err.Error(), "still exists") {
		t.Fatalf("live target was accepted: %v", err)
	}
	if _, err := os.Stat(ackPath); !os.IsNotExist(err) {
		t.Fatalf("refused acknowledgement wrote sidecar: %v", err)
	}
	runEngineGit(t, fixture.canonical, "push", "origin", ":deleted-target")
	options.Apply = false
	dryRun, err := AcknowledgeRetiredPrepareCandidate(context.Background(), options)
	if err != nil || dryRun.Candidate.SHA != receipt.TargetSHA || dryRun.DefaultBranch != "main" {
		t.Fatalf("dry run: ack=%+v err=%v", dryRun, err)
	}
	if _, err := os.Stat(ackPath); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote sidecar: %v", err)
	}
	options.Apply = true
	options.Actor = ""
	if _, err := AcknowledgeRetiredPrepareCandidate(context.Background(), options); err == nil || !strings.Contains(err.Error(), "--actor") {
		t.Fatalf("missing actor was accepted: %v", err)
	}
	options.Actor = "reviewer"
	ack, err := AcknowledgeRetiredPrepareCandidate(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if ack.ID == "" || ack.Candidate.SHA != receipt.TargetSHA || len(ack.Sources) != 1 {
		t.Fatalf("applied acknowledgement = %+v", ack)
	}
	if _, err := retiredcandidateack.Load(ackPath, retiredPrepareCandidateIdentity(receipt, receipt.ReceiptPath)); err != nil {
		t.Fatalf("sidecar did not bind to receipt: %v", err)
	}
	options.Reason = "idempotent retry"
	again, err := AcknowledgeRetiredPrepareCandidate(context.Background(), options)
	if err != nil || again.ID != ack.ID {
		t.Fatalf("retry changed acknowledgement: ack=%+v err=%v", again, err)
	}
	unchanged, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil || string(unchanged) != string(original) {
		t.Fatalf("historical receipt changed: err=%v", err)
	}
}

func TestAcknowledgeRetiredPrepareCandidateRefusesUncontainedCandidate(t *testing.T) {
	fixture, receipt := retiredPrepareCandidateFixture(t, true)
	runEngineGit(t, fixture.canonical, "push", "origin", ":deleted-target")
	_, err := AcknowledgeRetiredPrepareCandidate(context.Background(), WorktreeMergeRetiredPrepareCandidateAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true,
		Actor: "reviewer", Reason: "must refuse an uncontained candidate",
	})
	if err == nil || !strings.Contains(err.Error(), "not contained") {
		t.Fatalf("uncontained candidate was accepted: %v", err)
	}
	if _, err := os.Stat(retiredcandidateack.Path(receipt.ReceiptPath)); !os.IsNotExist(err) {
		t.Fatalf("refused acknowledgement wrote sidecar: %v", err)
	}
}

func TestAcknowledgeRetiredPrepareCandidateRefusesChangedCandidateState(t *testing.T) {
	for _, test := range []struct {
		name, want string
		change     func(*testing.T, WorktreeMergeReceipt)
	}{
		{name: "dirty worktree", want: "local changes", change: func(t *testing.T, receipt WorktreeMergeReceipt) {
			writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "untracked.txt"), "not committed\n")
		}},
		{name: "published branch", want: "published", change: func(t *testing.T, receipt WorktreeMergeReceipt) {
			runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", receipt.Candidate.Branch)
		}},
		{name: "advanced head", want: "no longer matches", change: func(t *testing.T, receipt WorktreeMergeReceipt) {
			writeEngineFile(t, filepath.Join(receipt.Candidate.Worktree, "new-commit.txt"), "candidate changed\n")
			runEngineGit(t, receipt.Candidate.Worktree, "add", "new-commit.txt")
			runEngineGit(t, receipt.Candidate.Worktree, "commit", "-m", "advance candidate")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, receipt := retiredPrepareCandidateFixture(t, false)
			runEngineGit(t, fixture.canonical, "push", "origin", ":deleted-target")
			test.change(t, receipt)
			_, err := AcknowledgeRetiredPrepareCandidate(context.Background(), WorktreeMergeRetiredPrepareCandidateAcknowledgementOptions{
				ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true,
				Actor: "reviewer", Reason: "candidate state must remain unchanged",
			})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("changed candidate was accepted or gave wrong error: %v", err)
			}
			if _, err := os.Stat(retiredcandidateack.Path(receipt.ReceiptPath)); !os.IsNotExist(err) {
				t.Fatalf("refused acknowledgement wrote sidecar: %v", err)
			}
		})
	}
}

func TestAcknowledgeRetiredPrepareCandidateRejectsTamperedSidecar(t *testing.T) {
	fixture, receipt := retiredPrepareCandidateFixture(t, false)
	runEngineGit(t, fixture.canonical, "push", "origin", ":deleted-target")
	options := WorktreeMergeRetiredPrepareCandidateAcknowledgementOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Apply: true,
		Actor: "reviewer", Reason: "candidate is contained in main",
	}
	if _, err := AcknowledgeRetiredPrepareCandidate(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	ackPath := retiredcandidateack.Path(receipt.ReceiptPath)
	if err := os.WriteFile(ackPath, []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AcknowledgeRetiredPrepareCandidate(context.Background(), options); err == nil {
		t.Fatal("tampered sidecar was accepted or overwritten")
	}
}

func TestAcknowledgeRetiredPrepareCandidateRejectsInvalidReceiptAndDetachedHead(t *testing.T) {
	t.Run("invalid receipt", func(t *testing.T) {
		fixture, receipt := retiredPrepareCandidateFixture(t, false)
		receipt.Candidate.SHA = receipt.TargetSHA
		if err := persistWorktreeMergeReceipt(receipt); err != nil {
			t.Fatal(err)
		}
		if _, err := AcknowledgeRetiredPrepareCandidate(context.Background(), WorktreeMergeRetiredPrepareCandidateAcknowledgementOptions{
			ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath,
		}); err == nil || !strings.Contains(err.Error(), "legacy empty-candidate") {
			t.Fatalf("invalid receipt was accepted: %v", err)
		}
	})
	t.Run("detached head", func(t *testing.T) {
		fixture, receipt := retiredPrepareCandidateFixture(t, false)
		runEngineGit(t, fixture.canonical, "push", "origin", ":deleted-target")
		runEngineGit(t, receipt.Candidate.Worktree, "checkout", "--detach")
		if _, err := AcknowledgeRetiredPrepareCandidate(context.Background(), WorktreeMergeRetiredPrepareCandidateAcknowledgementOptions{
			ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath,
		}); err == nil || !strings.Contains(err.Error(), "read candidate branch") {
			t.Fatalf("detached candidate was accepted: %v", err)
		}
	})
}

func TestValidateRetiredPrepareCandidateReceiptRejectsCandidateOrSourceConfusion(t *testing.T) {
	const targetSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	receipt := WorktreeMergeReceipt{
		ID: "legacy-prepare", Lane: worktreeMergeLaneID("acme/app", "deleted-target"),
		Phase: WorktreeMergePhasePrepare, Status: WorktreeMergeConflict,
		Repository: "acme/app", Target: "deleted-target", TargetSHA: targetSHA,
		Candidate: WorktreeMergeCandidate{Task: "candidate", Worktree: "/candidate", Branch: "wb/integration/candidate"},
		Sources:   []WorktreeMergeSource{{Task: "source", Worktree: "/source", Branch: "feature/source", SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},
	}
	path := "/reports/legacy-prepare.json"
	receipt.ReceiptPath = path
	if err := validateRetiredPrepareCandidateReceipt(receipt, path); err != nil {
		t.Fatalf("valid legacy candidate-only shape rejected: %v", err)
	}

	withCandidateSHA := receipt
	withCandidateSHA.Candidate.SHA = targetSHA
	if err := validateRetiredPrepareCandidateReceipt(withCandidateSHA, path); err == nil {
		t.Fatal("receipt recording a candidate SHA was accepted")
	}
	withPublishedCandidate := receipt
	withPublishedCandidate.PublishedCandidateSHA = targetSHA
	if err := validateRetiredPrepareCandidateReceipt(withPublishedCandidate, path); err == nil {
		t.Fatal("published candidate receipt was accepted")
	}
	withConfusedSource := receipt
	withConfusedSource.Sources[0].Worktree = receipt.Candidate.Worktree
	if err := validateRetiredPrepareCandidateReceipt(withConfusedSource, path); err == nil {
		t.Fatal("candidate/source worktree confusion was accepted")
	}
}
