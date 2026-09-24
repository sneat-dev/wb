package worktrees

import (
	"os"
	"strings"
	"testing"
)

// TestWriteBytesImmutableAtLosesRenameToACompetingWriter deterministically
// drives writeBytesImmutableAt's rename-failure path
// (internal/worktrees/worklog.go: the cleanup-unlink defer for the loser's
// own leftover temporary file, the post-failure idempotent readback, and
// the raw rename-error return) using the writeBytesImmutableAtBeforeRename
// test-only seam, instead of racing goroutines against a scheduler that can
// let one caller finish before another starts (the goroutine-barrier
// version of this test was found by review to cover the target statements
// only 0-1/5 under GOMAXPROCS=1).
//
// The seam runs after this call's own temporary file is fully written,
// synced and closed but before its rename-without-replace. Writing a
// competing file at the target name from inside the seam guarantees this
// call's own rename observes EEXIST every time, with no dependency on
// concurrency or scheduling at all.
func TestWriteBytesImmutableAtLosesRenameToACompetingWriter(t *testing.T) {
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()

	winner := []byte("winner-content")
	loser := []byte("loser-content")

	original := writeBytesImmutableAtBeforeRename
	defer func() { writeBytesImmutableAtBeforeRename = original }()
	called := false
	writeBytesImmutableAtBeforeRename = func(hookDirectory *os.File, name string) {
		called = true
		if name != "contested.txt" {
			t.Fatalf("hook name = %q, want contested.txt", name)
		}
		// The competing write below also goes through
		// writeBytesImmutableAt, which would call this same hook again
		// (infinite recursion) if left active; disable it for the
		// duration of the nested call. Nothing in this test calls
		// writeBytesImmutableAt again afterward, so it does not need to
		// be re-armed.
		writeBytesImmutableAtBeforeRename = original
		if err := writeBytesImmutableAt(hookDirectory, name, winner, 0o600, false); err != nil {
			t.Fatalf("competing writer inside hook: %v", err)
		}
	}

	writeErr := writeBytesImmutableAt(directory, "contested.txt", loser, 0o600, true)
	if !called {
		t.Fatal("writeBytesImmutableAtBeforeRename was never invoked")
	}
	if writeErr == nil {
		t.Fatal("writeBytesImmutableAt with a competing winner succeeded, want a rename-collision error")
	}
	// The competing winner's write (idempotent=false) is unrelated content,
	// so this call's post-failure idempotent readback (worklog.go:3584)
	// cannot match and falls through to the raw rename error
	// (worklog.go:3587) — the raw, unwrapped rename-without-replace error,
	// whose text is the bare errno ("file exists"), not the function's own
	// wrapped "immutable file already exists" message from the unrelated,
	// earlier existence check.
	if !strings.Contains(writeErr.Error(), "file exists") {
		t.Fatalf("writeBytesImmutableAt error = %v, want the raw rename-collision error", writeErr)
	}

	entries, err := os.ReadDir(directory.Name())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if len(names) != 1 || names[0] != "contested.txt" {
		t.Fatalf("directory entries = %v, want only the winner's file (the loser's temporary must be cleaned up)", names)
	}
	stored, err := os.ReadFile(directory.Name() + "/contested.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(winner) {
		t.Fatalf("stored content = %q, want the winner's %q", stored, winner)
	}
}

// TestWriteBytesImmutableAtIdempotentRewriteSurvivesACompetingIdenticalWriter
// is the matching idempotent case: the competing writer inside the hook
// writes the *same* content this call offers, so this call's post-failure
// readback finds it equal and returns nil (worklog.go:3584-3585) instead of
// an error.
func TestWriteBytesImmutableAtIdempotentRewriteSurvivesACompetingIdenticalWriter(t *testing.T) {
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()

	content := []byte("shared-content")

	original := writeBytesImmutableAtBeforeRename
	defer func() { writeBytesImmutableAtBeforeRename = original }()
	writeBytesImmutableAtBeforeRename = func(hookDirectory *os.File, name string) {
		// See the sibling test above: disable the hook before the nested
		// competing write to avoid infinite recursion into itself.
		writeBytesImmutableAtBeforeRename = original
		if err := writeBytesImmutableAt(hookDirectory, name, content, 0o600, false); err != nil {
			t.Fatalf("competing writer inside hook: %v", err)
		}
	}

	if err := writeBytesImmutableAt(directory, "shared.txt", content, 0o600, true); err != nil {
		t.Fatalf("idempotent writeBytesImmutableAt with an identical competing writer = %v, want nil", err)
	}

	stored, err := os.ReadFile(directory.Name() + "/shared.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(content) {
		t.Fatalf("stored content = %q, want %q", stored, content)
	}
}
