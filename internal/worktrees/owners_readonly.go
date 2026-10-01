package worktrees

import (
	"os"
	"time"

	"github.com/sneat-dev/wb/internal/worktreejournal"
)

// DeclaredOwnerReadOnly is DeclaredOwner for a reader that must not write: it
// reads the worktree's Work Log journal without ensuring the journal's exclude
// rule in the repository (DeclaredOwner opens the journal the way a writer does,
// and so may create `.git/info/exclude` entries and run Git), and returns only
// the liveness of the recorded owner process: OwnerLive, OwnerGone or
// OwnerUnstated. The Cockpit snapshotter reads it on every pass.
func DeclaredOwnerReadOnly(worktree string) string {
	state, _ := DeclaredOwnerLiveReadOnly(worktree)
	return state
}

// LiveOwner is what a worktree's journal records of its live declared owner
// process: its id, the agent it declared (runtime, or runtime/id) and when it
// registered.
type LiveOwner struct {
	PID   int
	Agent string
	At    time.Time
}

// DeclaredOwnerLiveReadOnly is DeclaredOwnerReadOnly with the live owner's
// registration, in one read of the journal. The owner is the zero value unless
// the state is OwnerLive (the process was alive when the journal was read), so
// a reader that matches a process id never matches an owner that has gone.
func DeclaredOwnerLiveReadOnly(worktree string) (state string, owner LiveOwner) {
	ports := ownerPorts()
	ports.ReadEvents = readOnlyLocalEvents
	state, view := ports.DeclaredOwnerView(worktree)
	if state != OwnerLive {
		return state, LiveOwner{}
	}
	return state, LiveOwner{PID: view.PID, Agent: view.Agent, At: view.At}
}

// readOnlyLocalEvents reads the journal's events over a plain read-only open of
// its directory, with no exclude rule ensured.
func readOnlyLocalEvents(worktree string) ([]LocalWorkLogEvent, error) {
	return worktreejournal.Store{OpenDirectory: func(worktree string, create bool) (*os.File, error) {
		return openJournalSubdirectory(worktree, worklogDirectory, create)
	}}.ReadLocalEvents(worktree)
}
