package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
)

// This file adds coverage for internal/session without changing production
// code. Every test asserts observable behaviour: the record or error the
// package returns, and the files it leaves behind. Helpers introduced here are
// prefixed gpCov to stay out of the way of the package's own tests.

func TestGpCovProcessAliveAndLookupRejectNonPositivePIDs(t *testing.T) {
	t.Parallel()
	if !ProcessAlive(os.Getpid()) {
		t.Fatal("ProcessAlive(this process) = false, want true")
	}
	for _, pid := range []int{0, -1, -4242, 1 << 30} {
		if ProcessAlive(pid) {
			t.Fatalf("ProcessAlive(%d) = true, want false", pid)
		}
	}
	dir := filepath.Join(t.TempDir(), "sessions")
	for _, pid := range []int{0, -1, -4242} {
		if record, ok := Lookup(dir, pid); ok {
			t.Fatalf("Lookup(%d) = (%#v, true), want no session for a non-positive PID", pid, record)
		}
	}
}

func TestGpCovRegisterRejectsAConflictingHarnessIdentity(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	_, err := Register(dir, Record{PID: os.Getpid(), NativeHarnessID: "native-1", AgentID: "legacy-1"})
	if err == nil {
		t.Fatal("Register accepted a record whose native harness ID conflicts with its legacy agent ID")
	}
	if !strings.Contains(err.Error(), "native-1") || !strings.Contains(err.Error(), "legacy-1") {
		t.Fatalf("conflict error = %q, want both declared identities named", err)
	}

	// The legacy spelling on its own is still accepted and promoted.
	written, err := Register(dir, Record{PID: os.Getpid(), AgentID: "legacy-only"})
	if err != nil {
		t.Fatalf("Register(legacy agent ID): %v", err)
	}
	if written.NativeHarnessID != "legacy-only" || written.AgentID != "legacy-only" {
		t.Fatalf("written = %#v, want the legacy ID promoted to the native harness ID", written)
	}
}

func TestGpCovRegisterReportsDirectoryAndRecordWriteFailures(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	blocked := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Register(blocked, Record{PID: os.Getpid(), Runtime: "codex"}); err == nil {
		t.Fatal("Register reported success with a regular file where its directory belongs")
	} else if !strings.Contains(err.Error(), "create session directory") {
		t.Fatalf("error = %q, want a session-directory failure", err)
	}

	dir := filepath.Join(root, "sessions")
	if err := os.MkdirAll(recordPath(dir, os.Getpid()), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Register(dir, Record{PID: os.Getpid(), Runtime: "codex"}); err == nil {
		t.Fatal("Register reported success with a directory at the record path")
	} else if !strings.Contains(err.Error(), "write session record") {
		t.Fatalf("error = %q, want a session-record write failure", err)
	}
}

func TestGpCovMarkParkedRejectsAnUnregisteredPID(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	if _, err := MarkParked(dir, 424242, "gp-park"); err == nil {
		t.Fatal("MarkParked accepted a PID that never registered")
	} else if !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("error = %q, want a not-registered refusal", err)
	}
}

