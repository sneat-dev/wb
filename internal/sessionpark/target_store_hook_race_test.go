package sessionpark

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

// TestOpenOrCreateRegularAtFallsBackToOpenAfterACompetingCreate
// deterministically drives openOrCreateRegularAt's create-collision path
// (internal/sessionpark/target_store.go: the O_CREAT|O_EXCL create losing to
// EEXIST, falling back to a plain open of the file a competing creator just
// made) using the openOrCreateRegularAtBeforeCreate test-only seam, instead
// of racing goroutines against a scheduler that can let one caller finish
// before another starts (review found the equivalent goroutine-barrier
// pattern elsewhere in this task covers its target statement only 0-1/5
// under GOMAXPROCS=1).
//
// The seam runs after openOrCreateRegularAt's own initial open finds the
// name absent but before its O_CREAT|O_EXCL create. Creating a competing
// file at the target name from inside the seam guarantees this call's own
// create observes EEXIST every time, with no dependency on concurrency or
// scheduling at all.
func TestOpenOrCreateRegularAtFallsBackToOpenAfterACompetingCreate(t *testing.T) {
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()

	original := openOrCreateRegularAtBeforeCreate
	defer func() { openOrCreateRegularAtBeforeCreate = original }()
	called := false
	var competingFD int
	openOrCreateRegularAtBeforeCreate = func(directoryFD int, name string) {
		called = true
		if name != "contested.lock" {
			t.Fatalf("hook name = %q, want contested.lock", name)
		}
		fd, createErr := unix.Openat(directoryFD, name,
			unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if createErr != nil {
			t.Fatalf("competing creator inside hook: %v", createErr)
		}
		competingFD = fd
	}
	defer func() {
		if competingFD != 0 {
			_ = unix.Close(competingFD)
		}
	}()

	fd, openErr := openOrCreateRegularAt(int(directory.Fd()), "contested.lock", 0o600)
	if !called {
		t.Fatal("openOrCreateRegularAtBeforeCreate was never invoked")
	}
	if openErr != nil {
		t.Fatalf("openOrCreateRegularAt with a competing creator = %v, want it to fall back to opening the winner's file", openErr)
	}
	defer func() { _ = unix.Close(fd) }()

	// The fallback open must have found the exact file the competing
	// creator made (fstat identity), not created a second one.
	var got, want unix.Stat_t
	if err := unix.Fstat(fd, &got); err != nil {
		t.Fatal(err)
	}
	if err := unix.Fstat(competingFD, &want); err != nil {
		t.Fatal(err)
	}
	if got.Dev != want.Dev || got.Ino != want.Ino {
		t.Fatalf("openOrCreateRegularAt fallback opened a different inode: got dev=%d ino=%d, want dev=%d ino=%d", got.Dev, got.Ino, want.Dev, want.Ino)
	}

	entries, err := os.ReadDir(directory.Name())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "contested.lock" {
		t.Fatalf("directory entries = %v, want only contested.lock", entries)
	}
}
