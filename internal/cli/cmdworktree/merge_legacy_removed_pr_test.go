package cmdworktree

import (
	"context"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/spf13/cobra"
	"testing"
)

func TestLandedFailureCommandForwardsExplicitLegacyRemovedPRProof(t *testing.T) {
	t.Parallel()
	inv := mergeTestInvocation(shared.Flags{ProjectsRoot: t.TempDir()})
	called := false
	inv.operations.AcknowledgeLandedMergeFailure = func(_ context.Context, o orchestrate.WorktreeMergeLandedFailureAcknowledgementOptions) (orchestrate.WorktreeMergeLandedFailureAcknowledgement, error) {
		called = true
		if !o.LegacyRemovedPR || o.Receipt != "receipt.json" || o.Apply {
			t.Fatalf("forwarded options=%+v", o)
		}
		return orchestrate.WorktreeMergeLandedFailureAcknowledgement{Status: "landed_failure_acknowledged"}, nil
	}
	_, _, err := mergeExecuteOriginal(t, "", func() *cobra.Command { return newWorktreeMergeAcknowledgeLandedFailedCmd(inv) }, "receipt.json", "--legacy-removed-pr", "--format=json")
	if err != nil || !called {
		t.Fatalf("called=%t error=%v", called, err)
	}
}