func TestGpCovMarkParkedIsIdempotentForTheSameParkedSession(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	registered, err := Register(dir, Record{
		PID: os.Getpid(), WBSessionID: "wbs-gp-park", Runtime: "codex",
		Lifecycle: "parked", ParkedSessionID: "gp-park-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	record, err := MarkParked(dir, registered.PID, "gp-park-1")
	if err != nil {
		t.Fatalf("re-parking with the identical parked session ID: %v", err)
	}
	if record.Lifecycle != "parked" || record.ParkedSessionID != "gp-park-1" {
		t.Fatalf("record = %#v, want the already-parked projection", record)
	}
	if _, err := MarkParked(dir, registered.PID, "gp-park-2"); err == nil {
		t.Fatal("MarkParked re-parked a session under a different parked session ID")
	} else if !strings.Contains(err.Error(), "already parked as gp-park-1") {
		t.Fatalf("error = %q, want the existing parked session ID named", err)
	}
}

func TestGpCovMarkParkedRefusesAStaleParkedMarker(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	registered, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: "wbs-gp-marker", Runtime: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MarkParked(dir, registered.PID, "gp-park-same"); err != nil {
		t.Fatalf("MarkParked: %v", err)
	}

	// The marker outlives the returned record, so a retry must read it back and
	// answer from it rather than writing a second marker.
	record, err := MarkParked(dir, registered.PID, "gp-park-same")
	if err != nil {
		t.Fatalf("retrying against an identical marker: %v", err)
	}
	if record.Lifecycle != "parked" || record.ParkedSessionID != "gp-park-same" {
		t.Fatalf("record = %#v, want the marker's parked projection", record)
	}
	if _, err := MarkParked(dir, registered.PID, "gp-park-other"); err == nil {
		t.Fatal("MarkParked replaced a parked session's marker")
	} else if !strings.Contains(err.Error(), "already parked") {
		t.Fatalf("error = %q, want an already-parked refusal", err)
	}

	// A corrupt marker is not a licence to park again either.
	if err := os.WriteFile(parkedMarkerPath(dir, registered.WBSessionID), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MarkParked(dir, registered.PID, "gp-park-other"); err == nil {
		t.Fatal("MarkParked treated an unreadable marker as absent")
	} else if !strings.Contains(err.Error(), "already parked") {
		t.Fatalf("error = %q, want an already-parked refusal", err)
	}
}

func TestGpCovMarkParkedRejectsASessionThatAlreadyResumed(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	registered, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: "wbs-gp-parked-then-resumed", Runtime: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MarkParked(dir, registered.PID, "gp-park-r"); err != nil {
		t.Fatal(err)
	}
	if _, err := MarkResumed(dir, registered.PID, "gp-park-r", "wbs-gp-successor"); err != nil {
		t.Fatal(err)
	}

	if _, err := MarkParked(dir, registered.PID, "gp-park-r2"); err == nil {
		t.Fatal("MarkParked re-parked a session whose resumed marker already exists")
	} else if !strings.Contains(err.Error(), "already resumed") {
		t.Fatalf("error = %q, want an already-resumed refusal", err)
	}
}

func TestGpCovMarkParkedRejectsAResumedRegistrationWithoutAMarker(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	registered, err := Register(dir, Record{
		PID: os.Getpid(), WBSessionID: "wbs-gp-resumed-only", Runtime: "codex", Lifecycle: "resumed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MarkParked(dir, registered.PID, "gp-park-x"); err == nil {
		t.Fatal("MarkParked parked a registration already projected as resumed")
	} else if !strings.Contains(err.Error(), "already resumed") {
		t.Fatalf("error = %q, want an already-resumed refusal", err)
	}
}

func TestGpCovMarkParkedRequiresAParkedSessionID(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	registered, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: "wbs-gp-no-parked-id", Runtime: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	for _, parkedID := range []string{"", "   "} {
		if _, err := MarkParked(dir, registered.PID, parkedID); err == nil {
			t.Fatalf("MarkParked accepted parked session ID %q", parkedID)
		} else if !strings.Contains(err.Error(), "parked session ID is required") {
			t.Fatalf("error = %q, want a required-parked-session-ID refusal", err)
		}
	}
}

func TestGpCovMarkParkedReportsALifecycleDirectoryThatIsAFile(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	registered, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: "wbs-gp-lifecycle-file", Runtime: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lifecycle"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := MarkParked(dir, registered.PID, "gp-park"); err == nil {
		t.Fatal("MarkParked reported success with a regular file where its marker directory belongs")
	}
	if _, err := os.Stat(parkedMarkerPath(dir, registered.WBSessionID)); err == nil {
		t.Fatal("a parked marker appeared despite the lifecycle directory failure")
	}
}

func TestGpCovMarkParkedReportsAParkedMarkerThatCannotBeCreated(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	// A session ID that names a missing subdirectory makes marker creation
	// itself impossible while the directory above it is still creatable.
	registered, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: "gp/nested", Runtime: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MarkParked(dir, registered.PID, "gp-park"); err == nil {
		t.Fatal("MarkParked reported success without writing its marker")
	} else if !strings.Contains(err.Error(), "record parked session lifecycle") {
		t.Fatalf("error = %q, want a parked-marker write failure", err)
	}
}

func TestGpCovMarkResumedRejectsAnUnregisteredPID(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	if _, err := MarkResumed(dir, 424242, "gp-park", "wbs-gp-successor"); err == nil {
		t.Fatal("MarkResumed accepted a PID that never registered")
	} else if !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("error = %q, want a not-registered refusal", err)
	}
}

func TestGpCovMarkResumedRequiresAParkedSource(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	registered, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: "wbs-gp-resume-src", Runtime: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MarkResumed(dir, registered.PID, "gp-park", "wbs-gp-successor"); err == nil {
		t.Fatal("MarkResumed resumed a session that was never parked")
	} else if !strings.Contains(err.Error(), "is not parked as gp-park") {
		t.Fatalf("error = %q, want a not-parked-as refusal", err)
	}

	if _, err := MarkParked(dir, registered.PID, "gp-park-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := MarkResumed(dir, registered.PID, "gp-park-2", "wbs-gp-successor"); err == nil {
		t.Fatal("MarkResumed resumed a session parked under a different parked session ID")
	} else if !strings.Contains(err.Error(), "is not parked as gp-park-2") {
		t.Fatalf("error = %q, want a not-parked-as refusal", err)
	}
}

func TestGpCovMarkResumedRequiresADistinctSuccessor(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	registered, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: "wbs-gp-succ-src", Runtime: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := MarkParked(dir, registered.PID, "gp-park"); err != nil {
		t.Fatal(err)
	}
	for _, successor := range []string{"", "   ", registered.WBSessionID} {
		if _, err := MarkResumed(dir, registered.PID, "gp-park", successor); err == nil {
			t.Fatalf("MarkResumed accepted successor WB session ID %q", successor)
		} else if !strings.Contains(err.Error(), "distinct successor") {
			t.Fatalf("error = %q, want a distinct-successor refusal", err)
		}
	}

	// A genuine successor still resumes, proving the refusals above were about
	// the successor alone.
	record, err := MarkResumed(dir, registered.PID, "gp-park", "wbs-gp-successor")
	if err != nil {
		t.Fatalf("MarkResumed with a distinct successor: %v", err)
	}
	if record.Lifecycle != "resumed" || record.ParkedSessionID != "gp-park" {
		t.Fatalf("record = %#v, want the resumed projection", record)
	}
}

func TestGpCovLifecycleMarkerReadersRejectCorruptPayloads(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(filepath.Join(dir, "lifecycle"), 0o755); err != nil {
		t.Fatal(err)
	}
	const wbsid = "wbs-gp-reader"
	at := time.Now().UTC()

	parkedPath := parkedMarkerPath(dir, wbsid)
	if err := os.WriteFile(parkedPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readParkedMarker(dir, wbsid); ok {
		t.Fatal("readParkedMarker accepted malformed JSON")
	}
	parkedVariants := map[string]parkedLifecycleMarker{
		"an unknown schema version": {SchemaVersion: 2, WBSessionID: wbsid, ParkedSessionID: "p", At: at},
		"a different session ID":    {SchemaVersion: 1, WBSessionID: "wbs-other", ParkedSessionID: "p", At: at},
		"no parked session ID":      {SchemaVersion: 1, WBSessionID: wbsid, At: at},
		"no timestamp":              {SchemaVersion: 1, WBSessionID: wbsid, ParkedSessionID: "p"},
	}
	for name, marker := range parkedVariants {
		raw, err := json.Marshal(marker)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(parkedPath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, ok := readParkedMarker(dir, wbsid); ok {
			t.Fatalf("readParkedMarker accepted a marker with %s", name)
		}
	}
	raw, err := json.Marshal(parkedLifecycleMarker{SchemaVersion: 1, WBSessionID: wbsid, ParkedSessionID: "p", At: at})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parkedPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if marker, ok := readParkedMarker(dir, wbsid); !ok || marker.ParkedSessionID != "p" {
		t.Fatalf("readParkedMarker(valid) = (%#v, %t), want the written marker", marker, ok)
	}

	resumedPath := resumedMarkerPath(dir, wbsid)
	if err := os.WriteFile(resumedPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readResumedMarker(dir, wbsid); ok {
		t.Fatal("readResumedMarker accepted malformed JSON")
	}
	resumedVariants := map[string]resumedLifecycleMarker{
		"an unknown schema version": {SchemaVersion: 2, WBSessionID: wbsid, ParkedSessionID: "p", SuccessorWBSessionID: "s", At: at},
		"a different session ID":    {SchemaVersion: 1, WBSessionID: "wbs-other", ParkedSessionID: "p", SuccessorWBSessionID: "s", At: at},
		"no parked session ID":      {SchemaVersion: 1, WBSessionID: wbsid, SuccessorWBSessionID: "s", At: at},
		"no successor":              {SchemaVersion: 1, WBSessionID: wbsid, ParkedSessionID: "p", At: at},
		"no timestamp":              {SchemaVersion: 1, WBSessionID: wbsid, ParkedSessionID: "p", SuccessorWBSessionID: "s"},
	}
	for name, marker := range resumedVariants {
		raw, err := json.Marshal(marker)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(resumedPath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, ok := readResumedMarker(dir, wbsid); ok {
			t.Fatalf("readResumedMarker accepted a marker with %s", name)
		}
	}
	raw, err = json.Marshal(resumedLifecycleMarker{SchemaVersion: 1, WBSessionID: wbsid, ParkedSessionID: "p", SuccessorWBSessionID: "s", At: at})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resumedPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if marker, ok := readResumedMarker(dir, wbsid); !ok || marker.SuccessorWBSessionID != "s" {
		t.Fatalf("readResumedMarker(valid) = (%#v, %t), want the written marker", marker, ok)
	}
}

func TestGpCovParkedAndResumedRejectBlankIdentities(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	if parked(dir, "") {
		t.Fatal(`parked("") = true, want false`)
	}
	if resumed(dir, "") {
		t.Fatal(`resumed("") = true, want false`)
	}
	if parked(dir, "wbs-gp-never-written") {
		t.Fatal("parked() reported a marker that was never written")
	}
	if resumed(dir, "wbs-gp-never-written") {
		t.Fatal("resumed() reported a marker that was never written")
	}
}

func TestGpCovListAndPruneReportAnUnreadableDirectory(t *testing.T) {
	t.Parallel()
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if views, err := List(blocked); err == nil {
		t.Fatalf("List(%s) = (%v, nil), want an error", blocked, views)
	}
	if removed, err := Prune(blocked); err == nil {
		t.Fatalf("Prune(%s) = (%d, nil), want an error", blocked, removed)
	}
}

func TestGpCovResolveForProcessStopsWhenAProcessCannotBeInspected(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	// No PID near 2^30 can exist, so the ancestor walk must give up rather than
	// invent an owner.
	if record, ok := ResolveForProcess(dir, 1<<30); ok {
		t.Fatalf("ResolveForProcess = (%#v, true), want no owner for an uninspectable PID", record)
	}
}

func TestGpCovResolveOrRegisterRefusesAResumedRegistrationAtTheSamePID(t *testing.T) {
	t.Parallel()
	testenv.Isolate(t)
	dir := filepath.Join(t.TempDir(), "sessions")
	if _, err := Register(dir, Record{
		PID: os.Getpid(), WBSessionID: "wbs-gp-resumed-collision", Runtime: "codex", Lifecycle: "resumed",
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(recordPath(dir, os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}

	_, registeredAtPark, err := ResolveOrRegisterForProcess(dir, os.Getpid(), AutoRegisterHints{PID: os.Getpid()})
	if err == nil {
		t.Fatal("park-time registration overwrote a resumed session's registry row")
	}
	if registeredAtPark {
		t.Fatal("a refused registration was reported as registered at park")
	}
	if !strings.Contains(err.Error(), "wbs-gp-resumed-collision") || !strings.Contains(err.Error(), "resumed") {
		t.Fatalf("error = %q, want the resumed registration named", err)
	}
	after, err := os.ReadFile(recordPath(dir, os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("a refused park-time registration mutated the existing record")
	}
}

func TestGpCovResolveOrRegisterReportsARegistrationFailure(t *testing.T) {
	t.Parallel()
	testenv.Isolate(t)
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	record, registeredAtPark, err := ResolveOrRegisterForProcess(blocked, os.Getpid(), AutoRegisterHints{PID: os.Getpid()})
	if err == nil {
		t.Fatalf("ResolveOrRegisterForProcess = (%#v, %t, nil), want a registration failure", record, registeredAtPark)
	}
	if registeredAtPark {
		t.Fatal("a failed registration was reported as registered at park")
	}
	if !strings.Contains(err.Error(), "create session directory") {
		t.Fatalf("error = %q, want the underlying directory failure", err)
	}
}

func TestGpCovLookupByWBSessionIDRejectsBlankIDsAndUnreadableDirectories(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	for _, wanted := range []string{"", "   "} {
		if record, ok := LookupByWBSessionID(dir, wanted); ok {
			t.Fatalf("LookupByWBSessionID(%q) = (%#v, true), want no match for a blank ID", wanted, record)
		}
	}

	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if record, ok := LookupByWBSessionID(blocked, "wbs-gp-any"); ok {
		t.Fatalf("LookupByWBSessionID over an unreadable directory = (%#v, true), want no match", record)
	}
}

func TestGpCovLookupExactRejectsInvalidInputs(t *testing.T) {
	t.Parallel()
	target := filepath.Join(t.TempDir(), "sessions")
	for _, pid := range []int{0, -1, -4242} {
		if _, _, err := LookupExact(target, pid); err == nil {
			t.Fatalf("LookupExact(pid=%d) succeeded, want a positive-PID refusal", pid)
		} else if !strings.Contains(err.Error(), "positive PID") {
			t.Fatalf("error = %q, want a positive-PID refusal", err)
		}
	}
	if _, _, err := LookupExact("relative/sessions", os.Getpid()); err == nil {
		t.Fatal("LookupExact accepted a relative session directory")
	} else if !strings.Contains(err.Error(), "clean absolute path") {
		t.Fatalf("error = %q, want a clean-absolute-path refusal", err)
	}
	if _, _, err := LookupExact(filepath.Join(target, "absent"), os.Getpid()); err == nil {
		t.Fatal("LookupExact accepted a session directory that does not exist")
	} else if !strings.Contains(err.Error(), "open exact session directory") {
		t.Fatalf("error = %q, want a directory-open failure", err)
	}
}

func TestGpCovLookupExactRejectsMalformedRecords(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	pid := os.Getpid()
	write := func(raw []byte) {
		t.Helper()
		path := recordPath(dir, pid)
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write([]byte("{not json"))
	if _, _, err := LookupExact(dir, pid); err == nil {
		t.Fatal("LookupExact accepted an undecodable record")
	} else if !strings.Contains(err.Error(), "decode exact session record") {
		t.Fatalf("error = %q, want a record-decode failure", err)
	}

	raw, err := json.Marshal(Record{PID: pid + 1, WBSessionID: "wbs-gp-lookupexact", StartedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	write(raw)
	if _, _, err := LookupExact(dir, pid); err == nil {
		t.Fatal("LookupExact accepted a record whose PID does not match its filename")
	} else if !strings.Contains(err.Error(), "does not match filename PID") {
		t.Fatalf("error = %q, want a filename/PID mismatch refusal", err)
	}
}

func TestGpCovIsRuntimeProcessRefusesProcessesThatAreNotTheDeclaredRuntime(t *testing.T) {
	t.Parallel()
	if IsRuntimeProcess(os.Getpid(), "codex") {
		t.Fatal("this test binary was mistaken for the Codex app-server")
	}
	for _, pid := range []int{0, -1, -4242} {
		if IsRuntimeProcess(pid, "codex") {
			t.Fatalf("IsRuntimeProcess(%d, codex) = true, want false", pid)
		}
	}
	for _, runtime := range []string{"", "   ", "claude-code", "gemini-cli"} {
		evidence := ProcessEvidence{Executable: "/usr/local/bin/codex", Args: []string{"codex", "app-server"}}
		if processEvidenceMatchesRuntime(evidence, runtime) {
			t.Fatalf("processEvidenceMatchesRuntime(codex app-server, %q) = true, want false", runtime)
		}
	}
}
