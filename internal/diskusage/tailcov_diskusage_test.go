package diskusage

import (
	"context"
	"os"
	"path/filepath"
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
