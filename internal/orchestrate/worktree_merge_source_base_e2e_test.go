//go:build e2e

package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestE2ESupersedeValidationFailedWorktreeMergePreservesSourceOwnBase(t *testing.T) {
	t.Parallel()
	fixture := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, fixture, "main-based-source", "feature/main-based-source", "source.txt", "source\n")
	sourceView, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: source.WorktreeDir})
	if err != nil || sourceView.Claim == nil {
		t.Fatalf("load source claim: %+v err=%v", sourceView, err)
	}
	if sourceView.Claim.Base != "main" {
		t.Fatalf("source claim base = %q, want main", sourceView.Claim.Base)
	}
	runEngineGit(t, fixture.canonical, "switch", "-c", "coverage-refactor")
	writeEngineFile(t, filepath.Join(fixture.canonical, "target.txt"), "target\n")
	runEngineGit(t, fixture.canonical, "add", "target.txt")
	runEngineGit(t, fixture.canonical, "commit", "-m", "test: advance coverage target")
	runEngineGit(t, fixture.canonical, "push", "origin", "coverage-refactor")

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "coverage-refactor", Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt.Status = WorktreeMergeValidationFailed
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	replacement := createMergeSourceOnBase(t, fixture, "replacement-on-coverage", "feature/replacement-on-coverage", "coverage-refactor", "replacement.txt", "replacement\n")
	runEngineGit(t, replacement.WorktreeDir, "merge", "--no-edit", receipt.Sources[0].SHA)

	ack, err := SupersedeValidationFailedWorktreeMerge(context.Background(), WorktreeMergeValidationFailureSupersessionOptions{
		ProjectsRoot: fixture.githubDir, Receipt: receipt.ReceiptPath, ReplacementWorktree: replacement.WorktreeDir,
	})
	if err != nil {
		t.Fatalf("supersede source based on main into coverage-refactor: %v", err)
	}
	if ack.Target != "coverage-refactor" || ack.Sources[0].SHA != receipt.Sources[0].SHA {
		t.Fatalf("supersession lost target or immutable source identity: %+v", ack)
	}
	for _, root := range []string{sourceView.Claim.BaseSHA, receipt.Sources[0].SHA, receipt.TargetSHA, ack.CurrentTargetSHA} {
		if contains, ancestorErr := isMergeAncestor(context.Background(), replacement.WorktreeDir, root, ack.Replacement.SHA); ancestorErr != nil || !contains {
			t.Fatalf("replacement root %s retained=%t err=%v", root, contains, ancestorErr)
		}
	}
	if _, statErr := os.Stat(validationFailureSupersessionPath(receipt.ReceiptPath)); !os.IsNotExist(statErr) {
		t.Fatalf("dry-run wrote supersession: %v", statErr)
	}

	// A claim for the same source identity cannot authorize a receipted commit
	// that is outside its own immutable base, even when its branch still matches.
	unrelated := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "commit-tree", "HEAD^{tree}", "-m", "test: unrelated root"))
	unrooted := receipt.Sources[0]
	unrooted.SHA = unrelated
	if _, _, err := validateValidationFailedSupersessionSourceWithRunner(context.Background(), defaultRunner, fixture.githubDir, receipt, unrooted); err == nil || !strings.Contains(err.Error(), "does not descend from immutable claim base") {
		t.Fatalf("unrooted source claim ancestry error = %v", err)
	}
}
