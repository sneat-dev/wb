//go:build e2e

package worktrees

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func projectionBoundaryRoot(t *testing.T) (string, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(base, "worktree")
	outside := filepath.Join(base, "outside")
	for _, path := range []string{worktree, outside} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return worktree, outside
}

func projectionBoundaryError(t *testing.T, err error) {
	t.Helper()
	if err == nil || (!errors.Is(err, syscall.ELOOP) && !errors.Is(err, syscall.ENOTDIR) && !strings.Contains(err.Error(), "symlink")) {
		t.Fatalf("projection component did not fail at no-follow directory open: %v", err)
	}
}

func TestE2EProjectionDirectoryRefusesSymlinkAndRegularOccupants(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"symlink", "regular-file"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			worktree, outside := projectionBoundaryRoot(t)
			outsideFile := filepath.Join(outside, workLogProjectionName)
			wantOutside := []byte("outside owner\n")
			if err := os.WriteFile(outsideFile, wantOutside, 0o600); err != nil {
				t.Fatal(err)
			}
			entry := filepath.Join(worktree, workLogProjectionDirectory)
			if kind == "symlink" {
				if err := os.Symlink(outside, entry); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(entry, []byte("owned occupant\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, create := range []bool{false, true} {
				directory, err := openWorkLogProjectionDirectory(worktree, create)
				if directory != nil {
					_ = directory.Close()
					t.Fatalf("create=%t returned a handle for %s occupant", create, kind)
				}
				projectionBoundaryError(t, err)
			}
			projectionBoundaryError(t, removeWorkLogProjection(worktree))
			gotOutside, err := os.ReadFile(outsideFile)
			if err != nil || !bytes.Equal(gotOutside, wantOutside) {
				t.Fatalf("outside projection changed: %q, %v", gotOutside, err)
			}
			if kind == "symlink" {
				if target, err := os.Readlink(entry); err != nil || target != outside {
					t.Fatalf("symlink occupant changed: %q, %v", target, err)
				}
			} else if got, err := os.ReadFile(entry); err != nil || string(got) != "owned occupant\n" {
				t.Fatalf("regular occupant changed: %q, %v", got, err)
			}
		})
	}
}

func TestE2EProjectionHeldDirectoryDetectsLaterPathSwap(t *testing.T) {
	t.Parallel()
	worktree, outside := projectionBoundaryRoot(t)
	held, err := openWorkLogProjectionDirectory(worktree, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := held.Close(); err != nil {
			t.Errorf("close held projection directory: %v", err)
		}
	})
	entry := filepath.Join(worktree, workLogProjectionDirectory)
	if !directoryStillMatches(entry, held) {
		t.Fatal("new projection directory did not match its held descriptor")
	}
	if info, err := held.Stat(); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("new projection directory mode = %v, %v", info, err)
	}
	wantHeld := []byte("pinned directory\n")
	if err := os.WriteFile(filepath.Join(entry, "owner.txt"), wantHeld, 0o600); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(worktree, "moved-projection")
	if err := os.Rename(entry, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, entry); err != nil {
		t.Fatal(err)
	}
	if directoryStillMatches(entry, held) {
		t.Fatal("held directory was mistaken for the rebound pathname")
	}
	gotHeld, err := readBytesAt(held, "owner.txt")
	if err != nil || !bytes.Equal(gotHeld, wantHeld) {
		t.Fatalf("held descriptor lost original bytes: %q, %v", gotHeld, err)
	}
	opened, err := openWorkLogProjectionDirectory(worktree, false)
	if opened != nil {
		_ = opened.Close()
		t.Fatal("rebound symlink yielded a projection directory")
	}
	projectionBoundaryError(t, err)
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("outside directory mutated: %v, %v", entries, err)
	}
}

