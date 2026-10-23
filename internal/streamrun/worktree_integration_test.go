package streamrun

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

//nolint:paralleltest // Process-wide environment changes in TestStreamWorktreeAdapterCreateAndRemove; these rows share their parent environment and remain sequential.
func TestStreamWorktreeAdapterCreateAndRemove(t *testing.T) {
	root := t.TempDir()
	seeds := t.TempDir()
	clone := filepath.Join(root, "acme", "app")
	testenv.CloneWithOrigin(t, seeds, "app", clone)
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	// The native creation path carries the same mandatory archived task request.
	workLog, err := worktrees.PrepareWorkLogOptions(root, "cw-stream", stdinWorkLog(t))
	if err != nil {
		t.Fatalf("prepare work log: %v", err)
	}
	adapter := &streamWorktrees{projectsRoot: root, workLog: workLog, base: "main", create: worktrees.Create, cleanup: worktrees.Cleanup}
	created, err := adapter.Create(context.Background(), "cw-stream", "stream/cw-stream", []string{"acme/app"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(created) != 1 || created[0].Repository != "acme/app" || created[0].Branch != "stream/cw-stream" || created[0].Worktree == "" {
		t.Fatalf("created = %+v", created)
	}
	if _, err := adapter.PlannedWorktree("cw-stream", "not-a-slug"); err == nil ||
		!strings.Contains(err.Error(), "must be owner/name") {
		t.Fatalf("PlannedWorktree on a bad slug = %v", err)
	}
	planned, err := adapter.PlannedWorktree("cw-stream", "acme/app")
	if err != nil || planned == "" {
		t.Fatalf("PlannedWorktree = %q, %v", planned, err)
	}

	// Remove delegates to the worktrees cleanup seam; stub it so every outcome
	// shape is reachable without touching the filesystem.
	applied := true
	adapter.cleanup = func(context.Context, worktrees.CleanupOptions) (worktrees.CleanupOutcome, error) {
		return worktrees.CleanupOutcome{Results: []worktrees.CleanupResult{{Repository: "acme/app", Applied: applied}}}, nil
	}
	if err := adapter.Remove(context.Background(), "cw-stream", "acme/app", "/tmp/wt", nil); err != nil {
		t.Fatalf("Remove applied: %v", err)
	}
	// A receipt is translated into the proof cleanup needs.
	var captured worktrees.CleanupOptions
	adapter.cleanup = func(_ context.Context, options worktrees.CleanupOptions) (worktrees.CleanupOutcome, error) {
		captured = options
		return worktrees.CleanupOutcome{Results: []worktrees.CleanupResult{{Repository: "acme/app", WorktreeGone: true}}}, nil
	}
	receipt := &streams.SquashAbsorptionReceipt{Target: "main", SourceBranch: "stream/cw", SourceSHA: "a", CandidateSHA: "b", LandingSHA: "c"}
	if err := adapter.Remove(context.Background(), "cw-stream", "acme/app", "/tmp/wt", receipt); err != nil {
		t.Fatalf("Remove with receipt: %v", err)
	}
	if len(captured.MergeReceiptProofs) != 1 || captured.MergeReceiptProofs[0].LandingSHA != "c" ||
		captured.MergeReceiptProofs[0].SourceWorktree != "/tmp/wt" {
		t.Fatalf("cleanup options = %+v", captured)
	}

	// Cleanup that refused without a reason still says why it could not retire.
	adapter.cleanup = func(context.Context, worktrees.CleanupOptions) (worktrees.CleanupOutcome, error) {
		return worktrees.CleanupOutcome{Results: []worktrees.CleanupResult{{Repository: "acme/app"}}}, nil
	}
	if err := adapter.Remove(context.Background(), "cw-stream", "acme/app", "/tmp/wt", nil); err == nil ||
		!strings.Contains(err.Error(), "cleanup reported no reason") {
		t.Fatalf("reasonless refusal = %v", err)
	}
	adapter.cleanup = func(context.Context, worktrees.CleanupOptions) (worktrees.CleanupOutcome, error) {
		return worktrees.CleanupOutcome{Results: []worktrees.CleanupResult{{Repository: "acme/app", Reason: "worktree busy"}}}, nil
	}
	if err := adapter.Remove(context.Background(), "cw-stream", "acme/app", "/tmp/wt", nil); err == nil ||
		!strings.Contains(err.Error(), "worktree busy") {
		t.Fatalf("refusal with reason = %v", err)
	}
	// No result for the requested repository at all is a refusal.
	adapter.cleanup = func(context.Context, worktrees.CleanupOptions) (worktrees.CleanupOutcome, error) {
		return worktrees.CleanupOutcome{Results: []worktrees.CleanupResult{{Repository: "other/repo", Applied: true}}}, nil
	}
	if err := adapter.Remove(context.Background(), "cw-stream", "acme/app", "/tmp/wt", nil); err == nil ||
		!strings.Contains(err.Error(), "cleanup reported no candidate") {
		t.Fatalf("missing candidate = %v", err)
	}
	// A cleanup error is surfaced unchanged.
	adapter.cleanup = func(context.Context, worktrees.CleanupOptions) (worktrees.CleanupOutcome, error) {
		return worktrees.CleanupOutcome{}, errors.New("cleanup exploded")
	}
	if err := adapter.Remove(context.Background(), "cw-stream", "acme/app", "/tmp/wt", nil); err == nil ||
		!strings.Contains(err.Error(), "cleanup exploded") {
		t.Fatalf("cleanup error = %v", err)
	}
}
