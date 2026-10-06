package main

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

// TestRunChangedAppendsOnlyTouchedPackagesToCommand proves `wb run --changed
// --target <base> -- <command>` runs the command once with only the
// packages the local diff touched appended, and never the ones it did not
// (spec/plans/coverage-to-100/README.md task-19, issue #570).
func TestRunChangedAppendsOnlyTouchedPackagesToCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the WB fleet runs on macOS and Linux")
	}
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("pkga/a.go", "package pkga\n")
	repo.writeFile("pkgb/b.go", "package pkgb\n")
	runCommand(t, repo.dir, "git", "add", "-A")
	runCommand(t, repo.dir, "git", "-c", "user.email=fixture@example.com", "-c", "user.name=Fixture", "commit", "-qm", "base")

	runCommand(t, repo.dir, "git", "checkout", "-q", "-b", "feature")
	repo.writeFile("pkga/a.go", "package pkga\n\nfunc Changed() {}\n")
	runCommand(t, repo.dir, "git", "add", "-A")
	runCommand(t, repo.dir, "git", "-c", "user.email=fixture@example.com", "-c", "user.name=Fixture", "commit", "-qm", "change pkga")

	t.Chdir(repo.dir)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"run", "--changed", "--target", "main", "--",
		"/bin/sh", "-c", `for a in "$@"; do echo "arg:$a"; done`, "sh",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "arg:./pkga") {
		t.Errorf("stdout = %q, want the touched package ./pkga appended", stdout.String())
	}
	if strings.Contains(stdout.String(), "arg:./pkgb") {
		t.Errorf("stdout = %q, want the untouched package ./pkgb NOT appended", stdout.String())
	}
}
