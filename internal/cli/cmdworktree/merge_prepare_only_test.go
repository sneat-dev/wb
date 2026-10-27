package cmdworktree

import (
	"context"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"strings"
	"testing"
)

func TestResumePrepareOnlyRequiresLocalValidationAndBindsReceipt(t *testing.T) {
	t.Parallel()
	inv := mergeRecordingInvocation(&shared.Flags{})
	called := false
	inv.operations.ResumeWorktreeMerge = func(_ context.Context, o orchestrate.WorktreeMergeLandOptions) (orchestrate.WorktreeMergeReceipt, error) {
		called = true
		if !o.PrepareOnly || !o.ValidateLocally || o.Receipt != "receipt.json" {
			t.Fatalf("options=%+v", o)
		}
		return orchestrate.WorktreeMergeReceipt{Status: orchestrate.WorktreeMergePrepared}, nil
	}
	if err := mergeExecute(newWorktreeMergeLandCmd(inv, "resume"), "receipt.json", "--prepare-only"); err != nil || !called {
		t.Fatalf("called=%t err=%v", called, err)
	}
	for _, flag := range []string{"--cleanup", "--stop-before-merge", "--defer-direct-ci-pr=42"} {
		called = false
		if err := mergeExecute(newWorktreeMergeLandCmd(inv, "resume"), "receipt.json", "--prepare-only", flag); err == nil || !strings.Contains(err.Error(), "--prepare-only") || called {
			t.Fatalf("flag=%s called=%t error=%v", flag, called, err)
		}
	}
}
