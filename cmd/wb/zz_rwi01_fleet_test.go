package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestRwi01FleetLocalScanErrorIsSurfaced drives fleet's localErr != nil
// branch: a missing projects root makes discover.ScanLocal fail, and that
// failure must short-circuit fleet before any remote lookup, returning a
// nil repo slice and the underlying error.
func TestRwi01FleetLocalScanErrorIsSurfaced(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	resolveOwnersCalled := false
	repos, err := fleet(missing, "", func() []string {
		resolveOwnersCalled = true
		return nil
	})
	if err == nil {
		t.Fatal("fleet with a missing projects root: want an error, got nil")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fleet with a missing projects root: err = %v, want os.ErrNotExist", err)
	}
	if repos != nil {
		t.Fatalf("repos = %v, want nil on a local scan error", repos)
	}
	// resolveOwners still runs (it races the local scan goroutine); this
	// isn't the block under test, just confirming the fixture wired the
	// callback wb actually calls.
	if !resolveOwnersCalled {
		t.Fatal("resolveOwners callback was never invoked")
	}
}
