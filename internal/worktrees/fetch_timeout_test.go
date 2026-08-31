package worktrees

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// hangingFetchGitShim puts a `git` on PATH whose exact-target fetch never
// answers, reproducing an unreachable remote. Everything else delegates to the
// real binary so the rest of the walk behaves normally.
func hangingFetchGitShim(t *testing.T) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git on PATH")
	}
	dir := t.TempDir()
	shim := filepath.Join(dir, "git")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = \"--no-tags\" ]; then sleep 600; exit 1; fi\n" +
		"done\n" +
		"exec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A remote that never answers must become a reported state, not an indefinite
// park. A live `--all-merged --apply --remote` sat 38 minutes on one unanswered
// `git fetch` in sneat-co/sneat-libs, holding that task's lock, indistinguishable
// from ordinary slowness because the command prints nothing until it finishes.
func TestInventoryReportsAnUnreachableRemoteInsteadOfHanging(t *testing.T) {
	fixture := newGitFixture(t)
	if _, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "unreachable-remote", WorkLog: WorkLogOptions{Model: "unknown"},
	}); err != nil {
		t.Fatal(err)
	}
	installFailingGitHubFixture(t)
	hangingFetchGitShim(t)

	previous := remoteTargetFetchTimeout
	remoteTargetFetchTimeout = 2 * time.Second
	t.Cleanup(func() { remoteTargetFetchTimeout = previous })

	started := time.Now()
	outcome, err := ListWithDiagnostics(context.Background(), ListOptions{
		ProjectsRoot: fixture.projectsRoot,
		GitHub:       true,
	})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("an unreachable remote must not fail the walk: %v", err)
	}
	if elapsed > 60*time.Second {
		t.Fatalf("the walk waited %s on an unreachable remote; the deadline did not end the call", elapsed)
	}
	if !hasDiagnosticContaining(outcome, "did not answer within") {
		t.Fatalf("an unreachable remote must be reported as such: %#v", outcome.Diagnostics)
	}
}

func TestFetchRemoteTargetHeadDoesNotUseSharedFetchHead(t *testing.T) {
	fixture := newGitFixture(t)
	want := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	fetchHeadPath := filepath.Join(fixture.canonical, ".git", "FETCH_HEAD")
	fetchHeadSentinel := []byte("caller-owned lifecycle FETCH_HEAD sentinel\n")
	if err := os.WriteFile(fetchHeadPath, fetchHeadSentinel, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := fetchRemoteTargetHead(context.Background(), fixture.canonical, "main")
	if err != nil || got != want {
		t.Fatalf("fetched origin/main target = %q, err=%v; want %q", got, err, want)
	}
	if got, err := os.ReadFile(fetchHeadPath); err != nil || string(got) != string(fetchHeadSentinel) {
		t.Fatalf("shared FETCH_HEAD changed to %q, err=%v", got, err)
	}
	if refs := gitTestOutput(t, fixture.canonical, "for-each-ref", "--format=%(refname)", "refs/wb/fetch-base"); refs != "" {
		t.Fatalf("private target fetch refs were not cleaned up: %q", refs)
	}
}

func hasDiagnosticContaining(outcome ListOutcome, needle string) bool {
	for _, diagnostic := range outcome.Diagnostics {
		if strings.Contains(diagnostic.Message, needle) {
			return true
		}
	}
	return false
}
