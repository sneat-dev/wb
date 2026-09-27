package quality

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSummaryFromProfile(t *testing.T) {
	t.Parallel()

	blocks := []CoverageBlock{
		{File: "example.com/app/pkg1/a.go", StartLine: 1, StartCol: 1, EndLine: 5, EndCol: 1, Statements: 10, Count: 2},
		{File: "example.com/app/pkg1/b.go", StartLine: 1, StartCol: 1, EndLine: 3, EndCol: 1, Statements: 5, Count: 0},
		{File: "example.com/app/pkg2/c.go", StartLine: 1, StartCol: 1, EndLine: 8, EndCol: 1, Statements: 20, Count: 5},
	}

	meta := CoverageSummaryMeta{
		Repository:     "example/app",
		SHA:            "abcdef123456",
		Ref:            "refs/heads/main",
		WorkflowRunID:  123456789,
		WorkflowRunURL: "https://github.com/example/app/actions/runs/123456789",
		ReportedAt:     time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC),
		Status:         StatusPassed,
	}

	summary := SummaryFromProfile(blocks, "example.com/app", meta)

	if summary.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", summary.SchemaVersion)
	}
	if summary.Repository != meta.Repository {
		t.Errorf("Repository = %q, want %q", summary.Repository, meta.Repository)
	}
	if summary.SHA != meta.SHA {
		t.Errorf("SHA = %q, want %q", summary.SHA, meta.SHA)
	}
	if summary.Ref != meta.Ref {
		t.Errorf("Ref = %q, want %q", summary.Ref, meta.Ref)
	}
	if summary.WorkflowRunID != meta.WorkflowRunID {
		t.Errorf("WorkflowRunID = %d, want %d", summary.WorkflowRunID, meta.WorkflowRunID)
	}
	if summary.WorkflowRunURL != meta.WorkflowRunURL {
		t.Errorf("WorkflowRunURL = %q, want %q", summary.WorkflowRunURL, meta.WorkflowRunURL)
	}
	if !summary.ReportedAt.Equal(meta.ReportedAt) {
		t.Errorf("ReportedAt = %v, want %v", summary.ReportedAt, meta.ReportedAt)
	}
	if summary.Status != StatusPassed {
		t.Errorf("Status = %q, want %q", summary.Status, StatusPassed)
	}

	// 10 + 5 + 20 = 35 statements total
	if summary.Statements != 35 {
		t.Errorf("Statements = %d, want 35", summary.Statements)
	}
	// 10 + 20 = 30 covered statements
	if summary.Covered != 30 {
		t.Errorf("Covered = %d, want 30", summary.Covered)
	}
	// 30 / 35 = 85.71428571428571%
	wantPct := percent(30, 35)
	if summary.Percentage != wantPct {
		t.Errorf("Percentage = %f, want %f", summary.Percentage, wantPct)
	}

	// Packages check
	pkg1, ok1 := summary.Packages["pkg1"]
	if !ok1 {
		t.Fatal("missing package 'pkg1' in summary")
	}
	if pkg1.Statements != 15 || pkg1.Covered != 10 {
		t.Errorf("pkg1 stats = (%d, %d), want (15, 10)", pkg1.Statements, pkg1.Covered)
	}

	pkg2, ok2 := summary.Packages["pkg2"]
	if !ok2 {
		t.Fatal("missing package 'pkg2' in summary")
	}
	if pkg2.Statements != 20 || pkg2.Covered != 20 {
		t.Errorf("pkg2 stats = (%d, %d), want (20, 20)", pkg2.Statements, pkg2.Covered)
	}
}

func TestSummaryFromProfileDefaultMeta(t *testing.T) {
	t.Parallel()

	blocks := []CoverageBlock{
		{File: "example.com/app/main.go", Statements: 10, Count: 1},
	}

	summary := SummaryFromProfile(blocks, "example.com/app", CoverageSummaryMeta{})

	if summary.ReportedAt.IsZero() {
		t.Error("expected non-zero default ReportedAt")
	}
	if summary.Status != StatusPassed {
		t.Errorf("Status = %q, want %q", summary.Status, StatusPassed)
	}
}

func TestCoverageSummaryRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "coverage-summary.json")

	original := CoverageSummary{
		SchemaVersion:  1,
		Repository:     "example/repo",
		SHA:            "123456",
		Ref:            "refs/heads/main",
		WorkflowRunID:  999,
		WorkflowRunURL: "https://example.com/run/999",
		ReportedAt:     time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC),
		Status:         StatusPassed,
		Statements:     100,
		Covered:        90,
		Percentage:     90.0,
		Modules: []ModuleCoverageSummary{
			{Path: ".", Statements: 100, Covered: 90, Percentage: 90.0},
		},
		Packages: map[string]PackageSummary{
			"pkg": {Statements: 100, Covered: 90, Percentage: 90.0},
		},
	}

	if err := WriteCoverageSummary(path, original); err != nil {
		t.Fatalf("WriteCoverageSummary: %v", err)
	}

	loaded, err := ReadCoverageSummary(path)
	if err != nil {
		t.Fatalf("ReadCoverageSummary: %v", err)
	}

	if loaded.SchemaVersion != original.SchemaVersion ||
		loaded.Repository != original.Repository ||
		loaded.SHA != original.SHA ||
		loaded.Statements != original.Statements ||
		loaded.Covered != original.Covered ||
		loaded.Percentage != original.Percentage {
		t.Fatalf("loaded summary mismatch: got %+v, want %+v", loaded, original)
	}
}

func TestReadCoverageSummaryErrors(t *testing.T) {
	t.Parallel()

	if _, err := ReadCoverageSummary("non-existent-file.json"); err == nil {
		t.Error("expected error for non-existent file, got nil")
	}

	dir := t.TempDir()
	invalidJSON := filepath.Join(dir, "invalid.json")
	if err := os.WriteFile(invalidJSON, []byte("{invalid-json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ReadCoverageSummary(invalidJSON); err == nil {
		t.Error("expected error for invalid json, got nil")
	}
}
