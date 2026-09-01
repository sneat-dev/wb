package orchestrate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func publishedForwardRepairFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt, WorktreeMergeValidationFailureSupersession, WorktreeMergePublishedForwardRepairOptions) {
	t.Helper()
	fixture, receipt, _, supersession, claimHash := selfSupersessionFixture(t)
	extra := createMergeSource(t, fixture, "published-forward-falsifier-extra", "feature/published-forward-falsifier-extra", "repair.txt", "repair\n")
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	supersessionHash, err := worktreeMergeReceiptSHA256(supersession.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	currentTarget, err := fetchExactMergeTarget(context.Background(), receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		t.Fatal(err)
	}
	extraSHA := strings.TrimSpace(runEngineGit(t, extra.WorktreeDir, "rev-parse", "HEAD"))
	return fixture, receipt, supersession, WorktreeMergePublishedForwardRepairOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Sources: []string{receipt.Sources[0].Worktree, extra.WorktreeDir},
		ExpectedReceiptSHA256: receiptHash, ExpectedImmutableClaimSHA256: claimHash, ExpectedSupersessionSHA256: supersessionHash,
		ExpectedCurrentTargetSHA: currentTarget, ExpectedSourceSHAs: []string{receipt.Sources[0].SHA, extraSHA},
		Apply: true, Actor: "reviewer", Reason: "must refuse changed repair evidence",
	}
}

func assertNoPublishedForwardRepairCandidate(t *testing.T, fixture engineFixture, receipt WorktreeMergeReceipt, options WorktreeMergePublishedForwardRepairOptions) {
	t.Helper()
	listed, err := worktrees.List(context.Background(), worktrees.ListOptions{ProjectsRoot: fixture.githubDir, Task: options.RepairTask(), Base: receipt.Target, Workers: 1})
	if err != nil || len(listed) != 0 {
		t.Fatalf("refusal leaked candidate worktree: listed=%+v err=%v", listed, err)
	}
}