func TestE2EProjectionDeletionKeepsLegacyAndNonemptyDirectorySeparate(t *testing.T) {
	t.Parallel()
	worktree, _ := projectionBoundaryRoot(t)
	currentDir := filepath.Join(worktree, workLogProjectionDirectory)
	if err := os.Mkdir(currentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(currentDir, workLogProjectionName)
	legacy := filepath.Join(worktree, legacyWorkLogProjectionName)
	sibling := filepath.Join(currentDir, "owner.txt")
	for path, contents := range map[string]string{current: "current\n", legacy: "legacy\n", sibling: "owned sibling\n"} {
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := removeWorkLogProjection(worktree); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(current); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("current projection survived deletion: %v", err)
	}
	if _, err := os.Stat(currentDir); err != nil {
		t.Fatalf("nonempty current directory was removed: %v", err)
	}
	for path, want := range map[string]string{legacy: "legacy\n", sibling: "owned sibling\n"} {
		if got, err := os.ReadFile(path); err != nil || string(got) != want {
			t.Fatalf("unrelated %s changed: %q, %v", path, got, err)
		}
	}
	if err := removeLegacyWorkLogProjection(worktree); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy pointer survived deletion: %v", err)
	}
	if got, err := os.ReadFile(sibling); err != nil || string(got) != "owned sibling\n" {
		t.Fatalf("current sibling changed after legacy deletion: %q, %v", got, err)
	}
}

