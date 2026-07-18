package specscore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseViolations(t *testing.T) {
	cases := map[string]int{
		"0 violations found":                     0,
		"12 violation(s) found.":                 12,
		"Fixed 3 file(s):\n\n1 violations found": 1,
		"no summary here":                        0,
	}
	for in, want := range cases {
		if got := parseViolations(in); got != want {
			t.Errorf("parseViolations(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestIsManaged(t *testing.T) {
	dir := t.TempDir()
	if IsManaged(dir) {
		t.Fatal("empty dir must not be SpecScore-managed")
	}
	if err := os.WriteFile(filepath.Join(dir, "specscore.yaml"), []byte("project:\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !IsManaged(dir) {
		t.Fatal("dir with specscore.yaml must be SpecScore-managed")
	}
}