func TestPreparePublishedForwardRepairBreaksSelfSupersessionCycleWithoutMutatingHistoricalEvidence(t *testing.T) {
	fixture, receipt, _, supersession, claimHash := selfSupersessionFixture(t)
	extra := createMergeSource(t, fixture, "published-forward-extra", "feature/published-forward-extra", "repair.txt", "repair\n")
	receiptBefore, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	originalClaim, err := validateMergeAcknowledgementCandidate(context.Background(), fixture.githubDir, receipt, receipt.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	claimBefore, err := os.ReadFile(originalClaim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	supersessionBefore, err := os.ReadFile(supersession.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	supersessionHash, err := worktreeMergeReceiptSHA256(supersession.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	currentTarget, err := fetchExactMergeTarget(context.Background(), receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		t.Fatal(err)
	}
	extraSHA := strings.TrimSpace(runEngineGit(t, extra.WorktreeDir, "rev-parse", "HEAD"))
	options := WorktreeMergePublishedForwardRepairOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath,
		Sources:               []string{receipt.Sources[0].Worktree, extra.WorktreeDir},
		ExpectedReceiptSHA256: receiptHash, ExpectedImmutableClaimSHA256: claimHash,
		ExpectedSupersessionSHA256: supersessionHash, ExpectedCurrentTargetSHA: currentTarget,
		ExpectedSourceSHAs: []string{receipt.Sources[0].SHA, extraSHA},
		Actor:              "reviewer", Reason: "construct a distinct audited repair candidate",
	}

	dryRun, err := PreparePublishedValidationFailureForwardRepair(context.Background(), options)
	if err != nil || dryRun.Status != "published_forward_repair_planned" || dryRun.Candidate.Task == "" {
		t.Fatalf("forward repair dry-run = %+v err=%v", dryRun, err)
	}
	if current, readErr := os.ReadFile(receipt.ReceiptPath); readErr != nil || !bytes.Equal(current, receiptBefore) {
		t.Fatalf("dry-run changed receipt: err=%v", readErr)
	}
	if current, readErr := os.ReadFile(originalClaim.ClaimPath); readErr != nil || !bytes.Equal(current, claimBefore) {
		t.Fatalf("dry-run changed original claim: err=%v", readErr)
	}
	if current, readErr := os.ReadFile(supersession.AcknowledgementPath); readErr != nil || !bytes.Equal(current, supersessionBefore) {
		t.Fatalf("dry-run changed self-supersession acknowledgement: err=%v", readErr)
	}

	options.Apply = true
	repair, err := PreparePublishedValidationFailureForwardRepair(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if repair.Status != "published_forward_repair_prepared" || repair.Candidate.SHA == "" || repair.Candidate.SHA == receipt.Candidate.SHA {
		t.Fatalf("repair candidate = %+v", repair)
	}
	for _, root := range []string{originalClaim.BaseSHA, receipt.TargetSHA, currentTarget, receipt.Sources[0].SHA, extraSHA} {
		contains, ancestorErr := isMergeAncestor(context.Background(), repair.Candidate.Worktree, root, repair.Candidate.SHA)
		if ancestorErr != nil || !contains {
			t.Fatalf("repair candidate lacks root %s: contains=%t err=%v", root, contains, ancestorErr)
		}
	}
	if current, readErr := os.ReadFile(receipt.ReceiptPath); readErr != nil || !bytes.Equal(current, receiptBefore) {
		t.Fatalf("apply changed receipt: err=%v", readErr)
	}
	if current, readErr := os.ReadFile(originalClaim.ClaimPath); readErr != nil || !bytes.Equal(current, claimBefore) {
		t.Fatalf("apply changed original claim: err=%v", readErr)
	}
	if current, readErr := os.ReadFile(supersession.AcknowledgementPath); readErr != nil || !bytes.Equal(current, supersessionBefore) {
		t.Fatalf("apply changed self-supersession acknowledgement: err=%v", readErr)
	}
	if _, err := CorrectValidationFailedSelfSupersession(context.Background(), WorktreeMergeSelfSupersessionCorrectionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: repair.Candidate.Worktree,
		ExpectedSupersessionSHA256: supersessionHash, ExpectedImmutableClaimSHA256: claimHash,
		Apply: true, Actor: "reviewer", Reason: "consume the separate published forward repair candidate",
	}); err != nil {
		t.Fatalf("correct self-supersession with forward repair candidate: %v", err)
	}
}

func TestPreparePublishedForwardRepairRefusesMismatchedPinnedEvidenceWithoutCandidate(t *testing.T) {
	fixture, receipt, _, supersession, claimHash := selfSupersessionFixture(t)
	extra := createMergeSource(t, fixture, "published-forward-refusal-extra", "feature/published-forward-refusal-extra", "repair.txt", "repair\n")
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	supersessionHash, err := worktreeMergeReceiptSHA256(supersession.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	currentTarget, err := fetchExactMergeTarget(context.Background(), receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		t.Fatal(err)
	}
	extraSHA := strings.TrimSpace(runEngineGit(t, extra.WorktreeDir, "rev-parse", "HEAD"))
	options := WorktreeMergePublishedForwardRepairOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Sources: []string{receipt.Sources[0].Worktree, extra.WorktreeDir},
		ExpectedReceiptSHA256: receiptHash, ExpectedImmutableClaimSHA256: claimHash, ExpectedSupersessionSHA256: supersessionHash,
		ExpectedCurrentTargetSHA: currentTarget, ExpectedSourceSHAs: []string{receipt.Sources[0].SHA, extraSHA},
		Apply: true, Actor: "reviewer", Reason: "must not leak a candidate on pinned-evidence refusal",
	}
	for _, test := range []struct {
		name   string
		mutate func(*WorktreeMergePublishedForwardRepairOptions)
	}{
		{name: "receipt", mutate: func(options *WorktreeMergePublishedForwardRepairOptions) {
			options.ExpectedReceiptSHA256 = strings.Repeat("0", 64)
		}},
		{name: "claim", mutate: func(options *WorktreeMergePublishedForwardRepairOptions) {
			options.ExpectedImmutableClaimSHA256 = strings.Repeat("0", 64)
		}},
		{name: "supersession", mutate: func(options *WorktreeMergePublishedForwardRepairOptions) {
			options.ExpectedSupersessionSHA256 = strings.Repeat("0", 64)
		}},
		{name: "target", mutate: func(options *WorktreeMergePublishedForwardRepairOptions) {
			options.ExpectedCurrentTargetSHA = strings.Repeat("0", 40)
		}},
		{name: "source", mutate: func(options *WorktreeMergePublishedForwardRepairOptions) {
			options.ExpectedSourceSHAs[1] = strings.Repeat("0", 40)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			refusal := options
			refusal.ExpectedSourceSHAs = append([]string(nil), options.ExpectedSourceSHAs...)
			test.mutate(&refusal)
			result, err := PreparePublishedValidationFailureForwardRepair(context.Background(), refusal)
			if err == nil || result.Candidate.Worktree != "" {
				t.Fatalf("%s evidence refusal = %+v err=%v", test.name, result, err)
			}
			assertNoPublishedForwardRepairCandidate(t, fixture, receipt, options)
		})
	}
}

func TestPreparePublishedForwardRepairRefusesTamperDriftAndRaceBeforeCandidateCreation(t *testing.T) {
	t.Run("malformed historical self-supersession", func(t *testing.T) {
		fixture, receipt, supersession, options := publishedForwardRepairFixture(t)
		receiptBefore, err := os.ReadFile(receipt.ReceiptPath)
		if err != nil {
			t.Fatal(err)
		}
		originalClaim, err := validateMergeAcknowledgementCandidate(context.Background(), fixture.githubDir, receipt, receipt.Candidate)
		if err != nil {
			t.Fatal(err)
		}
		claimBefore, err := os.ReadFile(originalClaim.ClaimPath)
		if err != nil {
			t.Fatal(err)
		}
		malformed := []byte("not json\\n")
		if err := os.WriteFile(supersession.AcknowledgementPath, malformed, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := PreparePublishedValidationFailureForwardRepair(context.Background(), options); err == nil || !strings.Contains(err.Error(), "read existing self-supersession") {
			t.Fatalf("malformed self-supersession refusal error = %v", err)
		}
		if current, readErr := os.ReadFile(receipt.ReceiptPath); readErr != nil || !bytes.Equal(current, receiptBefore) {
			t.Fatalf("malformed self-supersession changed failed receipt: err=%v", readErr)
		}
		if current, readErr := os.ReadFile(originalClaim.ClaimPath); readErr != nil || !bytes.Equal(current, claimBefore) {
			t.Fatalf("malformed self-supersession changed failed claim: err=%v", readErr)
		}
		if current, readErr := os.ReadFile(supersession.AcknowledgementPath); readErr != nil || !bytes.Equal(current, malformed) {
			t.Fatalf("malformed self-supersession acknowledgement changed: err=%v", readErr)
		}
		assertNoPublishedForwardRepairCandidate(t, fixture, receipt, options)
	})

	t.Run("dirty current source", func(t *testing.T) {
		fixture, receipt, _, options := publishedForwardRepairFixture(t)
		writeEngineFile(t, filepath.Join(options.Sources[1], "dirty.txt"), "dirty\n")
		if _, err := PreparePublishedValidationFailureForwardRepair(context.Background(), options); err == nil || !strings.Contains(err.Error(), "dirty") {
			t.Fatalf("dirty-source refusal error = %v", err)
		}
		assertNoPublishedForwardRepairCandidate(t, fixture, receipt, options)
	})

	t.Run("missing receipted source", func(t *testing.T) {
		fixture, receipt, _, options := publishedForwardRepairFixture(t)
		options.Sources = options.Sources[1:]
		options.ExpectedSourceSHAs = options.ExpectedSourceSHAs[1:]
		if _, err := PreparePublishedValidationFailureForwardRepair(context.Background(), options); err == nil || !strings.Contains(err.Error(), "retain exact receipted source") {
			t.Fatalf("missing-source refusal error = %v", err)
		}
		assertNoPublishedForwardRepairCandidate(t, fixture, receipt, options)
	})

	t.Run("current target drift", func(t *testing.T) {
		fixture, receipt, _, options := publishedForwardRepairFixture(t)
		writeEngineFile(t, filepath.Join(fixture.canonical, "target-drift.txt"), "drift\n")
		runEngineGit(t, fixture.canonical, "add", "target-drift.txt")
		runEngineGit(t, fixture.canonical, "commit", "-m", "test: published forward repair target drift")
		runEngineGit(t, fixture.canonical, "push", "origin", "main")
		if _, err := PreparePublishedValidationFailureForwardRepair(context.Background(), options); err == nil || !strings.Contains(err.Error(), "current target") {
			t.Fatalf("target-drift refusal error = %v", err)
		}
		assertNoPublishedForwardRepairCandidate(t, fixture, receipt, options)
	})

	t.Run("malformed existing correction", func(t *testing.T) {
		fixture, receipt, _, options := publishedForwardRepairFixture(t)
		if err := os.WriteFile(selfSupersessionCorrectionPath(receipt.ReceiptPath), []byte("not json\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := PreparePublishedValidationFailureForwardRepair(context.Background(), options); err == nil || !strings.Contains(err.Error(), "existing self-supersession correction is invalid") {
			t.Fatalf("malformed-correction refusal error = %v", err)
		}
		assertNoPublishedForwardRepairCandidate(t, fixture, receipt, options)
	})

	t.Run("self-supersession race", func(t *testing.T) {
		fixture, receipt, supersession, options := publishedForwardRepairFixture(t)
		previous := beforePublishedForwardRepairCreate
		beforePublishedForwardRepairCreate = func() {
			if err := os.WriteFile(supersession.AcknowledgementPath, []byte("tampered\n"), 0o600); err != nil {
				t.Fatalf("tamper race evidence: %v", err)
			}
		}
		t.Cleanup(func() { beforePublishedForwardRepairCreate = previous })
		if _, err := PreparePublishedValidationFailureForwardRepair(context.Background(), options); err == nil || !strings.Contains(err.Error(), "validation-failed supersession") {
			t.Fatalf("race refusal error = %v", err)
		}
		assertNoPublishedForwardRepairCandidate(t, fixture, receipt, options)
	})

	t.Run("post-construction acknowledgement race retires partial candidate", func(t *testing.T) {
		fixture, receipt, supersession, options := publishedForwardRepairFixture(t)
		previous := beforePublishedForwardRepairFinalRevalidation
		beforePublishedForwardRepairFinalRevalidation = func() {
			if err := os.WriteFile(supersession.AcknowledgementPath, []byte("tampered after candidate construction\n"), 0o600); err != nil {
				t.Fatalf("tamper post-construction evidence: %v", err)
			}
		}
		t.Cleanup(func() { beforePublishedForwardRepairFinalRevalidation = previous })
		if _, err := PreparePublishedValidationFailureForwardRepair(context.Background(), options); err == nil || !strings.Contains(err.Error(), "validation-failed supersession") {
			t.Fatalf("post-construction race refusal error = %v", err)
		}
		assertNoPublishedForwardRepairCandidate(t, fixture, receipt, options)
	})

	t.Run("post-construction current-source terminalization retires partial candidate", func(t *testing.T) {
		fixture, receipt, supersession, options := publishedForwardRepairFixture(t)
		receiptBefore, err := os.ReadFile(receipt.ReceiptPath)
		if err != nil {
			t.Fatal(err)
		}
		originalClaim, err := validateMergeAcknowledgementCandidate(context.Background(), fixture.githubDir, receipt, receipt.Candidate)
		if err != nil {
			t.Fatal(err)
		}
		originalClaimBefore, err := os.ReadFile(originalClaim.ClaimPath)
		if err != nil {
			t.Fatal(err)
		}
		supersessionBefore, err := os.ReadFile(supersession.AcknowledgementPath)
		if err != nil {
			t.Fatal(err)
		}
		previous := beforePublishedForwardRepairFinalRevalidation
		beforePublishedForwardRepairFinalRevalidation = func() {
			view, viewErr := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: options.Sources[1]})
			if viewErr != nil || view.Claim == nil {
				t.Fatalf("load current source claim for terminalization: view=%+v err=%v", view, viewErr)
			}
			contents, readErr := os.ReadFile(view.Claim.ClaimPath)
			if readErr != nil {
				t.Fatalf("read current source claim for terminalization: %v", readErr)
			}
			terminalized := bytes.Replace(contents, []byte(`"lifecycle":"active"`), []byte(`"lifecycle":"terminal"`), 1)
			terminalized = bytes.Replace(terminalized, []byte(`"lifecycle": "active"`), []byte(`"lifecycle": "terminal"`), 1)
			if bytes.Equal(terminalized, contents) {
				t.Fatal("current source claim has no active lifecycle to terminalize")
			}
			if writeErr := os.WriteFile(view.Claim.ClaimPath, terminalized, 0o600); writeErr != nil {
				t.Fatalf("terminalize current source claim: %v", writeErr)
			}
		}
		t.Cleanup(func() { beforePublishedForwardRepairFinalRevalidation = previous })
		if _, err := PreparePublishedValidationFailureForwardRepair(context.Background(), options); err == nil || !strings.Contains(err.Error(), "active Work Log claim") {
			t.Fatalf("terminalized current-source claim refusal error = %v", err)
		}
		if current, readErr := os.ReadFile(receipt.ReceiptPath); readErr != nil || !bytes.Equal(current, receiptBefore) {
			t.Fatalf("terminalized current-source claim changed failed receipt: err=%v", readErr)
		}
		if current, readErr := os.ReadFile(originalClaim.ClaimPath); readErr != nil || !bytes.Equal(current, originalClaimBefore) {
			t.Fatalf("terminalized current-source claim changed failed claim: err=%v", readErr)
		}
		if current, readErr := os.ReadFile(supersession.AcknowledgementPath); readErr != nil || !bytes.Equal(current, supersessionBefore) {
			t.Fatalf("terminalized current-source claim changed self-supersession acknowledgement: err=%v", readErr)
		}
		assertNoPublishedForwardRepairCandidate(t, fixture, receipt, options)
	})
}
