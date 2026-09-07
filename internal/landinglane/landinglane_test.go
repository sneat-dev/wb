package landinglane

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixedNow(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func TestAcquireGrantsFreshLane(t *testing.T) {
	home := t.TempDir()
	record, err := Acquire(home, AcquireRequest{
		Repository:  "sneat-dev/wb",
		Target:      "main",
		Self:        Owner{WBSessionID: "wbs-a", PID: 111, Command: "wb pr land"},
		IsOwnerLive: func(Owner) bool { return true },
		Now:         fixedNow(time.Unix(1000, 0)),
	})
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if record.Owner.WBSessionID != "wbs-a" {
		t.Fatalf("owner = %+v", record.Owner)
	}
	if record.TakenOver {
		t.Fatalf("fresh lane must not report a takeover")
	}
}

func TestAcquireRefusesDifferentLiveSession(t *testing.T) {
	home := t.TempDir()
	if _, err := Acquire(home, AcquireRequest{
		Repository:  "sneat-dev/wb",
		Target:      "main",
		Self:        Owner{WBSessionID: "wbs-a", PID: 111, Command: "wb pr land"},
		IsOwnerLive: func(Owner) bool { return true },
		Now:         fixedNow(time.Unix(1000, 0)),
	}); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	_, err := Acquire(home, AcquireRequest{
		Repository:  "sneat-dev/wb",
		Target:      "main",
		Self:        Owner{WBSessionID: "wbs-b", PID: 222, Command: "wb worktree merge"},
		IsOwnerLive: func(Owner) bool { return true },
		Now:         fixedNow(time.Unix(1001, 0)),
	})
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected ConflictError, got %v", err)
	}
	if conflict.Record.Owner.WBSessionID != "wbs-a" {
		t.Fatalf("conflict names %q, want wbs-a", conflict.Record.Owner.WBSessionID)
	}
	message := conflict.Error()
	if !contains(message, "wbs-a") || !contains(message, "wb pr land") || !contains(message, "request-handoff") || !contains(message, "--take-over-lane") {
		t.Fatalf("conflict message missing required detail: %s", message)
	}
}

func TestAcquireAdmitsSameSessionAndRefreshes(t *testing.T) {
	home := t.TempDir()
	if _, err := Acquire(home, AcquireRequest{
		Repository:  "sneat-dev/wb",
		Target:      "main",
		Self:        Owner{WBSessionID: "wbs-a", PID: 111, Command: "wb pr land", ReceiptPath: ""},
		IsOwnerLive: func(Owner) bool { return true },
		Now:         fixedNow(time.Unix(1000, 0)),
	}); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	record, err := Acquire(home, AcquireRequest{
		Repository:  "sneat-dev/wb",
		Target:      "main",
		Self:        Owner{WBSessionID: "wbs-a", PID: 111, Command: "wb pr land", ReceiptPath: "/tmp/receipt.json"},
		IsOwnerLive: func(Owner) bool { return true },
		Now:         fixedNow(time.Unix(2000, 0)),
	})
	if err != nil {
		t.Fatalf("second Acquire (same session): %v", err)
	}
	if record.Owner.ReceiptPath != "/tmp/receipt.json" {
		t.Fatalf("receipt path not refreshed: %+v", record.Owner)
	}
	if !record.Owner.AcquiredAt.Equal(time.Unix(1000, 0).UTC()) {
		t.Fatalf("AcquiredAt should be preserved across a same-session refresh, got %v", record.Owner.AcquiredAt)
	}
	if !record.Owner.HeartbeatAt.Equal(time.Unix(2000, 0).UTC()) {
		t.Fatalf("HeartbeatAt should advance, got %v", record.Owner.HeartbeatAt)
	}
}

func TestAcquireTakesOverDeadOwner(t *testing.T) {
	home := t.TempDir()
	if _, err := Acquire(home, AcquireRequest{
		Repository:  "sneat-dev/wb",
		Target:      "main",
		Self:        Owner{WBSessionID: "wbs-a", PID: 111},
		IsOwnerLive: func(Owner) bool { return true },
		Now:         fixedNow(time.Unix(1000, 0)),
	}); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	record, err := Acquire(home, AcquireRequest{
		Repository:  "sneat-dev/wb",
		Target:      "main",
		Self:        Owner{WBSessionID: "wbs-b", PID: 222},
		IsOwnerLive: func(Owner) bool { return false }, // prior owner's session is gone
		Now:         fixedNow(time.Unix(1001, 0)),
	})
	if err != nil {
		t.Fatalf("takeover Acquire: %v", err)
	}
	if !record.TakenOver || record.PriorOwner == nil || record.PriorOwner.WBSessionID != "wbs-a" {
		t.Fatalf("expected recorded takeover from wbs-a, got %+v", record)
	}
	if record.Owner.WBSessionID != "wbs-b" {
		t.Fatalf("new owner = %+v", record.Owner)
	}
	if record.TakeoverNote == "" || record.TakeoverReason != "" {
		t.Fatalf("automatic takeover should carry a note, not an explicit reason: %+v", record)
	}
}

