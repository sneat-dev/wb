package main

import (
	"bytes"
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

	args := []string{
		"coverage", "summary", profilePath,
		"--module", moduleDir,
		"--repo", "example/mymod",
		"--sha", "1234567890abcdef",
		"--ref", "refs/heads/main",
		"--workflow-run-id", "456",
		"--workflow-run-url", "https://github.com/example/mymod/actions/runs/456",
		"--out", outPath,
		"--non-interactive",
	}

	var stdout, stderr bytes.Buffer
	if exitCode := run(args, &stdout, &stderr); exitCode != exitOK {
		t.Fatalf("run(coverage summary) = %d, want %d; stderr: %s", exitCode, exitOK, stderr.String())
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

	// Missing module
	var stdout, stderr bytes.Buffer
	args := []string{"coverage", "summary", "non-existent.cov", "--module", filepath.Join(dir, "non-existent"), "--non-interactive"}
	if exitCode := run(args, &stdout, &stderr); exitCode == exitOK {
		t.Error("expected failure for non-existent module, got exitOK")
	}

	// Missing profile
	stdout.Reset()
	stderr.Reset()
	moduleDir := filepath.Join(dir, "mod")
	_ = os.MkdirAll(moduleDir, 0o755)
	_ = os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte("module example.com/mod\n\ngo 1.27\n"), 0o644)
	args = []string{"coverage", "summary", filepath.Join(dir, "non-existent.cov"), "--module", moduleDir, "--non-interactive"}
	if exitCode := run(args, &stdout, &stderr); exitCode == exitOK {
		t.Error("expected failure for non-existent profile, got exitOK")
	}

	// Unwritable out path
	stdout.Reset()
	stderr.Reset()
	profilePath := filepath.Join(dir, "valid.cov")
	_ = os.WriteFile(profilePath, []byte("mode: set\nexample.com/mod/a.go:1.1,2.1 1 1\n"), 0o644)
	args = []string{"coverage", "summary", profilePath, "--module", moduleDir, "--out", "/dev/null/impossible/summary.json", "--non-interactive"}
	if exitCode := run(args, &stdout, &stderr); exitCode == exitOK {
		t.Error("expected failure for unwritable out path, got exitOK")
	}
}
