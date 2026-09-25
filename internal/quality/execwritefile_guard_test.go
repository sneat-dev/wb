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
// scanner does not flag ordinary data-file writes (0o600/0o644), a mode
// expression it genuinely cannot trace to a literal (a struct field, or a
// literal too large for int64 so strconv.ParseInt itself fails), a
// WriteFile call whose receiver package is not literally os, or an
// unrelated three-argument call to a same-named function that never
// forwards into a raw write.
func TestScanExecWriteFileCallSitesIgnoresNonExecutableModes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeQualityFile(t, filepath.Join(dir, "go.mod"), "module example.test/execwritefile\n\ngo 1.26\n")
	writeQualityFile(t, filepath.Join(dir, "fixture_test.go"), `package fixture

import (
	"os"
	"testing"
)

type notOS struct{}

func (notOS) WriteFile(string, []byte, int) error { return nil }

func threeArgs(a, b, c int) int { return a + b + c }

type modeHolder struct{ mode os.FileMode }

func TestWritesOrdinaryDataFiles(t *testing.T) {
	_ = os.WriteFile("data.json", []byte("{}"), 0o600)
	_ = os.WriteFile("wide.txt", []byte("x"), 0o644)
	holder := modeHolder{mode: 0o755}
	_ = os.WriteFile("dynamic", []byte("x"), holder.mode)
	var other notOS
	_ = other.WriteFile("script", []byte("#!/bin/sh\n"), 0o755)
	_ = os.WriteFile("overflow", []byte("x"), 99999999999999999999999999)
	_ = threeArgs(1, 2, 3)
}
`)
	writeQualityFile(t, filepath.Join(dir, ".hidden", "fixture_test.go"), `package hidden

import (
	"os"
	"testing"
)

func TestWritesAFakeExecutableUnderAHiddenDir(t *testing.T) {
	_ = os.WriteFile("script", []byte("#!/bin/sh\n"), 0o755)
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

// TestScanExecWriteFileCallSitesFindsEveryWidenedShape pins the scanner's
// widened detections (task-21/#739 follow-up): a mode traced through a
// local variable, an os.FileMode(literal) conversion, a write followed by a
// same-path os.Chmod/os.Fchmod to an executable mode, a direct os.OpenFile
// with an executable literal, ioutil.WriteFile, and a helper function that
// forwards its own parameter straight into a raw write -- flagged at the
// call site that supplies the executable literal, not at the helper's own
// definition.
func TestScanExecWriteFileCallSitesFindsEveryWidenedShape(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeQualityFile(t, filepath.Join(dir, "go.mod"), "module example.test/execwritefile\n\ngo 1.26\n")
	writeQualityFile(t, filepath.Join(dir, "fixture_test.go"), `package fixture

import (
	"io/ioutil"
	"os"
	"testing"
)

func TestTracedLocalVariableMode(t *testing.T) {
	mode := 0o755
	_ = os.WriteFile("traced", []byte("#!/bin/sh\n"), os.FileMode(mode))
}

func TestFileModeConversionLiteral(t *testing.T) {
	_ = os.WriteFile("converted", []byte("#!/bin/sh\n"), os.FileMode(0o755))
}

func TestWriteThenChmodToExecutable(t *testing.T) {
	path := "chmodded"
	_ = os.WriteFile(path, []byte("#!/bin/sh\n"), 0o644)
	_ = os.Chmod(path, 0o755)
}

func TestWriteThenFchmodToExecutable(t *testing.T) {
	path := "fchmodded"
	file, _ := os.Open(path)
	_ = os.WriteFile(path, []byte("#!/bin/sh\n"), 0o644)
	_ = os.Fchmod(int(file.Fd()), 0o755)
}

func TestOpenFileWithExecutableLiteral(t *testing.T) {
	_, _ = os.OpenFile("opened", os.O_CREATE|os.O_WRONLY, 0o755)
}

func TestIoutilWriteFileWithExecutableLiteral(t *testing.T) {
	_ = ioutil.WriteFile("legacy", []byte("#!/bin/sh\n"), 0o755)
}

func hkFixtureWriteFile(t *testing.T, path string, content []byte, mode os.FileMode) {
	_ = os.WriteFile(path, content, mode)
}

func TestForwarderCallSiteWithExecutableLiteral(t *testing.T) {
	hkFixtureWriteFile(t, "forwarded", []byte("#!/bin/sh\n"), 0o755)
}
`)
	violations, err := ScanExecWriteFileCallSites(dir)
	if err != nil {
		t.Fatalf("ScanExecWriteFileCallSites: %v", err)
	}
	if len(violations) != 7 {
		t.Fatalf("violations = %#v, want exactly 7 (one per widened shape)", violations)
	}
}

// TestScanExecWriteFileCallSitesHandlesForwarderEdgeCases pins two edge
// cases in the forwarder-call-site pass, neither of which is a violation:
//
//   - A call site with fewer syntactic arguments than the forwarding
//     parameter's own index (go/parser does not type-check arity, so a
//     call passing a single multi-value-returning expression, or any other
//     arity mismatch, still parses) must not index out of call.Args.
//   - A function sharing a name with a real forwarder (the forwarders map
//     is keyed by function name only, not by package or file) but whose
//     own parameters are unnamed must still have its parameter list walked
//     without mismatching the other function's named forwarder key.
func TestScanExecWriteFileCallSitesHandlesForwarderEdgeCases(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeQualityFile(t, filepath.Join(dir, "go.mod"), "module example.test/execwritefile\n\ngo 1.26\n")
	writeQualityFile(t, filepath.Join(dir, "realforwarder", "fixture_test.go"), `package realforwarder

import (
	"os"
	"testing"
)

func helper(t *testing.T, path string, mode os.FileMode) {
	_ = os.WriteFile(path, nil, mode)
}

func TestArityMismatchCallSiteIsNotIndexedOutOfRange(t *testing.T) {
	helper(t)
}
`)
	writeQualityFile(t, filepath.Join(dir, "unnamedparams", "fixture_test.go"), `package unnamedparams

import "testing"

func helper(int, string) {}

func TestCallToUnrelatedSameNamedHelper(t *testing.T) {
	helper(1, "x")
}
`)
	violations, err := ScanExecWriteFileCallSites(dir)
	if err != nil {
		t.Fatalf("ScanExecWriteFileCallSites: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %#v, want none (neither call site supplies a resolvable executable-literal mode)", violations)
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
