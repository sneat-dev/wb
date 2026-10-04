package main

import (
	"testing"
)

//nolint:paralleltest // actual launcher-refusal fixture sets process-wide PATH.
func TestCwCovBrowserTargetAndCommand(t *testing.T) {
	// With no launcher on PATH the failure is reported, never swallowed.
	t.Setenv("PATH", t.TempDir())
	if err := openBrowser(t.TempDir()); err == nil {
		t.Error("openBrowser succeeded with no browser launcher on PATH")
	}
}