// TestAcquireRefusesLiveOwnerDespiteStaleHeartbeat proves the finding-1 fix:
// a live owner is never taken over implicitly, no matter how stale its
// heartbeat looks. A long CI wait can let a heartbeat go stale on its own
// even while the owning session is very much alive and actively landing —
// staleness is only ever a proxy for "the owner stopped running"; liveness
// (the session registry) is authoritative and must gate the takeover alone.
func TestAcquireRefusesLiveOwnerDespiteStaleHeartbeat(t *testing.T) {
	home := t.TempDir()
	if _, err := Acquire(home, AcquireRequest{
		Repository:  "sneat-dev/wb",
		Target:      "main",
		Self:        Owner{WBSessionID: "wbs-a", PID: 111},
		IsOwnerLive: func(Owner) bool { return true },
		StaleAfter:  10 * time.Minute,
		Now:         fixedNow(time.Unix(1000, 0)),
	}); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	// Still live by PID, and its heartbeat has not moved in over the stale
	// threshold: unlike a wedged process, this must NOT be treated as
	// abandoned without an explicit override.
	_, err := Acquire(home, AcquireRequest{
		Repository:  "sneat-dev/wb",
		Target:      "main",
		Self:        Owner{WBSessionID: "wbs-b", PID: 222},
		IsOwnerLive: func(Owner) bool { return true },
		StaleAfter:  10 * time.Minute,
		Now:         fixedNow(time.Unix(1000, 0).Add(11 * time.Minute)),
	})
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected a ConflictError refusing the live but stale owner, got %v", err)
	}
	if conflict.Record.Owner.WBSessionID != "wbs-a" {
		t.Fatalf("conflict names %q, want wbs-a", conflict.Record.Owner.WBSessionID)
	}
}

// TestAcquireExplicitTakeoverOverridesLiveStaleOwner proves the other half:
// --take-over-lane (with --lane-reason) still works against a live-but-stale
// owner, exactly as it does against a live-and-fresh one.
func TestAcquireExplicitTakeoverOverridesLiveStaleOwner(t *testing.T) {
	home := t.TempDir()
	if _, err := Acquire(home, AcquireRequest{
		Repository:  "sneat-dev/wb",
		Target:      "main",
		Self:        Owner{WBSessionID: "wbs-a", PID: 111},
		IsOwnerLive: func(Owner) bool { return true },
		StaleAfter:  10 * time.Minute,
		Now:         fixedNow(time.Unix(1000, 0)),
	}); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	record, err := Acquire(home, AcquireRequest{
		Repository:     "sneat-dev/wb",
		Target:         "main",
		Self:           Owner{WBSessionID: "wbs-b", PID: 222},
		IsOwnerLive:    func(Owner) bool { return true },
		StaleAfter:     10 * time.Minute,
		Now:            fixedNow(time.Unix(1000, 0).Add(11 * time.Minute)),
		TakeOver:       true,
		TakeoverReason: "predecessor stuck, founder approved handoff",
	})
	if err != nil {
		t.Fatalf("explicit takeover of live-but-stale owner: %v", err)
	}
	if record.Owner.WBSessionID != "wbs-b" || record.TakeoverReason == "" {
		t.Fatalf("explicit takeover not recorded: %+v", record)
	}
}

