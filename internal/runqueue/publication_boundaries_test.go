package runqueue

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHeavyAdmissionRechecksHeadAfterTakingLock(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	self := Participant{PID: os.Getpid(), Summary: "go test"}
	ticket := RegisterHeavy(root, self)
	t.Cleanup(ticket.Forget)
	if ticket.pathLocked() == "" {
		t.Fatal("ticket registration failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	calls := 0
	var held []*os.File
	lease, units, _, err := admitHeavyWithLock(ctx, root, self, ticket, func(root string) (*os.File, bool, error) {
		file, locked, err := tryLockHeavyAdmission(root)
		if err != nil || !locked {
			t.Fatalf("admission lock=%t,%v", locked, err)
		}
		held = append(held, file)
		calls++
		if err := os.Remove(ticket.pathLocked()); err != nil {
			t.Fatal(err)
		}
		if calls == 2 {
			cancel()
		}
		return file, locked, err
	})
	if !errors.Is(err, context.Canceled) || lease != nil || units != 0 || calls != 2 {
		t.Fatalf("head race admitted: lease=%v units=%d err=%v calls=%d", lease, units, err, calls)
	}
	for _, file := range held {
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("admission lock descriptor remains open: %v", err)
		}
	}
}

func TestMachineRegistrationUsesItsAdmissionQueue(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		argv       []string
		small      bool
		want       ticketNamespace
		registered bool
	}{
		{name: "ungoverned", argv: []string{"echo"}},
		{name: "focused small machine", argv: []string{"staticcheck", "./one"}, small: true, want: namespaceLegacy, registered: true},
		{name: "broad large machine", argv: []string{"cargo", "build"}, want: namespaceHeavy, registered: true},
		{name: "race large machine", argv: []string{"go", "test", "-race", "./one"}, want: namespaceHeavy, registered: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			self := Participant{PID: os.Getpid(), Summary: "go test"}
			ticket := registerForAdmissionOnMachine(root, tc.argv, self, tc.small)
			if !tc.registered {
				if ticket != nil {
					t.Fatalf("ungoverned operation registered: %+v", ticket)
				}
				return
			}
			wantDir := heavyWaitingDir(root)
			if tc.want == namespaceLegacy {
				wantDir = ticketDir(root)
			}
			if ticket == nil || ticket.pathLocked() == "" || ticket.namespace != tc.want {
				t.Fatalf("machine registration=%+v", ticket)
			}
			defer ticket.Forget()
			if filepath.Dir(ticket.pathLocked()) != wantDir {
				t.Fatalf("ticket outside admission queue: %s", ticket.pathLocked())
			}
		})
	}
}

func TestQueuePublicationRefusesInvalidClockWithoutReplacingEvidence(t *testing.T) {
	t.Parallel()
	invalid := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	self := Participant{PID: os.Getpid(), Summary: "go test"}
	t.Run("registration", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		ticket := registerAtTime(root, namespaceLegacy, ticketDir(root), self, invalid)
		if ticket.pathLocked() != "" {
			t.Fatalf("invalid timestamp published ticket: %s", ticket.pathLocked())
		}
		entries, err := os.ReadDir(ticketDir(root))
		if err != nil || len(entries) != 0 {
			t.Fatalf("invalid clock left artifacts: %v,%v", entries, err)
		}
	})
	t.Run("ticket heartbeat", func(t *testing.T) {
		t.Parallel()
		ticket := Register(t.TempDir(), self)
		defer ticket.Forget()
		path := ticket.pathLocked()
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		ticket.createdAt = invalid
		ticket.Heartbeat()
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("failed heartbeat replaced evidence: %q,%v", after, err)
		}
	})
	for _, phase := range []string{"announce", "holder heartbeat"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			file, err := os.CreateTemp(t.TempDir(), "slot")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = file.Close() })
			lease := &Lease{files: []*os.File{file}}
			if phase == "announce" {
				announcement := lease.announceAt(self, invalid)
				if len(announcement.paths) != 0 {
					t.Fatalf("invalid clock announced=%v", announcement.paths)
				}
				if _, err := os.Stat(holderPathFor(file.Name())); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("invalid clock left holder: %v", err)
				}
				return
			}
			announcement := lease.Announce(self)
			defer announcement.Cleanup()
			if len(announcement.paths) != 1 {
				t.Fatalf("announcement=%v", announcement.paths)
			}
			path := announcement.paths[0]
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			announcement.heartbeatAt(invalid)
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("failed holder heartbeat replaced evidence: %q,%v", after, err)
			}
		})
	}
}
