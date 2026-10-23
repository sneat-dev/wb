package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoverageChangedRejectsAMalformedTolerancePolicyBeforeMeasuring(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test/app\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".wb"), 0o755); err != nil {
		t.Fatal(err)
	}
	policy := "version: 1\ntiming_tolerance:\n  - package: .\n    statements: 2\n"
	if err := os.WriteFile(filepath.Join(dir, ".wb", "coverage-ratchet.yaml"), []byte(policy), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", dir, "--changed", "--target", "main", "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("code = 0, want nonzero for a tolerance entry without a reason")
	}
	if !strings.Contains(stderr.String(), "needs a reason") {
		t.Fatalf("stderr = %q, want the policy error", stderr.String())
	}
}
