package worktrees

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// concurrencyProbeGitShim puts a `git` on PATH that records a start/end marker
// around every exact-target fetch, so overlap in the real walk can be measured
// rather than assumed.
func concurrencyProbeGitShim(t *testing.T) func() int {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git on PATH")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "spans.log")
	shim := filepath.Join(dir, "git")
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = \"--no-tags\" ]; then\n" +
		"    echo start >> " + log + "\n" +
		"    sleep 0.3\n" +
		"    " + realGit + " \"$@\"\n" +
		"    code=$?\n" +
		"    echo end >> " + log + "\n" +
		"    exit $code\n" +
		"  fi\n" +
		"done\n" +
		"exec " + realGit + " \"$@\"\n"
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() int {
		data, readErr := os.ReadFile(log)
		if readErr != nil {
			return 0
		}
		depth, peak := 0, 0
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			switch strings.TrimSpace(line) {
			case "start":
				depth++
				if depth > peak {
					peak = depth
				}
			case "end":
				depth--
			}
		}
		return peak
	}
}

// The proof that concurrency actually overlaps fetches. Three *distinct*
// canonical repositories each owe their own exact-target fetch — the dedupe
// cannot collapse those — so their fetches must overlap when the ceiling allows
// it and must not when it does not. Every other test here would still pass if
// the flag were never threaded through, which is why this one exists.
func TestInventoryOverlapsFetchesAcrossRepositoriesOnlyWhenParallelAllowsIt(t *testing.T) {
	build := func(t *testing.T) *gitFixture {
		t.Helper()
		fixture := newGitFixture(t)
		for _, name := range []string{"storage", "third"} {
			gitTest(t, fixture.projectsRoot, "clone", fixture.remote, filepath.Join(fixture.projectsRoot, "acme", name))
		}
		if _, err := Create(context.Background(), []string{"acme/app", "acme/storage", "acme/third"}, CreateOptions{
			ProjectsRoot: fixture.projectsRoot,
			Operation:    "spread", WorkLog: WorkLogOptions{RunID: "spread", Model: "unknown"},
		}); err != nil {
			t.Fatal(err)
		}
		installFailingGitHubFixture(t)
		return fixture
	}

	measure := func(t *testing.T, workers int) int {
		t.Helper()
		fixture := build(t)
		peak := concurrencyProbeGitShim(t)
		if _, err := ListWithDiagnostics(context.Background(), ListOptions{
			ProjectsRoot: fixture.projectsRoot,
			GitHub:       true,
			Workers:      workers,
		}); err != nil {
			t.Fatal(err)
		}
		return peak()
	}

	t.Run("sequential", func(t *testing.T) {
		if got := measure(t, 1); got != 1 {
			t.Fatalf("workers=1 overlapped %d fetches; it must stay sequential", got)
		}
	})
	t.Run("bounded", func(t *testing.T) {
		if got := measure(t, 3); got < 2 {
			t.Fatalf("workers=3 across three repositories peaked at %d concurrent fetches; fetches serialise behind the target-cache mutex", got)
		}
	})
}
