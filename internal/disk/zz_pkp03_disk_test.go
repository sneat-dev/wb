package disk

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPkp03WorktreeRootsSkipsNonDirEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	// orgFile sits directly under the projects root: org.IsDir() is false.
	if err := os.WriteFile(filepath.Join(root, "orgFile"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write orgFile: %v", err)
	}

	orgDir := filepath.Join(root, "orgA")
	if err := os.MkdirAll(orgDir, 0o755); err != nil {
		t.Fatalf("mkdir orgA: %v", err)
	}
	// repoFile sits inside the org: repo.IsDir() is false.
	if err := os.WriteFile(filepath.Join(orgDir, "repoFile"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write repoFile: %v", err)
	}
	repoWorktrees := filepath.Join(orgDir, "repoX", ".worktrees")
	if err := os.MkdirAll(repoWorktrees, 0o755); err != nil {
		t.Fatalf("mkdir repoX/.worktrees: %v", err)
	}

	got := worktreeRoots(root)
	if len(got) != 1 || got[0] != repoWorktrees {
		t.Fatalf("worktreeRoots(%q) = %v, want [%s]", root, got, repoWorktrees)
	}
}

func TestPkp03RenderIncludesFindings(t *testing.T) {
	t.Parallel()
	report := Report{
		Filesystem:      Filesystem{Path: "/vol", TotalBytes: 1000, AvailableBytes: 250},
		Categories:      []Category{{Name: "cache", Kind: "cache", UnsharedBytes: 500, ApparentBytes: 600}},
		AttributedBytes: 500,
		Findings:        []string{"low disk space"},
		Skipped:         []string{"root: permission denied"},
	}
	out := Render(report)
	if !strings.Contains(out, "\nFindings:\n") || !strings.Contains(out, "  - low disk space\n") {
		t.Fatalf("Render output missing findings section:\n%s", out)
	}
}

// TestPkp03ScratchRootsSkipsNonDirWbEntries touches the real OS temp
// directory (the function under test has no injection seam), so it does not
// run in parallel and removes its marker file immediately after.
//
//nolint:paralleltest // writes a marker file into the shared os.TempDir(); must stay serial
func TestPkp03ScratchRootsSkipsNonDirWbEntries(t *testing.T) {
	f, err := os.CreateTemp(os.TempDir(), "wb-pkp03-*")
	if err != nil {
		t.Fatalf("create marker file: %v", err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		t.Fatalf("close marker file: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(name) })

	got := scratchRoots()
	for _, root := range got {
		if root == name {
			t.Fatalf("scratchRoots() included a non-directory wb- entry %q: %v", name, got)
		}
	}
}
