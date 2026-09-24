package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
)

// realModuleRoot resolves this repository's own module root, exactly the
// way ParallelGuardModuleRoot does for the real CLI, so tests can pass
// runAgainstBaseline the real (read-only) scan target while keeping every
// write or read of a baseline FILE pointed at a disposable temp copy.
func realModuleRoot(t *testing.T) string {
	t.Helper()
	root, err := quality.ParallelGuardModuleRoot()
	if err != nil {
		t.Fatalf("ParallelGuardModuleRoot: %v", err)
	}
	return root
}

// currentRendered computes exactly what runAgainstBaseline would compute
// for -- write and -check -- against today's real tree, without touching
// any file on disk.
func currentRendered(t *testing.T, root string) string {
	t.Helper()
	serial, bareNolint, err := quality.ScanSerialTests(root)
	if err != nil {
		t.Fatalf("ScanSerialTests: %v", err)
	}
	if len(bareNolint) != 0 {
		t.Fatalf("real tree has %d bare //nolint:paralleltest director(y/ies); fix before trusting this fixture: %v", len(bareNolint), bareNolint)
	}
	return quality.FormatParallelBaseline(serial)
}

func TestRunAgainstBaselineWritesTheRenderedBaselineByDefault(t *testing.T) {
	t.Parallel()
	root := realModuleRoot(t)
	want := currentRendered(t, root)
	target := filepath.Join(t.TempDir(), "baseline.txt")

	var stdout, stderr bytes.Buffer
	code := runAgainstBaseline(root, target, false, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runAgainstBaseline(check=false) = %d, want 0; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "wrote ") {
		t.Fatalf("stdout = %q, want a wrote-N-entries confirmation", stdout.String())
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read written baseline: %v", err)
	}
	if string(got) != want {
		t.Fatal("runAgainstBaseline wrote content that does not match FormatParallelBaseline's own output")
	}
}

