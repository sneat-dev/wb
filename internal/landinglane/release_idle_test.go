package landinglane

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseIdlePreservesActiveOperationsAndOtherOwners(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, caller  string
		operation     int
		alive, legacy bool
		snapshotErr   error
		want          string
	}{
		{name: "idle operation", caller: "owner", operation: 700},
		{name: "active operation", caller: "owner", operation: 700, alive: true, want: "still active"},
		{name: "different owner", caller: "other", operation: 700, want: "landing lane"},
		{name: "legacy interrupted command with live harness", caller: "owner", alive: true},
		{name: "legacy running command", caller: "owner", legacy: true, want: "another WB command"},
		{name: "legacy observation failure", caller: "owner", snapshotErr: errors.New("denied"), want: "denied"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			_, err := Acquire(home, AcquireRequest{Repository: "acme/app", Target: "main", Self: Owner{WBSessionID: "owner", PID: os.Getpid(), OperationPID: test.operation}})
			if err != nil {
				t.Fatal(err)
			}
			released, err := releaseIdle(home, "acme/app", "main", test.caller, func(int) bool { return test.alive }, func() (bool, error) { return test.legacy, test.snapshotErr })
			if test.want != "" {
				if err == nil || !strings.Contains(err.Error(), test.want) || released {
					t.Fatalf("release = %v, %v", released, err)
				}
			} else if err != nil || !released {
				t.Fatalf("release = %v, %v", released, err)
			}
			_, found, readErr := Read(home, "acme/app", "main")
			if readErr != nil || found == (test.want == "") {
				t.Fatalf("lane found=%v err=%v", found, readErr)
			}
		})
	}
}

func TestReleaseIdleIsIdempotentAndValidatesSession(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if released, err := ReleaseIdle(home, "acme/app", "main", "owner"); released || err != nil {
		t.Fatalf("absent = %v %v", released, err)
	}
	if _, err := ReleaseIdle(home, "acme/app", "main", ""); err == nil {
		t.Fatal("unregistered release succeeded")
	}
	if err := os.WriteFile(filepath.Join(home, "lanes"), []byte("blocked"), 0600); err == nil {
		t.Fatal("expected existing directory")
	}
	blocked := t.TempDir()
	if err := os.WriteFile(filepath.Join(blocked, "lanes"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReleaseIdle(blocked, "acme/app", "main", "owner"); err == nil {
		t.Fatal("blocked lock succeeded")
	}
	corrupt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(corrupt, "lanes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath(corrupt, LaneID("acme/app", "main")), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReleaseIdle(corrupt, "acme/app", "main", "owner"); err == nil {
		t.Fatal("corrupt record released")
	}
}

func TestLegacyProcessSnapshotRefusesEveryOtherWBExecutable(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input        string
		active, fail bool
	}{
		{input: "1 /sbin/launchd\n42 /opt/homebrew/bin/wb\n"},
		{input: "43 /opt/homebrew/bin/wb\n", active: true},
		{input: "43 /tmp/wb-lane\n", active: true},
		{input: "43 /tmp/wb0.191.1\n", active: true},
		{input: "43 C:/tools/wb.exe\n", active: true},
		{input: "43", fail: true},
		{input: "bad /bin/wb", fail: true},
	} {
		active, err := otherWBProcess(test.input, 42)
		if active != test.active || (err != nil) != test.fail {
			t.Fatalf("%q = %v %v", test.input, active, err)
		}
	}
}

func TestAcquireRefusesReplacingSameSessionsLiveOperation(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	owner := Owner{WBSessionID: "owner", PID: os.Getpid(), OperationPID: os.Getpid()}
	if _, err := Acquire(home, AcquireRequest{Repository: "acme/app", Target: "main", Self: owner}); err != nil {
		t.Fatal(err)
	}
	other := owner
	other.OperationPID = 0
	if _, err := Acquire(home, AcquireRequest{Repository: "acme/app", Target: "main", Self: other}); err == nil || !strings.Contains(err.Error(), "active operation") {
		t.Fatalf("replacement = %v", err)
	}
	record, err := Acquire(home, AcquireRequest{Repository: "acme/app", Target: "main", Self: owner})
	if err != nil || record.Owner.OperationPID != owner.OperationPID {
		t.Fatalf("same operation = %+v %v", record, err)
	}
}

func TestAcquireUpgradesLegacyClaimOnlyAfterOperationObservation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		active bool
		err    error
		want   string
	}{
		{name: "idle"}, {name: "active", active: true, want: "another WB command"}, {name: "unobservable", err: errors.New("unavailable"), want: "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			owner := Owner{WBSessionID: "owner", PID: os.Getpid()}
			if _, err := Acquire(home, AcquireRequest{Repository: "acme/app", Target: "main", Self: owner}); err != nil {
				t.Fatal(err)
			}
			owner.OperationPID = os.Getpid()
			record, err := Acquire(home, AcquireRequest{Repository: "acme/app", Target: "main", Self: owner, LegacyOperationActive: func() (bool, error) { return test.active, test.err }})
			if test.want != "" {
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("upgrade = %v", err)
				}
			} else if err != nil || record.Owner.OperationPID != owner.OperationPID {
				t.Fatalf("upgrade = %+v %v", record, err)
			}
		})
	}
}

func TestLegacyOperationObservationReportsProcessTableFailures(t *testing.T) {
	t.Parallel()
	if _, err := legacyOperationActiveWith(func() ([]byte, error) { return nil, errors.New("ps failed") }); err == nil || !strings.Contains(err.Error(), "ps failed") {
		t.Fatal(err)
	}
	if active, err := legacyOperationActiveWith(func() ([]byte, error) { return []byte("23 /bin/wb"), nil }); err != nil || !active {
		t.Fatalf("observation = %v %v", active, err)
	}

}

func TestReleaseIdleReportsRemovalFailure(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	if _, err := Acquire(home, AcquireRequest{Repository: "acme/app", Target: "main", Self: Owner{WBSessionID: "owner"}}); err != nil {
		t.Fatal(err)
	}
	_, err := releaseIdle(home, "acme/app", "main", "owner", func(int) bool { return false }, func() (bool, error) {
		path := recordPath(home, LaneID("acme/app", "main"))
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "child"), nil, 0600); err != nil {
			t.Fatal(err)
		}
		return false, nil
	})
	if err == nil || !strings.Contains(err.Error(), "release landing lane") {
		t.Fatalf("removal failure = %v", err)
	}
}

func TestLiveOperationKeepsLaneOwnedAfterHarnessExit(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	owner := Owner{WBSessionID: "owner", PID: -1, OperationPID: os.Getpid()}
	if _, err := Acquire(home, AcquireRequest{Repository: "acme/app", Target: "main", Self: owner}); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(home, AcquireRequest{Repository: "acme/app", Target: "main", Self: Owner{WBSessionID: "other"}}); err == nil {
		t.Fatal("live orphan operation lost its lane")
	}
	if defaultOwnerLive("", Owner{OperationPID: 999999999}) {
		t.Fatal("dead operation reported live")
	}
}
