package runqueue

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

// TestUnlockHeavyAdmissionOnNilFileIsANoOp drives unlockHeavyAdmission's
// nil-file guard (heavy.go): tryLockHeavyAdmission's own "someone else holds
// it" return leaves the caller with a nil *os.File, and every caller must be
// able to pass that straight to unlockHeavyAdmission without a nil check of
// its own.
func TestUnlockHeavyAdmissionOnNilFileIsANoOp(t *testing.T) {
	t.Parallel()
	unlockHeavyAdmission(nil) // must not panic
}

// TestRemoveHeavyHolderOnEmptyPathIsANoOp drives removeHeavyHolder's
// empty-path guard (heavy.go): announceHeavyHolder returns "" on failure,
// and a Lease built from that must still release safely.
func TestRemoveHeavyHolderOnEmptyPathIsANoOp(t *testing.T) {
	t.Parallel()
	removeHeavyHolder("") // must not panic or touch the filesystem
}

// TestHeartbeatHeavyHolderOnEmptyPathIsANoOp drives heartbeatHeavyHolder's
// empty-path guard (heavy.go), the same contract as removeHeavyHolder's.
func TestHeartbeatHeavyHolderOnEmptyPathIsANoOp(t *testing.T) {
	t.Parallel()
	heartbeatHeavyHolder("", Participant{PID: 1}, 1, time.Now()) // must not panic
}

// TestTryLockHeavyAdmissionReportsLockDirectoryCreationFailure drives
// tryLockHeavyAdmission's MkdirAll error branch (heavy.go): a read-only
// projects root cannot have its heavy/ subtree created under it.
func TestTryLockHeavyAdmissionReportsLockDirectoryCreationFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks")
	}
	t.Parallel()
	root := t.TempDir()
	if err := os.Chmod(root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	if _, _, err := tryLockHeavyAdmission(root); err == nil {
		t.Fatal("tryLockHeavyAdmission under a read-only projects root succeeded, want a directory-creation error")
	}
}

// TestTryLockHeavyAdmissionReportsOpenFailure drives tryLockHeavyAdmission's
// OpenFile error branch (heavy.go): a directory sitting at the lock file's
// own path makes O_CREATE|O_RDWR fail with EISDIR.
func TestTryLockHeavyAdmissionReportsOpenFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	lockPath := heavyLockPath(root)
	if err := os.MkdirAll(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tryLockHeavyAdmission(root); err == nil {
		t.Fatal("tryLockHeavyAdmission over a directory at the lock path succeeded, want an open error")
	}
}

// TestAnnounceHeavyHolderReportsEncodingFailure drives announceHeavyHolder's
// json.Marshal error branch (heavy.go): a StartedAt outside
// encoding/json's representable year range fails to encode, with no range
// check anywhere upstream.
func TestAnnounceHeavyHolderReportsEncodingFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path, ok := announceHeavyHolder(root, Participant{PID: 1}, 1, unencodableHeavyTime)
	if ok || path != "" {
		t.Fatalf("announceHeavyHolder(unencodable started_at) = (%q, %v), want (\"\", false)", path, ok)
	}
}

// TestAnnounceHeavyHolderReportsDirectoryCreationFailure drives
// announceHeavyHolder's MkdirAll error branch (heavy.go).
func TestAnnounceHeavyHolderReportsDirectoryCreationFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks")
	}
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(heavyRoot(root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(heavyRoot(root), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(heavyRoot(root), 0o700) })
	path, ok := announceHeavyHolder(root, Participant{PID: 1}, 1, time.Now())
	if ok || path != "" {
		t.Fatalf("announceHeavyHolder under a read-only heavy root = (%q, %v), want (\"\", false)", path, ok)
	}
}

