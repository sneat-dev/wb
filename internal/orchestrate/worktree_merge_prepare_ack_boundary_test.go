package orchestrate

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// This is an actual native claim/history acknowledgement. The historical
// failure text is an operator record, not simulated validation execution.
func prepareOwnerCollisionBoundary(t *testing.T, exactPath bool) prepareOwnerFixture {
	t.Helper()
	f := newPrepareOwnerFixture(t)
	f.createInterruptedCandidate(t)
	historical := append([]WorktreeMergeSource(nil), f.sources...)
	writeEngineFile(t, filepath.Join(f.source.WorktreeDir, "collision-advance.txt"), "native collision source advance\n")
	runEngineGit(t, f.source.WorktreeDir, "add", "collision-advance.txt")
	runEngineGit(t, f.source.WorktreeDir, "commit", "-m", "native collision source advance")
	head := strings.TrimSpace(runEngineGit(t, f.source.WorktreeDir, "rev-parse", "HEAD"))
	runEngineGit(t, f.receipt.Candidate.Worktree, "merge", "--no-edit", head)
	f.receipt.Sources[0].SHA = head
	f.receipt.SourceRefreshes = []WorktreeMergeSourceRefresh{{RecordedAt: time.Now().UTC(), Sources: historical}}
	f.receipt.Candidate.SHA = strings.TrimSpace(runEngineGit(t, f.receipt.Candidate.Worktree, "rev-parse", "HEAD"))
	f.receipt.Failure = "historical operator-reported collision"
	if exactPath {
		old := f.receipt.ReceiptPath
		operation := worktreeMergeOperationID(f.receipt.Lane, f.receipt.Sources)
		f.receipt.ReceiptPath = filepath.Join(filepath.Dir(old), operation+".json")
		if err := os.Remove(old); err != nil {
			t.Fatal(err)
		}
	}
	if err := persistWorktreeMergeReceipt(f.receipt); err != nil {
		t.Fatal(err)
	}
	claim, err := validateMergeAcknowledgementCandidate(t.Context(), f.engine.githubDir, f.receipt, f.receipt.Candidate)
	if err != nil {
		t.Fatal(err)
	}
	claimHash, err := worktreeMergeReceiptSHA256(claim.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	receiptHash, err := worktreeMergeReceiptSHA256(f.receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = AcknowledgeWorktreeMergeReceiptCollision(t.Context(), WorktreeMergeReceiptCollisionAcknowledgementOptions{
		ProjectsRoot: f.engine.githubDir, Receipt: f.receipt.ReceiptPath, Apply: true, Actor: "private-fixture", Reason: "native exact collision custody",
		ExpectedReceiptSHA256: receiptHash, ExpectedImmutableClaimSHA256: claimHash, ExpectedTargetSHA: f.receipt.TargetSHA,
		ExpectedCandidateSHA: f.receipt.Candidate.SHA, ExpectedCurrentSourceSHA: head, ExpectedHistoricalRefreshSourceSHA: historical[0].SHA,
	})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestPrepareOwnerNativeCollisionAcknowledgementStopsExactAndLockedRetry(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"exact receipt", "after native lock"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := prepareOwnerCollisionBoundary(t, mode == "exact receipt")
			ackPath := receiptCollisionAcknowledgementPath(f.receipt.ReceiptPath)
			ackBefore, err := os.ReadFile(ackPath)
			if err != nil {
				t.Fatal(err)
			}
			receiptBefore, err := os.ReadFile(f.receipt.ReceiptPath)
			if err != nil {
				t.Fatal(err)
			}
			var restored atomic.Bool
			options := f.options
			if mode == "after native lock" {
				hidden := filepath.Join(t.TempDir(), "authentic-hidden-ack.json")
				if err := os.Rename(ackPath, hidden); err != nil {
					t.Fatal(err)
				}
				writeEngineFile(t, filepath.Join(f.source.WorktreeDir, "next.txt"), "actual additive source after acknowledgement\n")
				runEngineGit(t, f.source.WorktreeDir, "add", "next.txt")
				runEngineGit(t, f.source.WorktreeDir, "commit", "-m", "native source advance before lock")
				options.Progress = func(event progress.Event) {
					if event.Phase == "acquire_lane" && event.State == progress.Completed && restored.CompareAndSwap(false, true) {
						if err := os.Rename(hidden, ackPath); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			got, err := PrepareWorktreeMerge(t.Context(), options)
			if err == nil || !strings.Contains(err.Error(), "has a receipt-collision acknowledgement and may proceed only as --rebatch-receipt original") || got.ReceiptPath != f.receipt.ReceiptPath || mode == "after native lock" && !restored.Load() {
				t.Fatalf("native %s authenticated collision retry=%+v, %v", mode, got, err)
			}
			for path, before := range map[string][]byte{ackPath: ackBefore, f.receipt.ReceiptPath: receiptBefore} {
				after, readErr := os.ReadFile(path)
				if readErr != nil || string(after) != string(before) {
					t.Fatalf("collision refusal changed authentic evidence %s: %v", path, readErr)
				}
			}
		})
	}
}

func TestPrepareOwnerNativeRebatchedOriginalExactRetryStaysHistorical(t *testing.T) {
	t.Parallel()
	f := newPrepareOwnerFixture(t)
	options := f.options
	options.Route = WorktreeMergeRoutePullRequest
	options.ValidateLocally = true
	original, err := PrepareWorktreeMerge(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	extra := createMergeSource(t, f.engine, "exact-rebatch-extra", "feature/exact-rebatch-extra", "extra.txt", "extra\n")
	replacementOptions := options
	replacementOptions.Sources = []string{f.source.WorktreeDir, extra.WorktreeDir}
	replacementOptions.RebatchReceipt = original.ReceiptPath
	replacement, err := PrepareWorktreeMerge(t.Context(), replacementOptions)
	if err != nil || replacement.RebatchOf != original.ReceiptPath {
		t.Fatalf("native completed rebatch=%+v, %v", replacement, err)
	}
	before, err := os.ReadFile(original.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := PrepareWorktreeMerge(t.Context(), options)
	if err == nil || !strings.Contains(err.Error(), "was rebatched into an audited replacement candidate") || got.ReceiptPath != original.ReceiptPath {
		t.Fatalf("exact original rebatch retry=%+v, %v", got, err)
	}
	after, readErr := os.ReadFile(original.ReceiptPath)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("refusal changed historical receipt: %v", readErr)
	}
}

//nolint:paralleltest // The established private GH executable changes PATH and WB_TEST_PR_* process environment; provider observations are controlled, Git/custody are actual.
func TestPrepareOwnerNativeExactAdoptedRetryReadsAuthenticAcknowledgement(t *testing.T) {
	f := newPrepareOwnerFixture(t)
	// Adoption rechecks source claims through the actual default root lookup.
	t.Setenv(wbhome.EnvOverride, f.engine.githubDir)
	options := f.options
	options.Route = WorktreeMergeRoutePullRequest
	options.ValidateLocally = true
	receipt, err := PrepareWorktreeMerge(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", receipt.Candidate.SHA+":refs/heads/"+receipt.Candidate.Branch)
	receipt.Status = WorktreeMergeConflict
	receipt.Failure = "recorded interrupted publication"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	installPublishedCandidateAdoptionGH(t)
	t.Setenv("WB_TEST_PR_STATE", "open")
	t.Setenv("WB_TEST_PR_BRANCH", receipt.Candidate.Branch)
	t.Setenv("WB_TEST_PR_SHA", receipt.Candidate.SHA)
	t.Setenv("WB_TEST_PR_HEAD_REPO", receipt.Repository)
	t.Setenv("WB_TEST_PR_BASE", receipt.Target)
	t.Setenv("WB_TEST_PR_BASE_REPO", receipt.Repository)
	view, viewErr := worktrees.LoadWorkLogView(t.Context(), worktrees.LoadWorkLogOptions{Worktree: receipt.Sources[0].Worktree})
	if viewErr != nil || view.Claim == nil || view.Claim.Lifecycle != "active" || view.Claim.Repository != receipt.Repository || view.Claim.Task != receipt.Sources[0].Task || view.Claim.Branch != receipt.Sources[0].Branch || filepath.Clean(view.Claim.Worktree) != filepath.Clean(receipt.Sources[0].Worktree) {
		t.Fatalf("actual default-root source claim must satisfy adoption custody: %+v, %v", view.Claim, viewErr)
	}
	ack, err := AdoptPublishedWorktreeMergeCandidate(t.Context(), WorktreeMergePublishedCandidateAdoptionOptions{ProjectsRoot: f.engine.githubDir, Receipt: receipt.ReceiptPath, PullRequest: "7", Apply: true, Actor: "private-fixture", Reason: "exact adopted retry"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(ack.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, adopted, err := adoptedPublishedCandidate(t.Context(), receipt); err != nil || !adopted {
		t.Fatalf("actual authenticated adoption preflight=%v, %v", adopted, err)
	}
	got, err := PrepareWorktreeMerge(t.Context(), f.options)
	if err == nil || !strings.Contains(err.Error(), "unsupported merge route") || got.Candidate.Worktree != receipt.Candidate.Worktree || got.Status != WorktreeMergeConflict {
		t.Fatalf("exact adopted retry did not traverse native candidate reuse: %+v, %v", got, err)
	}
	after, readErr := os.ReadFile(ack.AcknowledgementPath)
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("exact adopted retry changed immutable acknowledgement: %v", readErr)
	}
}

func TestPrepareOwnerNativeAbsorbedAcknowledgementRefusesReappearingExactSource(t *testing.T) {
	t.Parallel()
	f := newPrepareOwnerFixture(t)
	options := f.options
	options.Route = WorktreeMergeRoutePullRequest
	options.ValidateLocally = true
	receipt, err := PrepareWorktreeMerge(t.Context(), options)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeConflict
	receipt.Failure = "recorded prepare conflict"
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, f.engine.canonical, "merge", "--ff-only", receipt.Sources[0].SHA)
	runEngineGit(t, f.engine.canonical, "push", "origin", "main")
	holding := filepath.Join(t.TempDir(), "authentic-source-holding")
	if err := os.Rename(f.source.WorktreeDir, holding); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := os.Stat(holding); err == nil {
			if err := os.Rename(holding, f.source.WorktreeDir); err != nil {
				t.Error(err)
			}
		} else if !os.IsNotExist(err) {
			t.Error(err)
		}
	})
	if _, err := os.Stat(f.source.WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("actual source was not absent at acknowledgement: %v", err)
	}
	ack, err := AcknowledgeAbsorbedConflict(t.Context(), WorktreeMergeAbsorbedConflictAcknowledgementOptions{ProjectsRoot: f.engine.githubDir, Receipt: receipt.ReceiptPath, Apply: true, Actor: "private-fixture", Reason: "native source contained by target while exact path absent"})
	if err != nil || len(ack.SourceProofs) != 1 || ack.SourceProofs[0].Method != "ancestor" {
		t.Fatalf("actual absorbed acknowledgement=%+v, %v", ack, err)
	}
	ackBefore, err := os.ReadFile(ack.AcknowledgementPath)
	if err != nil {
		t.Fatal(err)
	}
	// Restore the same native directory and bytes; no claim is reconstructed,
	// no Git-admin registration is recreated, and every admission is re-proven.
	if err := os.Rename(holding, f.source.WorktreeDir); err != nil {
		t.Fatal(err)
	}
	guard, err := worktrees.Guard(t.Context(), f.source.WorktreeDir, worktrees.GuardOptions{ProjectsRoot: f.engine.githubDir, Base: "main"})
	if err != nil || guard.Kind != "linked" || guard.Branch != f.source.Branch {
		t.Fatalf("restored source lost genuine native registration/custody: %+v, %v", guard, err)
	}
	view, err := worktrees.LoadWorkLogView(t.Context(), worktrees.LoadWorkLogOptions{ProjectsRoot: f.engine.githubDir, Worktree: f.source.WorktreeDir})
	if err != nil || view.Claim == nil || view.Claim.Task != receipt.Sources[0].Task || view.Claim.Branch != receipt.Sources[0].Branch {
		t.Fatalf("restored authentic Work Log identity=%+v, %v", view.Claim, err)
	}
	got, err := PrepareWorktreeMerge(t.Context(), options)
	if err == nil || !strings.Contains(err.Error(), "was acknowledged as a proved absorbed conflict") || got.ReceiptPath != receipt.ReceiptPath {
		t.Fatalf("reappearing exact source bypassed historical acknowledgement: %+v, %v", got, err)
	}
	for path, want := range map[string][]byte{receipt.ReceiptPath: before, ack.AcknowledgementPath: ackBefore} {
		after, readErr := os.ReadFile(path)
		if readErr != nil || string(after) != string(want) {
			t.Fatalf("historical absorbed refusal changed evidence %s: %v", path, readErr)
		}
	}
}
