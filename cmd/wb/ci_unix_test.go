//go:build unix

package main

import (
	"os"
	"testing"

	"github.com/sneat-dev/wb/internal/ciaudit"
)

// TestAuditReportsPropagatesAbsError covers cmd/wb/ci.go:339: auditReports
// must return filepath.Abs's error rather than continue to ciaudit.Audit.
// filepath.Abs only fails when the path is relative and os.Getwd fails; this
// test reproduces that by t.Chdir-ing into a temp directory and then
// removing it out from under the process, so Getwd can no longer resolve
// the current directory. Windows cannot remove a process's current working
// directory, so this lives in a unix-only file, and it cannot run in
// parallel because it changes the process-wide working directory.
func TestAuditReportsPropagatesAbsError(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Remove(dir); err != nil {
		t.Fatalf("os.Remove(%q): %v", dir, err)
	}

	called := false
	fake := func(root, target string) ([]ciaudit.Finding, error) {
		called = true
		return nil, nil
	}

	reports, err := auditReports([]string{"relative-repo-path"}, "", fake)
	if err == nil {
		t.Fatal("auditReports err = nil, want an error from filepath.Abs")
	}
	if reports != nil {
		t.Fatalf("reports = %+v, want nil on error", reports)
	}
	if called {
		t.Fatal("comparator was called after filepath.Abs failed")
	}
}
