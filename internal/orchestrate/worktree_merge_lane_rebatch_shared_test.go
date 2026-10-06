package orchestrate

import (
	"context"
	"github.com/sneat-dev/wb/internal/worktrees"
	"path/filepath"
	"testing"
)

// mergeLaneRebatchJourney retains the native default journey and all original
// assertions. Only the E2E caller selects a separately created target and
// observes the global scanner; nil selection preserves original setup order.
func mergeLaneRebatchJourney(t *testing.T, selectTarget func(engineFixture, worktrees.CreateResult, worktrees.CreateResult) string, observe func(engineFixture, WorktreeMergeReceipt, WorktreeMergeReceipt)) {
	t.Helper()

	fixture := newEngineFixture(t)
	original := createMergeSource(t, fixture, "rebatch-skip-original", "feature/rebatch-skip-original", "original.txt", "original\n")
	target := "main"
	var extra worktrees.CreateResult
	if selectTarget != nil {
		extra = createMergeSource(t, fixture, "rebatch-skip-extra", "feature/rebatch-skip-extra", "extra.txt", "extra\n")
		target = selectTarget(fixture, original, extra)
	}
	old, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{original.WorktreeDir}, Target: target, Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if selectTarget == nil {
		extra = createMergeSource(t, fixture, "rebatch-skip-extra", "feature/rebatch-skip-extra", "extra.txt", "extra\n")
	}
	replacement, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{original.WorktreeDir, extra.WorktreeDir}, Target: target, Model: "test-model", AgentRuntime: "test",
		RebatchReceipt: old.ReceiptPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ReceiptPath == old.ReceiptPath {
		t.Fatalf("rebatch reused the original receipt path %s", old.ReceiptPath)
	}
	reread, err := readWorktreeMergeReceipt(old.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if rebatched, rebatchErr := hasPreparedWorktreeMergeRebatch(reread); rebatchErr != nil || !rebatched {
		t.Fatalf("hasPreparedWorktreeMergeRebatch(original) = %t, %v; want a valid, authenticated sidecar", rebatched, rebatchErr)
	}
	// Excluding the replacement forces the scan to consider only the
	// rebatched original, deterministically reaching (and covering) the
	// `if rebatched { continue }` branch regardless of directory order.
	active, err := activeWorktreeMergeLaneReceipt(context.Background(), fixture.githubDir, filepath.Dir(old.ReceiptPath), old.Lane, replacement.ReceiptPath)
	if err != nil {
		t.Fatalf("lane scan aborted on the valid rebatch sidecar: %v", err)
	}
	if active != nil {
		t.Fatalf("active lane after excluding the replacement = %+v, want nil (the rebatched original must be skipped)", active)
	}
	// Without excluding anything, the lane's one live receipt is the
	// replacement, whichever order the scan visits the two files in.
	active, err = activeWorktreeMergeLaneReceipt(context.Background(), fixture.githubDir, filepath.Dir(old.ReceiptPath), old.Lane)
	if err != nil {
		t.Fatal(err)
	}
	if active == nil || active.ReceiptPath != replacement.ReceiptPath {
		t.Fatalf("active lane = %+v, want the replacement %s", active, replacement.ReceiptPath)
	}

	if observe != nil {
		observe(fixture, old, replacement)
	}
}
