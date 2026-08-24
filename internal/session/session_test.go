package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRegisterRecordsWhatWBKnowsAboutItself(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")

	written, err := Register(dir, Record{PID: os.Getpid(), Runtime: "claude-code", Model: "m"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if written.WBVersion == "" {
		t.Fatal("WBVersion is empty; WB always knows its own version")
	}
	if written.WBPath == "" {
		t.Fatal("WBPath is empty; WB always knows which binary it is")
	}
	if written.StartedAt.IsZero() {
		t.Fatal("StartedAt is zero")
	}
}

func TestRegisterRequiresAPID(t *testing.T) {
	if _, err := Register(filepath.Join(t.TempDir(), "sessions"), Record{Runtime: "codex"}); err == nil {
		t.Fatal("Register accepted a record with no PID")
	}
}

// Re-registering must replace, so a session correcting its model does not have
// to find and delete the previous file.
func TestRegisterReplacesTheRecordForOnePID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	pid := os.Getpid()

	if _, err := Register(dir, Record{PID: pid, Runtime: "claude-code", Model: "old"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Register(dir, Record{PID: pid, Runtime: "claude-code", Model: "new"}); err != nil {
		t.Fatal(err)
	}

	views, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("records = %d, want 1 after re-registering the same PID", len(views))
	}
	if views[0].Model != "new" {
		t.Fatalf("Model = %q, want the corrected value", views[0].Model)
	}
}

func TestListReportsLiveness(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	if _, err := Register(dir, Record{PID: os.Getpid(), Runtime: "claude-code"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Register(dir, Record{PID: 424242, Runtime: "codex"}); err != nil {
		t.Fatal(err)
	}

	views, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	states := map[int]string{}
	for _, view := range views {
		states[view.PID] = view.State
	}
	if states[os.Getpid()] != StateLive {
		t.Fatalf("this process reported %q, want live", states[os.Getpid()])
	}
	if states[424242] != StateGone {
		t.Fatalf("an exited PID reported %q, want gone", states[424242])
	}
}

// A missing directory means nobody registered yet, which is not an error.
func TestListToleratesAMissingDirectory(t *testing.T) {
	views, err := List(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(views) != 0 {
		t.Fatalf("views = %d, want none", len(views))
	}
}

// One corrupt file must not hide every other session.
func TestListSkipsAMalformedRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	if _, err := Register(dir, Record{PID: os.Getpid(), Runtime: "claude-code"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "999999.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	views, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(views) != 1 || views[0].PID != os.Getpid() {
		t.Fatalf("views = %+v, want only the valid record", views)
	}
}

func TestPruneRemovesOnlyExitedSessions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	if _, err := Register(dir, Record{PID: os.Getpid(), Runtime: "claude-code"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Register(dir, Record{PID: 424242, Runtime: "codex"}); err != nil {
		t.Fatal(err)
	}

	removed, err := Prune(dir)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	views, _ := List(dir)
	if len(views) != 1 || views[0].PID != os.Getpid() {
		t.Fatalf("survivors = %+v, want only the live session", views)
	}
}

func TestLookupIgnoresAnExitedSession(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	if _, err := Register(dir, Record{PID: 424242, Runtime: "codex"}); err != nil {
		t.Fatal(err)
	}

	if _, ok := Lookup(dir, 424242); ok {
		t.Fatal("Lookup returned an exited session as usable")
	}
}

// Resolution must find a session registered by an ancestor, which is the whole
// point: WB runs as a grandchild of the agent, not as the agent itself.
func TestResolveForProcessFindsAnAncestorSession(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	if _, err := Register(dir, Record{PID: os.Getppid(), Runtime: "claude-code", Model: "m"}); err != nil {
		t.Fatal(err)
	}

	record, ok := ResolveForProcess(dir, os.Getpid())
	if !ok {
		t.Fatal("ResolveForProcess did not find the session registered by our parent")
	}
	if record.PID != os.Getppid() || record.Runtime != "claude-code" {
		t.Fatalf("record = %+v, want the parent's registration", record)
	}
}

// An unregistered ancestor is never treated as an owner: this confirms
// declarations, it does not invent them.
func TestResolveForProcessIgnoresUnregisteredAncestors(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")

	if _, ok := ResolveForProcess(dir, os.Getpid()); ok {
		t.Fatal("ResolveForProcess claimed an owner with nothing registered")
	}
}

func TestResolveForProcessIgnoresARegisteredButExitedAncestor(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	if _, err := Register(dir, Record{
		PID: 424242, Runtime: "codex", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	if _, ok := ResolveForProcess(dir, os.Getpid()); ok {
		t.Fatal("ResolveForProcess resolved to a session that has exited")
	}
}
