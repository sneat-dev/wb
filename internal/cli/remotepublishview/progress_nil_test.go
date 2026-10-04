package remotepublishview

import (
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// TestRemotePublishProgressNilReceiverIsANoOp drives the "progress == nil"
// early-return branch of every Progress method.
func TestRemotePublishProgressNilReceiverIsANoOp(t *testing.T) {
	t.Parallel()
	var progress *Progress
	progress.Start(3)
	progress.RepositoryComplete("acme/one", nil)
	progress.Phase("scanning")
	progress.Worktree(worktrees.ListProgress{Path: "/tmp/acme/one", Done: true})
	progress.Finish("done")
	progress.Fail(nil)
}
