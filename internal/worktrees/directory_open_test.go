package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenDirectoryAtNoFollowOwnsOnlyChild(t *testing.T) {
	t.Parallel()
	rootPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(rootPath, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })

	for _, test := range []struct {
		name    string
		missing bool
	}{
		{name: "child"},
		{name: "missing", missing: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			child, err := openDirectoryAtNoFollow(int(parent.Fd()), test.name, "wb-test-receive-child",
				"open exact test child", "wrap exact test child")
			if test.missing {
				if child != nil || err == nil || !strings.HasPrefix(err.Error(), "open exact test child: ") || !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing child = %v, err = %v", child, err)
				}
			} else {
				if err != nil || child == nil {
					t.Fatalf("open child = %v, err = %v", child, err)
				}
				t.Cleanup(func() { _ = child.Close() })
				if child.Name() != "wb-test-receive-child" {
					t.Fatalf("child descriptor name = %q", child.Name())
				}
				if err := child.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := child.Stat(); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("child after close: %v, want closed", err)
				}
			}
			if _, err := parent.Stat(); err != nil {
				t.Fatalf("parent closed by child operation: %v", err)
			}
		})
	}
}
