//go:build e2e && !windows

package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestE2EBranchTransitionTransferRetirementIdentityRefusals(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"parent-open", "second-identity", "unlink", "absence-error", "absence-occupant"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := filepath.Join(root, "replacement")
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			held, err := openAbsoluteDirectoryNoFollow(path, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = held.Close() })
			original, err := held.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if boundary == "parent-open" {
				missing := filepath.Join(root, "missing", "replacement")
				if err := retireRepositoryTransferReplacement(missing, held); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("native missing parent = %v", err)
				}
				return
			}
			hit := false
			var nativeCause error
			err = retireRepositoryTransferReplacementWithObservation(path, held, func(stage string, parent *os.File) {
				if boundary == "second-identity" && stage == "identity" {
					hit = true
					if err := os.Rename(path, path+"-held"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(path, "occupant"), []byte("retain replacement"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if boundary == "unlink" && stage == "unlink" {
					hit = true
					if err := parent.Close(); err != nil {
						t.Fatal(err)
					}
					nativeCause = unlinkResidueEntry(parent, root, "replacement", unix.AT_REMOVEDIR)
				}
				if boundary == "absence-error" && stage == "absence" {
					hit = true
					if err := parent.Close(); err != nil {
						t.Fatal(err)
					}
					_, nativeCause = noFollowChildAbsent(int(parent.Fd()), "replacement")
				}
				if boundary == "absence-occupant" && stage == "absence" {
					hit = true
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(path, "occupant"), []byte("retain late occupant"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			})
			if !hit || err == nil {
				t.Fatalf("native retirement %s = %v, hit=%v", boundary, err, hit)
			}
			switch boundary {
			case "second-identity":
				if !strings.Contains(err.Error(), "changed during retirement") {
					t.Fatalf("identity refusal = %v", err)
				}
			case "unlink":
				cause := errors.Unwrap(nativeCause)
				if cause == nil || !errors.Is(err, cause) || err.Error() != nativeCause.Error() {
					t.Fatalf("unlink error = %v, native control=%v", err, nativeCause)
				}
			case "absence-error":
				if nativeCause == nil || !strings.Contains(err.Error(), "verify retired") {
					t.Fatalf("absence error = %v, native control=%v", err, nativeCause)
				}
			case "absence-occupant":
				if !strings.Contains(err.Error(), "verify retired") {
					t.Fatalf("late occupant = %v", err)
				}
			}
			if current, err := held.Stat(); err != nil || !os.SameFile(original, current) {
				t.Fatalf("held original identity lost: %v, %v", current, err)
			}
			if boundary == "second-identity" || boundary == "absence-occupant" {
				if raw, err := os.ReadFile(filepath.Join(path, "occupant")); err != nil || !strings.HasPrefix(string(raw), "retain ") {
					t.Fatalf("new occupant lost: %q, %v", raw, err)
				}
			}
		})
	}
}

func TestE2EBranchTransitionCloneMovesConsumeEveryOwnedDescriptor(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"success", "occupied", "reopen-refusal"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
			if err := os.Mkdir(source, 0700); err != nil {
				t.Fatal(err)
			}
			hooks := moveRenameDirectoryHooks{}
			if boundary == "occupied" {
				if err := os.Mkdir(destination, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if boundary == "reopen-refusal" {
				if os.Geteuid() == 0 {
					t.Skip("root can bypass directory permission refusal")
				}
				hooks.afterMove = func() {
					if err := os.Chmod(destination, 0); err != nil {
						t.Fatal(err)
					}
				}
				t.Cleanup(func() { _ = os.Chmod(destination, 0700) })
			}
			var owned *os.File
			var nativeErr error
			err := moveCloneDirectoryAndClose(source, destination, hooks, func(file *os.File, cause error) { owned, nativeErr = file, cause })
			if boundary == "occupied" {
				if owned != nil || !errors.Is(err, os.ErrExist) {
					t.Fatalf("collision ownership = %v, %v", owned, err)
				}
				return
			}
			if owned == nil {
				t.Fatalf("native moved ownership missing: %v", err)
			}
			if boundary == "success" && err != nil {
				t.Fatal(err)
			}
			if boundary == "reopen-refusal" && (nativeErr == nil || err != nativeErr) {
				t.Fatalf("real owned-FD plus error lost: %v, %v", nativeErr, err)
			}
			if _, err := owned.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("native owned descriptor leaked: %v", err)
			}
		})
	}
}
