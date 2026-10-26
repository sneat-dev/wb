//go:build e2e

package orchestrate

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
)

//nolint:paralleltest // engine fixture installs process-wide hosted metadata scripts.
func TestE2ESharedEffortHelpersResumeAfterPartialTerminalCleanup(t *testing.T) {
	fixture := newEngineFixture(t)
	first := createMergeSourceOnBase(t, fixture, "alias-helper-a", "feature/alias-a", "main", "a.txt", "a\n", "shared-parent")
	second := createMergeSourceOnBase(t, fixture, "alias-helper-b", "feature/alias-b", "main", "b.txt", "b\n", "shared-parent")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{first.WorktreeDir, second.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", receipt.Candidate.SHA)
	receipt, err = LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, Route: WorktreeMergeRouteAuto, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond})
	if err != nil || receipt.Status != WorktreeMergeLanded {
		t.Fatalf("land: %+v %v", receipt, err)
	}
	claims := map[string]string{}
	before := map[string][]byte{}
	for _, helper := range []worktrees.CreateResult{first, second} {
		view, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: helper.WorktreeDir})
		if err != nil || view.Claim == nil || view.Claim.EffortID != "shared-parent" {
			t.Fatalf("helper claim: %+v %v", view, err)
		}
		claims[helper.WorktreeDir] = view.Claim.ClaimPath
		before[helper.WorktreeDir], err = os.ReadFile(view.Claim.ClaimPath)
		if err != nil {
			t.Fatal(err)
		}
	}
	// The former store is no longer the configured root. Recovery must discover
	// the still-registered helpers from their exact immutable claims.
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	writeEngineFile(t, filepath.Join(configHome, "wb", "worktrees.yaml"), fmt.Sprintf("version: 1\nworktrees:\n  root: %q\n", filepath.Join(t.TempDir(), "new-store")))
	// Actual named cleanup must find this helper without retiring its sibling.
	externallyTerminalizeTask(t, fixture, &receipt, "alias-helper-a")
	if _, err := os.Stat(second.WorktreeDir); err != nil {
		t.Fatalf("sibling changed: %v", err)
	}
	receipt.Cleanup = true
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	complete, err := recoverAlreadyTerminalizedWorktreeMergeCleanup(t.Context(), fixture.githubDir, &receipt, 0, 0)
	if err != nil || complete || !reflect.DeepEqual(receipt.CleanedTasks, []string{"alias-helper-a"}) {
		t.Fatalf("partial recovery: complete=%v receipt=%+v err=%v", complete, receipt, err)
	}
	if err := cleanupWorktreeMergeAssets(t.Context(), fixture.githubDir, &receipt); err != nil {
		t.Fatal(err)
	}
	expectations, err := terminalWorkLogExpectations(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := worktrees.ValidateRemovedTerminalWorkLogs(fixture.githubDir, expectations); err != nil {
		t.Fatal(err)
	}
	if err := requireTerminalCleanupBranchesAbsent(t.Context(), fixture.githubDir, receipt, expectations, 0, 0); err != nil {
		t.Fatal(err)
	}
	for worktree, claimPath := range claims {
		if _, err := os.Stat(worktree); !os.IsNotExist(err) {
			t.Fatalf("helper remains: %s: %v", worktree, err)
		}
		after, err := os.ReadFile(claimPath)
		if err != nil || !bytes.Equal(before[worktree], after) {
			t.Fatalf("immutable helper claim changed: %v", err)
		}
	}
}
