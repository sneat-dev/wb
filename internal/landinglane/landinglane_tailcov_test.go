package landinglane

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
)

// tailCovLaneHome returns a fresh WB home directory for lane records.
func tailCovLaneHome(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func tailCovWantError(t *testing.T, what string, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: got nil error, want one containing %q", what, want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("%s: error = %q, want it to contain %q", what, err, want)
	}
}

// TestTailCovConflictErrorRendersModelAndFallbackDetails covers the optional
// fields the refusal message renders, including the model-qualified runtime
// and the two placeholder strings.
func TestTailCovConflictErrorRendersModelAndFallbackDetails(t *testing.T) {
	record := Record{
		Lane:       "lane-acme-app-main-deadbeef",
		Repository: "acme/app",
		Target:     "main",
		Owner: Owner{
			WBSessionID: "wbs-a",
			PID:         4242,
			Model:       "opus-4",
		},
	}
	message := (&ConflictError{Record: record}).Error()
	for _, want := range []string{
		"lane-acme-app-main-deadbeef", "acme/app", "main", "wbs-a", "4242",
		"opus-4", "unknown time", "a landing command", "no receipt written yet",
		"`wb session recall wbs-a`", "--take-over-lane --lane-reason",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("conflict message %q is missing %q", message, want)
		}
	}

	record.Owner.Runtime = "codex"
	record.Owner.Command = "wb pr land"
	record.Owner.ReceiptPath = "/tmp/receipt.json"
	record.Owner.AcquiredAt = time.Date(2026, 9, 8, 1, 2, 3, 0, time.UTC)
	message = (&ConflictError{Record: record}).Error()
	for _, want := range []string{"codex/opus-4", "wb pr land", "/tmp/receipt.json", "2026-09-08T01:02:03Z"} {
		if !strings.Contains(message, want) {
			t.Errorf("conflict message %q is missing %q", message, want)
		}
	}
	if strings.Contains(message, "unknown time") || strings.Contains(message, "a landing command") {
		t.Errorf("placeholder text leaked into a fully-populated refusal: %q", message)
	}
}

