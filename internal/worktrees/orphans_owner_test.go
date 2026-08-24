package worktrees

import (
	"os"
	"strings"
	"testing"
	"time"
)

func recentEntry() OrphanWorktree {
	return OrphanWorktree{
		LastCommit: time.Now().Add(-2 * 24 * time.Hour),
		AgeDays:    2,
		OwnerState: OwnerUnstated,
	}
}

func disposition(t *testing.T, entry OrphanWorktree) (string, []string) {
	t.Helper()
	return orphanDisposition(entry, "main", 14*24*time.Hour, time.Now())
}

func evidenceContains(evidence []string, want string) bool {
	for _, line := range evidence {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}

// A running declared session is proof. It must outrank the age heuristic and
// say so, rather than reporting the same "likely" guess.
func TestLiveOwnerIsReportedAsProof(t *testing.T) {
	entry := recentEntry()
	entry.OwnerState, entry.OwnerAgent, entry.OwnerPID = OwnerLive, "claude-code/sess-1", os.Getpid()

	got, evidence := disposition(t, entry)

	if got != DispositionActive {
		t.Fatalf("disposition = %q, want active", got)
	}
	if !evidenceContains(evidence, "is running") {
		t.Fatalf("evidence does not cite the running owner: %v", evidence)
	}
	if evidenceContains(evidence, "age alone") {
		t.Fatalf("evidence still falls back to age: %v", evidence)
	}
}

// A live owner that has not committed yet is working, not abandoned. Without
// this the no-commit branch would claim it "may never have started".
func TestLiveOwnerBeatsTheNoCommitCase(t *testing.T) {
	entry := OrphanWorktree{OwnerState: OwnerLive, OwnerAgent: "codex", OwnerPID: os.Getpid()}

	if got, _ := disposition(t, entry); got != DispositionActive {
		t.Fatalf("disposition = %q, want active for a live owner with no commits", got)
	}
}

// This is the case the whole change exists for: recent work whose session has
// exited used to be reported as "likely still in use".
func TestExitedOwnerWithRecentWorkNeedsADecision(t *testing.T) {
	entry := recentEntry()
	entry.OwnerState, entry.OwnerAgent, entry.OwnerPID = OwnerGone, "claude-code/sess-1", 424242

	got, evidence := disposition(t, entry)

	if got != DispositionDecide {
		t.Fatalf("disposition = %q, want decide once the owner has exited", got)
	}
	if !evidenceContains(evidence, "has exited") {
		t.Fatalf("evidence does not explain the exited owner: %v", evidence)
	}
}

// Uncommitted work outranks everything, including a dead owner: the session
// exiting is exactly when its unsaved work is most at risk.
func TestDirtyOutranksOwnerState(t *testing.T) {
	entry := recentEntry()
	entry.Dirty = true
	entry.OwnerState, entry.OwnerPID = OwnerGone, 424242

	if got, _ := disposition(t, entry); got != DispositionReview {
		t.Fatalf("disposition = %q, want review for a dirty worktree", got)
	}
}

// Merged work is removable regardless of who owns it; keeping it because a
// session is live would never let a family be swept.
func TestMergedOutranksALiveOwner(t *testing.T) {
	entry := recentEntry()
	entry.Merged = true
	entry.OwnerState, entry.OwnerPID = OwnerLive, os.Getpid()

	if got, _ := disposition(t, entry); got != DispositionRemove {
		t.Fatalf("disposition = %q, want remove for merged work", got)
	}
}

// With nothing declared the age heuristic still applies, but the evidence must
// admit it is an inference rather than repeating "likely still in use".
func TestUnstatedOwnerFallsBackToAgeAndSaysSo(t *testing.T) {
	got, evidence := disposition(t, recentEntry())

	if got != DispositionActive {
		t.Fatalf("disposition = %q, want active from age", got)
	}
	if !evidenceContains(evidence, "age alone") {
		t.Fatalf("evidence does not admit it is inferred: %v", evidence)
	}
	if !evidenceContains(evidence, "wb worktree own") {
		t.Fatalf("evidence does not say how to make it knowable: %v", evidence)
	}
}

func TestStaleUnstatedWorktreeStillNeedsADecision(t *testing.T) {
	entry := OrphanWorktree{
		LastCommit: time.Now().Add(-40 * 24 * time.Hour),
		AgeDays:    40,
		OwnerState: OwnerUnstated,
	}

	if got, _ := disposition(t, entry); got != DispositionDecide {
		t.Fatalf("disposition = %q, want decide for long-idle work", got)
	}
}

// DeclaredOwner reads real recorded state, and must not mistake a provenance
// entry with no PID for an abandoned session.
func TestDeclaredOwnerDistinguishesUnstatedFromGone(t *testing.T) {
	clearIdentity(t)

	unstated := custodyWorktree(t)
	if err := RecordCustody(unstated, "", "worktree set", AgentIdentity{}); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := DeclaredOwner(unstated); state != OwnerUnstated {
		t.Fatalf("state = %q, want unstated for a provenance-only entry", state)
	}

	gone := custodyWorktree(t)
	if err := RecordCustody(gone, "", "worktree set",
		AgentIdentity{Runtime: "codex", PID: 424242}); err != nil {
		t.Fatal(err)
	}
	if state, _, pid := DeclaredOwner(gone); state != OwnerGone || pid != 424242 {
		t.Fatalf("state = %q pid = %d, want gone/424242", state, pid)
	}

	live := custodyWorktree(t)
	if err := RecordCustody(live, "", "worktree set",
		AgentIdentity{Runtime: "claude-code", PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := DeclaredOwner(live); state != OwnerLive {
		t.Fatalf("state = %q, want live for this running process", state)
	}
}

// A live session anywhere in the chain means the worktree is live: a running
// process is running whether or not a later session declared itself and then
// exited. Both orders are checked, because only one of them is the obvious one.
func TestDeclaredOwnerReportsLiveFromAnyLinkInTheChain(t *testing.T) {
	clearIdentity(t)
	worktree := custodyWorktree(t)

	if err := RecordCustody(worktree, "", "worktree set",
		AgentIdentity{Runtime: "codex", PID: 424242}); err != nil {
		t.Fatal(err)
	}
	if err := RecordCustody(worktree, "", "worktree own",
		AgentIdentity{Runtime: "claude-code", PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}

	state, _, pid := DeclaredOwner(worktree)
	if state != OwnerLive || pid != os.Getpid() {
		t.Fatalf("state = %q pid = %d, want the live successor", state, pid)
	}

	// Reverse order: the live session declared first, a later one has exited.
	reversed := custodyWorktree(t)
	if err := RecordCustody(reversed, "", "worktree own",
		AgentIdentity{Runtime: "claude-code", PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	if err := RecordCustody(reversed, "", "worktree set",
		AgentIdentity{Runtime: "codex", PID: 424242}); err != nil {
		t.Fatal(err)
	}
	if state, _, pid := DeclaredOwner(reversed); state != OwnerLive || pid != os.Getpid() {
		t.Fatalf("state = %q pid = %d; a dead later session must not mask a running one", state, pid)
	}
}