// TestAcquireTakesOverDeadOwnerRegardlessOfHeartbeatFreshness proves that a
// confirmed-dead owner is always taken over automatically, even with a
// heartbeat well inside the stale window — liveness is authoritative and
// gates the decision alone; a fresh heartbeat from a dead process (the last
// beat before it crashed) must never block the takeover.
func TestAcquireTakesOverDeadOwnerRegardlessOfHeartbeatFreshness(t *testing.T) {
	home := t.TempDir()
	if _, err := Acquire(home, AcquireRequest{
		Repository:  "sneat-dev/wb",
		Target:      "main",
		Self:        Owner{WBSessionID: "wbs-a", PID: 111},
		IsOwnerLive: func(Owner) bool { return true },
		StaleAfter:  30 * time.Minute,
		Now:         fixedNow(time.Unix(1000, 0)),
	}); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	record, err := Acquire(home, AcquireRequest{
		Repository:  "sneat-dev/wb",
		Target:      "main",
		Self:        Owner{WBSessionID: "wbs-b", PID: 222},
		IsOwnerLive: func(Owner) bool { return false }, // confirmed dead
		StaleAfter:  30 * time.Minute,
		Now:         fixedNow(time.Unix(1000, 0).Add(1 * time.Second)), // well inside the stale window
	})
	if err != nil {
		t.Fatalf("takeover of dead owner: %v", err)
	}
	if !record.TakenOver || record.Owner.WBSessionID != "wbs-b" {
		t.Fatalf("expected an automatic takeover, got %+v", record)
	}
}

func TestAcquireExplicitTakeoverRequiresReason(t *testing.T) {
	home := t.TempDir()
	if _, err := Acquire(home, AcquireRequest{
		Repository: "sneat-dev/wb", Target: "main",
		Self: Owner{WBSessionID: "wbs-a", PID: 111}, IsOwnerLive: func(Owner) bool { return true },
		Now: fixedNow(time.Unix(1000, 0)),
	}); err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	if _, err := Acquire(home, AcquireRequest{
		Repository: "sneat-dev/wb", Target: "main",
		Self: Owner{WBSessionID: "wbs-b", PID: 222}, IsOwnerLive: func(Owner) bool { return true },
		TakeOver: true, Now: fixedNow(time.Unix(1001, 0)),
	}); !errors.Is(err, ErrTakeoverReasonRequired) {
		t.Fatalf("expected ErrTakeoverReasonRequired, got %v", err)
	}

	record, err := Acquire(home, AcquireRequest{
		Repository: "sneat-dev/wb", Target: "main",
		Self: Owner{WBSessionID: "wbs-b", PID: 222}, IsOwnerLive: func(Owner) bool { return true },
		TakeOver: true, TakeoverReason: "predecessor stuck mid-CI, founder approved handoff", Now: fixedNow(time.Unix(1001, 0)),
	})
	if err != nil {
		t.Fatalf("explicit takeover: %v", err)
	}
	if record.TakeoverReason != "predecessor stuck mid-CI, founder approved handoff" {
		t.Fatalf("takeover reason not recorded: %+v", record)
	}
	if record.PriorOwner == nil || record.PriorOwner.WBSessionID != "wbs-a" {
		t.Fatalf("prior owner not recorded: %+v", record)
	}
}

func TestReleaseOnlyRemovesOwnersLane(t *testing.T) {
	home := t.TempDir()
	if _, err := Acquire(home, AcquireRequest{
		Repository: "sneat-dev/wb", Target: "main",
		Self: Owner{WBSessionID: "wbs-a", PID: 111}, IsOwnerLive: func(Owner) bool { return true },
		Now: fixedNow(time.Unix(1000, 0)),
	}); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// A non-owner's release must be a no-op.
	if err := Release(home, "sneat-dev/wb", "main", "wbs-other"); err != nil {
		t.Fatalf("non-owner Release: %v", err)
	}
	if _, found, err := Read(home, "sneat-dev/wb", "main"); err != nil || !found {
		t.Fatalf("lane should still exist after a non-owner release: found=%v err=%v", found, err)
	}

	if err := Release(home, "sneat-dev/wb", "main", "wbs-a"); err != nil {
		t.Fatalf("owner Release: %v", err)
	}
	if _, found, err := Read(home, "sneat-dev/wb", "main"); err != nil || found {
		t.Fatalf("lane should be gone after the owner released it: found=%v err=%v", found, err)
	}

	// Releasing again is a harmless no-op.
	if err := Release(home, "sneat-dev/wb", "main", "wbs-a"); err != nil {
		t.Fatalf("second Release: %v", err)
	}
}

