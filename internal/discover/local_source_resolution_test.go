package discover

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotLocalSourceResolutionFailures(t *testing.T) {
	t.Parallel()
	failure := errors.New("resolution unavailable")
	t.Run("absolute path", func(t *testing.T) {
		t.Parallel()
		got, err := snapshotLocalSourceResolved("relative", func(string) (string, error) { return "", failure }, os.Stat)
		if !errors.Is(err, failure) || got.fingerprint != "" {
			t.Fatalf("snapshot = %+v, err = %v", got, err)
		}
	})
	t.Run("root removed after symlink resolution", func(t *testing.T) {
		t.Parallel()
		got, err := snapshotLocalSourceResolved(t.TempDir(), filepath.Abs, func(string) (os.FileInfo, error) { return nil, failure })
		if !errors.Is(err, failure) || got.fingerprint != "" {
			t.Fatalf("snapshot = %+v, err = %v", got, err)
		}
	})
	t.Run("owner removed during observation", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		root, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatal(err)
		}
		owner := filepath.Join(root, "acme")
		if err := os.Mkdir(owner, 0o755); err != nil {
			t.Fatal(err)
		}
		calls := 0
		got, err := snapshotLocalSourceResolved(root, filepath.Abs, func(path string) (os.FileInfo, error) {
			if path == owner {
				calls++
				return nil, os.ErrNotExist
			}
			return os.Stat(path)
		})
		if err != nil || got.fingerprint == "" || calls != 1 {
			t.Fatalf("snapshot = %+v, err = %v, owner observations = %d", got, err, calls)
		}
	})
}
