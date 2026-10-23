package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func TestCwWtInventoryProgressStreamsAndSummarises(t *testing.T) {
	var out bytes.Buffer
	progress := newInventoryProgress(&invocation{}, &out, true)
	if !progress.enabled {
		t.Fatal("verbose progress must be enabled")
	}

	// Two start events in a row: the first line is left open, so the second
	// start closes it before announcing itself.
	progress.report(worktrees.ListProgress{Index: 1, Task: "alpha", Path: "/tmp/acme/app"})
	progress.report(worktrees.ListProgress{Index: 2, Task: "beta", Path: "/tmp/acme/other"})
	// A completion that closes the open start line.
	progress.report(worktrees.ListProgress{Index: 2, Task: "beta", Path: "/tmp/acme/other", Done: true, Elapsed: 1500 * time.Millisecond})
	// A completion with no open line prints its own full line.
	progress.report(worktrees.ListProgress{Index: 3, Task: "gamma", Path: "/tmp/acme/third", Done: true, Elapsed: 42 * time.Millisecond})
	progress.finish()

	text := out.String()
	for _, want := range []string{
		"[   1] alpha acme/app", "[   2] beta acme/other", "1.5s",
		"[   3] gamma acme/third  42ms",
		"inspected 2 candidates in ", "slowest 1.5s (beta acme/other)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("progress output missing %q:\n%s", want, text)
		}
	}
}

func TestCwWtInventoryProgressDisabledAndNilAreInert(t *testing.T) {
	t.Setenv(console.EnvDisable, "1")
	var out bytes.Buffer
	disabled := newInventoryProgress(&invocation{}, &out, false)
	if disabled.enabled {
		t.Fatal("WB_NON_INTERACTIVE must disable progress without --verbose")
	}
	disabled.report(worktrees.ListProgress{Index: 1, Task: "alpha", Path: "/tmp/acme/app"})
	disabled.finish()
	if out.Len() != 0 {
		t.Fatalf("disabled progress wrote %q", out.String())
	}

	var nilProgress *inventoryProgress
	nilProgress.report(worktrees.ListProgress{Index: 1})
	nilProgress.finish()
}

func TestCwWtInventoryProgressZeroCountWritesNothing(t *testing.T) {
	var out bytes.Buffer
	progress := newInventoryProgress(&invocation{}, &out, true)
	progress.finish()
	if out.Len() != 0 {
		t.Fatalf("zero-candidate finish wrote %q", out.String())
	}

	// An open start line is closed even when nothing completed.
	out.Reset()
	progress = newInventoryProgress(&invocation{}, &out, true)
	progress.report(worktrees.ListProgress{Index: 1, Task: "alpha", Path: "/tmp/acme/app"})
	progress.finish()
	if got := out.String(); !strings.HasSuffix(got, "\n") || strings.Contains(got, "inspected") {
		t.Fatalf("open-line finish = %q", got)
	}
}

func TestCwWtWorktreeGCCmdUsageAndInProcess(t *testing.T) {
	projects := t.TempDir()
	_, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeGCCmd(&invocation{projectsRoot: projects}) }, "--session-freshness", "-1s")
	if code := exitCodeOf(t, err); code != exitUsage {
		t.Fatalf("negative session freshness exit = %d (%v)", code, err)
	}
	if err == nil || !strings.Contains(err.Error(), "--session-freshness cannot be negative") {
		t.Fatalf("negative session freshness error = %v", err)
	}

	_, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeGCCmd(&invocation{projectsRoot: projects}) }, "--format", "bogus")
	if err == nil || !strings.Contains(err.Error(), "format") {
		t.Fatalf("bogus format error = %v", err)
	}

	stdout, stderr, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeGCCmd(&invocation{projectsRoot: projects}) })
	if err != nil {
		t.Fatalf("gc on an empty root: %v (stderr=%s)", err, stderr)
	}
	if !strings.Contains(stdout, "no WB worktrees") {
		t.Fatalf("gc on empty root stdout = %q", stdout)
	}

	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeGCCmd(&invocation{projectsRoot: projects}) }, "--format", "json")
	if err != nil {
		t.Fatalf("gc json on an empty root: %v", err)
	}
	if !strings.Contains(stdout, "schema_version") {
		t.Fatalf("gc json stdout = %q", stdout)
	}
}

func TestCwWtWorktreeGCCmdRefusesDirtyCheckoutInProcess(t *testing.T) {
	projects, _, _ := initGCFixture(t)
	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeGCCmd(&invocation{projectsRoot: projects}) })
	if code := exitCodeOf(t, err); code != exitFindings {
		t.Fatalf("gc exit = %d (%v)\n%s", code, err, stdout)
	}
	if !strings.Contains(stdout, "kept") {
		t.Fatalf("gc text output = %q", stdout)
	}
	if err != nil && !strings.Contains(err.Error(), "checkout(s) were kept") {
		t.Fatalf("gc findings error = %v", err)
	}
}
