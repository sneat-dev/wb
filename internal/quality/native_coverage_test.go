package quality

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

//nolint:paralleltest // TMPDIR is process-wide and must be restored after scratch creation fails.
func TestCombinedCoverageRejectsInvalidScopeAndUnavailableScratchRoot(t *testing.T) {
	if _, _, err := runCoverageWithOptions(context.Background(), RunOptions{IncludeE2E: true, GoTestPackages: []string{"-run=bad"}}, t.TempDir(), "unused"); err == nil {
		t.Fatal("accepted flag as package")
	}
	root := t.TempDir()
	t.Setenv("TMPDIR", filepath.Join(root, "missing"))
	if _, _, err := runCoverageWithOptions(context.Background(), RunOptions{IncludeE2E: true, CheckTimeout: time.Second}, root, "unused"); err == nil {
		t.Fatal("accepted unavailable scratch root")
	}
}
