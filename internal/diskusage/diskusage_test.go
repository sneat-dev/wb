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

func TestMeasureCountsRegularFilesOnce(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.bin"), 4096)
	writeFile(t, filepath.Join(root, "nested", "b.bin"), 8192)

	usage, err := Measure(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if usage.ApparentBytes != 4096+8192 {
		t.Fatalf("apparent = %d, want %d", usage.ApparentBytes, 4096+8192)
	}
	if usage.UnsharedBytes <= 0 {
		t.Fatalf("unshared = %d, want the blocks both files occupy", usage.UnsharedBytes)
	}
	if usage.Files != 2 {
		t.Fatalf("files = %d, want 2", usage.Files)
	}
}

func TestMeasureCountsAHardLinkedInodeOnlyOnce(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "store", "pkg.bin")
	writeFile(t, original, 16384)
	link := filepath.Join(root, "node_modules", "pkg.bin")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, link); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	usage, err := Measure(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if usage.ApparentBytes != 16384 {
		t.Fatalf("apparent = %d, want one copy of 16384", usage.ApparentBytes)
	}
	// Every link lives inside the measured tree, so removing the tree frees the
	// blocks: they are unshared with anything outside it.
	if usage.UnsharedBytes < 16384 {
		t.Fatalf("unshared = %d, want at least 16384 because every link is inside the tree", usage.UnsharedBytes)
	}
}

// This is the pnpm case the Feature is measured against: node_modules content
// is hard-linked into a store outside the worktree, so the apparent size
// promises a reclaim the deletion cannot deliver.
func TestMeasureExcludesBytesSharedWithAnInodeOutsideTheTree(t *testing.T) {
	parent := t.TempDir()
	store := filepath.Join(parent, "store")
	writeFile(t, filepath.Join(store, "pkg.bin"), 32768)
	tree := filepath.Join(parent, "worktree")
	if err := os.MkdirAll(filepath.Join(tree, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(store, "pkg.bin"), filepath.Join(tree, "node_modules", "pkg.bin")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	writeFile(t, filepath.Join(tree, "src", "main.go"), 1024)

	usage, err := Measure(context.Background(), tree)
	if err != nil {
		t.Fatal(err)
	}
	if usage.ApparentBytes != 32768+1024 {
		t.Fatalf("apparent = %d, want %d", usage.ApparentBytes, 32768+1024)
	}
	if usage.UnsharedBytes >= 32768 {
		t.Fatalf("unshared = %d must exclude the store-shared 32768 bytes", usage.UnsharedBytes)
	}
	if usage.SharedBytes != 32768 {
		t.Fatalf("shared = %d, want the hard-linked 32768 bytes", usage.SharedBytes)
	}
}

func TestMeasureIgnoresSymlinksAndDoesNotFollowThem(t *testing.T) {
	parent := t.TempDir()
	writeFile(t, filepath.Join(parent, "outside", "big.bin"), 65536)
	tree := filepath.Join(parent, "tree")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(parent, "outside"), filepath.Join(tree, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	writeFile(t, filepath.Join(tree, "small.bin"), 512)

	usage, err := Measure(context.Background(), tree)
	if err != nil {
		t.Fatal(err)
	}
	if usage.ApparentBytes != 512 {
		t.Fatalf("apparent = %d, want only the 512-byte regular file", usage.ApparentBytes)
	}
}

func TestMeasureReportsZeroForAMissingPath(t *testing.T) {
	usage, err := Measure(context.Background(), filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("a path that is not there is an answer, not a failure: %v", err)
	}
	if usage.ApparentBytes != 0 || usage.UnsharedBytes != 0 || usage.Files != 0 {
		t.Fatalf("usage = %#v, want zero", usage)
	}
}

func TestMeasureStopsWhenTheContextIsCancelled(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 64; i++ {
		writeFile(t, filepath.Join(root, "d", string(rune('a'+i%26)), "f.bin"), 128)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Measure(ctx, root); err == nil {
		t.Fatal("a cancelled measurement must report the cancellation")
	}
}

func TestAddSumsTwoMeasurements(t *testing.T) {
	total := Usage{ApparentBytes: 10, UnsharedBytes: 4, SharedBytes: 6, Files: 1}
	total = total.Add(Usage{ApparentBytes: 5, UnsharedBytes: 5, Files: 2})
	if total != (Usage{ApparentBytes: 15, UnsharedBytes: 9, SharedBytes: 6, Files: 3}) {
		t.Fatalf("total = %#v", total)
	}
}

func TestHumanBytesRendersBothFigures(t *testing.T) {
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
