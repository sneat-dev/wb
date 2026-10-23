package runqueue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

//nolint:paralleltest // SetNumCPUForTest changes the package-wide admission CPU count.
func TestHeavyAdmissionCanceledBelowShareFloorPreservesNativeCustody(t *testing.T) {
	defer SetNumCPUForTest(18)()
	root := t.TempDir()
	// Acquire both existing holders through native admission, rather than
	// asserting capacity with caller-created holder DTOs or fabricated leases.
	for _, running := range []struct {
		summary string
		units   int
	}{{"first running", 18}, {"second running", 9}} {
		self := Participant{PID: os.Getpid(), Summary: running.summary}
		ticket := RegisterHeavy(root, self)
		t.Cleanup(ticket.Forget)
		if ticket.pathLocked() == "" {
			t.Fatal("native running ticket registration failed")
		}
		lease, units, _, err := admitHeavy(t.Context(), root, self, ticket)
		if lease != nil {
			t.Cleanup(lease.Release)
		}
		if err != nil || lease == nil || units != running.units || ticket.pathLocked() != "" {
			t.Fatalf("native fixture admission: lease=%v units=%d want=%d ticket=%q err=%v", lease, units, running.units, ticket.pathLocked(), err)
		}
	}
	holdersBefore := make(map[string]Holder)
	entries, err := os.ReadDir(heavyRunningDir(root))
	if err != nil || len(entries) != 2 {
		t.Fatalf("native fixture did not publish two running holders: entries=%v err=%v", entries, err)
	}
	for _, entry := range entries {
		path := filepath.Join(heavyRunningDir(root), entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var holder Holder
		if err := json.Unmarshal(data, &holder); err != nil {
			t.Fatal(err)
		}
		holdersBefore[path] = holder
	}
	self := Participant{PID: os.Getpid(), Summary: "canceled below share floor"}
	ticket := RegisterHeavy(root, self)
	t.Cleanup(ticket.Forget)
	pendingPath := ticket.pathLocked()
	if pendingPath == "" {
		t.Fatal("native pending ticket registration failed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	calls := 0
	var held *os.File
	var pendingBytes []byte
	lease, units, _, err := admitHeavyWithLock(ctx, root, self, ticket, func(lockRoot string) (*os.File, bool, error) {
		file, locked, err := tryLockHeavyAdmission(lockRoot)
		if err != nil || !locked || file == nil {
			t.Fatalf("actual admission lock=%t file=%v err=%v", locked, file, err)
		}
		held = file
		t.Cleanup(func() { unlockHeavyAdmission(file) })
		calls++
		waiting := readTicketsIn(heavyWaitingDir(root))
		running := readHeavyHolders(root)
		if len(waiting) != 1 || waiting[0].path != pendingPath || len(running) != 2 {
			t.Fatalf("native queue custody changed before cancellation: waiting=%+v running=%+v", waiting, running)
		}
		if share, k := heavyAdmissionCandidate(running, len(waiting)); share != 0 || k != 3 || numCPU()/heavyShareFloorDivisor != 4 {
			t.Fatalf("native fixture does not require share-floor wait: share=%d k=%d", share, k)
		}
		pendingBytes, err = os.ReadFile(pendingPath)
		if err != nil {
			t.Fatal(err)
		}
		cancel()
		return file, locked, nil
	})
	if lease != nil {
		t.Cleanup(lease.Release)
	}
	if !errors.Is(err, context.Canceled) || lease != nil || units != 0 || calls != 1 {
		t.Fatalf("share-floor cancellation granted custody: lease=%v units=%d calls=%d err=%v", lease, units, calls, err)
	}
	if _, err := held.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("canceled admission retained native lock descriptor: %v", err)
	}
	reacquired, locked, err := tryLockHeavyAdmission(root)
	if err != nil || !locked || reacquired == nil {
		t.Fatalf("canceled admission retained native file lock: locked=%t file=%v err=%v", locked, reacquired, err)
	}
	unlockHeavyAdmission(reacquired)
	if ticket.pathLocked() != pendingPath {
		t.Fatalf("cancellation forgot or replaced native waiter: %q", ticket.pathLocked())
	}
	if got, err := os.ReadFile(pendingPath); err != nil || !bytes.Equal(got, pendingBytes) {
		t.Fatalf("cancellation changed native waiting evidence: %v", err)
	}
	if waiting := readTicketsIn(heavyWaitingDir(root)); len(waiting) != 1 || waiting[0].path != pendingPath {
		t.Fatalf("cancellation created or removed native tickets: %+v", waiting)
	}
	entries, err = os.ReadDir(heavyRunningDir(root))
	if err != nil || len(entries) != len(holdersBefore) {
		t.Fatalf("cancellation created an extra native holder: entries=%v err=%v", entries, err)
	}
	for _, entry := range entries {
		path := filepath.Join(heavyRunningDir(root), entry.Name())
		want, exists := holdersBefore[path]
		data, err := os.ReadFile(path)
		if err != nil || !exists {
			t.Fatalf("cancellation replaced native holder %s: exists=%t err=%v", path, exists, err)
		}
		var got Holder
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		// Existing leases own their heartbeat timestamps; their allocation and
		// participant/start identity must survive this unrelated canceled waiter.
		got.UpdatedAt = want.UpdatedAt
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("cancellation changed existing native holder: got=%+v want=%+v", got, want)
		}
	}
}