//nolint:paralleltest // the native Git fixture scopes WB home and Git configuration through process environment.
func TestE2EProjectionWriteRefusesDirectoryOccupantBeforeInstructionPublication(t *testing.T) {
	fixture := newGitFixture(t)
	worktree := fixture.canonical
	currentDir := filepath.Join(worktree, workLogProjectionDirectory)
	if err := os.Mkdir(currentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	occupied := filepath.Join(currentDir, workLogProjectionName)
	if err := os.Mkdir(occupied, 0o700); err != nil {
		t.Fatal(err)
	}
	ownerFile := filepath.Join(occupied, "owner.txt")
	if err := os.WriteFile(ownerFile, []byte("owned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	instructions := filepath.Join(worktree, worktreeInstructionsName)
	wantInstructions := []byte("# Repository instructions\n")
	if err := os.WriteFile(instructions, wantInstructions, 0o600); err != nil {
		t.Fatal(err)
	}
	projection := workLogProjection{Version: 1, EffortID: "effort", RunID: "run", ClaimID: strings.Repeat("a", 64), Lifecycle: "active"}
	err := writeWorkLogProjection(worktree, projection)
	if !errors.Is(err, syscall.EISDIR) && !errors.Is(err, syscall.ENOTEMPTY) && !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("directory occupant did not stop atomic projection publication: %v", err)
	}
	if got, err := os.ReadFile(ownerFile); err != nil || string(got) != "owned\n" {
		t.Fatalf("projection occupant changed: %q, %v", got, err)
	}
	if got, err := os.ReadFile(instructions); err != nil || !bytes.Equal(got, wantInstructions) {
		t.Fatalf("repository instructions changed after refused publication: %q, %v", got, err)
	}
	if entries, err := os.ReadDir(currentDir); err != nil || len(entries) != 1 || entries[0].Name() != workLogProjectionName {
		t.Fatalf("atomic write left a temporary entry: %v, %v", entries, err)
	}
	if err := os.Remove(ownerFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(occupied); err != nil {
		t.Fatal(err)
	}
	if err := writeWorkLogProjection(worktree, projection); err != nil {
		t.Fatalf("valid projection publication failed after occupant removal: %v", err)
	}
	if got, err := os.ReadFile(instructions); err != nil || !bytes.Equal(got, wantInstructions) {
		t.Fatalf("repository instructions changed after valid publication: %q, %v", got, err)
	}
	readBack, err := readWorkLogProjection(worktree)
	if err != nil || readBack != projection {
		t.Fatalf("published projection = %#v, %v; want %#v", readBack, err, projection)
	}
}

func TestE2EProjectionRemovalRefusesDirectoryOccupants(t *testing.T) {
	t.Parallel()
	for _, legacy := range []bool{false, true} {
		name := "current"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			worktree, outside := projectionBoundaryRoot(t)
			parent := worktree
			leaf := legacyWorkLogProjectionName
			if !legacy {
				parent = filepath.Join(worktree, workLogProjectionDirectory)
				leaf = workLogProjectionName
				if err := os.Mkdir(parent, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			occupant := filepath.Join(parent, leaf)
			if err := os.Mkdir(occupant, 0o700); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(occupant, "owner.txt")
			if err := os.WriteFile(marker, []byte("owned\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			var err error
			if legacy {
				err = removeLegacyWorkLogProjection(worktree)
				if err == nil || !strings.Contains(err.Error(), "remove legacy work-log projection") {
					t.Fatalf("legacy directory occupant refusal = %v", err)
				}
			} else {
				err = removeWorkLogProjection(worktree)
				if err == nil || !strings.Contains(err.Error(), "reset old work-log projection") {
					t.Fatalf("current directory occupant refusal = %v", err)
				}
			}
			if got, err := os.ReadFile(marker); err != nil || string(got) != "owned\n" {
				t.Fatalf("occupied projection changed: %q, %v", got, err)
			}
			if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
				t.Fatalf("outside directory mutated: %v, %v", entries, err)
			}
		})
	}
}

func TestE2EProjectionDeletionReportsNonemptyParentRefusal(t *testing.T) {
	t.Parallel()
	worktree, outside := projectionBoundaryRoot(t)
	directory := filepath.Join(worktree, workLogProjectionDirectory)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	projection := filepath.Join(directory, workLogProjectionName)
	if err := os.WriteFile(projection, []byte("current\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(worktree, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(worktree, 0o700) })
	err := removeWorkLogProjection(worktree)
	if err == nil || !strings.Contains(err.Error(), "remove empty work-log projection directory") ||
		(!errors.Is(err, syscall.EACCES) && !errors.Is(err, syscall.EPERM)) {
		t.Fatalf("parent deletion refusal = %v", err)
	}
	if _, err := os.Lstat(projection); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("projection leaf was not removed before parent refusal: %v", err)
	}
	if _, err := os.Stat(directory); err != nil {
		t.Fatalf("projection directory disappeared despite parent refusal: %v", err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("outside directory mutated: %v, %v", entries, err)
	}
}

func TestE2ELegacyProjectionDeletionRefusesNonDirectoryWorktree(t *testing.T) {
	t.Parallel()
	root, outside := projectionBoundaryRoot(t)
	blocked := filepath.Join(root, "not-a-worktree")
	want := []byte("owned file\n")
	if err := os.WriteFile(blocked, want, 0o600); err != nil {
		t.Fatal(err)
	}
	err := removeLegacyWorkLogProjection(blocked)
	if !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("legacy deletion did not refuse a regular-file worktree: %v", err)
	}
	if got, err := os.ReadFile(blocked); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("non-directory worktree changed: %q, %v", got, err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("outside directory mutated: %v, %v", entries, err)
	}
}

func TestE2EManagedInstructionsReportOpenAndWriteRefusals(t *testing.T) {
	t.Parallel()
	t.Run("symlinked-instructions", func(t *testing.T) {
		t.Parallel()
		worktree, outside := projectionBoundaryRoot(t)
		outsideFile := filepath.Join(outside, "instructions")
		want := []byte("outside instructions\n")
		if err := os.WriteFile(outsideFile, want, 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(worktree, worktreeInstructionsName)
		if err := os.Symlink(outsideFile, link); err != nil {
			t.Fatal(err)
		}
		if err := writeManagedWorktreeInstructions(worktree); err != nil {
			t.Fatalf("a repository-owned symlink must be preserved without following it: %v", err)
		}
		if target, err := os.Readlink(link); err != nil || target != outsideFile {
			t.Fatalf("instruction symlink changed: %q, %v", target, err)
		}
		if got, err := os.ReadFile(outsideFile); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("outside instructions changed: %q, %v", got, err)
		}
	})
	t.Run("non-directory-worktree", func(t *testing.T) {
		t.Parallel()
		worktree, _ := projectionBoundaryRoot(t)
		blocked := filepath.Join(worktree, "blocked")
		want := []byte("owned file\n")
		if err := os.WriteFile(blocked, want, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeManagedWorktreeInstructions(blocked); !errors.Is(err, syscall.ENOTDIR) {
			t.Fatalf("instruction open refusal = %v", err)
		}
		if got, err := os.ReadFile(blocked); err != nil || !bytes.Equal(got, want) {
			t.Fatalf("non-directory worktree changed: %q, %v", got, err)
		}
	})
	t.Run("read-only-worktree", func(t *testing.T) {
		t.Parallel()
		worktree, outside := projectionBoundaryRoot(t)
		if err := os.Chmod(worktree, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(worktree, 0o700) })
		err := writeManagedWorktreeInstructions(worktree)
		if !errors.Is(err, syscall.EACCES) && !errors.Is(err, syscall.EPERM) {
			t.Fatalf("instruction atomic write refusal = %v", err)
		}
		if _, err := os.Lstat(filepath.Join(worktree, worktreeInstructionsName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("instructions appeared despite write refusal: %v", err)
		}
		if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
			t.Fatalf("outside directory mutated: %v, %v", entries, err)
		}
	})
}
