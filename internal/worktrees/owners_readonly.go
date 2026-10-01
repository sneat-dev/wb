package worktrees

import (
	"os"

	"github.com/sneat-dev/wb/internal/worktreejournal"
)

// DeclaredOwnerReadOnly is DeclaredOwner for a reader that must not write: it
// reads the worktree's Work Log journal without ensuring the journal's exclude
// rule in the repository (DeclaredOwner opens the journal the way a writer does,
// and so may create `.git/info/exclude` entries and run Git), and returns only
// the liveness of the recorded owner process: OwnerLive, OwnerGone or
// OwnerUnstated. The Cockpit snapshotter reads it on every pass.
func DeclaredOwnerReadOnly(worktree string) string {
	ports := ownerPorts()
	ports.ReadEvents = readOnlyLocalEvents
	state, _, _ := ports.DeclaredOwner(worktree)
	return state
}

// readOnlyLocalEvents reads the journal's events over a plain read-only open of
// its directory, with no exclude rule ensured.
func readOnlyLocalEvents(worktree string) ([]LocalWorkLogEvent, error) {
	return worktreejournal.Store{OpenDirectory: func(worktree string, create bool) (*os.File, error) {
		return openJournalSubdirectory(worktree, worklogDirectory, create)
	}}.ReadLocalEvents(worktree)
}
