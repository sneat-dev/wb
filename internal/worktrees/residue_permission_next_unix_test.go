//go:build !windows

package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

func TestResiduePermissionRepairPreservesFailureAndOwnership(t *testing.T) {
	t.Parallel()
	requireUnprivilegedResidueTest(t)
	for _, phase := range []string{"success", "stat", "chmod", "retry", "vanished", "operation not permitted"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			target := filepath.Join(root, "entry")
			if err := os.WriteFile(target, []byte("retained"), 0600); err != nil {
				t.Fatal(err)
			}
			parent, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = parent.Close(); _ = os.Chmod(root, 0700) })
			if err := parent.Chmod(0500); err != nil {
				t.Fatal(err)
			}
			access := nativeResidueRemovalIO()
			nativeUnlink, nativeChmod := access.unlink, access.chmod
			calls := 0
			var denial error
			access.unlink = func(fd int, name string, flags int) error {
				calls++
				if fd != int(parent.Fd()) || name != "entry" || flags != 0 {
					t.Fatalf("unlink arguments = %d %q %d", fd, name, flags)
				}
				if calls == 1 && phase == "operation not permitted" {
					denial = unix.EPERM
					return denial
				}
				if calls == 2 && phase == "retry" {
					if err := parent.Close(); err != nil {
						t.Fatal(err)
					}
					return nativeUnlink(int(parent.Fd()), name, flags)
				}
				err := nativeUnlink(fd, name, flags)
				if calls == 1 {
					denial = err
					if !errors.Is(err, unix.EACCES) && !errors.Is(err, unix.EPERM) {
						t.Fatalf("native denial = %v", err)
					}
					if phase == "stat" {
						if err := parent.Close(); err != nil {
							t.Fatal(err)
						}
					}
				}
				return err
			}
			access.chmod = func(fd int, mode uint32) error {
				if mode != 0700 {
					t.Fatalf("repaired mode = %#o", mode)
				}
				if phase == "chmod" {
					if err := parent.Close(); err != nil {
						t.Fatal(err)
					}
				}
				err := nativeChmod(int(parent.Fd()), mode)
				if err == nil && phase == "vanished" {
					if err := os.Remove(target); err != nil {
						t.Fatal(err)
					}
				}
				return err
			}
			err = unlinkResidueEntryWithIO(parent, root, "entry", 0, access)
			fails := phase == "stat" || phase == "chmod" || phase == "retry"
			if fails {
				if !errors.Is(err, syscall.EBADF) {
					t.Fatalf("native failure = %v", err)
				}
				if phase != "retry" && !errors.Is(err, denial) {
					t.Fatalf("original denial lost: %v", err)
				}
				if phase == "retry" && !strings.Contains(err.Error(), "after granting") {
					t.Fatalf("retry diagnostic = %v", err)
				}
				raw, readErr := os.ReadFile(target)
				if readErr != nil || string(raw) != "retained" {
					t.Fatalf("occupant changed: %q %v", raw, readErr)
				}
				if _, err := parent.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("original descriptor = %v", err)
				}
			} else {
				if err != nil || calls != 2 {
					t.Fatalf("repair = %v; calls %d", err, calls)
				}
				if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("entry remains: %v", err)
				}
				info, err := parent.Stat()
				if err != nil || info.Mode().Perm() != 0700 {
					t.Fatalf("caller descriptor/mode = %v %v", info, err)
				}
			}
		})
	}
}

func TestResiduePermissionNativeClassificationAndAnchoring(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	parent, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	if err := unlinkResidueEntry(parent, root, "absent", 0); err != nil {
		t.Fatalf("vanished entry = %v", err)
	}
	child := filepath.Join(root, "nonempty")
	if err := os.Mkdir(child, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "keep"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := unlinkResidueEntry(parent, root, "nonempty", unix.AT_REMOVEDIR); err == nil || !strings.Contains(err.Error(), "remove residue") {
		t.Fatalf("nonempty directory = %v", err)
	}
	if _, err := os.Stat(filepath.Join(child, "keep")); err != nil {
		t.Fatalf("occupant lost: %v", err)
	}
	// Permission repair acts on the retained directory even when its name is replaced.
	heldPath := root + "-held"
	if err := os.Rename(root, heldPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(heldPath) })
	if err := os.Mkdir(root, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0700) })
	if err := parent.Chmod(0440); err != nil {
		t.Fatal(err)
	}
	if err := grantOwnerWriteAt(parent, root); err != nil {
		t.Fatal(err)
	}
	original, err := parent.Stat()
	if err != nil || original.Mode().Perm() != 0740 {
		t.Fatalf("held mode = %v %v", original, err)
	}
	replacement, err := os.Stat(root)
	if err != nil || replacement.Mode().Perm() != 0500 {
		t.Fatalf("replacement modified = %v %v", replacement, err)
	}
}
