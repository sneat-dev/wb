package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestDqCovProvenanceSameBinaryMatchesIdentityFields pins the exact fields that
// make two provenance records the same binary. A development build that shares
// a version string but not a digest must not be treated as identical.
func TestDqCovProvenanceSameBinaryMatchesIdentityFields(t *testing.T) {
	base := Provenance{Executable: "/wb", SHA256: "digest", Version: "1.2.3", Revision: "abc", Built: "yesterday"}
	if !base.SameBinary(base) {
		t.Fatal("identical provenance did not match itself")
	}
	different := []struct {
		name  string
		other Provenance
	}{
		{"executable", Provenance{Executable: "/other", SHA256: "digest", Version: "1.2.3", Revision: "abc"}},
		{"digest", Provenance{Executable: "/wb", SHA256: "other", Version: "1.2.3", Revision: "abc"}},
		{"version", Provenance{Executable: "/wb", SHA256: "digest", Version: "9.9.9", Revision: "abc"}},
		{"revision", Provenance{Executable: "/wb", SHA256: "digest", Version: "1.2.3", Revision: "def"}},
	}
	for _, check := range different {
		t.Run(check.name, func(t *testing.T) {
			if base.SameBinary(check.other) {
				t.Fatalf("provenance differing only by %s matched: %#v", check.name, check.other)
			}
		})
	}
	// Built is deliberately excluded: it is display metadata, not identity.
	rebuild := base
	rebuild.Built = "today"
	if !base.SameBinary(rebuild) {
		t.Fatal("Build timestamp changed binary identity")
	}
}

func TestDqCovStateValidRejectsIncompatibleSchemasAndMissingListener(t *testing.T) {
	valid := NewStarting(nil, "127.0.0.1:8766", Provenance{Executable: "/wb"}, "owner", time.Unix(1, 0))
	if err := valid.Valid(); err != nil {
		t.Fatalf("Valid() on a fresh state = %v", err)
	}
	checks := []struct {
		name   string
		mutate func(*State)
		want   string
	}{
		{"state schema", func(s *State) { s.SchemaVersion = 99 }, "unsupported daemon state schema 99"},
		{"queue schema", func(s *State) { s.Queue.SchemaVersion = 99 }, "unsupported daemon queue schema 99"},
		{"listener", func(s *State) { s.Listen = "" }, "daemon state has no listener"},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			broken := valid
			check.mutate(&broken)
			err := broken.Valid()
			if err == nil || !strings.Contains(err.Error(), check.want) {
				t.Fatalf("Valid() = %v, want %q", err, check.want)
			}
			if saveErr := (Store{Path: filepath.Join(t.TempDir(), "state.json")}).Save(broken); saveErr == nil {
				t.Fatal("Save() accepted an invalid state")
			}
		})
	}
}

// TestDqCovNewStartingRepairsLegacyQueueSchema proves a pre-schema queue
// record from an older daemon is upgraded rather than rejected, and that the
// handoff pointer is only recorded when a previous executable is known.
func TestDqCovNewStartingRepairsLegacyQueueSchema(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	legacy := State{Queue: Queue{Generation: 41, OwnerToken: "legacy-owner"}}
	next := NewStarting(&legacy, "127.0.0.1:9000", Provenance{SHA256: "new"}, "new-owner", now)
	if next.Queue.SchemaVersion != QueueSchemaVersion {
		t.Fatalf("queue schema = %d, want %d", next.Queue.SchemaVersion, QueueSchemaVersion)
	}
	if next.Queue.Generation != 42 {
		t.Fatalf("generation = %d, want 42", next.Queue.Generation)
	}
	if next.Queue.HandoffFrom != nil || next.Queue.HandoffAt != nil {
		t.Fatalf("handoff recorded without a previous executable: %#v", next.Queue)
	}
	if next.Status != StatusStarting || !next.StartedAt.Equal(now) || !next.UpdatedAt.Equal(now) {
		t.Fatalf("fresh state = %#v", next)
	}
}

func TestDqCovStateTransitionsRecordStatusPIDAndTimestamp(t *testing.T) {
	start := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	state := NewStarting(nil, "127.0.0.1:8766", Provenance{Executable: "/wb"}, "owner", start)

	state.MarkStartingPID(4242, start.Add(time.Second))
	if state.Status != StatusStarting || state.PID != 4242 || !state.UpdatedAt.Equal(start.Add(time.Second)) {
		t.Fatalf("MarkStartingPID = %#v", state)
	}
	state.MarkReady(4242, start.Add(2*time.Second))
	if state.Status != StatusReady || state.PID != 4242 || !state.UpdatedAt.Equal(start.Add(2*time.Second)) {
		t.Fatalf("MarkReady = %#v", state)
	}
	state.MarkDraining(start.Add(3 * time.Second))
	if state.Status != StatusDraining || state.PID != 4242 || !state.UpdatedAt.Equal(start.Add(3*time.Second)) {
		t.Fatalf("MarkDraining = %#v", state)
	}
	state.MarkStopped(start.Add(4 * time.Second))
	if state.Status != StatusStopped || state.PID != 0 || !state.UpdatedAt.Equal(start.Add(4*time.Second)) {
		t.Fatalf("MarkStopped = %#v", state)
	}
}

