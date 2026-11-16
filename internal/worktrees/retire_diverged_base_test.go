package worktrees

import (
	"path/filepath"
	"testing"
)

func TestPreservationDiagnosticsKeepsUnrelatedFailures(t *testing.T) {
	t.Parallel()
	original := ListDiagnostic{DiscoveryScope: "selected-proof", Path: filepath.Join(t.TempDir(), "missing"), Message: "unreadable claim"}
	kept := preservationDiagnostics(t.TempDir(), []ListDiagnostic{original})
	if len(kept) != 1 || kept[0] != original {
		t.Fatalf("unrelated failure hidden: %+v", kept)
	}
}
