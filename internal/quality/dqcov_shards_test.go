package quality

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDqCovMergeCoverageProfilesRejectsEmptyAndUnreadableInputs pins the two
// preconditions that must fail before a merged profile can be published.
func TestDqCovMergeCoverageProfilesRejectsEmptyAndUnreadableInputs(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := mergeCoverageProfiles(nil, filepath.Join(directory, "out.cov")); err == nil || !strings.Contains(err.Error(), "at least one coverage profile") {
		t.Fatalf("empty input error = %v, want the profile requirement", err)
	}
	err := mergeCoverageProfiles([]string{filepath.Join(directory, "absent.cov")}, filepath.Join(directory, "out.cov"))
	if err == nil || !strings.Contains(err.Error(), "open coverage profile") {
		t.Fatalf("unreadable input error = %v, want the open failure", err)
	}
}

// TestDqCovReadCoverageProfileRejectsEveryMalformedShape asserts the profile
// reader fails closed for each malformed shape instead of reporting a partial
// union.
func TestDqCovReadCoverageProfileRejectsEveryMalformedShape(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	long := strings.Repeat("x", 70000)
	for _, tc := range []struct {
		name     string
		contents string
		want     string
	}{
		{name: "empty", contents: "", want: "is empty"},
		{name: "invalid mode header", contents: "set\nexample/a.go:1.1,2.2 2 1\n", want: "invalid mode header"},
		{name: "unsupported mode", contents: "mode: weird\nexample/a.go:1.1,2.2 2 1\n", want: "unsupported coverage mode"},
		{name: "wrong field count", contents: "mode: set\nexample/a.go:1.1,2.2 2\n", want: "invalid coverage profile"},
		{name: "non-numeric counts", contents: "mode: set\nexample/a.go:1.1,2.2 two three\n", want: "invalid coverage profile"},
		{name: "negative count", contents: "mode: set\nexample/a.go:1.1,2.2 2 -1\n", want: "invalid coverage profile"},
		{name: "zero statements", contents: "mode: set\nexample/a.go:1.1,2.2 0 1\n", want: ""},
		{name: "duplicate block", contents: "mode: set\nexample/a.go:1.1,2.2 2 1\nexample/a.go:1.1,2.2 2 1\n", want: "duplicate coverage block"},
		{name: "oversized header line", contents: long, want: "bufio.Scanner: token too long"},
		{name: "oversized block line", contents: "mode: set\n" + long + "\n", want: "bufio.Scanner: token too long"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(directory, strings.ReplaceAll(tc.name, " ", "-")+".cov")
			writeQualityFile(t, path, tc.contents)
			mode, blocks, err := readCoverageProfile(path)
			if tc.want == "" {
				if err != nil || mode != "set" || len(blocks) != 1 || blocks[0].statements != 0 {
					t.Fatalf("read = mode %q blocks %+v err %v, want a zero-statement block", mode, blocks, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want containing %q", err, tc.want)
			}
		})
	}

	if _, _, err := readCoverageProfile(filepath.Join(directory, "missing.cov")); err == nil || !strings.Contains(err.Error(), "open coverage profile") {
		t.Fatalf("missing profile error = %v, want the open failure", err)
	}
}

// TestDqCovWriteCoverageProfileAtomicallyFailsClosed covers the two publication
// failures a caller can observe: an unusable destination directory and a
// destination occupied by a directory.
func TestDqCovWriteCoverageProfileAtomicallyFailsClosed(t *testing.T) {
	t.Parallel()
	blocks := map[string]coverageBlock{"example/a.go:1.1,2.2": {location: "example/a.go:1.1,2.2", statements: 2, count: 1}}

	t.Run("unusable destination directory", func(t *testing.T) {
		t.Parallel()
		output := filepath.Join(t.TempDir(), "missing", "merged.cov")
		err := writeCoverageProfileAtomically(output, "set", blocks)
		if err == nil || !strings.Contains(err.Error(), "create merged coverage profile beside") {
			t.Fatalf("error = %v, want a scratch-file failure", err)
		}
		if _, statErr := os.Stat(output); !os.IsNotExist(statErr) {
			t.Fatalf("merged profile appeared despite the failure: %v", statErr)
		}
	})

	t.Run("destination is a directory", func(t *testing.T) {
		t.Parallel()
		directory := t.TempDir()
		output := filepath.Join(directory, "occupied.cov")
		if err := os.Mkdir(output, 0o755); err != nil {
			t.Fatal(err)
		}
		err := writeCoverageProfileAtomically(output, "set", blocks)
		if err == nil || !strings.Contains(err.Error(), "publish merged coverage profile") {
			t.Fatalf("error = %v, want a publish failure", err)
		}
		entries, readErr := os.ReadDir(directory)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".wb-coverage-merge-") {
				t.Fatalf("scratch file %q survived a failed publication", entry.Name())
			}
		}
	})
}
