package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCanonicalRepositoryFromOwnedRootClosesRootOnFailure(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		gitDir      bool
		wrongPath   bool
		wantMessage string
	}{
		{name: "missing Git directory", wantMessage: "open canonical Git directory"},
		{name: "changed root path", gitDir: true, wrongPath: true, wantMessage: "canonical repository path changed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rootPath := t.TempDir()
			if test.gitDir {
				if err := os.Mkdir(filepath.Join(rootPath, ".git"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.Open(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = root.Close() })
			path := rootPath
			if test.wrongPath {
				path = t.TempDir()
			}
			canonical, err := openCanonicalRepositoryFromOwnedRoot(path, root, "test-canonical-git-directory")
			if canonical != nil || err == nil || !strings.Contains(err.Error(), test.wantMessage) {
				t.Fatalf("canonical=%v err=%v, want %q", canonical, err, test.wantMessage)
			}
			if _, err := root.Stat(); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("owned root after failure: %v, want closed", err)
			}
		})
	}
}

func TestOpenSessionReceiveCanonicalFromHeldRootKeepsCallerDescriptor(t *testing.T) {
	t.Parallel()
	for _, gitDir := range []bool{false, true} {
		name := "missing Git directory"
		if gitDir {
			name = "valid canonical root"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rootPath := t.TempDir()
			if gitDir {
				if err := os.Mkdir(filepath.Join(rootPath, ".git"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			held, err := os.Open(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = held.Close() })
			canonical, err := openSessionReceiveCanonicalFromHeldRoot(rootPath, held)
			if canonical != nil {
				t.Cleanup(canonical.close)
			}
			if gitDir {
				if err != nil || canonical == nil {
					t.Fatalf("retain valid root: canonical=%v err=%v", canonical, err)
				}
				canonicalRoot := canonical.root
				canonical.close()
				if _, err := canonicalRoot.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("retained root after canonical close: %v, want closed", err)
				}
			} else if canonical != nil || err == nil || !strings.Contains(err.Error(), "open canonical Git directory") {
				t.Fatalf("retain missing Git directory: canonical=%v err=%v", canonical, err)
			}
			if _, err := held.Stat(); err != nil {
				t.Fatalf("caller-held root closed by canonical opener: %v", err)
			}
		})
	}
}