// TestAcquireFailsClosedOnCorruptRecord proves finding 4: a lane record that
// exists but cannot be parsed must never be treated as an absent one — that
// would grant the lane to whoever asks next with no conflict check at all,
// silently discarding whatever the corrupt file was recording. It must
// refuse, naming the file, until an explicit --take-over-lane --lane-reason
// override replaces it.
func TestAcquireFailsClosedOnCorruptRecord(t *testing.T) {
	home := t.TempDir()
	lane := LaneID("sneat-dev/wb", "main")
	if err := os.MkdirAll(filepath.Join(home, DirName), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	recordFile := filepath.Join(home, DirName, lane+".json")
	if err := os.WriteFile(recordFile, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("write corrupt record: %v", err)
	}

	_, err := Acquire(home, AcquireRequest{
		Repository:  "sneat-dev/wb",
		Target:      "main",
		Self:        Owner{WBSessionID: "wbs-a", PID: 111},
		IsOwnerLive: func(Owner) bool { return true },
		Now:         fixedNow(time.Unix(1000, 0)),
	})
	var corrupt *CorruptRecordError
	if !errors.As(err, &corrupt) {
		t.Fatalf("expected a CorruptRecordError, got %v", err)
	}
	if corrupt.Path != recordFile {
		t.Fatalf("corrupt error names %q, want %q", corrupt.Path, recordFile)
	}

	// The same acquire, but with the explicit override, must replace the
	// corrupt record rather than staying refused forever.
	record, err := Acquire(home, AcquireRequest{
		Repository:     "sneat-dev/wb",
		Target:         "main",
		Self:           Owner{WBSessionID: "wbs-a", PID: 111},
		IsOwnerLive:    func(Owner) bool { return true },
		Now:            fixedNow(time.Unix(1000, 0)),
		TakeOver:       true,
		TakeoverReason: "prior lane record was corrupt, replacing it",
	})
	if err != nil {
		t.Fatalf("explicit takeover of corrupt record: %v", err)
	}
	if record.Owner.WBSessionID != "wbs-a" || !record.TakenOver {
		t.Fatalf("expected the corrupt record replaced by an explicit takeover, got %+v", record)
	}
}

func TestHeartbeatAdvancesOwnedLane(t *testing.T) {
	home := t.TempDir()
	if _, err := Acquire(home, AcquireRequest{
		Repository: "sneat-dev/wb", Target: "main",
		Self: Owner{WBSessionID: "wbs-a", PID: 111}, IsOwnerLive: func(Owner) bool { return true },
		Now: fixedNow(time.Unix(1000, 0)),
	}); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := Heartbeat(home, "sneat-dev/wb", "main", "wbs-a", fixedNow(time.Unix(1500, 0))); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	record, found, err := Read(home, "sneat-dev/wb", "main")
	if err != nil || !found {
		t.Fatalf("Read after heartbeat: found=%v err=%v", found, err)
	}
	if !record.Owner.HeartbeatAt.Equal(time.Unix(1500, 0).UTC()) {
		t.Fatalf("heartbeat not advanced: %v", record.Owner.HeartbeatAt)
	}
}

// TestAcquireConcurrentRaceIsFileLockSafe exercises requirement 4: a race
// between two sessions acquiring the same fresh lane must be serialized by
// the flock, not corrupt the record or admit both.
func TestAcquireConcurrentRaceIsFileLockSafe(t *testing.T) {
	home := t.TempDir()
	const contenders = 12
	var wins int64
	var group sync.WaitGroup
	for i := 0; i < contenders; i++ {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			_, err := Acquire(home, AcquireRequest{
				Repository:  "sneat-dev/wb",
				Target:      "main",
				Self:        Owner{WBSessionID: sessionName(i), PID: 1000 + i},
				IsOwnerLive: func(Owner) bool { return true },
				Now:         fixedNow(time.Unix(1000, 0)),
			})
			if err == nil {
				atomic.AddInt64(&wins, 1)
			} else {
				var conflict *ConflictError
				if !errors.As(err, &conflict) {
					t.Errorf("contender %d: unexpected error %v", i, err)
				}
			}
		}(i)
	}
	group.Wait()

	if wins != 1 {
		t.Fatalf("exactly one contender should win a fresh lane, got %d", wins)
	}
	record, found, err := Read(home, "sneat-dev/wb", "main")
	if err != nil || !found {
		t.Fatalf("Read after race: found=%v err=%v", found, err)
	}
	if record.Owner.WBSessionID == "" {
		t.Fatalf("record left without a coherent owner: %+v", record)
	}
}

func sessionName(i int) string {
	return "wbs-race-" + string(rune('a'+i))
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
