package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/unixcompat"
)

// shellResidueNextPhysicalTemp resolves platform temp aliases before no-follow traversal.
func shellResidueNextPhysicalTemp(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// newShellResidueNextHandle holds registered native parent and checkout authority.
func newShellResidueNextHandle(t *testing.T) *cleanupWorktreeHandle {
	t.Helper()
	parentPath := shellResidueNextPhysicalTemp(t)
	checkoutPath := filepath.Join(parentPath, "checkout")
	if err := os.Mkdir(checkoutPath, 0700); err != nil {
		t.Fatal(err)
	}
	parent, err := openAbsoluteDirectoryNoFollow(parentPath, false)
	if err != nil {
		t.Fatal(err)
	}
	checkout, err := openAbsoluteDirectoryNoFollow(checkoutPath, false)
	if err != nil {
		_ = parent.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = checkout.Close(); _ = parent.Close() })
	return &cleanupWorktreeHandle{parentPath: parentPath, parent: parent, worktreePath: checkoutPath, worktree: checkout}
}

func TestShellResidueNextRemovalRetainsNativeClosedEnumerationCause(t *testing.T) {
	t.Parallel()
	handle := newShellResidueNextHandle(t)
	var nativeCause error
	err := removeWorktreeResidueObserved(handle, func(phase residueRemovalPhase, held *cleanupWorktreeHandle) {
		if phase != residueAfterValidation {
			return
		}
		if err := held.worktree.Close(); err != nil {
			t.Fatal(err)
		}
		_, nativeCause = openResidueDirectoryAt(held.worktree, ".", held.worktreePath)
		if nativeCause == nil {
			t.Fatal("closed owned checkout did not refuse Openat")
		}
	})
	if nativeCause == nil || !errors.Is(err, nativeCause) || !strings.Contains(err.Error(), "list residue directory") {
		t.Fatalf("enumeration refusal=%v native=%v", err, nativeCause)
	}
	if _, err := os.Stat(handle.worktreePath); err != nil {
		t.Fatalf("refused checkout removed: %v", err)
	}
}

func TestShellResidueNextRemovalRetainsConcurrentNativeOccupant(t *testing.T) {
	t.Parallel()
	handle := newShellResidueNextHandle(t)
	occupant := filepath.Join(handle.worktreePath, "successor")
	observed := false
	var nativeUnlinkCause error
	err := removeWorktreeResidueObserved(handle, func(phase residueRemovalPhase, held *cleanupWorktreeHandle) {
		if phase != residueBeforeRootRemoval {
			return
		}
		observed = true
		if err := os.WriteFile(occupant, []byte("successor bytes"), 0600); err != nil {
			t.Fatal(err)
		}
		nativeUnlinkCause = unix.Unlinkat(int(held.parent.Fd()), filepath.Base(held.worktreePath), unix.AT_REMOVEDIR)
		if nativeUnlinkCause == nil {
			t.Fatal("nonempty actual checkout did not refuse AT_REMOVEDIR")
		}
	})
	if !observed || nativeUnlinkCause == nil || !errors.Is(err, nativeUnlinkCause) || !strings.Contains(err.Error(), "remove residual worktree") {
		t.Fatalf("concurrent occupant refusal=%v observed=%v", err, observed)
	}
	if got, err := os.ReadFile(occupant); err != nil || string(got) != "successor bytes" {
		t.Fatalf("occupant removed: %q %v", got, err)
	}
}

func TestShellResidueNextUnregisteredInspectionRetainsNativeRefusals(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	file := filepath.Join(parent, "file")
	if err := os.WriteFile(file, []byte("parent bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(file, "checkout")
	_, cause := os.Lstat(path)
	if cause == nil {
		t.Fatal("regular-file parent did not refuse Lstat")
	}
	removed, err := removeUnregisteredWorktreeResidue(nil, path)
	var native *os.PathError
	if removed || !errors.As(cause, &native) || !errors.Is(err, native.Err) {
		t.Fatalf("inspection refusal=%v %v native=%v", removed, err, cause)
	}
	handle := newShellResidueNextHandle(t)
	if err := handle.parent.Close(); err != nil {
		t.Fatal(err)
	}
	removed, err = removeUnregisteredWorktreeResidue(handle, handle.worktreePath)
	if removed || err == nil || !strings.Contains(err.Error(), "parent path changed") {
		t.Fatalf("authority refusal=%v %v", removed, err)
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != "parent bytes" {
		t.Fatalf("original occupant altered: %q %v", got, err)
	}
}

func TestShellResidueNextTraversalRetainsClosedOwnedDescriptorCause(t *testing.T) {
	t.Parallel()
	handle := newShellResidueNextHandle(t)
	if err := handle.worktree.Close(); err != nil {
		t.Fatal(err)
	}
	native := unix.Fstatat(int(handle.worktree.Fd()), "entry", &unix.Stat_t{}, unix.AT_SYMLINK_NOFOLLOW)
	if native == nil {
		t.Fatal("closed owned descriptor accepted Fstatat")
	}
	if err := removeResidueEntry(handle.worktree, handle.worktreePath, "entry", 0); !errors.Is(err, native) {
		t.Fatalf("entry cause lost: %v native=%v", err, native)
	}
	if err := removeDirectoryContentsAt(handle.worktree, handle.worktreePath, 0); err == nil {
		t.Fatal("closed owned directory accepted traversal")
	}
}
