package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestMarkParkedKeepsRegistrationImmutableAndRemovesLiveLookup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	source, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: "wbs-park", Runtime: "codex", StartedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(os.Getpid())+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MarkParked(dir, os.Getpid(), "park-test"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(os.Getpid())+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("park rewrote the immutable PID registration")
	}
	if _, ok := Lookup(dir, os.Getpid()); ok {
		t.Fatal("parked session remains live")
	}
	views, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].State != StateParked || views[0].WBSessionID != source.WBSessionID {
		t.Fatalf("views = %#v", views)
	}
}

func TestMarkResumedAppendsProjectionAndListPrefersIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	source, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: "wbs-resume-source", Runtime: "codex", StartedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	registration := filepath.Join(dir, strconv.Itoa(os.Getpid())+".json")
	before, err := os.ReadFile(registration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MarkParked(dir, source.PID, "park-resume-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := MarkResumed(dir, source.PID, "park-resume-test", "wbs-resume-successor"); err != nil {
		t.Fatal(err)
	}
	if _, err := MarkResumed(dir, source.PID, "park-resume-test", "wbs-resume-successor"); err != nil {
		t.Fatalf("identical resumed retry: %v", err)
	}
	after, err := os.ReadFile(registration)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("resumed projection rewrote immutable PID registration")
	}
	if _, ok := Lookup(dir, source.PID); ok {
		t.Fatal("resumed source remains live")
	}
	views, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].State != StateResumed || views[0].Lifecycle != "resumed" || views[0].ParkedSessionID != "park-resume-test" {
		t.Fatalf("views = %#v", views)
	}
	if _, err := os.Stat(parkedMarkerPath(dir, source.WBSessionID)); err != nil {
		t.Fatalf("immutable parked history disappeared: %v", err)
	}
	if _, err := MarkResumed(dir, source.PID, "park-resume-test", "wbs-other"); err == nil {
		t.Fatal("conflicting resumed projection accepted")
	}
}

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

func TestRegisterAssignsStableWBIdentityAndMachine(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	pid := os.Getpid()

	first, err := Register(dir, Record{PID: pid, Runtime: "codex"})
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if first.WBSessionID == "" {
		t.Fatal("WBSessionID is empty")
	}
	if first.Machine == "" {
		t.Fatal("Machine is empty")
	}

	second, err := Register(dir, Record{PID: pid, Runtime: "codex", Model: "corrected"})
	if err != nil {
		t.Fatalf("second Register: %v", err)
	}
	if second.WBSessionID != first.WBSessionID {
		t.Fatalf("WBSessionID changed across re-registration: %q -> %q", first.WBSessionID, second.WBSessionID)
	}
	if second.Machine != first.Machine {
		t.Fatalf("Machine changed across re-registration: %q -> %q", first.Machine, second.Machine)
	}
}

func TestRegisterRetainsPreallocatedLineageAndHarnessIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	want := Record{
		PID:                    os.Getpid(),
		WBSessionID:            "wbs-successor",
		Machine:                "hetzner-vm1",
		Runtime:                "codex",
		Model:                  "gpt-5",
		NativeHarnessID:        "native-123",
		TmuxName:               "wb-session-wbs-successor",
		PredecessorWBSessionID: "wbs-source",
		HandoffID:              "handoff-123",
	}

	written, err := Register(dir, want)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if written.WBSessionID != want.WBSessionID || written.Machine != want.Machine ||
		written.NativeHarnessID != want.NativeHarnessID || written.TmuxName != want.TmuxName ||
		written.PredecessorWBSessionID != want.PredecessorWBSessionID || written.HandoffID != want.HandoffID {
		t.Fatalf("written identity = %+v, want fields from %+v", written, want)
	}

	reRegistered, err := Register(dir, Record{PID: want.PID, Runtime: want.Runtime, Model: "corrected"})
	if err != nil {
		t.Fatalf("re-register successor: %v", err)
	}
	if reRegistered.WBSessionID != written.WBSessionID || reRegistered.Machine != written.Machine ||
		reRegistered.NativeHarnessID != written.NativeHarnessID || reRegistered.TmuxName != written.TmuxName ||
		reRegistered.PredecessorWBSessionID != written.PredecessorWBSessionID || reRegistered.HandoffID != written.HandoffID ||
		!reRegistered.StartedAt.Equal(written.StartedAt) {
		t.Fatalf("re-registration erased stable identity or lineage: first=%+v second=%+v", written, reRegistered)
	}

	loaded, ok := Lookup(dir, want.PID)
	if !ok {
		t.Fatal("Lookup did not return the registered successor")
	}
	if loaded.WBSessionID != want.WBSessionID || loaded.PredecessorWBSessionID != want.PredecessorWBSessionID || loaded.HandoffID != want.HandoffID {
		t.Fatalf("loaded lineage = %+v, want fields from %+v", loaded, want)
	}
}

func TestListKeepsLegacyPIDOnlyRecordsReadable(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]any{
		"pid":        os.Getpid(),
		"runtime":    "claude-code",
		"agent_id":   "legacy-native-id",
		"started_at": time.Now().UTC(),
	}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath(dir, os.Getpid()), raw, 0o644); err != nil {
		t.Fatal(err)
	}

	views, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(views) != 1 || views[0].PID != os.Getpid() || views[0].AgentID != "legacy-native-id" {
		t.Fatalf("views = %+v, want the legacy PID record", views)
	}
	if views[0].WBSessionID != "" {
		t.Fatalf("legacy WBSessionID = %q, want empty rather than an invented unstable identity", views[0].WBSessionID)
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

func TestLookupExactRefusesLinkedRecordsAndRequiresLivePID(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "sessions")
	record, err := Register(directory, Record{
		PID: os.Getpid(), WBSessionID: "wbs-successor", Machine: "target-vm", Runtime: "codex",
		TmuxName: "wb-session-wbs-successor", PredecessorWBSessionID: "wbs-source", HandoffID: "handoff-123",
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, live, err := LookupExact(directory, record.PID)
	if err != nil || !live || loaded.WBSessionID != record.WBSessionID || loaded.TmuxName != record.TmuxName {
		t.Fatalf("LookupExact = (%#v, live=%t, err=%v)", loaded, live, err)
	}

	path := recordPath(directory, record.PID)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "forged.json")
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(external, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LookupExact(directory, record.PID); err == nil {
		t.Fatal("LookupExact followed a session-record symlink")
	}
}