// TestHeartbeatHeavyHolderReportsEncodingFailure drives
// heartbeatHeavyHolder's json.Marshal error branch (heavy.go): its
// UpdatedAt is always time.Now().UTC(), so the only encoding failure it can
// hit is via StartedAt, which it still encodes into the same Holder record.
func TestHeartbeatHeavyHolderReportsEncodingFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "holder.json")
	// Must not panic and must not write a file: the marshal error must
	// short-circuit before atomicWriteFile.
	heartbeatHeavyHolder(path, Participant{PID: 1}, 1, unencodableHeavyTime)
	if _, err := os.Stat(path); err == nil {
		t.Fatal("heartbeatHeavyHolder(unencodable started_at) wrote a holder file despite an encoding failure")
	}
}

var unencodableHeavyTime = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)

// TestAdmitHeavyReturnsImmediatelyWhenContextIsAlreadyDone drives
// admitHeavy's own top-of-loop ctx.Done() check (heavy.go).
func TestAdmitHeavyReturnsImmediatelyWhenContextIsAlreadyDone(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ticket := RegisterHeavy(root, Participant{PID: 1})
	t.Cleanup(func() { ticket.Forget() })
	if _, _, _, err := admitHeavy(ctx, root, Participant{PID: 1}, ticket); err == nil {
		t.Fatal("admitHeavy with an already-cancelled context succeeded, want its Err()")
	}
}

// TestAdmitHeavyWaitsBehindAnEarlierWaiterThenReportsCancellation drives
// admitHeavy's "not the head of the FIFO queue yet" branch and its
// sleepOrDone-driven cancellation (heavy.go): a second ticket registered
// after an earlier one is never the head, so it always takes the wait path;
// giving it a context whose deadline lands well inside a single
// retryInterval sleep, but after the top-of-loop check, deterministically
// exercises the cancellation return from that wait without any real
// production seam.
func TestAdmitHeavyWaitsBehindAnEarlierWaiterThenReportsCancellation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	head := RegisterHeavy(root, Participant{PID: 1})
	t.Cleanup(func() { head.Forget() })
	behind := RegisterHeavy(root, Participant{PID: 2})
	t.Cleanup(func() { behind.Forget() })

	ctx, cancel := context.WithTimeout(context.Background(), retryInterval/4)
	t.Cleanup(func() { cancel() })
	if _, _, _, err := admitHeavy(ctx, root, Participant{PID: 2}, behind); err == nil {
		t.Fatal("admitHeavy behind an earlier waiter, with a context that expires mid-wait, succeeded, want its Err()")
	}
}

// TestAdmitHeavyWaitsWhenAdmissionLockIsHeldElsewhereThenReportsCancellation
// drives admitHeavy's "!locked" branch and its own sleepOrDone-driven
// cancellation (heavy.go): a real flock held by this test process itself
// (a distinct, already-open file descriptor on the very same lock path)
// makes unix.Flock's LOCK_EX|LOCK_NB fail exactly as a concurrent WB command
// holding it would.
func TestAdmitHeavyWaitsWhenAdmissionLockIsHeldElsewhereThenReportsCancellation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ticket := RegisterHeavy(root, Participant{PID: 1})
	t.Cleanup(func() { ticket.Forget() })

	lockPath := heavyLockPath(root)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	holderFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holderFile.Close() })
	if err := unix.Flock(int(holderFile.Fd()), unix.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), retryInterval/4)
	t.Cleanup(func() { cancel() })
	if _, _, _, err := admitHeavy(ctx, root, Participant{PID: 1}, ticket); err == nil {
		t.Fatal("admitHeavy against an already-held admission lock, with a context that expires mid-wait, succeeded, want its Err()")
	}
}

