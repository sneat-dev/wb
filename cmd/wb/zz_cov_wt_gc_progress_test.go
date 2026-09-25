package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/diskusage"
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

func TestCwWtShortPathTrimsToOwnerSlashRepository(t *testing.T) {
	cases := map[string]string{
		"/tmp/projects/acme/app":        "acme/app",
		"/tmp/projects/acme/app/":       "acme/app",
		"/tmp/projects/acme/app/.//":    "acme/app",
		"app":                           "app",
		"":                              ".",
		"/":                             "",
		"relative/acme/app":             "acme/app",
		"/tmp/projects/acme/app/../app": "acme/app",
	}
	for input, want := range cases {
		if got := shortPath(input); got != want {
			t.Errorf("shortPath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCwWtFormatWorktreeGCOutcomeInProcess(t *testing.T) {
	command := newWorktreeGCCmd(&invocation{})
	var out bytes.Buffer
	command.SetOut(&out)
	outcome := worktrees.GCOutcome{
		SchemaVersion: 1,
		Entries: []worktrees.GCEntry{
			{
				Task: "alpha", Repository: "acme/app", WorktreeDir: "/tmp/wt/alpha",
				Class: "dirty", Reason: "uncommitted changes", Evidence: []string{"a.go"},
				Warnings: []string{"branch renamed"}, SanctionedCommand: "wb worktree cleanup alpha",
				Management: "unmanaged", Error: "boom",
			},
			{Task: "beta", Repository: "acme/app", Class: "unpushed", Reason: "never pushed", Management: "managed"},
		},
		PartialTasks: []worktrees.GCPartialTask{{Task: "gamma", Retired: []string{"a"}, LeftAlone: []string{"b"}}},
		Artifacts:    []worktrees.LifecycleArtifact{{Kind: "stage", Path: "/tmp/stage", Reason: "quarantined"}},
		Shells:       []worktrees.RetiredShell{{Task: "alpha", Path: "/tmp/shell", Error: "shell failed"}},
		Totals: map[string]int{
			"retired": 1, "eligible": 2, "refused": 3, "purged_artefacts": 4,
			"retired_shells": 5, "eligible_shells": 6,
		},
		Reclaimable: diskusage.Usage{ApparentBytes: 1024, UnsharedBytes: 512},
	}
	if err := printWorktreeGC(command, outcome); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"dirty", "uncommitted changes", "evidence: [a.go]", "warning: branch renamed",
		"resolve with: wb worktree cleanup alpha", "WB management: unmanaged", "error: boom",
		"partial: task gamma retired [a] and left [b] behind",
		"artifact stage /tmp/stage: quarantined",
		"shell alpha /tmp/shell: shell failed",
		"\n1 retired, 2 eligible, 3 kept, 4 terminal artefacts purged, 0 repository-root stages to purge, 6 empty shells to retire; reclaimable",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("gc text missing %q:\n%s", want, text)
		}
	}

	// --apply switches the footer to the reclaimed figure and retired shells.
	out.Reset()
	outcome.Apply = true
	if err := printWorktreeGC(command, outcome); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "reclaimed") || !strings.Contains(got, "5 empty shells retired") {
		t.Fatalf("apply footer = %q", got)
	}

	// An empty sweep still says so.
	out.Reset()
	if err := printWorktreeGC(command, worktrees.GCOutcome{}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "no WB worktrees") {
		t.Fatalf("empty gc output = %q", got)
	}
}

func TestCwWtPrintWorktreeGCPropagatesWriteFailures(t *testing.T) {
	outcome := worktrees.GCOutcome{
		Entries:      []worktrees.GCEntry{{Task: "alpha", Repository: "acme/app", Reason: "dirty", Evidence: []string{"e"}, Warnings: []string{"w"}, SanctionedCommand: "cmd", Management: "unmanaged", Error: "boom"}},
		PartialTasks: []worktrees.GCPartialTask{{Task: "gamma"}},
		Artifacts:    []worktrees.LifecycleArtifact{{Kind: "k", Path: "p", Reason: "r"}},
		Shells:       []worktrees.RetiredShell{{Task: "s", Path: "p", Error: "e"}},
		Totals:       map[string]int{"retired": 1},
	}
	for allow := 0; allow < 11; allow++ {
		command := newWorktreeGCCmd(&invocation{})
		command.SetOut(&cwWtFailWriter{Allow: allow})
		if err := printWorktreeGC(command, outcome); err == nil {
			t.Fatalf("printWorktreeGC with %d writes allowed returned nil, want write failure", allow)
		}
	}
	// An empty outcome still writes one line.
	command := newWorktreeGCCmd(&invocation{})
	command.SetOut(&cwWtFailWriter{Allow: 0})
	if err := printWorktreeGC(command, worktrees.GCOutcome{}); err == nil {
		t.Fatal("empty printWorktreeGC did not propagate the write failure")
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
	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeGCCmd(&invocation{}) })
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

func TestCwWtDisabledWhenZero(t *testing.T) {
	if got := disabledWhenZero(0); got != worktrees.DisableSessionFreshness {
		t.Fatalf("disabledWhenZero(0) = %v, want the library disable value", got)
	}
	if got := disabledWhenZero(3 * time.Hour); got != 3*time.Hour {
		t.Fatalf("disabledWhenZero(3h) = %v", got)
	}
}
