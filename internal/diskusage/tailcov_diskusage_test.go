package diskusage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tailCovWrite creates a file of the given size under root, creating parents.
func tailCovWrite(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestTailCovMeasureSkipsAnUnreadableDirectory pins the documented policy that
// a fleet sweep must not fail because one worktree directory cannot be listed:
// the walk skips it and still measures everything else.
func TestTailCovMeasureSkipsAnUnreadableDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tailCovWrite(t, filepath.Join(root, "readable", "kept.bin"), 2048)
	blocked := filepath.Join(root, "blocked")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	tailCovWrite(t, filepath.Join(blocked, "hidden.bin"), 4096)
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })

	usage, err := Measure(context.Background(), root)
	if err != nil {
		t.Fatalf("an unreadable subdirectory must not fail the measurement: %v", err)
	}
	if usage.ApparentBytes != 2048 {
		t.Fatalf("apparent = %d, want only the readable 2048 bytes", usage.ApparentBytes)
	}
}

// TestTailCovMeasureReportsAWalkErrorThatIsNotAMissingPath separates "already
// removed" from "not a directory": only the former is an answer of zero, and a
// path whose parent is a regular file must surface as an error.
func TestTailCovMeasureReportsAWalkErrorThatIsNotAMissingPath(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	usage, err := Measure(context.Background(), filepath.Join(file, "child"))
	if err == nil {
		t.Fatalf("Measure through a regular file = %#v, want an error", usage)
	}
	if !strings.Contains(err.Error(), "measure ") {
		t.Fatalf("Measure error = %v, want it to name the measured root", err)
	}
	if usage != (Usage{}) {
		t.Fatalf("usage = %#v alongside an error, want the zero value", usage)
	}
}

// TestTailCovWalkMeasurePropagatesAMeasurementFailure makes the accounting unit
// fail the same way a direct Measure does, and proves the walk keeps its
// previously recorded trees intact rather than dropping them.
func TestTailCovWalkMeasurePropagatesAMeasurementFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tailCovWrite(t, filepath.Join(root, "kept.bin"), 1024)

	walk := NewWalk()
	if _, err := walk.Measure(context.Background(), root); err != nil {
		t.Fatal(err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	usage, err := walk.Measure(cancelled, root)
	if err == nil {
		t.Fatalf("Measure under a cancelled context = %#v, want the cancellation error", usage)
	}
	if usage != (Usage{}) {
		t.Fatalf("usage = %#v alongside an error, want the zero value", usage)
	}
	if total := walk.Total(); total.ApparentBytes != 1024 {
		t.Fatalf("total after a failed measurement = %#v, want the earlier 1024 bytes still counted", total)
	}
}

// TestTailCovZeroValueWalkIsUsable covers the documented zero value: a caller
// that declares a Walk and measures into it must get a working accounting unit
// rather than a nil-map panic.
func TestTailCovZeroValueWalkIsUsable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tailCovWrite(t, filepath.Join(root, "a.bin"), 512)

	var walk Walk
	if _, err := walk.Measure(context.Background(), root); err != nil {
		t.Fatalf("zero-value Walk.Measure: %v", err)
	}
	total := walk.Total()
	if total.ApparentBytes != 512 || total.Files != 1 {
		t.Fatalf("zero-value Walk total = %#v, want the measured 512-byte file", total)
	}
}

// TestTailCovWalkTotalIsZeroForRootsItNeverMeasured pins the selected-subset
// accounting: asking what removing a tree that this walk never saw would
// reclaim must answer nothing, not everything.
func TestTailCovWalkTotalIsZeroForRootsItNeverMeasured(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	tailCovWrite(t, filepath.Join(root, "pkgs", "pkg.bin"), 4096)

	walk := NewWalk()
	measured, err := walk.Measure(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if measured.ApparentBytes != 4096 {
		t.Fatalf("measured = %#v, want the fixture to hold 4096 apparent bytes", measured)
	}

	if unknown := walk.Total(filepath.Join(filepath.Dir(root), "never-measured")); unknown != (Usage{}) {
		t.Fatalf("Total(unmeasured root) = %#v, want the zero usage", unknown)
	}
	if all := walk.Total(); all.ApparentBytes != 4096 {
		t.Fatalf("Total() with no roots = %#v, want every measured tree", all)
	}
}

// TestTailCovHumanRendersPetabyteScale covers the last unit in the ladder: a
// figure above the TB suffix must not fall off the end of the loop and print
// the wrong scale.
func TestTailCovHumanRendersPetabyteScale(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		bytes int64
		want  string
	}{
		{1 << 40, "1.0 TB"},
		{1 << 50, "1.0 PB"},
		{3 << 50, "3.0 PB"},
	} {
		if got := Human(testCase.bytes); got != testCase.want {
			t.Errorf("Human(%d) = %q, want %q", testCase.bytes, got, testCase.want)
		}
	}
}

// TestTailCovMeasureSkipsAnEntryItCannotStat is the concurrency case a fleet
// sweep hits when a worktree is deleted or replaced underneath it: an entry the
// directory listing showed but Lstat can no longer resolve is skipped instead
// of aborting the whole measurement.
func TestTailCovMeasureSkipsAnEntryItCannotStat(t *testing.T) {
	t.Parallel()
	// The platform caps a path argument at PATH_MAX (1024 bytes on Darwin, 4096
	// on Linux). A directory whose own path stays under that limit can be
	// listed, while a 255-byte entry inside it has a full path that is over the
	// limit and can no longer be Lstat'ed. Renaming a directory that already
	// holds such an entry reaches exactly that state — the same Lstat failure a
	// concurrently deleted file produces.
	parent := t.TempDir()
	short := filepath.Join(parent, "d")
	if err := os.Mkdir(short, 0o700); err != nil {
		t.Fatal(err)
	}
	name := strings.Repeat("n", 255)
	tailCovWrite(t, filepath.Join(short, name), 128)

	// Grow the nesting one 200-byte component at a time until the platform
	// refuses the next component, leaving deep just under the path limit.
	deep := parent
	for {
		next := filepath.Join(deep, strings.Repeat("c", 200))
		if err := os.Mkdir(next, 0o700); err != nil {
			break
		}
		deep = next
	}
	long := filepath.Join(deep, "renamed")
	if err := os.Rename(short, long); err != nil {
		t.Fatalf("rename to the deepest fixture directory: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(long, name)); err == nil {
		t.Skipf("this platform still resolves a %d-byte path; the Lstat failure cannot be produced hermetically here",
			len(long)+1+len(name))
	}

	usage, err := Measure(context.Background(), long)
	if err != nil {
		t.Fatalf("an entry that cannot be stat'ed must be skipped, not fatal: %v", err)
	}
	if usage.Files != 0 || usage.ApparentBytes != 0 {
		t.Fatalf("usage = %#v, want the unreachable entry excluded", usage)
	}
}
