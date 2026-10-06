//go:build e2e

package orchestrate

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestE2ECleanupAssetsCheckpointFailureRecoversNativeTerminalEvidence(t *testing.T) {
	// This fixture uses Setenv and hosted protocol scripts; keep it serial.
	fixture, _, receipt, claims := landedTerminalCleanupFixture(t)
	receipt.Cleanup = true
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	durable, err := os.ReadFile(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	expectations, err := terminalWorkLogExpectations(receipt)
	if err != nil {
		t.Fatal(err)
	}
	tasks := sortedUniqueMergeTasks(receipt)
	if len(tasks) != 2 || len(receipt.CleanedTasks) != 0 || len(receipt.CleanupReports) != 0 {
		t.Fatalf("fixture does not have two pending native assets: %+v", receipt)
	}
	first := tasks[0]
	var retired, remaining worktrees.TerminalWorkLogExpectation
	for _, asset := range expectations {
		if asset.Task == first {
			retired = asset
		} else {
			remaining = asset
		}
		if got, err := mergeRevision(t.Context(), defaultRunner, asset.Worktree, "HEAD"); err != nil || got != asset.FinalCommit {
			t.Fatalf("native pre-cleanup task %s HEAD=%s want=%s: %v", asset.Task, got, asset.FinalCommit, err)
		}
	}
	claimBytes := make(map[string][]byte, len(claims))
	for task, path := range claims {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		claimBytes[task] = data
	}
	// Non-rebatch cleanup passes native identity proofs, not ReceiptPath, into
	// Cleanup. A real directory at the merge checkpoint destination therefore
	// leaves native custody/retirement intact and makes its later rename fail.
	saved := receipt.ReceiptPath + ".checkpoint-preserved"
	if err := os.Rename(receipt.ReceiptPath, saved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := os.Stat(saved); err == nil {
			_ = os.Remove(receipt.ReceiptPath)
			_ = os.Rename(saved, receipt.ReceiptPath)
		}
	})
	if err := os.Mkdir(receipt.ReceiptPath, 0700); err != nil {
		t.Fatal(err)
	}
	err = cleanupWorktreeMergeAssets(t.Context(), fixture.githubDir, &receipt)
	var renameErr *os.LinkError
	if !errors.As(err, &renameErr) || renameErr.Op != "rename" || renameErr.New != receipt.ReceiptPath || filepath.Dir(renameErr.Old) != filepath.Dir(receipt.ReceiptPath) || !strings.HasPrefix(filepath.Base(renameErr.Old), ".merge-receipt-") || (!errors.Is(err, syscall.EISDIR) && !errors.Is(err, os.ErrExist)) {
		t.Fatalf("expected actual post-retirement checkpoint rename failure: %v", err)
	}
	if _, err := os.Stat(renameErr.Old); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed checkpoint left temporary receipt %s: %v", renameErr.Old, err)
	}
	if !reflect.DeepEqual(receipt.CleanedTasks, []string{first}) || len(receipt.CleanupReports) != 1 {
		t.Fatalf("failed checkpoint lost native in-memory task/report progress: %+v", receipt)
	}
	if got, err := os.ReadFile(saved); err != nil || !bytes.Equal(got, durable) {
		t.Fatalf("checkpoint failure changed preserved durable receipt: %v", err)
	}
	if _, err := os.Stat(retired.Worktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkpoint failed before real task retirement: %s: %v", retired.Worktree, err)
	}
	if got, err := mergeRevision(t.Context(), defaultRunner, remaining.Worktree, "HEAD"); err != nil || got != remaining.FinalCommit {
		t.Fatalf("checkpoint refusal changed remaining task: HEAD=%s want=%s: %v", got, remaining.FinalCommit, err)
	}
	firstAssets := []worktrees.TerminalWorkLogExpectation{retired}
	if err := worktrees.ValidateRemovedTerminalWorkLogs(fixture.githubDir, firstAssets); err != nil {
		t.Fatalf("first native task lacks exact removed terminal evidence: %v", err)
	}
	if err := requireTerminalCleanupBranchesAbsent(t.Context(), fixture.githubDir, receipt, firstAssets, 0, 0); err != nil {
		t.Fatalf("first native task still owns a local/origin ref: %v", err)
	}
	if err := worktrees.ValidateTerminalCleanupReports(receipt.CleanupReports, receipt.Repository, []string{first}); err != nil {
		t.Fatalf("first native cleanup report was not actually applied: %v", err)
	}
	firstClaims := map[string]string{first: claims[first]}
	firstTerminalBytes := terminalWorkLogBytes(t, firstClaims)
	lostReport := receipt.CleanupReports[0]
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "refs/remotes/origin/main")); got != receipt.LandingSHA {
		t.Fatalf("cleanup changed exact fetched landing: got=%s want=%s", got, receipt.LandingSHA)
	}
	if err := os.Remove(receipt.ReceiptPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(saved, receipt.ReceiptPath); err != nil {
		t.Fatal(err)
	}
	retry, err := readWorktreeMergeReceipt(receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(retry.CleanedTasks) != 0 || len(retry.CleanupReports) != 0 {
		t.Fatalf("restored durable receipt invented failed checkpoint: %+v", retry)
	}
	complete, err := recoverAlreadyTerminalizedWorktreeMergeCleanup(t.Context(), fixture.githubDir, &retry, 0, 0)
	if err != nil || complete || !reflect.DeepEqual(retry.CleanedTasks, []string{first}) || len(retry.CleanupReports) != 0 {
		t.Fatalf("partial native recovery invented completion/report: complete=%v receipt=%+v err=%v", complete, retry, err)
	}
	recovered, err := readWorktreeMergeReceipt(retry.ReceiptPath)
	if err != nil || !reflect.DeepEqual(recovered.CleanedTasks, []string{first}) || len(recovered.CleanupReports) != 0 {
		t.Fatalf("partial native recovery was not durably checkpointed: %+v: %v", recovered, err)
	}
	if err := cleanupWorktreeMergeAssets(t.Context(), fixture.githubDir, &retry); err != nil {
		t.Fatalf("remaining native cleanup retry: %v", err)
	}
	stored, err := readWorktreeMergeReceipt(retry.ReceiptPath)
	if err != nil || !reflect.DeepEqual(stored.CleanedTasks, tasks) || len(stored.CleanupReports) != 1 {
		t.Fatalf("retry failed exact task checkpoints or invented lost report: %+v: %v", stored, err)
	}
	for _, asset := range expectations {
		if _, err := os.Stat(asset.Worktree); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("retry left native task %s: %v", asset.Task, err)
		}
	}
	if err := worktrees.ValidateRemovedTerminalWorkLogs(fixture.githubDir, expectations); err != nil {
		t.Fatalf("retry lacks exact native terminal evidence: %v", err)
	}
	if err := requireTerminalCleanupBranchesAbsent(t.Context(), fixture.githubDir, retry, expectations, 0, 0); err != nil {
		t.Fatal(err)
	}
	// Inspect the real reports together without adding the lost report to the
	// restored receipt: terminal evidence, not a manufactured report, recovered it.
	reports := append([]string{lostReport}, stored.CleanupReports...)
	if err := worktrees.ValidateTerminalCleanupReports(reports, retry.Repository, tasks); err != nil {
		t.Fatalf("actual retirement reports do not corroborate both tasks: %v", err)
	}
	for task, path := range claims {
		if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, claimBytes[task]) {
			t.Fatalf("cleanup/recovery changed immutable claim %s: %v", task, err)
		}
	}
	assertTerminalWorkLogBytes(t, firstClaims, firstTerminalBytes)
}
