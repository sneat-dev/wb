//go:build e2e

package worktrees

import (
	"context"
	"strings"
	"testing"
)

//nolint:paralleltest // newGitFixture uses t.Setenv to isolate WB state for native Git subprocesses.
func TestE2EValidateDependencyDeltasWrapper(t *testing.T) {
	fixture := newGitFixture(t)
	targetHead := gitTestOutput(t, fixture.canonical, "rev-parse", "origin/main")
	entry := ListResult{
		Task: "plain-task", Repository: "acme/app", Branch: "wb/plain-task",
		CanonicalDir: fixture.canonical, WorktreeDir: t.TempDir(),
		HeadSHA: targetHead, RemoteTargetSHA: targetHead,
	}
	if err := ValidateDependencyDeltas(context.Background(), SupersessionReceipt{Version: 1}, entry); err != nil {
		t.Fatalf("generic receipt should not require dependency proof: %v", err)
	}
	withEvidence := SupersessionReceipt{Version: 1, DependencyDeltasComplete: true}
	err := ValidateDependencyDeltas(context.Background(), withEvidence, entry)
	if err == nil || !strings.Contains(err.Error(), "requires original_pr") {
		t.Fatalf("dependency evidence without original_pr error = %v", err)
	}
	campaign := ListResult{Task: "deps-upgrade", Branch: "wb/deps/upgrade"}
	err = ValidateDependencyDeltas(context.Background(), SupersessionReceipt{Version: 1}, campaign)
	if err == nil || !strings.Contains(err.Error(), "dependency campaign supersession requires original_pr") {
		t.Fatalf("campaign receipt error = %v", err)
	}
}
