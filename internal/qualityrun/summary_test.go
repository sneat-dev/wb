package qualityrun

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
)

func TestCoverageSummaryCmd(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	moduleDir := filepath.Join(dir, "mymod")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte("module example.com/mymod\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	profilePath := filepath.Join(dir, "profile.cov")
	profileContent := `mode: set
example.com/mymod/pkg/a.go:1.1,5.1 10 1
example.com/mymod/pkg/b.go:1.1,3.1 5 0
`
	if err := os.WriteFile(profilePath, []byte(profileContent), 0o644); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(dir, "summary.json")

	if err := Summary(context.Background(), SummaryRequest{Profile: profilePath, Module: moduleDir, Out: outPath, Meta: quality.CoverageSummaryMeta{Repository: "example/mymod", SHA: "1234567890abcdef", Ref: "refs/heads/main", WorkflowRunID: 456, WorkflowRunURL: "https://github.com/example/mymod/actions/runs/456"}}); err != nil {
		t.Fatal(err)
	}

	summary, err := quality.ReadCoverageSummary(outPath)
	if err != nil {
		t.Fatalf("read generated summary: %v", err)
	}

	if summary.Repository != "example/mymod" {
		t.Errorf("Repository = %q, want %q", summary.Repository, "example/mymod")
	}
	if summary.SHA != "1234567890abcdef" {
		t.Errorf("SHA = %q, want %q", summary.SHA, "1234567890abcdef")
	}
	if summary.Ref != "refs/heads/main" {
		t.Errorf("Ref = %q, want %q", summary.Ref, "refs/heads/main")
	}
	if summary.WorkflowRunID != 456 {
		t.Errorf("WorkflowRunID = %d, want 456", summary.WorkflowRunID)
	}
	if summary.Statements != 15 {
		t.Errorf("Statements = %d, want 15", summary.Statements)
	}
	if summary.Covered != 10 {
		t.Errorf("Covered = %d, want 10", summary.Covered)
	}
}

func TestCoverageSummaryCmdErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	if err := Summary(context.Background(), SummaryRequest{Profile: "missing.cov", Module: filepath.Join(dir, "missing")}); err == nil {
		t.Fatal("missing module accepted")
	}
	moduleDir := filepath.Join(dir, "mod")
	if err := os.MkdirAll(moduleDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte("module example.com/mod\n\ngo 1.27\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Summary(context.Background(), SummaryRequest{Profile: filepath.Join(dir, "missing.cov"), Module: moduleDir}); err == nil {
		t.Fatal("missing profile accepted")
	}
	profilePath := filepath.Join(dir, "valid.cov")
	if err := os.WriteFile(profilePath, []byte("mode: set\nexample.com/mod/a.go:1.1,2.1 1 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := Summary(context.Background(), SummaryRequest{Profile: profilePath, Module: moduleDir, Out: "/dev/null/impossible/summary.json"}); err == nil {
		t.Fatal("unwritable output accepted")
	}
}