func TestRunAgainstBaselineWriteReportsAnUnwritablePath(t *testing.T) {
	t.Parallel()
	root := realModuleRoot(t)
	// A path under a file (not a directory) can never be created.
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(blocker, "baseline.txt")

	var stdout, stderr bytes.Buffer
	code := runAgainstBaseline(root, target, false, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("runAgainstBaseline(unwritable path) = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "parallelbaseline:") {
		t.Fatalf("stderr = %q, want the write error reported", stderr.String())
	}
}

func TestRunAgainstBaselineCheckReportsUpToDate(t *testing.T) {
	t.Parallel()
	root := realModuleRoot(t)
	rendered := currentRendered(t, root)
	target := filepath.Join(t.TempDir(), "baseline.txt")
	if err := os.WriteFile(target, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runAgainstBaseline(root, target, true, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runAgainstBaseline(check, up to date) = %d, want 0; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "up to date") {
		t.Fatalf("stdout = %q, want an up-to-date confirmation", stdout.String())
	}
}

func TestRunAgainstBaselineCheckReportsAStaleFile(t *testing.T) {
	t.Parallel()
	root := realModuleRoot(t)
	target := filepath.Join(t.TempDir(), "baseline.txt")
	if err := os.WriteFile(target, []byte("pkg::TestStale\tstale reason\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runAgainstBaseline(root, target, true, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("runAgainstBaseline(check, stale) = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "is stale") {
		t.Fatalf("stderr = %q, want a stale-baseline message", stderr.String())
	}
}

func TestRunAgainstBaselineCheckReportsAMissingFile(t *testing.T) {
	t.Parallel()
	root := realModuleRoot(t)
	target := filepath.Join(t.TempDir(), "does-not-exist.txt")

	var stdout, stderr bytes.Buffer
	code := runAgainstBaseline(root, target, true, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("runAgainstBaseline(check, missing file) = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "parallelbaseline:") {
		t.Fatalf("stderr = %q, want the read error reported", stderr.String())
	}
}

func TestRunAgainstBaselineCheckRejectsAnEntryMissingAReason(t *testing.T) {
	t.Parallel()
	root := realModuleRoot(t)
	target := filepath.Join(t.TempDir(), "baseline.txt")
	if err := os.WriteFile(target, []byte("pkg::TestNoReason\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := runAgainstBaseline(root, target, true, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("runAgainstBaseline(check, missing reason) = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "reason") {
		t.Fatalf("stderr = %q, want the missing-reason error reported", stderr.String())
	}
}

func TestRunAgainstBaselineScanErrorIsReported(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "broken_test.go"), []byte("package p\n\nfunc TestBroken(t *testing.T {\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "baseline.txt")

	var stdout, stderr bytes.Buffer
	code := runAgainstBaseline(root, target, true, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("runAgainstBaseline(scan error) = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "parallelbaseline:") {
		t.Fatalf("stderr = %q, want the scan error reported", stderr.String())
	}
}

func TestRunAgainstBaselineReportsEveryBareNolintDirective(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	fixture := `package p

import "testing"

//nolint:paralleltest
func TestBareDirective(t *testing.T) {
	t.Setenv("X", "1")
}
`
	if err := os.WriteFile(filepath.Join(root, "bare_test.go"), []byte(fixture), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "baseline.txt")

	var stdout, stderr bytes.Buffer
	code := runAgainstBaseline(root, target, true, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("runAgainstBaseline(bare nolint) = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "TestBareDirective") {
		t.Fatalf("stderr = %q, want the offending test named", stderr.String())
	}
}

func TestRunParsesTheCheckFlagFromArgv(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	// run resolves the real repository root and its real committed
	// baseline; -check against the checked-in file must already be up to
	// date (every earlier test in this package keeps it that way).
	code := run([]string{"-check"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run([-check]) = %d, want 0; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "up to date") {
		t.Fatalf("stdout = %q, want an up-to-date confirmation", stdout.String())
	}
}

func TestRunReportsAnUnknownFlag(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := run([]string{"-not-a-real-flag"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("run([-not-a-real-flag]) = %d, want 2", code)
	}
}

// TestRunReportsAModuleRootResolutionFailure exercises run's own error
// branch around moduleRootResolver, which the real ParallelGuardModuleRoot
// only takes when go.mod cannot be found walking up from the caller -- not
// reproducible from inside this module's own test binary. The seam lets the
// failure be forced directly instead.
//
// concurrent test resolving the real root during this window would race it.
//
//nolint:paralleltest // mutates the package-level moduleRootResolver seam; a
func TestRunReportsAModuleRootResolutionFailure(t *testing.T) {
	original := moduleRootResolver
	defer func() { moduleRootResolver = original }()
	moduleRootResolver = func() (string, error) {
		return "", errors.New("no go.mod found")
	}

	var stdout, stderr bytes.Buffer
	code := run(nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("run(nil) = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "no go.mod found") {
		t.Fatalf("stderr = %q, want the resolver error", stderr.String())
	}
}

// TestMainExitsWithRunsReturnCode exercises main's own statement, the one
// line this package's from-zero coverage baseline (task-3) does not
// grandfather away the way cmd/wb/main.go's main is: see the osExit seam's
// doc comment. It forces an argv that makes run return a known, non-zero
// code so the assertion is unambiguous.
//
//nolint:paralleltest // mutates os.Args and the package-level osExit seam.
func TestMainExitsWithRunsReturnCode(t *testing.T) {
	originalArgs := os.Args
	originalExit := osExit
	defer func() {
		os.Args = originalArgs
		osExit = originalExit
	}()

	os.Args = []string{"parallelbaseline", "-not-a-real-flag"}
	var gotCode int
	exited := false
	osExit = func(code int) {
		gotCode = code
		exited = true
	}

	main()

	if !exited {
		t.Fatal("main() never called osExit")
	}
	if gotCode != 2 {
		t.Fatalf("main() exit code = %d, want 2", gotCode)
	}
}