// TestAdmitHeavyWaitsWhenDefensiveMaxIsAlreadyReachedThenReportsCancellation
// drives admitHeavy's heavyDefensiveMax branch and its own
// sleepOrDone-driven cancellation (heavy.go): three pre-seeded, genuinely
// live holder records (this test process's own PID, a fresh UpdatedAt) make
// readHeavyHolders report heavyDefensiveMax holders without any new
// production seam. The context outlives one full retryInterval so the loop
// also takes its normal continue at least once before it is cancelled.
func TestAdmitHeavyWaitsWhenDefensiveMaxIsAlreadyReachedThenReportsCancellation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ticket := RegisterHeavy(root, Participant{PID: 1})
	t.Cleanup(func() { ticket.Forget() })

	dir := heavyRunningDir(root)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < heavyDefensiveMax; i++ {
		holder := Holder{
			Participant: Participant{PID: os.Getpid()},
			Units:       1,
			StartedAt:   time.Now().UTC(),
			UpdatedAt:   time.Now().UTC(),
		}
		payload, err := json.Marshal(holder)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, fmt.Sprintf("holder-%d.json", i))
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), retryInterval*3+retryInterval/4)
	t.Cleanup(func() { cancel() })
	if _, _, _, err := admitHeavy(ctx, root, Participant{PID: 1}, ticket); err == nil {
		t.Fatal("admitHeavy with heavyDefensiveMax holders already running, with a context that expires mid-wait, succeeded, want its Err()")
	}
}

// TestEffectiveGOFLAGSFallsBackToEmptyWhenGoIsMissing drives
// EffectiveGOFLAGS's exec.Command error branch (queue.go): with PATH
// pointing at an empty directory, "go env GOFLAGS" cannot find an
// executable, so exec.Command's Output() genuinely errors.
//
// Not parallel: t.Setenv is process-wide.
func TestEffectiveGOFLAGSFallsBackToEmptyWhenGoIsMissing(t *testing.T) {
	t.Setenv("GOFLAGS", "")
	t.Setenv("PATH", t.TempDir())
	if got := EffectiveGOFLAGS(); got != "" {
		t.Fatalf("EffectiveGOFLAGS() with no go on PATH = %q, want \"\"", got)
	}
}

// TestUnitsOnALargeMachinePicksHeavyShareForRaceOrCover drives Units'
// KindRaceOrCover branch on a large machine (queue.go): distinct from
// KindFocused's branch, exercised elsewhere.
//
// Not parallel: SetNumCPUForTest mutates a package-level variable.
func TestUnitsOnALargeMachinePicksHeavyShareForRaceOrCover(t *testing.T) {
	restore := SetNumCPUForTest(32)
	defer restore()
	got := Units([]string{"go", "test", "-race", "./x"}, 8)
	if got != clampToCapacity(heavyShare(1), 8) {
		t.Fatalf("Units(race, large machine) = %d, want clampToCapacity(heavyShare(1), budget)", got)
	}
}

// TestAdmitLegacyClampsUnitsToBudget drives admitLegacy's own clamp
// (queue.go): AdmitExplicit passes its caller's declared want straight
// through, and a want above the machine's budget-sum ceiling is reported as
// what was actually held, not what was asked for.
func TestAdmitLegacyClampsUnitsToBudget(t *testing.T) {
	restore := SetNumCPUForTest(1)
	defer restore()
	root := t.TempDir()
	admission, err := AdmitExplicit(context.Background(), root, 999, Participant{PID: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Lease.Release()
	if admission.Units != Budget() {
		t.Fatalf("AdmitExplicit(want=999) = %d units, want it clamped to Budget()=%d", admission.Units, Budget())
	}
}

// TestAdmitOnALargeMachineRegistersItsOwnTicketForAHeavyCommand drives
// Admit's own-ticket path for a heavy kind (queue.go): a caller (the
// daemon's executors) that passes a nil ticket gets one registered and
// forgotten internally.
//
// Not parallel: SetNumCPUForTest mutates a package-level variable.
func TestAdmitOnALargeMachineRegistersItsOwnTicketForAHeavyCommand(t *testing.T) {
	restore := SetNumCPUForTest(32)
	defer restore()
	root := t.TempDir()
	admission, err := Admit(context.Background(), root, []string{"go", "test", "./..."}, Participant{PID: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer admission.Lease.Release()
	if admission.Units <= 0 {
		t.Fatalf("Admit(broad, large machine, nil ticket) = %#v, want a positive Units", admission)
	}
}
