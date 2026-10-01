package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestArchiveSourceReadRefusesRedirectedPaths(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory := filepath.Join(root, "real")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(directory, "source")
	if err := os.WriteFile(source, []byte("source bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := readArchiveSourceNoFollow(source); err != nil || string(got) != "source bytes" {
		t.Fatalf("held source read = %q, %v", got, err)
	}
	if _, err := readArchiveSourceNoFollow(filepath.Join(directory, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing source read = %v", err)
	}
	leaf := filepath.Join(directory, "leaf-link")
	if err := os.Symlink(source, leaf); err != nil {
		t.Fatal(err)
	}
	if _, err := readArchiveSourceNoFollow(leaf); !errors.Is(err, syscall.ELOOP) {
		t.Fatalf("redirected source leaf = %v", err)
	}
	parent := filepath.Join(root, "parent-link")
	if err := os.Symlink(directory, parent); err != nil {
		t.Fatal(err)
	}
	if _, err := readArchiveSourceNoFollow(filepath.Join(parent, "source")); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("redirected source parent = %v", err)
	}
}

func TestArchiveCopyKeepsZeroModeFallbackAndReportsUnreadableSource(t *testing.T) {
	t.Parallel()
	if got := archiveCopyMode(0); got != 0o600 {
		t.Fatalf("zero-mode archive fallback = %04o", got)
	}
	if got := archiveCopyMode(0o640); got != 0o640 {
		t.Fatalf("archive mode changed = %04o", got)
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(source, "blocked")
	if err := os.WriteFile(blocked, []byte("private bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(blocked); err == nil {
		t.Skip("effective user can read a zero-mode file")
	}
	destination := filepath.Join(root, "destination")
	if err := copyDir(source, destination); !errors.Is(err, syscall.EACCES) {
		t.Fatalf("unreadable archive source = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(destination, "blocked")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreadable source published private bytes: %v", err)
	}
}

func TestArchiveCopyPreservesExistingFileModeAndEmptyDirectories(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	if err := os.MkdirAll(filepath.Join(source, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "record"), []byte("new bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(destination, "record")
	if err := os.WriteFile(target, []byte("old bytes"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := copyDir(source, destination); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "new bytes" {
		t.Fatalf("existing archive target = %q, %v", got, err)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("existing archive target mode = %v, %v", info, err)
	}
	if info, err := os.Stat(filepath.Join(destination, "empty")); err != nil || !info.IsDir() {
		t.Fatalf("empty archive directory = %v, %v", info, err)
	}
}

//nolint:paralleltest // umask is process-wide; this checks archive permissions under a restrictive caller umask.
func TestArchiveCopyPreservesPrivateDirectoryAndExistingFileModesUnderUmask(t *testing.T) {
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "new", "destination")
	if err := os.MkdirAll(filepath.Join(source, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "record"), []byte("new bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(destination, "record")
	if err := os.WriteFile(target, []byte("old bytes"), 0o640); err != nil {
		t.Fatal(err)
	}
	oldUmask := syscall.Umask(0o077)
	defer syscall.Umask(oldUmask)
	if err := copyDir(source, destination); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{target, filepath.Join(destination, "empty")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o700)
		if path == target {
			want = 0o640
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode = %04o, want %04o", path, info.Mode().Perm(), want)
		}
	}
}

func TestArchiveCreatesPrivateDirectoriesWithoutFollowingExistingLinks(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	private := filepath.Join(root, "archive", "nested")
	held, err := openPrivateArchiveDirectory(private)
	if err != nil {
		t.Fatal(err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(root, "archive"), private} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("new archive directory mode = %v, %v", info, err)
		}
	}
	redirect := filepath.Join(root, "redirect")
	if err := os.Symlink(private, redirect); err != nil {
		t.Fatal(err)
	}
	if _, err := openPrivateArchiveDirectory(filepath.Join(redirect, "nested")); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("redirected archive directory = %v", err)
	}
}

func TestArchiveCopyRefusesRedirectedDestinationEntries(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"root", "nested", "leaf"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			source := filepath.Join(root, "source")
			if err := os.MkdirAll(filepath.Join(source, "nested"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "nested", "record"), []byte("archive bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(root, "outside")
			if err := os.Mkdir(outside, 0o700); err != nil {
				t.Fatal(err)
			}
			outsideFile := filepath.Join(outside, "record")
			if err := os.WriteFile(outsideFile, []byte("outside bytes"), 0o600); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(root, "destination")
			switch kind {
			case "root":
				if err := os.Symlink(outside, destination); err != nil {
					t.Fatal(err)
				}
			case "nested":
				if err := os.Mkdir(destination, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(destination, "nested")); err != nil {
					t.Fatal(err)
				}
			case "leaf":
				if err := os.MkdirAll(filepath.Join(destination, "nested"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outsideFile, filepath.Join(destination, "nested", "record")); err != nil {
					t.Fatal(err)
				}
			}
			if err := copyDir(source, destination); err == nil || !strings.Contains(err.Error(), "symlink") && !strings.Contains(err.Error(), "nonregular target") {
				t.Fatalf("%s destination redirected archive: %v", kind, err)
			}
			if got, err := os.ReadFile(outsideFile); err != nil || string(got) != "outside bytes" {
				t.Fatalf("%s redirect changed outside file: %q, %v", kind, got, err)
			}
			if _, err := os.Lstat(filepath.Join(outside, "nested", "record")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s redirect published outside archive: %v", kind, err)
			}
		})
	}
}

func TestArchiveCopyReplacesHardlinkWithoutChangingOutsideInode(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "record"), []byte("archive bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("outside bytes"), 0o640); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(destination, "record")
	if err := os.Link(outside, target); err != nil {
		t.Fatal(err)
	}
	if err := copyDir(source, destination); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "outside bytes" {
		t.Fatalf("archive changed outside hardlink inode: %q, %v", got, err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "archive bytes" {
		t.Fatalf("archive target after hardlink replacement: %q, %v", got, err)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("hardlink replacement changed observed target mode: %v, %v", info, err)
	}
}

func TestArchiveTargetWriteAndModeRefuseInvalidHeldEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	parent, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if mode, err := archiveTargetMode(parent, "missing", 0o600); err != nil || mode != 0o600 {
		t.Fatalf("new target mode = %04o, %v", mode, err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := archiveTargetMode(parent, "directory", 0o600); err == nil || !strings.Contains(err.Error(), "nonregular target") {
		t.Fatalf("directory target mode = %v", err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := archiveTargetMode(parent, "closed", 0o600); !errors.Is(err, syscall.EBADF) {
		t.Fatalf("closed target parent = %v", err)
	}
	redirect := filepath.Join(root, "redirect")
	if err := os.Symlink(root, redirect); err != nil {
		t.Fatal(err)
	}
	if err := writeArchiveTargetNoFollow(filepath.Join(redirect, "record"), []byte("private"), 0o600); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("redirected target parent = %v", err)
	}
}
