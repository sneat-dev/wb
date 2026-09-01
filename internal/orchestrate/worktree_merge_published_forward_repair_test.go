package orchestrate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// A historical source hash is audit evidence for the failed candidate, not an
// arbitrary merge root.  Both Sources and SourceRefreshes enter the shared
// helper, so exercise each list independently with a real, conflict-free
// commit that the failed candidate never contained.
func TestRequireImmutableHistoricalWorktreeMergeSourcesRefusesSideCommitOutsideFailedCandidate(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*WorktreeMergeReceipt, WorktreeMergeSource)
	}{
		{
			name: "sources",
			mutate: func(receipt *WorktreeMergeReceipt, side WorktreeMergeSource) {
				receipt.Sources[0] = side
			},
		},
		{
			name: "source refreshes",
			mutate: func(receipt *WorktreeMergeReceipt, side WorktreeMergeSource) {
				receipt.SourceRefreshes = []WorktreeMergeSourceRefresh{{RecordedAt: time.Now().UTC(), Sources: []WorktreeMergeSource{side}}}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, receipt, _, _, _ := selfSupersessionFixture(t)
			tree := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", receipt.TargetSHA+"^{tree}"))
			sideSHA := strings.TrimSpace(runEngineGit(t, fixture.canonical, "commit-tree", tree, "-p", receipt.TargetSHA, "-m", "test: historical side source"))
			if contains, err := isMergeAncestor(context.Background(), receipt.Candidate.Worktree, sideSHA, receipt.Candidate.SHA); err != nil || contains {
				t.Fatalf("side source unexpectedly belongs to failed candidate: contains=%t err=%v", contains, err)
			}
			side := receipt.Sources[0]
			side.SHA = sideSHA
			test.mutate(&receipt, side)

			if err := requireImmutableHistoricalWorktreeMergeSources(context.Background(), receipt.Candidate.Worktree, receipt); err == nil || !strings.Contains(err.Error(), "is not an ancestor of failed candidate") {
				t.Fatalf("historical side-source validation error = %v", err)
			}
		})
	}
}

type historicalRefreshSideState struct {
	fixture      engineFixture
	receipt      WorktreeMergeReceipt
	replacement  worktrees.CreateResult
	supersession WorktreeMergeValidationFailureSupersession
	claimHash    string
	options      WorktreeMergePublishedForwardRepairOptions
}

type historicalSideVariant struct {
	name string
}

var historicalSideVariants = []historicalSideVariant{{name: "sources"}, {name: "source refreshes"}}

