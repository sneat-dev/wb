package diskusage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAddSumsTwoMeasurements(t *testing.T) {
	t.Parallel()
	total := Usage{ApparentBytes: 10, UnsharedBytes: 4, SharedBytes: 6, Files: 1}
	total = total.Add(Usage{ApparentBytes: 5, UnsharedBytes: 5, Files: 2})
	if total != (Usage{ApparentBytes: 15, UnsharedBytes: 9, SharedBytes: 6, Files: 3}) {
		t.Fatalf("total = %#v", total)
	}
}

func TestHumanBytesRendersBothFigures(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{2048, "2.0 KB"},
		{5 * 1024 * 1024, "5.0 MB"},
		{3 * 1024 * 1024 * 1024, "3.0 GB"},
	} {
		if got := Human(testCase.bytes); got != testCase.want {
			t.Errorf("Human(%d) = %q, want %q", testCase.bytes, got, testCase.want)
		}
	}
}

// The fleet case the reclaim footer gets wrong when it sums per-tree figures:
// two worktrees hard-linking the same store file. Summed, its apparent size is
// counted twice; and because neither tree owns every link, neither counts it as
// unshared even though removing both would return the blocks.
func TestWalkCountsAnInodeSharedBetweenTwoTreesExactlyOnce(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	first := filepath.Join(parent, "worktree-a")
	second := filepath.Join(parent, "worktree-b")
	writeFile(t, filepath.Join(first, "node_modules", "pkg.bin"), 20480)
	if err := os.MkdirAll(filepath.Join(second, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(first, "node_modules", "pkg.bin"), filepath.Join(second, "node_modules", "pkg.bin")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	writeFile(t, filepath.Join(first, "own.bin"), 1024)
	writeFile(t, filepath.Join(second, "own.bin"), 2048)

	walk := NewWalk()
	firstUsage, err := walk.Measure(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	secondUsage, err := walk.Measure(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	summed := firstUsage.Add(secondUsage)
	if summed.ApparentBytes != 20480*2+1024+2048 {
		t.Fatalf("per-tree sum = %#v; the fixture must be one that a naive sum double-counts", summed)
	}

	total := walk.Total()
	if total.ApparentBytes != 20480+1024+2048 {
		t.Fatalf("walk apparent = %d, want the shared inode counted once", total.ApparentBytes)
	}
	if total.UnsharedBytes <= summed.UnsharedBytes {
		t.Fatalf("walk unshared = %d, want more than the per-tree sum %d: removing both trees frees the shared blocks",
			total.UnsharedBytes, summed.UnsharedBytes)
	}
	if total.SharedBytes != 0 {
		t.Fatalf("walk shared = %d, want zero: every link is inside the measured set", total.SharedBytes)
	}
}

func TestWalkTotalForASubsetKeepsTheSharedInodeShared(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	first := filepath.Join(parent, "worktree-a")
	second := filepath.Join(parent, "worktree-b")
	writeFile(t, filepath.Join(first, "pkg.bin"), 8192)
	if err := os.MkdirAll(second, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(first, "pkg.bin"), filepath.Join(second, "pkg.bin")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	walk := NewWalk()
	if _, err := walk.Measure(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if _, err := walk.Measure(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	// Only one of the two was actually retired, so the other link survives and
	// the blocks do not come back.
	subset := walk.Total(first)
	if subset.UnsharedBytes != 0 || subset.SharedBytes != 8192 {
		t.Fatalf("subset total = %#v, want the inode reported as still shared", subset)
	}
	if both := walk.Total(first, second); both.UnsharedBytes < 8192 {
		t.Fatalf("both-trees total = %#v, want the blocks reclaimed", both)
	}
}
