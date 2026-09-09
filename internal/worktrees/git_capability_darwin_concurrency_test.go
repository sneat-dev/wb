//go:build darwin

package worktrees

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/unixcompat"
)

// Freezing a capability root's parent mutates a directory WB shares. The
// parent of every canonical clone is its owner directory — one
// ~/projects/sneat-co for a dozen repositories — so two secure Git helpers
// under one owner are inside this window at the same time as soon as anything
// applies two repositories concurrently, and it happens today whenever two wb
// processes overlap.
//
// Unserialised, the second helper reads the mode the first had already
// cleared, treats 0555 as the mode to put back, and leaves the owner directory
// permanently read-only — after which no clone or worktree can be created
// under it again. That is a silent, persistent break of a directory outside
// WB's own state, produced by a successful operation.
func TestDarwinCapabilityParentFreezeIsExclusiveAcrossHelpers(t *testing.T) {
	container := t.TempDir()
	if resolved, resolveErr := filepath.EvalSymlinks(container); resolveErr == nil {
		container = resolved
	}
	owner := filepath.Join(container, "owner")
	if err := os.Mkdir(owner, 0o755); err != nil {
		t.Fatal(err)
	}
	originalMode := modeOfDirectory(t, owner)

	capabilityFor := func(name string) gitFilesystemCapability {
		path := filepath.Join(owner, name)
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		directory, err := openAbsoluteDirectoryNoFollow(path, false)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = directory.Close() })
		capability, err := newGitFilesystemCapability(gitFilesystemCapabilityRoot{path: path, directory: directory})
		if err != nil {
			t.Fatal(err)
		}
		return capability
	}
	first, second := capabilityFor("repository-one"), capabilityFor("repository-two")

	firstGuards, err := lockDarwinCapabilityParents(first)
	if err != nil {
		t.Fatal(err)
	}
	if frozen := modeOfDirectory(t, owner); frozen&0o222 != 0 {
		t.Fatalf("the first helper did not freeze the shared owner directory: mode %v", frozen)
	}

	secondDone := make(chan []darwinCapabilityParentGuard, 1)
	go func() {
		guards, lockErr := lockDarwinCapabilityParents(second)
		if lockErr != nil {
			t.Errorf("second helper: %v", lockErr)
		}
		secondDone <- guards
	}()

	// Give the second helper every chance to enter the window while the first
	// still holds it. It must not get in.
	select {
	case guards := <-secondDone:
		restoreDarwinCapabilityParents(guards)
		restoreDarwinCapabilityParents(firstGuards)
		t.Fatal("two helpers froze one shared owner directory at once; the second will restore the frozen mode as if it were the original")
	case <-time.After(250 * time.Millisecond):
	}

	restoreDarwinCapabilityParents(firstGuards)
	select {
	case guards := <-secondDone:
		restoreDarwinCapabilityParents(guards)
	case <-time.After(10 * time.Second):
		t.Fatal("the second helper never acquired the shared owner directory after the first released it")
	}

	if got := modeOfDirectory(t, owner); got != originalMode {
		t.Fatalf("shared owner directory left at %v, want its original %v", got, originalMode)
	}
}

// The sandbox deliberately lets Git hooks invoke WB itself, so a hook that
// reached a WB worktree operation on the same owner directory would wait on a
// lock its own ancestor holds. A plain blocking lock would hang there forever
// while still holding a task lock; the wait is bounded so that shape is
// reported instead.
func TestDarwinCapabilityParentLockWaitIsBounded(t *testing.T) {
	previous := capabilityParentLockTimeout
	capabilityParentLockTimeout = 50 * time.Millisecond
	t.Cleanup(func() { capabilityParentLockTimeout = previous })

	container := t.TempDir()
	if resolved, resolveErr := filepath.EvalSymlinks(container); resolveErr == nil {
		container = resolved
	}
	owner := filepath.Join(container, "owner")
	rootPath := filepath.Join(owner, "repository")
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		t.Fatal(err)
	}
	originalMode := modeOfDirectory(t, owner)
	root, err := openAbsoluteDirectoryNoFollow(rootPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	capability, err := newGitFilesystemCapability(gitFilesystemCapabilityRoot{path: rootPath, directory: root})
	if err != nil {
		t.Fatal(err)
	}

	// Stand in for the ancestor helper: a separate open file description, which
	// is what flock arbitrates between, held for the whole test.
	holder, err := openAbsoluteDirectoryNoFollow(owner, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Close() }()
	if err := unix.Flock(int(holder.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		guards, lockErr := lockDarwinCapabilityParents(capability)
		restoreDarwinCapabilityParents(guards)
		done <- lockErr
	}()
	select {
	case lockErr := <-done:
		if lockErr == nil {
			t.Fatal("a parent another helper holds must not be frozen anyway")
		}
		if !strings.Contains(lockErr.Error(), "another WB Git helper held it") {
			t.Fatalf("want a bounded-wait diagnostic naming the holder, got %v", lockErr)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the wait for a held capability parent never ended; a hook that reaches WB would hang here")
	}
	if got := modeOfDirectory(t, owner); got != originalMode {
		t.Fatalf("a refused freeze changed the owner directory to %v, want its original %v", got, originalMode)
	}
}
