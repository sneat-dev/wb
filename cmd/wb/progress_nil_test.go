package main

import (
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// TestRemotePublishProgressNilReceiverIsANoOp drives the "progress == nil"
// early-return branch of every remotePublishProgress method.
func TestRemotePublishProgressNilReceiverIsANoOp(t *testing.T) {
	t.Parallel()
	var progress *remotePublishProgress
	progress.start(3)
	progress.repositoryComplete("acme/one", nil)
	progress.phase("scanning")
	progress.worktree(worktrees.ListProgress{Path: "/tmp/acme/one", Done: true})
	progress.finish("done")
	progress.fail(nil)
}
