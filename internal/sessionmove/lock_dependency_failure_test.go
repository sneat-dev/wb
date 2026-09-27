//go:build darwin || linux

package sessionmove

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

// openAuthorityFDs finds the four descriptors that an acquisition has opened
// for this test's unique temporary store. It runs inside the failing syscall,
// before the acquisition's cleanup path has had a chance to close them.
// The Darwin/Linux test runner must expose /dev/fd; absence fails the test.
func openAuthorityFDs(t *testing.T, fixture smCovLockFixture) map[string]int {
	t.Helper()
	paths := map[string]string{
		"root":    fixture.root,
		"handoff": filepath.Join(fixture.root, fixture.request.HandoffID),
		"request": filepath.Join(fixture.root, fixture.request.HandoffID, requestFileName),
		"lock":    filepath.Join(fixture.root, fixture.request.HandoffID, executionLockFileName),
	}
	want := make(map[string]os.FileInfo, len(paths))
	for name, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s authority: %v", name, err)
		}
		want[name] = info
	}
	entries, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Fatalf("list process descriptors: %v", err)
	}
	got := make(map[string]int, len(paths))
	for _, entry := range entries {
		fd, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			continue // the descriptor used to list /dev/fd has already closed
		}
		for name, expected := range want {
			fileStat := expected.Sys().(*syscall.Stat_t)
			if uint64(stat.Dev) == uint64(fileStat.Dev) && uint64(stat.Ino) == uint64(fileStat.Ino) {
				got[name] = fd
			}
		}
	}
	for name := range want {
		if _, ok := got[name]; !ok {
			t.Fatalf("%s authority descriptor was not open at the failing syscall", name)
		}
	}
	return got
}

//nolint:paralleltest // descriptor-number reuse makes this process-wide check serial
func TestAcquireExecutionLockClosesAuthorityAfterDependencyFailure(t *testing.T) {
	// Descriptor numbers can be reused by unrelated parallel tests, so this
	// test runs serially and checks closure immediately after each failed call.
	for _, fault := range []string{"chmod", "flock"} {
		//nolint:paralleltest // each failure must finish before descriptor reuse
		t.Run(fault, func(t *testing.T) {
			fixture := smCovNewLockFixture(t)
			var captured map[string]int
			injectedCalls := 0
			chmod := unix.Fchmod
			flock := unix.Flock
			switch fault {
			case "chmod":
				chmod = func(fd int, mode uint32) error {
					injectedCalls++
					captured = openAuthorityFDs(t, fixture)
					if captured["lock"] != fd || mode != 0o600 {
						t.Fatalf("chmod received fd %d mode %o, want lock fd %d mode 600", fd, mode, captured["lock"])
					}
					return syscall.EIO
				}
			case "flock":
				flock = func(fd, operation int) error {
					injectedCalls++
					captured = openAuthorityFDs(t, fixture)
					if captured["lock"] != fd || operation != unix.LOCK_EX|unix.LOCK_NB {
						t.Fatalf("flock received fd %d operation %d, want lock fd %d exclusive nonblocking", fd, operation, captured["lock"])
					}
					return syscall.EIO
				}
			}
			lock, err := fixture.store.acquireExecutionLock(context.Background(), fixture.request.HandoffID, fixture.digest, chmod, flock)
			if lock != nil || !errors.Is(err, syscall.EIO) {
				if lock != nil {
					_ = lock.Close()
				}
				t.Fatalf("failed acquisition = (%v, %v), want nil lock and EIO", lock, err)
			}
			if !strings.Contains(err.Error(), map[string]string{"chmod": "secure handoff execution lock", "flock": "lock handoff execution"}[fault]) {
				t.Fatalf("failed acquisition lost %s context: %v", fault, err)
			}
			if injectedCalls != 1 || len(captured) != 4 {
				t.Fatalf("%s fault callback ran %d times and captured %d authority descriptors, want 1 and 4", fault, injectedCalls, len(captured))
			}
			for name, fd := range captured {
				if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, syscall.EBADF) {
					t.Errorf("%s authority fd %d remains open after %s failure: %v", name, fd, fault, err)
				}
			}
			if t.Failed() {
				return
			}
			retried, err := fixture.store.AcquireExecutionLock(context.Background(), fixture.request.HandoffID, fixture.digest)
			if err != nil {
				t.Fatalf("acquire after %s failure: %v", fault, err)
			}
			defer func() { _ = retried.Close() }()
			if !retried.HeldForStore(fixture.root, fixture.request, fixture.digest) {
				t.Fatalf("retry after %s failure lacks admitted authority", fault)
			}
		})
	}
}
