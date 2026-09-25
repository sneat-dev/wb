package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMatchRepositoryPathReportsBadGlobPatternFromRepoRoot covers
// matchRepositoryPath's filepath.Glob error branch: the detection pattern
// alone is a valid glob (filepath.Match already validated it), but the
// repository root it gets joined with contains an unterminated character
// class, which only becomes a bad pattern once combined. The function must
// report that error rather than silently treating it as "no match".
func TestMatchRepositoryPathReportsBadGlobPatternFromRepoRoot(t *testing.T) {
	t.Parallel()
	repoRoot := filepath.Join(t.TempDir(), "repo[unclosed")
	if err := os.MkdirAll(repoRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	matched, relative, err := matchRepositoryPath(repoRoot, "*.txt")
	if err == nil {
		t.Fatalf("matchRepositoryPath(bad glob repoRoot) = (%v, %q, nil), want a Glob error", matched, relative)
	}
	if matched || relative != "" {
		t.Fatalf("matchRepositoryPath(bad glob repoRoot) = (%v, %q, %v), want (false, \"\", err)", matched, relative, err)
	}
	if !strings.Contains(err.Error(), "syntax error") {
		t.Fatalf("matchRepositoryPath(bad glob repoRoot) error = %v, want filepath.ErrBadPattern's syntax error", err)
	}
}
