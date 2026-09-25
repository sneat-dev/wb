package diskusage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMeasureMissingRootReturnsNilError(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	usage, inodes, err := measure(context.Background(), missing)
	if err != nil {
		t.Fatalf("measure on missing root returned error: %v", err)
	}
	if usage.ApparentBytes != 0 || usage.Files != 0 || usage.SharedBytes != 0 || usage.UnsharedBytes != 0 {
		t.Fatalf("usage = %+v, want all zero", usage)
	}
	if inodes == nil || len(inodes) != 0 {
		t.Fatalf("inodes = %v, want an empty non-nil map", inodes)
	}
}
