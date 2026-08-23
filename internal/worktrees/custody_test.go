package worktrees

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/buildinfo"
)

// custodyWorktree is a real Git checkout, because the local work log manages
// the repository's info/exclude and cannot be written outside one.
func custodyWorktree(t *testing.T) string {
	t.Helper()
	// t.TempDir() returns a path under /var on macOS, and /var is a symlink to
	// /private/var. WB's secure directory walk opens each segment O_NOFOLLOW
	// and refuses a symlinked one — correctly, since that is the property it
	// exists to enforce. Resolve the fixture path so the test exercises the
	// refusal on real symlinks rather than tripping over the temp directory.
	// Linux CI has no such symlink, which is why this only bites locally.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	command := exec.Command("git", "init", "-q", "-b", "main", dir)
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return dir
}

// clearIdentity removes any declaration inherited from the environment the
// suite happens to run in, so a test controls exactly what WB is told.
func clearIdentity(t *testing.T) {
	t.Helper()
	t.Setenv(EnvAgentPID, "")
	t.Setenv(EnvAgentRuntime, "")
	t.Setenv(EnvAgentModel, "")
	t.Setenv(EnvAgentID, "")
	TakeOwnerWarnings()
}

func ownerEvents(t *testing.T, worktree string) []OwnerRegistration {
	t.Helper()
	views, err := ownerViews(worktree)
	if err != nil {
		t.Fatalf("ownerViews: %v", err)
	}
	owners := make([]OwnerRegistration, 0, len(views))
	for _, view := range views {
		owners = append(owners, view.OwnerRegistration)
	}
	return owners
}

func declared() AgentIdentity {
	return AgentIdentity{Runtime: "claude-code", AgentID: "sess-1", Model: "m", PID: 4321}
}

// A session doing repeated work must leave one custody record, not a command
// trace, or the chain becomes unreadable.
func TestRecordCustodyAppendsOnceForOneSession(t *testing.T) {
	clearIdentity(t)
	worktree := custodyWorktree(t)

	for range 3 {
		if err := RecordCustody(worktree, "effort-a", "worktree set", declared()); err != nil {
			t.Fatalf("RecordCustody: %v", err)
		}
	}

	owners := ownerEvents(t, worktree)
	if len(owners) != 1 {
		t.Fatalf("owner entries = %d, want 1", len(owners))
	}
	if owners[0].PID != 4321 || owners[0].Agent != "claude-code/sess-1" {
		t.Fatalf("owner = %+v", owners[0])
	}
	if owners[0].WBVersion == "" {
		t.Fatal("WBVersion is empty; WB always knows its own version")
	}
	if owners[0].Command != "worktree set" {
		t.Fatalf("Command = %q, want the triggering command", owners[0].Command)
	}
}

// Handover is the case the chain exists for: a second session must not be
// silently recorded as the first.
func TestRecordCustodyAppendsOnHandover(t *testing.T) {
	clearIdentity(t)
	worktree := custodyWorktree(t)

	first := declared()
	second := AgentIdentity{Runtime: "codex", AgentID: "sess-2", Model: "m2", PID: 9876}

	for _, identity := range []AgentIdentity{first, second} {
		if err := RecordCustody(worktree, "effort-a", "worktree set", identity); err != nil {
			t.Fatalf("RecordCustody: %v", err)
		}
	}

	owners := ownerEvents(t, worktree)
	if len(owners) != 2 {
		t.Fatalf("owner entries = %d, want 2 (handover)", len(owners))
	}
	if owners[0].PID != 4321 || owners[1].PID != 9876 {
		t.Fatalf("chain = %+v; want first then second", owners)
	}
}

// A WB upgrade is worth a new link even for the same session, so provenance
// can attribute a change to the binary that made it.
func TestRecordCustodyAppendsOnWBVersionChange(t *testing.T) {
	clearIdentity(t)
	worktree := custodyWorktree(t)
	t.Cleanup(func() { buildinfo.Set("") })

	buildinfo.Set("v1.0.0")
	if err := RecordCustody(worktree, "e", "worktree set", declared()); err != nil {
		t.Fatal(err)
	}
	buildinfo.Set("v2.0.0")
	if err := RecordCustody(worktree, "e", "worktree set", declared()); err != nil {
		t.Fatal(err)
	}

	owners := ownerEvents(t, worktree)
	if len(owners) != 2 {
		t.Fatalf("owner entries = %d, want 2 across a version change", len(owners))
	}
	if owners[0].WBVersion != "v1.0.0" || owners[1].WBVersion != "v2.0.0" {
		t.Fatalf("versions = %q, %q", owners[0].WBVersion, owners[1].WBVersion)
	}
}

