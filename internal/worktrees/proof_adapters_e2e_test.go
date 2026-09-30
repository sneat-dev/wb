//go:build e2e

package worktrees

import (
	"context"
	"testing"

	"github.com/sneat-dev/wb/internal/worktreelanding"
)

func TestProofAncestorAdapterUsesRunnerFake(t *testing.T) {
	t.Parallel()
	ctx := lifecycleGitContext(t, "repo", lifecycleGitReply{operation: "merge-base", output: ""})
	yes, err := isAncestor(ctx, "repo", "a", "b")
	if err != nil || !yes {
		t.Fatalf("ancestor %v %v", yes, err)
	}
}

//nolint:paralleltest // newGitFixture configures process-wide WB and Git environment for a real repository.
func TestProofUncachedTargetAdapterUsesPrivateRef(t *testing.T) {
	fixture := newGitFixture(t)
	want := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	got, err := worktreelanding.FetchRemoteTargetHeadUncached(context.Background(), fixture.canonical, "main", remoteTargetFetchTimeout, fetchRemoteTargetPrivate)
	if err != nil || got != want {
		t.Fatalf("uncached target = %q, %v; want %q", got, err, want)
	}
}
