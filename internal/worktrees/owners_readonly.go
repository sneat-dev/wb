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
	state, _ := DeclaredOwnerPIDReadOnly(worktree)
	return state
}

// DeclaredOwnerPIDReadOnly is DeclaredOwnerReadOnly with the recorded owner's
// process id, in one read of the journal. The id is the live owner's when the
// state is OwnerLive (the process was alive when the journal was read), and 0
// for every other state, so a reader that matches a process id never matches a
// process that has since been replaced.
func DeclaredOwnerPIDReadOnly(worktree string) (state string, pid int) {
	ports := ownerPorts()
	ports.ReadEvents = readOnlyLocalEvents
	state, _, pid = ports.DeclaredOwner(worktree)
	if state != OwnerLive {
		pid = 0
	}
	return state, pid
}

// readOnlyLocalEvents reads the journal's events over a plain read-only open of
// its directory, with no exclude rule ensured.
func readOnlyLocalEvents(worktree string) ([]LocalWorkLogEvent, error) {
	return worktreejournal.Store{OpenDirectory: func(worktree string, create bool) (*os.File, error) {
		return openJournalSubdirectory(worktree, worklogDirectory, create)
	}}.ReadLocalEvents(worktree)
}
