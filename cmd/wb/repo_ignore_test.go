package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scratchGit runs git in dir and fails the test on error.
func scratchGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// scratchRepo creates an initialized repo in a temp dir.
func scratchRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	scratchGit(t, dir, "init", "-q", "-b", "main")
	return dir
}

func markerValue(t *testing.T, dir string) (string, bool) {
	t.Helper()
	cmd := exec.Command("git", "config", "--local", "--get", "wb.skip-sync")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

func TestRepoIgnoreSetsAndUnsetsMarker(t *testing.T) {
	dir := scratchRepo(t)

	if res := runWBIn(t, dir, "repo", "ignore"); res.exitCode != 0 {
		t.Fatalf("wb repo ignore: exit %d\n%s%s", res.exitCode, res.stdout, res.stderr)
	}
	value, ok := markerValue(t, dir)
	if !ok || value != "true" {
		t.Fatalf("marker = %q (present=%v), want \"true\"", value, ok)
	}

	if res := runWBIn(t, dir, "repo", "ignore", "--unset"); res.exitCode != 0 {
		t.Fatalf("wb repo ignore --unset: exit %d\n%s%s", res.exitCode, res.stdout, res.stderr)
	}
	if value, ok := markerValue(t, dir); ok {
		t.Fatalf("marker still present after --unset: %q", value)
	}
}

// Unsetting a repo that was never marked must succeed rather than reporting
// git's "key absent" exit status as a failure.
func TestRepoIgnoreUnsetIsIdempotent(t *testing.T) {
	dir := scratchRepo(t)

	for i := range 2 {
		if res := runWBIn(t, dir, "repo", "ignore", "--unset"); res.exitCode != 0 {
			t.Fatalf("wb repo ignore --unset (run %d): exit %d\n%s%s", i+1, res.exitCode, res.stdout, res.stderr)
		}
	}
}

// A duplicated key must be cleared entirely, not left in place behind a
// success report.
func TestRepoIgnoreUnsetClearsDuplicateValues(t *testing.T) {
	dir := scratchRepo(t)
	scratchGit(t, dir, "config", "--local", "--add", "wb.skip-sync", "true")
	scratchGit(t, dir, "config", "--local", "--add", "wb.skip-sync", "true")

	if res := runWBIn(t, dir, "repo", "ignore", "--unset"); res.exitCode != 0 {
		t.Fatalf("wb repo ignore --unset: exit %d\n%s%s", res.exitCode, res.stdout, res.stderr)
	}
	if value, ok := markerValue(t, dir); ok {
		t.Fatalf("duplicate marker values survived --unset: %q", value)
	}
}

// The path argument must be honoured, since defaulting to "." is what makes
// these commands dangerous to run from the wrong directory.
func TestRepoIgnoreAcceptsExplicitPath(t *testing.T) {
	dir := scratchRepo(t)
	elsewhere := t.TempDir()

	if res := runWBIn(t, elsewhere, "repo", "ignore", dir); res.exitCode != 0 {
		t.Fatalf("wb repo ignore <path>: exit %d\n%s%s", res.exitCode, res.stdout, res.stderr)
	}
	if value, ok := markerValue(t, dir); !ok || value != "true" {
		t.Fatalf("marker = %q (present=%v), want \"true\"", value, ok)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, ".git")); err == nil {
		t.Fatal("wb repo ignore initialized a repo in the working directory")
	}
}
