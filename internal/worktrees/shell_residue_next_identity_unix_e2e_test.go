//go:build e2e && (darwin || linux)

package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/unixcompat"
)

func TestE2EShellResidueNextFinalIdentityRetainsEmptyReplacement(t *testing.T) {
	t.Parallel()
	handle := newShellResidueNextHandle(t)
	original, err := handle.worktree.Stat()
	if err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(handle.worktreePath, "authorized-residue")
	if err := os.WriteFile(payload, []byte("authorized cleanup bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(handle.parentPath, "sibling-authority")
	if err := os.WriteFile(sibling, []byte("parent sibling bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	retained := handle.worktreePath + ".retained"
	var replacement os.FileInfo
	observed := false
	err = removeWorktreeResidueObserved(handle, func(phase residueRemovalPhase, held *cleanupWorktreeHandle) {
		if phase != residueBeforeRootRemoval {
			return
		}
		observed = true
		if entries, err := os.ReadDir(held.worktreePath); err != nil || len(entries) != 0 {
			t.Fatalf("authorized residue not emptied before final boundary: %v %v", entries, err)
		}
		if err := os.Rename(held.worktreePath, retained); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(held.worktreePath, 0700); err != nil {
			t.Fatal(err)
		}
		replacement, err = os.Stat(held.worktreePath)
		if err != nil {
			t.Fatal(err)
		}
		if os.SameFile(original, replacement) {
			t.Fatal("native empty replacement reused owned inode")
		}
	})
	if !observed || err == nil || !strings.Contains(err.Error(), "remove residual worktree") || !strings.Contains(err.Error(), "directory identity changed before retirement") {
		t.Fatalf("empty replacement admitted: %v observed=%v", err, observed)
	}
	heldInfo, err := handle.worktree.Stat()
	if err != nil {
		t.Fatal(err)
	}
	oldInfo, err := os.Stat(retained)
	if err != nil || !os.SameFile(original, oldInfo) || !os.SameFile(heldInfo, oldInfo) {
		t.Fatalf("owned original lost: %v %v", oldInfo, err)
	}
	newInfo, err := os.Stat(handle.worktreePath)
	if err != nil || !os.SameFile(replacement, newInfo) || os.SameFile(original, newInfo) {
		t.Fatalf("unowned empty replacement lost: %v %v", newInfo, err)
	}
	for _, path := range []string{retained, handle.worktreePath} {
		if entries, err := os.ReadDir(path); err != nil || len(entries) != 0 {
			t.Fatalf("refusal changed empty namespace or published metadata: %s %v %v", path, entries, err)
		}
	}
	if got, err := os.ReadFile(sibling); err != nil || string(got) != "parent sibling bytes" {
		t.Fatalf("refusal changed sibling evidence: %q %v", got, err)
	}
}

func TestE2EShellResidueNextFinalIdentityAcceptsNativeAbsence(t *testing.T) {
	t.Parallel()
	handle := newShellResidueNextHandle(t)
	observed := false
	err := removeWorktreeResidueObserved(handle, func(phase residueRemovalPhase, held *cleanupWorktreeHandle) {
		if phase != residueBeforeRootRemoval {
			return
		}
		observed = true
		if err := os.Remove(held.worktreePath); err != nil {
			t.Fatal(err)
		}
	})
	if !observed || err != nil {
		t.Fatalf("native absent entry stopped ENOENT success: %v observed=%v", err, observed)
	}
	if _, err := os.Lstat(handle.worktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absent retirement was recreated: %v", err)
	}
}

func TestE2EShellResidueNextFinalIdentityPreservesClosedParentCause(t *testing.T) {
	t.Parallel()
	handle := newShellResidueNextHandle(t)
	var nativeCause error
	observed := false
	err := removeWorktreeResidueObserved(handle, func(phase residueRemovalPhase, held *cleanupWorktreeHandle) {
		if phase != residueBeforeRootRemoval {
			return
		}
		observed = true
		if err := held.parent.Close(); err != nil {
			t.Fatal(err)
		}
		nativeCause = unix.Fstatat(int(held.parent.Fd()), filepath.Base(held.worktreePath), &unix.Stat_t{}, unix.AT_SYMLINK_NOFOLLOW)
		if nativeCause == nil {
			t.Fatal("closed owned parent accepted absence inspection")
		}
	})
	if !observed || nativeCause == nil || !errors.Is(err, nativeCause) || !strings.Contains(err.Error(), "remove residual worktree") {
		t.Fatalf("closed-parent cause lost: %v native=%v observed=%v", err, nativeCause, observed)
	}
	if info, err := os.Stat(handle.worktreePath); err != nil || !info.IsDir() {
		t.Fatalf("inspection refusal removed checkout: %v %v", info, err)
	}
}