// persistHashConsistentHistoricalSideCommit keeps every receipt, claim, and
// self-ack identity internally consistent while placing a resolvable side
// commit in one historical source list. The side commit is intentionally not
// an ancestor of the failed candidate.
func persistHashConsistentHistoricalSideCommit(t *testing.T, fixture engineFixture, receipt *WorktreeMergeReceipt, supersession *WorktreeMergeValidationFailureSupersession, variant historicalSideVariant) (claimHash, supersessionHash string) {
	t.Helper()
	tree := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", receipt.TargetSHA+"^{tree}"))
	sideSHA := strings.TrimSpace(runEngineGit(t, fixture.canonical, "commit-tree", tree, "-p", receipt.TargetSHA, "-m", "test: hash-consistent historical side source"))
	if contains, err := isMergeAncestor(context.Background(), receipt.Candidate.Worktree, sideSHA, receipt.Candidate.SHA); err != nil || contains {
		t.Fatalf("side source unexpectedly belongs to failed candidate: contains=%t err=%v", contains, err)
	}
	side := receipt.Sources[0]
	side.SHA = sideSHA
	switch variant.name {
	case "sources":
		receipt.Sources[0] = side
		oldTask := receipt.Candidate.Task
		receipt.ID = worktreeMergeOperationID(receipt.Lane, receipt.Sources)
		receipt.Candidate.Task = receipt.ID
		view, err := validateMergeAcknowledgementCandidate(context.Background(), fixture.githubDir, *receipt, WorktreeMergeCandidate{Task: oldTask, Worktree: receipt.Candidate.Worktree, Branch: receipt.Candidate.Branch, SHA: receipt.Candidate.SHA})
		if err != nil {
			t.Fatalf("load original candidate claim before sources side mutation: %v", err)
		}
		contents, err := os.ReadFile(view.ClaimPath)
		if err != nil {
			t.Fatal(err)
		}
		updated := strings.Replace(string(contents), `"task": "`+oldTask+`"`, `"task": "`+receipt.Candidate.Task+`"`, 1)
		if updated == string(contents) {
			t.Fatalf("candidate claim lacks task %q", oldTask)
		}
		if err := os.WriteFile(view.ClaimPath, []byte(updated), 0o600); err != nil {
			t.Fatal(err)
		}
	case "source refreshes":
		receipt.SourceRefreshes = []WorktreeMergeSourceRefresh{{RecordedAt: time.Now().UTC(), Sources: []WorktreeMergeSource{side}}}
	default:
		t.Fatalf("unknown historical side variant %q", variant.name)
	}
	if err := persistWorktreeMergeReceipt(*receipt); err != nil {
		t.Fatal(err)
	}
	claim, err := validateMergeAcknowledgementCandidate(context.Background(), fixture.githubDir, *receipt, receipt.Candidate)
	if err != nil {
		t.Fatalf("validate hash-consistent failed candidate: %v", err)
	}
	claimBytes, err := os.ReadFile(claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	claimDigest := sha256.Sum256(claimBytes)
	claimHash = hex.EncodeToString(claimDigest[:])
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	supersession.ReceiptID, supersession.ReceiptSHA256 = receipt.ID, receiptHash
	supersession.OriginalCandidate, supersession.Replacement = receipt.Candidate, receipt.Candidate
	supersession.Sources = append([]WorktreeMergeSource(nil), receipt.Sources...)
	supersession.ID = validationFailureSupersessionID(*supersession)
	if err := persistValidationFailureSupersession(supersession.AcknowledgementPath, *supersession); err != nil {
		t.Fatal(err)
	}
	supersessionHash, err = worktreeMergeReceiptSHA256(supersession.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	return claimHash, supersessionHash
}

// historicalSideFixture makes a hash-consistent receipt, claim, and
// self-supersession with a resolvable historical side commit outside the
// failed candidate's DAG.
func historicalSideFixture(t *testing.T, variant historicalSideVariant) historicalRefreshSideState {
	t.Helper()
	fixture, receipt, replacement, supersession, _ := selfSupersessionFixture(t)
	currentSourceSHA := receipt.Sources[0].SHA
	claimHash, supersessionHash := persistHashConsistentHistoricalSideCommit(t, fixture, &receipt, &supersession, variant)
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	currentTarget, err := fetchExactMergeTarget(context.Background(), receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		t.Fatal(err)
	}
	extra := createMergeSource(t, fixture, "historical-refresh-side-extra", "feature/historical-refresh-side-extra", "repair.txt", "repair\n")
	extraSHA := strings.TrimSpace(runEngineGit(t, extra.WorktreeDir, "rev-parse", "HEAD"))
	return historicalRefreshSideState{
		fixture: fixture, receipt: receipt, replacement: replacement, supersession: supersession, claimHash: claimHash,
		options: WorktreeMergePublishedForwardRepairOptions{
			ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Sources: []string{receipt.Sources[0].Worktree, extra.WorktreeDir},
			ExpectedReceiptSHA256: receiptHash, ExpectedImmutableClaimSHA256: claimHash, ExpectedSupersessionSHA256: supersessionHash,
			ExpectedCurrentTargetSHA: currentTarget, ExpectedSourceSHAs: []string{currentSourceSHA, extraSHA},
			Actor: "reviewer", Reason: "must refuse historical side source outside failed candidate DAG",
		},
	}
}

func snapshotHistoricalSideArtifacts(t *testing.T, fixture engineFixture, receipt WorktreeMergeReceipt, supersession WorktreeMergeValidationFailureSupersession) (receiptBytes, claimBytes, supersessionBytes []byte) {
	t.Helper()
	var err error
	if receiptBytes, err = os.ReadFile(receipt.ReceiptPath); err != nil {
		t.Fatal(err)
	}
	claim, err := validateMergeAcknowledgementCandidate(context.Background(), fixture.githubDir, receipt, receipt.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	if claimBytes, err = os.ReadFile(claim.ClaimPath); err != nil {
		t.Fatal(err)
	}
	if supersessionBytes, err = os.ReadFile(supersession.AcknowledgementPath); err != nil {
		t.Fatal(err)
	}
	return receiptBytes, claimBytes, supersessionBytes
}

func assertHistoricalSideArtifacts(t *testing.T, fixture engineFixture, receipt WorktreeMergeReceipt, supersession WorktreeMergeValidationFailureSupersession, receiptBytes, claimBytes, supersessionBytes []byte) {
	t.Helper()
	currentReceipt, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil || !bytes.Equal(currentReceipt, receiptBytes) {
		t.Fatalf("failed receipt changed: err=%v", err)
	}
	claim, err := validateMergeAcknowledgementCandidate(context.Background(), fixture.githubDir, receipt, receipt.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	currentClaim, err := os.ReadFile(claim.ClaimPath)
	if err != nil || !bytes.Equal(currentClaim, claimBytes) {
		t.Fatalf("failed candidate claim changed: err=%v", err)
	}
	currentSupersession, err := os.ReadFile(supersession.AcknowledgementPath)
	if err != nil || !bytes.Equal(currentSupersession, supersessionBytes) {
		t.Fatalf("self-supersession acknowledgement changed: err=%v", err)
	}
}

func TestHistoricalSideCommitRefusesForwardRepairCorrectionAndEffectiveReader(t *testing.T) {
	for _, variant := range historicalSideVariants {
		variant := variant
		t.Run(variant.name, func(t *testing.T) {
			t.Run("forward repair dry-run and apply", func(t *testing.T) {
				for _, apply := range []bool{false, true} {
					t.Run(fmt.Sprintf("apply=%t", apply), func(t *testing.T) {
						state := historicalSideFixture(t, variant)
						receiptBytes, claimBytes, supersessionBytes := snapshotHistoricalSideArtifacts(t, state.fixture, state.receipt, state.supersession)
						options := state.options
						options.Apply = apply
						result, err := PreparePublishedValidationFailureForwardRepair(context.Background(), options)
						if err == nil || result.Candidate.Worktree != "" || !strings.Contains(err.Error(), "is not an ancestor of failed candidate") {
							t.Fatalf("historical refresh side forward repair = %+v err=%v", result, err)
						}
						assertHistoricalSideArtifacts(t, state.fixture, state.receipt, state.supersession, receiptBytes, claimBytes, supersessionBytes)
						assertNoPublishedForwardRepairCandidate(t, state.fixture, state.receipt, options)
					})
				}
			})

			t.Run("correction", func(t *testing.T) {
				state := historicalSideFixture(t, variant)
				receiptBytes, claimBytes, supersessionBytes := snapshotHistoricalSideArtifacts(t, state.fixture, state.receipt, state.supersession)
				_, err := CorrectValidationFailedSelfSupersession(context.Background(), WorktreeMergeSelfSupersessionCorrectionOptions{
					ProjectsRoot: state.fixture.githubDir, Receipt: state.receipt.ReceiptPath, ReplacementWorktree: state.replacement.WorktreeDir,
					ExpectedSupersessionSHA256: state.options.ExpectedSupersessionSHA256, ExpectedImmutableClaimSHA256: state.claimHash,
					Apply: true, Actor: "reviewer", Reason: "must reject a hash-consistent historical side source",
				})
				if err == nil || !strings.Contains(err.Error(), "is not an ancestor of failed candidate") {
					t.Fatalf("historical refresh side correction error = %v", err)
				}
				assertHistoricalSideArtifacts(t, state.fixture, state.receipt, state.supersession, receiptBytes, claimBytes, supersessionBytes)
				if _, statErr := os.Stat(selfSupersessionCorrectionPath(state.receipt.ReceiptPath)); !os.IsNotExist(statErr) {
					t.Fatalf("historical refresh side correction wrote artifact: %v", statErr)
				}
			})

			t.Run("effective reader", func(t *testing.T) {
				fixture, receipt, replacement, supersession, claimHash := selfSupersessionFixture(t)
				supersessionHash, err := worktreeMergeReceiptSHA256(supersession.AcknowledgementPath)
				if err != nil {
					t.Fatal(err)
				}
				correction, err := CorrectValidationFailedSelfSupersession(context.Background(), WorktreeMergeSelfSupersessionCorrectionOptions{
					ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir,
					ExpectedSupersessionSHA256: supersessionHash, ExpectedImmutableClaimSHA256: claimHash,
					Apply: true, Actor: "reviewer", Reason: "establish reader fixture before side-source tamper",
				})
				if err != nil {
					t.Fatal(err)
				}
				mutatedClaimHash, updatedSupersessionHash := persistHashConsistentHistoricalSideCommit(t, fixture, &receipt, &supersession, variant)
				receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
				if err != nil {
					t.Fatal(err)
				}
				correction.ReceiptSHA256, correction.ImmutableClaimSHA256 = receiptHash, mutatedClaimHash
				correction.SupersessionSHA256, correction.SupersessionID = updatedSupersessionHash, supersession.ID
				correction.OriginalCandidate, correction.Sources = receipt.Candidate, append([]WorktreeMergeSource(nil), receipt.Sources...)
				correction.ID = selfSupersessionCorrectionID(correction)
				correctionBytes, err := json.MarshalIndent(correction, "", "  ")
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(correction.CorrectionPath, append(correctionBytes, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
				receiptBytes, claimBytes, supersessionBytes := snapshotHistoricalSideArtifacts(t, fixture, receipt, supersession)
				correctionBefore, err := os.ReadFile(correction.CorrectionPath)
				if err != nil {
					t.Fatal(err)
				}
				if superseded, err := hasValidationFailureSupersession(context.Background(), fixture.githubDir, receipt); err == nil || superseded || !strings.Contains(err.Error(), "is not an ancestor of failed candidate") {
					t.Fatalf("historical refresh side effective reader = superseded=%t err=%v", superseded, err)
				}
				assertHistoricalSideArtifacts(t, fixture, receipt, supersession, receiptBytes, claimBytes, supersessionBytes)
				if current, readErr := os.ReadFile(correction.CorrectionPath); readErr != nil || !bytes.Equal(current, correctionBefore) {
					t.Fatalf("effective reader changed correction: err=%v", readErr)
				}
			})
		})
	}
}

func TestPreparePublishedForwardRepairRetainsImmutableHistoricalSourcesWhileCurrentSourcesAdvance(t *testing.T) {
	fixture, receipt, _, supersession, claimHash := selfSupersessionFixture(t)
	if len(receipt.SourceRefreshes) != 0 {
		t.Fatalf("fixture source refreshes = %+v, want none", receipt.SourceRefreshes)
	}
	historicalSource := receipt.Sources[0]
	writeEngineFile(t, filepath.Join(historicalSource.Worktree, "advanced-current-source.txt"), "advanced current source\n")
	runEngineGit(t, historicalSource.Worktree, "add", "advanced-current-source.txt")
	runEngineGit(t, historicalSource.Worktree, "commit", "-m", "test: advance current source beyond immutable receipt")
	advancedHistoricalSource := strings.TrimSpace(runEngineGit(t, historicalSource.Worktree, "rev-parse", "HEAD"))
	contains, err := isMergeAncestor(context.Background(), historicalSource.Worktree, historicalSource.SHA, advancedHistoricalSource)
	if err != nil || !contains {
		t.Fatalf("advanced current source does not retain immutable source: contains=%t err=%v", contains, err)
	}
	extra := createMergeSource(t, fixture, "published-forward-current-extra", "feature/published-forward-current-extra", "repair.txt", "repair\n")
	extraSHA := strings.TrimSpace(runEngineGit(t, extra.WorktreeDir, "rev-parse", "HEAD"))
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
	options := WorktreeMergePublishedForwardRepairOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath,
		Sources:               []string{historicalSource.Worktree, extra.WorktreeDir},
		ExpectedReceiptSHA256: receiptHash, ExpectedImmutableClaimSHA256: claimHash,
		ExpectedSupersessionSHA256: supersessionHash, ExpectedCurrentTargetSHA: currentTarget,
		ExpectedSourceSHAs: []string{advancedHistoricalSource, extraSHA},
		Actor:              "reviewer", Reason: "retain immutable source roots while using current managed sources",
	}

	dryRun, err := PreparePublishedValidationFailureForwardRepair(context.Background(), options)
	if err != nil || dryRun.Status != "published_forward_repair_planned" {
		t.Fatalf("advanced-current-source forward repair dry-run = %+v err=%v", dryRun, err)
	}
	assertNoPublishedForwardRepairCandidate(t, fixture, receipt, options)
	for _, path := range []string{receipt.ReceiptPath, originalClaim.ClaimPath, supersession.AcknowledgementPath} {
		want := map[string][]byte{receipt.ReceiptPath: receiptBefore, originalClaim.ClaimPath: claimBefore, supersession.AcknowledgementPath: supersessionBefore}[path]
		if current, readErr := os.ReadFile(path); readErr != nil || !bytes.Equal(current, want) {
			t.Fatalf("dry-run changed historical artifact %s: err=%v", path, readErr)
		}
	}

	options.Apply = true
	repair, err := PreparePublishedValidationFailureForwardRepair(context.Background(), options)
	if err != nil {
		t.Fatalf("advanced-current-source forward repair apply: %v", err)
	}
	if repair.Candidate.SHA == "" || repair.Candidate.SHA == receipt.Candidate.SHA {
		t.Fatalf("advanced-current-source repair candidate = %+v", repair.Candidate)
	}
	for _, root := range []string{originalClaim.BaseSHA, receipt.TargetSHA, currentTarget, historicalSource.SHA, advancedHistoricalSource, extraSHA} {
		contains, ancestorErr := isMergeAncestor(context.Background(), repair.Candidate.Worktree, root, repair.Candidate.SHA)
		if ancestorErr != nil || !contains {
			t.Fatalf("repair candidate lacks root %s: contains=%t err=%v", root, contains, ancestorErr)
		}
	}
	for _, path := range []string{receipt.ReceiptPath, originalClaim.ClaimPath, supersession.AcknowledgementPath} {
		want := map[string][]byte{receipt.ReceiptPath: receiptBefore, originalClaim.ClaimPath: claimBefore, supersession.AcknowledgementPath: supersessionBefore}[path]
		if current, readErr := os.ReadFile(path); readErr != nil || !bytes.Equal(current, want) {
			t.Fatalf("apply changed historical artifact %s: err=%v", path, readErr)
		}
	}
	if _, err := CorrectValidationFailedSelfSupersession(context.Background(), WorktreeMergeSelfSupersessionCorrectionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: repair.Candidate.Worktree,
		ExpectedSupersessionSHA256: supersessionHash, ExpectedImmutableClaimSHA256: claimHash,
		Apply: true, Actor: "reviewer", Reason: "consume retained historical-source repair candidate",
	}); err != nil {
		t.Fatalf("correct self-supersession with advanced-current-source repair candidate: %v", err)
	}
	if superseded, err := hasValidationFailureSupersession(context.Background(), fixture.githubDir, receipt); err != nil || !superseded {
		t.Fatalf("advanced-current-source corrected supersession = superseded=%t err=%v", superseded, err)
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

	t.Run("omitted historical worktree remains an immutable root", func(t *testing.T) {
		fixture, receipt, _, options := publishedForwardRepairFixture(t)
		options.Sources = options.Sources[1:]
		options.ExpectedSourceSHAs = options.ExpectedSourceSHAs[1:]
		options.Apply = false
		planned, err := PreparePublishedValidationFailureForwardRepair(context.Background(), options)
		if err != nil || planned.Status != "published_forward_repair_planned" {
			t.Fatalf("omitted historical worktree plan = %+v err=%v", planned, err)
		}
		foundHistoricalRoot := false
		for _, root := range planned.RequiredRoots {
			if root.Kind == "receipted_source:"+receipt.Sources[0].Task && root.SHA == receipt.Sources[0].SHA {
				foundHistoricalRoot = true
				break
			}
		}
		if !foundHistoricalRoot {
			t.Fatalf("omitted historical source was absent from immutable roots: %+v", planned.RequiredRoots)
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
