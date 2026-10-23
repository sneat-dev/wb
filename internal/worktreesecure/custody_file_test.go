package worktreesecure

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenOrCreateNoFollowDirectoryFileOwnsHeldChild(t *testing.T) {
	t.Parallel()
	root := physicalTempDir(t)
	parent := openTestDirectory(t, root)
	child, err := OpenOrCreateNoFollowDirectoryFile(int(parent.Fd()), "child", "held-child")
	if err != nil || child == nil {
		t.Fatalf("create child: descriptor=%v err=%v", child, err)
	}
	t.Cleanup(func() { _ = child.Close() })
	created, err := child.Stat()
	if err != nil || !created.IsDir() || child.Name() != "held-child" {
		t.Fatalf("created child: info=%v name=%q err=%v", created, child.Name(), err)
	}
	reopened, err := OpenOrCreateNoFollowDirectoryFile(int(parent.Fd()), "child", "reopened-child")
	if err != nil || reopened == nil {
		t.Fatalf("reopen child: descriptor=%v err=%v", reopened, err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedInfo, err := reopened.Stat()
	if err != nil || !os.SameFile(created, reopenedInfo) {
		t.Fatalf("reopened different directory: info=%v err=%v", reopenedInfo, err)
	}
	if err := os.Rename(filepath.Join(root, "child"), filepath.Join(root, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	moved, err := os.Stat(filepath.Join(root, "moved"))
	if err != nil || !os.SameFile(created, moved) {
		t.Fatalf("held descriptor lost moved directory: info=%v err=%v", moved, err)
	}
	replacement, err := os.Stat(filepath.Join(root, "child"))
	if err != nil || os.SameFile(created, replacement) {
		t.Fatalf("held descriptor followed replacement: info=%v err=%v", replacement, err)
	}
	if err := os.WriteFile(filepath.Join(root, "moved", "original-entry"), []byte("held"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := child.ReadDir(-1)
	if err != nil || len(entries) != 1 || entries[0].Name() != "original-entry" {
		t.Fatalf("held directory entries after rename: %v, %v", entries, err)
	}
}

func TestOpenOrCreateNoFollowDirectoryFileRefusesUnsafeChild(t *testing.T) {
	t.Parallel()
	root := physicalTempDir(t)
	parent := openTestDirectory(t, root)
	outside := physicalTempDir(t)
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "regular"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		wantError string
	}{
		{name: "linked", wantError: "refusing symlinked secure worktree directory"},
		{name: "regular", wantError: "open secure worktree directory"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			child, err := OpenOrCreateNoFollowDirectoryFile(int(parent.Fd()), test.name, "unsafe-child")
			if child != nil || err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("unsafe child opened: descriptor=%v err=%v", child, err)
			}
			if test.name == "linked" {
				if entries, readErr := os.ReadDir(outside); readErr != nil || len(entries) != 0 {
					t.Fatalf("linked outside changed: entries=%v err=%v", entries, readErr)
				}
			} else {
				if data, readErr := os.ReadFile(filepath.Join(root, "regular")); readErr != nil || string(data) != "keep" {
					t.Fatalf("regular child changed: data=%q err=%v", data, readErr)
				}
			}
		})
	}
}

//nolint:paralleltest // A parallel open could reuse the closed process FD before the refusal call.
func TestOpenOrCreateNoFollowDirectoryFileRefusesClosedParent(t *testing.T) {
	parent := openTestDirectory(t, physicalTempDir(t))
	fd := int(parent.Fd())
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	child, err := OpenOrCreateNoFollowDirectoryFile(fd, "child", "closed-parent")
	if child != nil || err == nil || !strings.Contains(err.Error(), "create secure worktree directory") {
		t.Fatalf("closed parent opened child: descriptor=%v err=%v", child, err)
	}
}