// TestTailCovCorruptRecordErrorNamesPathAndUnwraps covers the corrupt-record
// error's message and its Unwrap, which callers use to inspect the parse
// failure underneath.
func TestTailCovCorruptRecordErrorNamesPathAndUnwraps(t *testing.T) {
	home := tailCovLaneHome(t)
	lane := LaneID("acme/app", "main")
	if err := os.MkdirAll(filepath.Join(home, DirName), 0o700); err != nil {
		t.Fatal(err)
	}
	recordFile := filepath.Join(home, DirName, lane+".json")
	if err := os.WriteFile(recordFile, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Acquire(home, AcquireRequest{
		Repository: "acme/app", Target: "main",
		Self: Owner{WBSessionID: "wbs-a", PID: 111},
		Now:  fixedNow(time.Unix(1000, 0)),
	})
	var corrupt *CorruptRecordError
	if !errors.As(err, &corrupt) {
		t.Fatalf("expected a CorruptRecordError, got %v", err)
	}
	message := err.Error()
	for _, want := range []string{recordFile, "is corrupt", "refusing to grant the lane", "--take-over-lane --lane-reason"} {
		if !strings.Contains(message, want) {
			t.Errorf("corrupt-record message %q is missing %q", message, want)
		}
	}
	inner := errors.Unwrap(corrupt)
	if inner == nil {
		t.Fatal("CorruptRecordError must unwrap to the underlying parse failure")
	}
	if !strings.Contains(inner.Error(), "invalid character") {
		t.Errorf("unwrapped error = %q, want the JSON parser's complaint", inner)
	}
}

// TestTailCovAcquireValidatesInputs covers the fast input guards: a lane
// always needs a repository, a target, and the acquiring session's identity.
func TestTailCovAcquireValidatesInputs(t *testing.T) {
	home := tailCovLaneHome(t)
	base := AcquireRequest{
		Repository: "acme/app",
		Target:     "main",
		Self:       Owner{WBSessionID: "wbs-a", PID: 111},
		Now:        fixedNow(time.Unix(1000, 0)),
	}
	cases := []struct {
		name   string
		mutate func(*AcquireRequest)
		want   string
	}{
		{"missing repository", func(r *AcquireRequest) { r.Repository = "   " }, "requires both a repository and a target branch"},
		{"missing target", func(r *AcquireRequest) { r.Target = "" }, "requires both a repository and a target branch"},
		{"missing session ID", func(r *AcquireRequest) { r.Self.WBSessionID = "  " }, "requires the acquiring session's WB session ID"},
	}
	for _, tc := range cases {
		request := base
		tc.mutate(&request)
		_, err := Acquire(home, request)
		tailCovWantError(t, tc.name, err, tc.want)
		if entries, readErr := os.ReadDir(home); readErr != nil {
			t.Fatal(readErr)
		} else if len(entries) != 0 {
			t.Fatalf("%s: a rejected request wrote to the WB home: %v", tc.name, entries)
		}
	}
}

// TestTailCovAcquireUsesSessionRegistryLiveness drives Acquire with no
// IsOwnerLive override so the package's own registry-backed liveness check
// decides: the registered live owner is protected, an unregistered one is not.
func TestTailCovAcquireUsesSessionRegistryLiveness(t *testing.T) {
	home := tailCovLaneHome(t)
	sessionDir := t.TempDir()
	pid := os.Getpid()
	if _, err := session.Register(sessionDir, session.Record{
		PID: pid, WBSessionID: "wbs-live", StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("register session: %v", err)
	}

	if _, err := Acquire(home, AcquireRequest{
		Repository: "acme/app", Target: "main",
		Self:       Owner{WBSessionID: "wbs-live", PID: pid},
		SessionDir: sessionDir,
		Now:        fixedNow(time.Unix(1000, 0)),
	}); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	// The registry confirms the holder is live, so a different session is refused.
	_, err := Acquire(home, AcquireRequest{
		Repository: "acme/app", Target: "main",
		Self:       Owner{WBSessionID: "wbs-other", PID: pid + 1},
		SessionDir: sessionDir,
		Now:        fixedNow(time.Unix(1001, 0)),
	})
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected the registry-backed check to refuse a live owner, got %v", err)
	}
	if conflict.Record.Owner.WBSessionID != "wbs-live" {
		t.Fatalf("conflict names %q, want wbs-live", conflict.Record.Owner.WBSessionID)
	}

	// An empty registry reports the same holder as gone, so the lane moves.
	record, err := Acquire(home, AcquireRequest{
		Repository: "acme/app", Target: "main",
		Self:       Owner{WBSessionID: "wbs-other", PID: pid + 1},
		SessionDir: t.TempDir(),
		Now:        fixedNow(time.Unix(1001, 0)),
	})
	if err != nil {
		t.Fatalf("takeover with an empty registry: %v", err)
	}
	if !record.TakenOver || record.Owner.WBSessionID != "wbs-other" {
		t.Fatalf("expected an automatic takeover, got %+v", record)
	}
}

// TestTailCovDefaultOwnerLiveRequiresRegistryEntry pins every branch of the
// registry-backed liveness predicate directly.
func TestTailCovDefaultOwnerLiveRequiresRegistryEntry(t *testing.T) {
	pid := os.Getpid()
	if defaultOwnerLive("", Owner{PID: pid, WBSessionID: "wbs-x"}) {
		t.Error("an empty session directory must report not live")
	}
	if defaultOwnerLive(t.TempDir(), Owner{PID: 0, WBSessionID: "wbs-x"}) {
		t.Error("a non-positive PID must report not live")
	}
	if defaultOwnerLive(t.TempDir(), Owner{PID: pid, WBSessionID: "wbs-x"}) {
		t.Error("a PID with no registry record must report not live")
	}

	sessionDir := t.TempDir()
	if _, err := session.Register(sessionDir, session.Record{
		PID: pid, WBSessionID: "wbs-live", StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("register session: %v", err)
	}
	if !defaultOwnerLive(sessionDir, Owner{PID: pid, WBSessionID: "wbs-live"}) {
		t.Error("a registered live PID must report live")
	}
	if defaultOwnerLive(sessionDir, Owner{PID: pid, WBSessionID: "wbs-other"}) {
		t.Error("a mismatched session ID must report not live")
	}
	if !defaultOwnerLive(sessionDir, Owner{PID: pid}) {
		t.Error("an owner without a session ID must accept the registered live record")
	}
}

// TestTailCovAcquireNotesStaleHeartbeatOnDeadOwnerTakeover covers the note
// enrichment: a confirmed-dead owner whose last heartbeat is older than the
// stale window has that timestamp recorded for the reader.
func TestTailCovAcquireNotesStaleHeartbeatOnDeadOwnerTakeover(t *testing.T) {
	home := tailCovLaneHome(t)
	staleAfter := 10 * time.Minute
	if _, err := Acquire(home, AcquireRequest{
		Repository: "acme/app", Target: "main",
		Self: Owner{WBSessionID: "wbs-a", PID: 111}, IsOwnerLive: func(Owner) bool { return true },
		StaleAfter: staleAfter, Now: fixedNow(time.Unix(1000, 0)),
	}); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	record, err := Acquire(home, AcquireRequest{
		Repository: "acme/app", Target: "main",
		Self: Owner{WBSessionID: "wbs-b", PID: 222}, IsOwnerLive: func(Owner) bool { return false },
		StaleAfter: staleAfter, Now: fixedNow(time.Unix(1000, 0).Add(11 * time.Minute)),
	})
	if err != nil {
		t.Fatalf("takeover Acquire: %v", err)
	}
	if !record.TakenOver || record.TakeoverReason != "" {
		t.Fatalf("an automatic takeover must carry a note, not a reason: %+v", record)
	}
	if !strings.HasPrefix(record.TakeoverNote, "prior owner's WB session is no longer live") {
		t.Fatalf("takeover note = %q, want the dead-session explanation", record.TakeoverNote)
	}
	if !strings.Contains(record.TakeoverNote, "heartbeat stale since 1970-01-01T00:16:40Z") {
		t.Fatalf("takeover note = %q, want the stale heartbeat timestamp", record.TakeoverNote)
	}

	// A dead owner whose heartbeat is still fresh gets no stale suffix.
	home2 := tailCovLaneHome(t)
	if _, err := Acquire(home2, AcquireRequest{
		Repository: "acme/app", Target: "main",
		Self: Owner{WBSessionID: "wbs-a", PID: 111}, IsOwnerLive: func(Owner) bool { return true },
		StaleAfter: staleAfter, Now: fixedNow(time.Unix(1000, 0)),
	}); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	fresh, err := Acquire(home2, AcquireRequest{
		Repository: "acme/app", Target: "main",
		Self: Owner{WBSessionID: "wbs-b", PID: 222}, IsOwnerLive: func(Owner) bool { return false },
		StaleAfter: staleAfter, Now: fixedNow(time.Unix(1000, 0).Add(time.Second)),
	})
	if err != nil {
		t.Fatalf("takeover of a fresh dead owner: %v", err)
	}
	if fresh.TakeoverNote != "prior owner's WB session is no longer live" {
		t.Fatalf("takeover note = %q, want no stale suffix for a fresh heartbeat", fresh.TakeoverNote)
	}
}

// TestTailCovAcquireSurfacesUnreadableRecord proves a non-parse read failure
// is reported as an I/O error rather than being mistaken for corruption (or,
// worse, for an absent record).
func TestTailCovAcquireSurfacesUnreadableRecord(t *testing.T) {
	home := tailCovLaneHome(t)
	lane := LaneID("acme/app", "main")
	if err := os.MkdirAll(filepath.Join(home, DirName, lane+".json"), 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := Acquire(home, AcquireRequest{
		Repository: "acme/app", Target: "main",
		Self: Owner{WBSessionID: "wbs-a", PID: 111},
		Now:  fixedNow(time.Unix(1000, 0)),
	})
	tailCovWantError(t, "Acquire", err, "read landing lane record")
	var corrupt *CorruptRecordError
	if errors.As(err, &corrupt) {
		t.Fatalf("an unreadable record is not a corrupt one: %v", err)
	}
}

// tailCovAssertLaneEntryPointsFail runs all four lane entry points against a
// home whose lock plumbing is broken and asserts each reports it.
func tailCovAssertLaneEntryPointsFail(t *testing.T, home, want string) {
	t.Helper()
	now := fixedNow(time.Unix(1000, 0))

	_, err := Acquire(home, AcquireRequest{
		Repository: "acme/app", Target: "main",
		Self: Owner{WBSessionID: "wbs-a", PID: 111}, IsOwnerLive: func(Owner) bool { return true },
		Now: now,
	})
	tailCovWantError(t, "Acquire", err, want)

	tailCovWantError(t, "Heartbeat", Heartbeat(home, "acme/app", "main", "wbs-a", now), want)
	tailCovWantError(t, "Release", Release(home, "acme/app", "main", "wbs-a"), want)

	if _, _, err := Read(home, "acme/app", "main"); err == nil {
		t.Fatalf("Read: got nil error, want one containing %q", want)
	} else if !strings.Contains(err.Error(), want) {
		t.Fatalf("Read: error = %q, want it to contain %q", err, want)
	}
}

// TestTailCovLaneEntryPointsReportLockFileFailures covers both lock-plumbing
// failures: the lanes directory cannot be created, and the lock file itself
// cannot be opened.
func TestTailCovLaneEntryPointsReportLockFileFailures(t *testing.T) {
	blocked := tailCovLaneHome(t)
	if err := os.WriteFile(filepath.Join(blocked, DirName), []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tailCovAssertLaneEntryPointsFail(t, blocked, "create landing lane directory")

	occupied := tailCovLaneHome(t)
	lane := LaneID("acme/app", "main")
	if err := os.MkdirAll(filepath.Join(occupied, DirName, lane+".lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	tailCovAssertLaneEntryPointsFail(t, occupied, "open landing lane lock")
}

// TestTailCovHeartbeatAndReleaseIgnoreUnheldOrUnreadableLanes covers the
// quiet no-op path and the read-failure path shared by Heartbeat and Release.
func TestTailCovHeartbeatAndReleaseIgnoreUnheldOrUnreadableLanes(t *testing.T) {
	home := tailCovLaneHome(t)
	now := fixedNow(time.Unix(1000, 0))

	if err := Heartbeat(home, "acme/app", "main", "wbs-a", now); err != nil {
		t.Fatalf("Heartbeat on an unheld lane must be a no-op, got %v", err)
	}
	if err := Release(home, "acme/app", "main", "wbs-a"); err != nil {
		t.Fatalf("Release of an unheld lane must be a no-op, got %v", err)
	}

	lane := LaneID("acme/app", "main")
	recordPath := filepath.Join(home, DirName, lane+".json")
	if err := os.MkdirAll(filepath.Dir(recordPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	tailCovWantError(t, "Heartbeat on a corrupt record", Heartbeat(home, "acme/app", "main", "wbs-a", now), "corrupt")
	tailCovWantError(t, "Release of a corrupt record", Release(home, "acme/app", "main", "wbs-a"), "corrupt")
}

// TestTailCovHeartbeatRejectsOtherOwnersLane proves a heartbeat from a
// session that does not hold the lane leaves the record untouched.
func TestTailCovHeartbeatRejectsOtherOwnersLane(t *testing.T) {
	home := tailCovLaneHome(t)
	if _, err := Acquire(home, AcquireRequest{
		Repository: "acme/app", Target: "main",
		Self: Owner{WBSessionID: "wbs-a", PID: 111}, IsOwnerLive: func(Owner) bool { return true },
		Now: fixedNow(time.Unix(1000, 0)),
	}); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := Heartbeat(home, "acme/app", "main", "wbs-other", fixedNow(time.Unix(2000, 0))); err != nil {
		t.Fatalf("Heartbeat from a non-owner must be a no-op, got %v", err)
	}
	record, found, err := Read(home, "acme/app", "main")
	if err != nil || !found {
		t.Fatalf("Read: found=%v err=%v", found, err)
	}
	if !record.Owner.HeartbeatAt.Equal(time.Unix(1000, 0).UTC()) {
		t.Fatalf("a non-owner's heartbeat moved the record: %v", record.Owner.HeartbeatAt)
	}
}

// TestTailCovReadRecordSurfacesReadFailures covers the non-NotExist read
// failure path in readRecord.
func TestTailCovReadRecordSurfacesReadFailures(t *testing.T) {
	record, found, err := readRecord(t.TempDir())
	if err == nil {
		t.Fatal("readRecord of a directory must fail")
	}
	if found {
		t.Error("found = true for an unreadable record")
	}
	if record != (Record{}) {
		t.Errorf("record = %+v, want the zero value", record)
	}
	if !strings.Contains(err.Error(), "read landing lane record") {
		t.Errorf("error = %q, want the read wrapper", err)
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("an unreadable path must not be reported as a missing record: %v", err)
	}
}

// TestTailCovWriteRecordSurfacesEncodeWriteAndRenameFailures covers every
// writeRecord failure branch.
func TestTailCovWriteRecordSurfacesEncodeWriteAndRenameFailures(t *testing.T) {
	dir := t.TempDir()

	encodePath := filepath.Join(dir, "encode.json")
	err := writeRecord(encodePath, Record{Owner: Owner{AcquiredAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}})
	tailCovWantError(t, "writeRecord (encode)", err, "encode landing lane record")
	if _, statErr := os.Stat(encodePath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("an unencodable record must not be written: stat err = %v", statErr)
	}

	writePath := filepath.Join(dir, "write.json")
	if err := os.Mkdir(writePath+".tmp", 0o700); err != nil {
		t.Fatal(err)
	}
	err = writeRecord(writePath, Record{SchemaVersion: SchemaVersion, Lane: "lane-write"})
	tailCovWantError(t, "writeRecord (write)", err, "write landing lane record")

	renamePath := filepath.Join(dir, "rename.json")
	if err := os.MkdirAll(filepath.Join(renamePath, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	err = writeRecord(renamePath, Record{SchemaVersion: SchemaVersion, Lane: "lane-rename"})
	tailCovWantError(t, "writeRecord (rename)", err, "commit landing lane record")
}

// TestTailCovLaneIDSanitizesTruncatesAndHashes covers the lane id's stable
// readable-plus-hash shape, including the 48-character cap.
func TestTailCovLaneIDSanitizesTruncatesAndHashes(t *testing.T) {
	sum := sha256.Sum256([]byte("acme/app\x00main"))
	want := "lane-acme-app-main-" + hex.EncodeToString(sum[:6])
	if got := LaneID("acme/app", "main"); got != want {
		t.Fatalf("LaneID = %q, want %q", got, want)
	}
	if got := LaneID("acme/app", "main"); got != LaneID("acme/app", "main") {
		t.Fatalf("LaneID is not deterministic: %q then %q", got, LaneID("acme/app", "main"))
	}
	if LaneID("acme/app", "main") == LaneID("acme/app", "release") {
		t.Fatal("different targets must not share a lane id")
	}

	repository := strings.Repeat("owner.with_underscores and spaces/", 4)
	lane := LaneID(repository, "main")
	if !strings.HasPrefix(lane, "lane-") {
		t.Fatalf("lane id %q is missing the lane- prefix", lane)
	}
	if len(lane) != len("lane-")+48+1+12 {
		t.Fatalf("lane id %q has length %d, want a 48-character readable body plus a 12-character hash", lane, len(lane))
	}
	if lane[len("lane-")+48] != '-' {
		t.Fatalf("lane id %q is missing the separator after the readable body", lane)
	}
	if strings.ContainsAny(lane[:len("lane-")+48], "/._ ") {
		t.Fatalf("lane id %q kept filesystem-hostile characters in its readable body", lane)
	}
	if _, err := hex.DecodeString(lane[len(lane)-12:]); err != nil {
		t.Fatalf("lane id %q does not end in a hex digest: %v", lane, err)
	}
}
