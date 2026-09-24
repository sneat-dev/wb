package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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

// TestRunChangedExitsZeroWithoutRunningWhenNothingChanged proves an
// identical target and HEAD never runs the command at all — the command
// here is /bin/false, which would fail the test if it ever executed.
func TestRunChangedExitsZeroWithoutRunningWhenNothingChanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the WB fleet runs on macOS and Linux")
	}
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.commitAll("base")

	t.Chdir(repo.dir)
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--changed", "--target", "HEAD", "--", "/bin/false"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (nothing changed); stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "nothing to run") {
		t.Errorf("stdout = %q, want an explanation that nothing changed", stdout.String())
	}
}

// TestRunChangedRequiresTargetWhenDefaultBranchCannotBeDetected proves a
// repository with no configured origin and no explicit --target fails
// closed with a usage error rather than guessing a base.
func TestRunChangedRequiresTargetWhenDefaultBranchCannotBeDetected(t *testing.T) {
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.commitAll("base")

	t.Chdir(repo.dir)
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--changed", "--", "/bin/true"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "could not detect this repository's default branch") {
		t.Errorf("stderr = %q, want an explanation naming --target", stderr.String())
	}
}

// TestRunHistoryRejectsChangedFlag proves --history's own standalone-report
// convention rejects --changed the same way it rejects every other WB-mode
// flag.
func TestRunHistoryRejectsChangedFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--history", "--changed"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cannot be used with --history") {
		t.Errorf("stderr does not explain the incompatible flag: %s", stderr.String())
	}
}

// TestRunChangedCannotCombineWithAsync proves --changed is synchronous-only.
func TestRunChangedCannotCombineWithAsync(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--changed", "--async", "--worker", "codex-local", "--", "go", "test"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--changed cannot be combined with --async") {
		t.Errorf("stderr = %q, want an explanation", stderr.String())
	}
}

// TestRunTargetRequiresChanged proves --target alone (without --changed) is
// rejected rather than silently ignored.
func TestRunTargetRequiresChanged(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--target", "main", "--", "go", "test"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--target requires --changed") {
		t.Errorf("stderr = %q, want an explanation", stderr.String())
	}
}

// TestRunChangedRequiresCommandMode proves --changed is rejected outside
// `run --` command mode (recipe mode).
func TestRunChangedRequiresCommandMode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--changed", "some-recipe"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--changed and --target require command mode with run --") {
		t.Errorf("stderr = %q, want an explanation", stderr.String())
	}
}

// TestRunChangedPropagatesChangedPackagesError proves a --target WB cannot
// resolve a merge base for surfaces as a findings-level error, not a usage
// error or a silent success.
func TestRunChangedPropagatesChangedPackagesError(t *testing.T) {
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.commitAll("base")

	t.Chdir(repo.dir)
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--changed", "--target", "does-not-exist", "--", "/bin/true"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit code = %d, want non-zero for an unresolvable --target; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "wb run --changed") {
		t.Errorf("stderr = %q, want the --changed context in the error", stderr.String())
	}
}

// TestExpandChangedRunArgsFailsClosedWhenCurrentDirectoryIsGone proves
// expandChangedRunArgs surfaces an os.Getwd error instead of panicking or
// silently defaulting to some other directory.
func TestExpandChangedRunArgsFailsClosedWhenCurrentDirectoryIsGone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the WB fleet runs on macOS and Linux")
	}
	gone := t.TempDir()
	t.Chdir(gone)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	command := &cobra.Command{}
	command.SetContext(context.Background())
	if _, err := expandChangedRunArgs(command, []string{"go", "test"}, "main"); err == nil {
		t.Fatal("want error when the current directory no longer exists")
	}
}

// TestExpandChangedRunArgsPropagatesPrintErrorWhenNothingChanged proves the
// "nothing to run" line's own write error surfaces rather than being
// swallowed, when nothing changed against target.
func TestExpandChangedRunArgsPropagatesPrintErrorWhenNothingChanged(t *testing.T) {
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.commitAll("base")
	t.Chdir(repo.dir)

	command := &cobra.Command{}
	command.SetContext(context.Background())
	command.SetOut(failingWriter{err: errors.New("write refused")})
	if _, err := expandChangedRunArgs(command, []string{"/bin/true"}, "HEAD"); err == nil {
		t.Fatal("want error when the explanatory line cannot be printed")
	}
}

// TestRunChangedAppendsAtModuleRoot proves a module-root-only change
// appends "." rather than an empty or malformed pattern.
func TestRunChangedAppendsAtModuleRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the WB fleet runs on macOS and Linux")
	}
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("main.go", "package main\n\nfunc main() {}\n")
	repo.commitAll("base")

	runCommand(t, repo.dir, "git", "checkout", "-q", "-b", "feature")
	repo.writeFile("main.go", "package main\n\nfunc main() { _ = 1 }\n")
	runCommand(t, repo.dir, "git", "add", "-A")
	runCommand(t, repo.dir, "git", "-c", "user.email=fixture@example.com", "-c", "user.name=Fixture", "commit", "-qm", "change root")

	t.Chdir(repo.dir)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"run", "--changed", "--target", "main", "--",
		"/bin/sh", "-c", `for a in "$@"; do echo "arg:$a"; done`, "sh",
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "arg:.") {
		t.Errorf("stdout = %q, want the module root pattern \".\" appended", stdout.String())
	}
}
