//go:build e2e

package archiveprune

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2EDeletionRefusesRootOpenFailureAfterRealRevalidation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	run(t, root, "git", "init", "-q", "-b", "main")
	run(t, root, "git", "config", "user.email", "wb@example.test")
	run(t, root, "git", "config", "user.name", "WB Test")
	mustWriteFile(t, filepath.Join(root, "tracked.txt"), "tracked\n")
	run(t, root, "git", "add", "tracked.txt")
	run(t, root, "git", "commit", "-qm", "initial")
	mustWriteFile(t, filepath.Join(root, "untracked.txt"), "preserve untracked\n")
	planned, err := planUntracked(root, []string{"untracked.txt"})
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("clone root became unavailable after revalidation")
	opens := 0
	err = deleteExactUntrackedWithOpen(context.Background(), root, planned, func(path string) (int, error) {
		opens++
		if path != root {
			t.Fatalf("clone root=%q", path)
		}
		return -1, failure
	})
	if !errors.Is(err, failure) || opens != 1 || !strings.Contains(err.Error(), "open clone root for deletion") {
		t.Fatalf("error=%v opens=%d", err, opens)
	}
	for path, want := range map[string]string{"tracked.txt": "tracked\n", "untracked.txt": "preserve untracked\n"} {
		if got, err := os.ReadFile(filepath.Join(root, path)); err != nil || string(got) != want {
			t.Fatalf("%s changed: %q %v", path, got, err)
		}
	}
}