func TestDqCovStoreLoadReportsMissingCorruptAndUnreadableState(t *testing.T) {
	directory := t.TempDir()
	store := Store{Path: filepath.Join(directory, "state.json")}

	if state, ok, err := store.Load(); ok || err != nil || state != (State{}) {
		t.Fatalf("Load() on a missing file = %#v, %t, %v", state, ok, err)
	}

	if err := os.WriteFile(store.Path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Load(); ok || err == nil || !strings.Contains(err.Error(), "decode daemon state") {
		t.Fatalf("Load() on corrupt JSON = %t, %v", ok, err)
	}

	invalid := NewStarting(nil, "127.0.0.1:8766", Provenance{Executable: "/wb"}, "owner", time.Unix(1, 0))
	invalid.SchemaVersion = 77
	contents, err := json.Marshal(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Load(); ok || err == nil || !strings.Contains(err.Error(), "unsupported daemon state schema") {
		t.Fatalf("Load() on an invalid state = %t, %v", ok, err)
	}

	directoryPath := filepath.Join(directory, "as-directory.json")
	if err := os.Mkdir(directoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := (Store{Path: directoryPath}).Load(); ok || err == nil || strings.Contains(err.Error(), "decode") {
		t.Fatalf("Load() on a directory = %t, %v", ok, err)
	}
}

func TestDqCovStoreSaveRejectsUnusableDirectories(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := NewStarting(nil, "127.0.0.1:8766", Provenance{Executable: "/wb"}, "owner", time.Unix(1, 0))
	err := (Store{Path: filepath.Join(file, "nested", "state.json")}).Save(state)
	if err == nil {
		t.Fatal("Save() succeeded under a regular file")
	}

	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("POSIX directory permissions are not enforced for this account")
	}
	readOnly := filepath.Join(t.TempDir(), "read-only")
	if err := os.Mkdir(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })
	err = (Store{Path: filepath.Join(readOnly, "state.json")}).Save(state)
	if err == nil {
		t.Fatal("Save() succeeded in a directory that cannot create temporary files")
	}
	if _, statErr := os.Stat(filepath.Join(readOnly, "state.json")); !os.IsNotExist(statErr) {
		t.Fatalf("failed Save left a state file behind: %v", statErr)
	}
}

// TestDqCovStoreSaveRejectsStateThatCannotBeEncoded proves a state whose
// timestamps fall outside the RFC 3339 range is rejected before any file is
// created, so a failed save cannot publish a partial record.
func TestDqCovStoreSaveRejectsStateThatCannotBeEncoded(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "state.json")
	state := NewStarting(nil, "127.0.0.1:8766", Provenance{Executable: "/wb"}, "owner", time.Unix(1, 0))
	if err := state.Valid(); err != nil {
		t.Fatalf("baseline state is invalid: %v", err)
	}
	state.StartedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	err := (Store{Path: path}).Save(state)
	if err == nil || !strings.Contains(err.Error(), "year outside of range") {
		t.Fatalf("Save() with an unencodable timestamp = %v", err)
	}
	if entries, readErr := os.ReadDir(directory); readErr != nil || len(entries) != 0 {
		t.Fatalf("failed Save left %d entries behind: %v", len(entries), readErr)
	}
}

// TestDqCovProvenanceForExecutableHashesResolvedBinary checks that provenance
// records the resolved path and the exact digest of the bytes on disk, so a
// swapped binary cannot masquerade as the trusted generation.
func TestDqCovProvenanceForExecutableHashesResolvedBinary(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "wb")
	contents := []byte("#!/bin/sh\necho wb\n")
	if err := os.WriteFile(executable, contents, 0o700); err != nil {
		t.Fatal(err)
	}
	provenance, err := ProvenanceForExecutable(executable, "1.2.3", "rev-1", "built-today")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(contents)
	if provenance.Executable != resolved {
		t.Fatalf("Executable = %q, want resolved %q", provenance.Executable, resolved)
	}
	if provenance.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("SHA256 = %q, want %q", provenance.SHA256, hex.EncodeToString(digest[:]))
	}
	if provenance.Version != "1.2.3" || provenance.Revision != "rev-1" || provenance.Built != "built-today" {
		t.Fatalf("metadata = %#v", provenance)
	}

	if _, err := ProvenanceForExecutable(filepath.Join(t.TempDir(), "absent"), "1", "2", "3"); err == nil || !strings.Contains(err.Error(), "read WB executable") {
		t.Fatalf("ProvenanceForExecutable(missing) = %v", err)
	}
}
