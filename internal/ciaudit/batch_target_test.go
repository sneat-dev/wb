package ciaudit

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestAuditReportsMergesAndSortsTargetFindings verifies the batch service
// merges target findings directly, with a fake comparator, so the
// CompareAgainstTarget call site and its finding-merge/sort logic
// are exercised without git or a real target branch. The fake returns two
// findings deliberately out of Code order, so a correct sort -- not merely
// a correct append -- is required to pass.
func TestAuditReportsMergesAndSortsTargetFindings(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	calls := 0
	fake := func(root, target string) ([]Finding, error) {
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
		return []Finding{
			{Code: "unit-tier-pending-total-rose", Message: "zzz", File: "z.go"},
			{Code: "go-coverage-threshold", Message: "aaa", File: "a.go"},
		}, nil
	}

	reports, err := testAuditReports([]string{dir}, "main", fake)
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
// error stops the batch audit rather than being swallowed.
func TestAuditReportsPropagatesComparatorError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	wantErr := errors.New("boom: could not fetch target")
	fake := func(root, target string) ([]Finding, error) {
		return nil, wantErr
	}

	reports, err := testAuditReports([]string{dir}, "main", fake)
	if !errors.Is(err, wantErr) {
		t.Fatalf("auditReports err = %v, want %v", err, wantErr)
	}
	if reports != nil {
		t.Fatalf("reports = %+v, want nil on error", reports)
	}
}

// TestAuditBatchPropagatesMissingPath verifies that the real filesystem audit
// surfaces fs.ErrNotExist through the batch operation. CLI exit classification
// is tested independently with a fake operation in cmdci.
func TestAuditBatchPropagatesMissingPath(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "does-not-exist")

	_, err := AuditBatch(BatchOptions{Path: missing})
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
	fake := func(root, target string) ([]Finding, error) {
		called = true
		return nil, nil
	}

	reports, err := testAuditReports([]string{dir}, "", fake)
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

func testAuditReports(paths []string, target string, compare func(string, string) ([]Finding, error)) ([]Report, error) {
	deps := defaultBatchDependencies()
	deps.compare = compare
	return auditReportsWithDeps(paths, target, deps)
}

// TestAuditReportsSortsTargetFindingsAndReports verifies the batch service's two
// sort.Slice comparators: the per-report findings tie-break on matching code
// (falling back to File) and the outer reports-by-path ordering.
func TestAuditReportsSortsTargetFindingsAndReports(t *testing.T) {
	t.Parallel()
	first, second := t.TempDir(), t.TempDir()
	// second/first: intentionally out of alphabetical Path order so the
	// outer sort.Slice comparator (reports[i].Path < reports[j].Path) has
	// something to swap.
	compare := func(root, target string) ([]Finding, error) {
		return []Finding{
			{Code: "z-code", Message: "z finding", File: "z.yml"},
			// Same code as above with a lexically earlier file: exercises the
			// tie-break branch (Findings[i].Code == Findings[j].Code).
			{Code: "z-code", Message: "z finding earlier file", File: "a.yml"},
		}, nil
	}
	reports, err := testAuditReports([]string{second, first}, "main", compare)
	if err != nil {
		t.Fatalf("testAuditReports() error = %v", err)
	}
	if len(reports) != 2 || reports[0].Path >= reports[1].Path {
		t.Fatalf("reports paths = [%q, %q]; want ascending order", reports[0].Path, reports[1].Path)
	}
	findings := reports[0].Findings
	if len(findings) != 2 || findings[0].File != "a.yml" || findings[1].File != "z.yml" {
		t.Fatalf("findings = %+v; want the same-code tie broken by File", findings)
	}
}