// Provenance is still worth recording with nobody declared; what must not
// happen is inventing a PID.
func TestRecordCustodyRecordsProvenanceWhenUndeclared(t *testing.T) {
	clearIdentity(t)
	worktree := custodyWorktree(t)

	if err := RecordCustody(worktree, "", "worktree set", AgentIdentity{}); err != nil {
		t.Fatalf("RecordCustody: %v", err)
	}

	owners := ownerEvents(t, worktree)
	if len(owners) != 1 {
		t.Fatalf("owner entries = %d, want 1", len(owners))
	}
	if owners[0].PID != 0 {
		t.Fatalf("PID = %d, want 0 when nothing was declared", owners[0].PID)
	}
	if owners[0].WBVersion == "" || owners[0].Command == "" {
		t.Fatalf("provenance missing: %+v", owners[0])
	}
	if status := ownerPIDStatus(owners[0].PID); status != "unknown" {
		t.Fatalf("PIDStatus = %q, want unknown rather than a claim about liveness", status)
	}
	if warnings := TakeOwnerWarnings(); len(warnings) != 1 || warnings[0] != worktree {
		t.Fatalf("warnings = %v, want exactly this worktree", warnings)
	}
}

func TestTakeOwnerWarningsIsSilentOnceDeclared(t *testing.T) {
	clearIdentity(t)
	worktree := custodyWorktree(t)

	if err := RecordCustody(worktree, "", "worktree set", declared()); err != nil {
		t.Fatal(err)
	}

	if warnings := TakeOwnerWarnings(); len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none for a declared owner", warnings)
	}
}

// An automatic write must not erase the effort the creator recorded.
func TestRecordCustodyInheritsEffortWhenUnspecified(t *testing.T) {
	clearIdentity(t)
	worktree := custodyWorktree(t)

	if err := RecordCustody(worktree, "effort-a", "worktree create", declared()); err != nil {
		t.Fatal(err)
	}
	other := declared()
	other.PID = 5555
	if err := RecordCustody(worktree, "", "worktree log steer", other); err != nil {
		t.Fatal(err)
	}

	owners := ownerEvents(t, worktree)
	if len(owners) != 2 {
		t.Fatalf("owner entries = %d, want 2", len(owners))
	}
	if owners[1].Effort != "effort-a" {
		t.Fatalf("Effort = %q, want it inherited from the previous owner", owners[1].Effort)
	}
}

// Any worktree write should carry the chain forward without each call site
// having to remember to do it.
func TestWorktreeWritesRecordCustodyAutomatically(t *testing.T) {
	clearIdentity(t)
	t.Setenv(EnvAgentPID, "4321")
	t.Setenv(EnvAgentRuntime, "claude-code")
	worktree := custodyWorktree(t)

	if _, _, err := appendLocalEvent(worktree, LocalWorkLogEvent{
		Type: LocalEventCheckpoint, Message: "work happened",
	}); err != nil {
		t.Fatalf("appendLocalEvent: %v", err)
	}

	owners := ownerEvents(t, worktree)
	if len(owners) != 1 {
		t.Fatalf("owner entries = %d, want 1 recorded by the write itself", len(owners))
	}
	if owners[0].PID != 4321 {
		t.Fatalf("PID = %d, want the declared session", owners[0].PID)
	}
}

// The custody record must precede the work it vouches for.
func TestCustodyEntryPrecedesTheEventThatTriggeredIt(t *testing.T) {
	clearIdentity(t)
	worktree := custodyWorktree(t)

	if _, _, err := appendLocalEvent(worktree, LocalWorkLogEvent{
		Type: LocalEventCheckpoint, Message: "work happened",
	}); err != nil {
		t.Fatal(err)
	}

	events, err := readLocalEvents(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 {
		t.Fatalf("events = %d, want the owner entry plus the checkpoint", len(events))
	}
	if events[0].Type != LocalEventOwner {
		t.Fatalf("first event = %q, want the custody record first", events[0].Type)
	}
}
