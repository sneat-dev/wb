package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareCanonicalWorktreesRootClassifiesTreeAndDirectoryProof(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		gitErr    error
		treeEntry string
		symlink   bool
		wantError string
	}{
		{name: "canonical Git query fails", gitErr: errors.New("Git read failed"), wantError: "inspect .worktrees"},
		{name: "tracked root", treeEntry: "040000 tree recorded .worktrees", wantError: "tracks .worktrees"},
		{name: "symlinked root", symlink: true, wantError: "refusing symlinked secure worktree directory"},
		{name: "accepted root"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, ".git", "info"), 0o700); err != nil {
				t.Fatal(err)
			}
			if test.symlink {
				if err := os.Symlink(t.TempDir(), filepath.Join(root, ".worktrees")); err != nil {
					t.Fatal(err)
				}
			}
			canonical, err := openCanonicalRepository(root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(canonical.close)
			ctx := withCanonicalGitInterceptor(t.Context(), func(_ context.Context, _ []string, _ func() ([]byte, error)) ([]byte, error) {
				return []byte(test.treeEntry), test.gitErr
			})
			path, directory, err := prepareCanonicalWorktreesRoot(ctx, canonical, strings.Repeat("a", 40))
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) || path != "" || directory != nil {
					t.Fatalf("path=%q directory=%v err=%v, want %q", path, directory, err, test.wantError)
				}
				return
			}
			if err != nil || path != filepath.Join(root, ".worktrees") || directory == nil || !directoryStillMatches(path, directory) {
				t.Fatalf("accepted root: path=%q directory=%v err=%v", path, directory, err)
			}
			_ = directory.Close()
		})
	}
}

func TestPrepareCanonicalWorktreesRootRefusesUnreadableExclude(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git", "info", "exclude"), 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, err := openCanonicalRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(canonical.close)
	ctx := withCanonicalGitInterceptor(t.Context(), func(_ context.Context, _ []string, _ func() ([]byte, error)) ([]byte, error) {
		return nil, nil
	})
	path, directory, err := prepareCanonicalWorktreesRoot(ctx, canonical, strings.Repeat("a", 40))
	if err == nil || !strings.Contains(err.Error(), "exclude canonical .worktrees root") || path != "" || directory != nil {
		t.Fatalf("unreadable exclude: path=%q directory=%v err=%v", path, directory, err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".worktrees")); !os.IsNotExist(err) {
		t.Fatalf("unreadable exclude created .worktrees: %v", err)
	}
}

func TestOpenRelativeParentDirectoryRefusesLaterSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "owner"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "owner", "linked")); err != nil {
		t.Fatal(err)
	}
	base, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	directory, path, err := openRelativeParentDirectory(base, root, "owner/linked")
	if err == nil || !strings.Contains(err.Error(), "refusing symlinked secure worktree directory") || directory != nil || path != "" {
		t.Fatalf("later symlink: directory=%v path=%q err=%v", directory, path, err)
	}
	if _, err := base.Stat(); err != nil {
		t.Fatalf("caller-owned base descriptor was closed: %v", err)
	}
}

func TestOpenRelativeParentDirectoryReturnsOwnedNestedParent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	base, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	directory, path, err := openRelativeParentDirectory(base, root, "owner/nested")
	if err != nil || directory == nil || path != filepath.Join(root, "owner", "nested") || !directoryStillMatches(path, directory) {
		t.Fatalf("nested parent: directory=%v path=%q err=%v", directory, path, err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := base.Stat(); err != nil {
		t.Fatalf("caller-owned base descriptor was closed: %v", err)
	}
}

func TestPrepareOperationRootRefusesOccupiedOperation(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	worktrees := filepath.Join(home, "worktrees")
	if err := os.Mkdir(worktrees, 0o700); err != nil {
		t.Fatal(err)
	}
	occupied := filepath.Join(worktrees, "occupied")
	if err := os.WriteFile(occupied, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := prepareOperationRoot(home, "occupied", nil)
	if err == nil || !strings.Contains(err.Error(), "open secure worktree directory occupied") {
		root.close()
		t.Fatalf("occupied operation accepted: root=%#v err=%v", root, err)
	}
	if data, readErr := os.ReadFile(occupied); readErr != nil || string(data) != "keep" {
		t.Fatalf("occupied operation changed: data=%q err=%v", data, readErr)
	}
}

func TestOpenDirectDirectoryNoFollowOwnsOnlyRealDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	directory, err := openDirectDirectoryNoFollow(root)
	if err != nil || directory == nil {
		t.Fatalf("open real directory: directory=%v err=%v", directory, err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(root, linked); err != nil {
		t.Fatal(err)
	}
	if directory, err := openDirectDirectoryNoFollow(linked); err == nil || directory != nil {
		t.Fatalf("opened symlinked directory: directory=%v err=%v", directory, err)
	}
}
