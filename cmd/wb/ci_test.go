package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/ciaudit"
)

// TestAuditReportsMergesAndSortsTargetFindings covers cmd/wb/ci.go's
// auditReports (the extracted body of runCIAudit's --target branch, review
// note #769 B1) directly, with a fake comparator, so the
// ciaudit.CompareAgainstTarget call site and its finding-merge/sort logic
// are exercised without git or a real target branch. The fake returns two
// findings deliberately out of Code order, so a correct sort -- not merely
// a correct append -- is required to pass.
func TestAuditReportsMergesAndSortsTargetFindings(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	calls := 0
	fake := func(root, target string) ([]ciaudit.Finding, error) {
		calls++
		if target != "main" {
			t.Fatalf("target = %q, want %q", target, "main")
		}
		want, err := filepath.Abs(dir)
		if err != nil {
			t.Fatal(err)
		}
		if root != want {
			t.Fatalf("root = %q, want %q", root, want)
		}
		return []ciaudit.Finding{
			{Code: "unit-tier-pending-total-rose", Message: "zzz", File: "z.go"},
			{Code: "go-coverage-threshold", Message: "aaa", File: "a.go"},
		}, nil
	}

	reports, err := auditReports([]string{dir}, "main", fake)
	if err != nil {
		t.Fatalf("auditReports: %v", err)
	}
	if calls != 1 {
		t.Fatalf("comparator called %d times, want 1", calls)
	}
	if len(reports) != 1 || len(reports[0].Findings) != 2 {
		t.Fatalf("reports = %+v, want 1 report with 2 findings", reports)
	}
	if got := reports[0].Findings[0].Code; got != "go-coverage-threshold" {
		t.Fatalf("Findings[0].Code = %q, want the lexicographically first code first (sorted, not appended)", got)
	}
	if got := reports[0].Findings[1].Code; got != "unit-tier-pending-total-rose" {
		t.Fatalf("Findings[1].Code = %q, want unit-tier-pending-total-rose", got)
	}
}

// TestAuditReportsPropagatesComparatorError proves a --target comparator
// error stops the audit rather than being swallowed (cmd/wb/ci.go:316-317).
func TestAuditReportsPropagatesComparatorError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	wantErr := errors.New("boom: could not fetch target")
	fake := func(root, target string) ([]ciaudit.Finding, error) {
		return nil, wantErr
	}

	reports, err := auditReports([]string{dir}, "main", fake)
	if !errors.Is(err, wantErr) {
		t.Fatalf("auditReports err = %v, want %v", err, wantErr)
	}
	if reports != nil {
		t.Fatalf("reports = %+v, want nil on error", reports)
	}
}

// TestRunCIAuditPropagatesAuditError covers cmd/wb/ci.go:305 and :343:
// runCIAudit's explicit (non-fleet) path is handed straight to auditReports,
// and when ciaudit.Audit cannot walk that path (here: it does not exist)
// runCIAudit must surface exit code 1 and the underlying error rather than
// swallow it. No --target is set, so ciaudit.CompareAgainstTarget is never
// reached and this stays free of git.
func TestRunCIAuditPropagatesAuditError(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "does-not-exist")

	code, err := runCIAudit(missing, "", "", "", false, false, false)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist", err)
	}
}

// TestAuditReportsSkipsComparatorWhenTargetEmpty proves the comparator is
// never invoked without --target, so ordinary `ci audit` runs with no
// target stay free of any target-branch dependency.
func TestAuditReportsSkipsComparatorWhenTargetEmpty(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	called := false
	fake := func(root, target string) ([]ciaudit.Finding, error) {
		called = true
		return nil, nil
	}

	reports, err := auditReports([]string{dir}, "", fake)
	if err != nil {
		t.Fatalf("auditReports: %v", err)
	}
	if called {
		t.Fatal("comparator was called with no --target")
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %+v, want 1", reports)
	}
}
