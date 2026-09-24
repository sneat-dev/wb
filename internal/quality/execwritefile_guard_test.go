package quality

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestNoDirectExecWriteFileOutsideTestenv is the CI guard for task-21/#739:
// no _test.go file outside internal/execfile may write an executable file
// with os.WriteFile directly, because that leaves a writable file
// descriptor open at the final path for a window a concurrent fork
// elsewhere in the process can inherit before its own exec, racing
// "text file busy" (golang/go#22315). testenv.WriteExecutableFile (backed
// by execfile.WriteExecutableFile) closes that window and must be used
// instead.
func TestNoDirectExecWriteFileOutsideTestenv(t *testing.T) {
	t.Parallel()
	root, err := ParallelGuardModuleRoot()
	if err != nil {
		t.Fatalf("ParallelGuardModuleRoot: %v", err)
	}
	violations, err := ScanExecWriteFileCallSites(root)
	if err != nil {
		t.Fatalf("ScanExecWriteFileCallSites: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("os.WriteFile with an executable mode outside internal/execfile "+
			"(use testenv.WriteExecutableFile instead; golang/go#22315, task-21, #739):\n%s",
			strings.Join(violations, "\n"))
	}
}

// TestScanExecWriteFileCallSitesFindsALiteralExecutableModeWrite pins the
// scanner's own positive case against a fixture file, independent of
// whatever this repository currently contains.
func TestScanExecWriteFileCallSitesFindsALiteralExecutableModeWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeQualityFile(t, filepath.Join(dir, "go.mod"), "module example.test/execwritefile\n\ngo 1.26\n")
	writeQualityFile(t, filepath.Join(dir, "fixture_test.go"), `package fixture

import (
	"os"
	"testing"
)

func TestWritesAFakeExecutable(t *testing.T) {
	_ = os.WriteFile("script", []byte("#!/bin/sh\n"), 0o755)
}
`)
	violations, err := ScanExecWriteFileCallSites(dir)
	if err != nil {
		t.Fatalf("ScanExecWriteFileCallSites: %v", err)
	}
	if len(violations) != 1 || !strings.Contains(violations[0], "0o755") {
		t.Fatalf("violations = %#v, want exactly one entry naming the 0o755 literal", violations)
	}
}

// TestScanExecWriteFileCallSitesIgnoresNonExecutableModes proves the
// scanner does not flag ordinary data-file writes (0o600/0o644), a dynamic
// mode expression it cannot classify, or a WriteFile call in a package that
// merely happens to be named os in a different import.
func TestScanExecWriteFileCallSitesIgnoresNonExecutableModes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeQualityFile(t, filepath.Join(dir, "go.mod"), "module example.test/execwritefile\n\ngo 1.26\n")
	writeQualityFile(t, filepath.Join(dir, "fixture_test.go"), `package fixture

import (
	"os"
	"testing"
)

func TestWritesOrdinaryDataFiles(t *testing.T) {
	_ = os.WriteFile("data.json", []byte("{}"), 0o600)
	_ = os.WriteFile("wide.txt", []byte("x"), 0o644)
	mode := os.FileMode(0o755)
	_ = os.WriteFile("dynamic", []byte("x"), mode)
}
`)
	violations, err := ScanExecWriteFileCallSites(dir)
	if err != nil {
		t.Fatalf("ScanExecWriteFileCallSites: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %#v, want none", violations)
	}
}

// TestScanExecWriteFileCallSitesSkipsExecfileItself proves the guard's own
// exclusion works: a fake fixture rooted at internal/execfile is not
// scanned, even though it contains an executable-mode os.WriteFile.
func TestScanExecWriteFileCallSitesSkipsExecfileItself(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeQualityFile(t, filepath.Join(dir, "go.mod"), "module example.test/execwritefile\n\ngo 1.26\n")
	writeQualityFile(t, filepath.Join(dir, "internal", "execfile", "fixture_test.go"), `package execfile

import (
	"os"
	"testing"
)

func TestWritesAFakeExecutable(t *testing.T) {
	_ = os.WriteFile("script", []byte("#!/bin/sh\n"), 0o755)
}
`)
	violations, err := ScanExecWriteFileCallSites(dir)
	if err != nil {
		t.Fatalf("ScanExecWriteFileCallSites: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %#v, want none (internal/execfile is excluded)", violations)
	}
}

// TestScanExecWriteFileCallSitesReportsUnparseableFiles pins the walk's own
// error path: a _test.go file that is not valid Go surfaces a parse error
// rather than being silently skipped.
func TestScanExecWriteFileCallSitesReportsUnparseableFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeQualityFile(t, filepath.Join(dir, "go.mod"), "module example.test/execwritefile\n\ngo 1.26\n")
	writeQualityFile(t, filepath.Join(dir, "broken_test.go"), "not valid go\n")

	if _, err := ScanExecWriteFileCallSites(dir); err == nil {
		t.Fatal("ScanExecWriteFileCallSites returned no error for an unparseable _test.go file")
	}
}

// TestScanExecWriteFileCallSitesReportsMissingRoot pins the walk failure
// for a root that does not exist.
func TestScanExecWriteFileCallSitesReportsMissingRoot(t *testing.T) {
	t.Parallel()
	if _, err := ScanExecWriteFileCallSites(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("ScanExecWriteFileCallSites returned no error for a missing root")
	}
}
