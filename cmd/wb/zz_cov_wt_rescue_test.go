package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/canonicalrescue"
	"github.com/spf13/cobra"
)

// cwWtDirtyCanonicalClone builds a real canonical clone with one uncommitted
// file so the rescue verbs have something honest to find.
func cwWtDirtyCanonicalClone(t *testing.T) (projects, clone string) {
	t.Helper()
	seeds := t.TempDir()
	projects = t.TempDir()
	clone = filepath.Join(projects, "acme", "app")
	cwCovCloneWithOrigin(t, seeds, "app", clone)
	if err := os.WriteFile(filepath.Join(clone, "wip.txt"), []byte("in progress\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return projects, clone
}

func TestCwWtWorktreeRescueReportsAndApplies(t *testing.T) {
	projects, clone := cwWtDirtyCanonicalClone(t)
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))

	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRescueCmd(&invocation{}) }, clone)
	if code := exitCodeOf(t, err); code != exitFindings {
		t.Fatalf("rescue report exit = %d (%v)", code, err)
	}
	for _, want := range []string{"holds 1 uncommitted change(s)", "?? wip.txt", "Nothing has been changed", "wb worktree rescue"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("rescue report missing %q:\n%s", want, stdout)
		}
	}

	// JSON reports the same facts without the exit-1 wrapper.
	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRescueCmd(&invocation{}) }, clone, "--format", "json")
	if err != nil {
		t.Fatalf("rescue json: %v", err)
	}
	if !strings.Contains(stdout, "\"untracked_count\"") || !strings.Contains(stdout, "wip.txt") {
		t.Fatalf("rescue json = %q", stdout)
	}

	// --restore without --apply is refused.
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRescueCmd(&invocation{}) }, clone, "--restore"); err == nil || !strings.Contains(err.Error(), "--restore requires --apply") {
		t.Fatalf("rescue --restore alone = %v", err)
	}

	// --apply captures the content onto a branch and leaves the clone dirty.
	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRescueCmd(&invocation{}) }, clone, "--apply")
	if err != nil {
		t.Fatalf("rescue --apply: %v", err)
	}
	for _, want := range []string{"captured onto rescue/canonical-", "is still dirty on purpose", "--restore"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("rescue apply output missing %q:\n%s", want, stdout)
		}
	}
	if _, err := os.Stat(filepath.Join(clone, "wip.txt")); err != nil {
		t.Fatalf("--apply must not clean the clone: %v", err)
	}
}

func TestCwWtWorktreeRescuePushAndRestore(t *testing.T) {
	projects, clone := cwWtDirtyCanonicalClone(t)
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))

	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRescueCmd(&invocation{}) }, clone, "--apply", "--push", "--restore", "--branch", "rescue/cw-wt")
	if err != nil {
		t.Fatalf("rescue --apply --push --restore: %v", err)
	}
	for _, want := range []string{"captured onto rescue/cw-wt", "pushed to the remote", "is now clean"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("rescue push output missing %q:\n%s", want, stdout)
		}
	}
	if _, err := os.Stat(filepath.Join(clone, "wip.txt")); !os.IsNotExist(err) {
		t.Fatalf("--restore left the captured file behind: %v", err)
	}

	// A clean clone reports that there is nothing to do.
	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRescueCmd(&invocation{}) }, clone)
	if err != nil {
		t.Fatalf("rescue of a clean clone: %v", err)
	}
	if !strings.Contains(stdout, "is clean") {
		t.Fatalf("clean rescue output = %q", stdout)
	}
}

func TestCwWtWorktreeRescueFleetAndArgs(t *testing.T) {
	projects, clone := cwWtDirtyCanonicalClone(t)
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))

	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRescueCmd(&invocation{}) }, "--fleet")
	if code := exitCodeOf(t, err); code != exitFindings {
		t.Fatalf("rescue --fleet exit = %d (%v)", code, err)
	}
	if !strings.Contains(stdout, "change(s)") || !strings.Contains(stdout, "--apply --push") {
		t.Fatalf("rescue --fleet output = %q", stdout)
	}

	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRescueCmd(&invocation{}) }, "--fleet", clone); err == nil || !strings.Contains(err.Error(), "do not also name one") {
		t.Fatalf("--fleet with an argument = %v", err)
	}
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRescueCmd(&invocation{}) }, "--fleet", "--apply"); err == nil || !strings.Contains(err.Error(), "only reports") {
		t.Fatalf("--fleet --apply = %v", err)
	}
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRescueCmd(&invocation{}) }, "--fleet", "--restore"); err == nil || !strings.Contains(err.Error(), "only reports") {
		t.Fatalf("--fleet --restore = %v", err)
	}

	// An all-clean fleet says so.
	cleanProjects := t.TempDir()
	cwCovCloneWithOrigin(t, t.TempDir(), "clean", filepath.Join(cleanProjects, "acme", "clean"))
	stdout, _, err = cwCovExec(t, cleanProjects, func() *cobra.Command { return newWorktreeRescueCmd(&invocation{}) }, "--fleet")
	if err != nil {
		t.Fatalf("clean fleet rescue: %v", err)
	}
	if !strings.Contains(stdout, "is clean") {
		t.Fatalf("clean fleet rescue output = %q", stdout)
	}
	// The json fleet report encodes and exits 0: the findings exit code is a
	// property of the text renderer, which returns the exitError itself.
	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeRescueCmd(&invocation{}) }, "--fleet", "--format", "json")
	if err != nil {
		t.Fatalf("fleet json: %v", err)
	}
	if !strings.Contains(stdout, "\"path\"") {
		t.Fatalf("fleet rescue json = %q", stdout)
	}

	// A projects root that cannot be scanned is reported.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cwCovExec(t, blocker, func() *cobra.Command { return newWorktreeRescueCmd(&invocation{}) }, "--fleet"); err == nil || !strings.Contains(err.Error(), "scan local repositories") {
		t.Fatalf("fleet rescue on an unreadable root = %v", err)
	}
}

func TestCwWtRenderRescueReportTruncationAndFailures(t *testing.T) {
	changes := make([]canonicalrescue.Change, 0, 25)
	for index := 0; index < 25; index++ {
		changes = append(changes, canonicalrescue.Change{Status: "M", Path: "file-" + string(rune('a'+index))})
	}
	report := canonicalrescue.Report{Path: "/tmp/clone", Changes: changes, UntrackedCount: 25}
	var out strings.Builder
	if err := renderRescueReport(cwWtStringCmd(&out), "text", false, report); err == nil {
		t.Fatal("a dirty report without --apply must return the findings exit error")
	}
	if !strings.Contains(out.String(), "… and 5 more") {
		t.Fatalf("truncated rescue report = %q", out.String())
	}

	out.Reset()
	applied := report
	applied.RescueBranch, applied.RescueCommit, applied.Pushed, applied.Restored = "rescue/x", "deadbeef", true, true
	if err := renderRescueReport(cwWtStringCmd(&out), "text", true, applied); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "pushed to the remote") || !strings.Contains(out.String(), "is now clean") {
		t.Fatalf("restored rescue report = %q", out.String())
	}

	out.Reset()
	if err := renderRescueReport(cwWtStringCmd(&out), "text", true, canonicalrescue.Report{Path: "/tmp/clone", Changes: []canonicalrescue.Change{{Status: "M", Path: "a"}}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "is still dirty on purpose") {
		t.Fatalf("unrestored rescue report = %q", out.String())
	}

	out.Reset()
	if err := renderRescueReport(cwWtStringCmd(&out), "json", false, report); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\"changes\"") {
		t.Fatalf("rescue json report = %q", out.String())
	}

	// Every write failure is propagated.
	for allow := 0; allow < 3; allow++ {
		command := newWorktreeRescueCmd(&invocation{})
		command.SetOut(&cwWtFailWriter{Allow: allow})
		if err := renderRescueReport(command, "text", true, applied); err == nil {
			t.Fatalf("renderRescueReport with %d writes allowed returned nil", allow)
		}
	}
	command := newWorktreeRescueCmd(&invocation{})
	command.SetOut(&cwWtFailWriter{Allow: 0})
	if err := renderRescueReport(command, "text", false, canonicalrescue.Report{Path: "/tmp/clean"}); err == nil {
		t.Fatal("clean renderRescueReport did not propagate the write failure")
	}
}

func TestCwWtRunFleetRescueReportFailurePropagation(t *testing.T) {
	projects, _ := cwWtDirtyCanonicalClone(t)
	t.Setenv("WB_HOME", filepath.Join(t.TempDir(), "wb-home"))
	previousRoot := projectsRoot
	projectsRoot = projects
	t.Cleanup(func() { projectsRoot = previousRoot })

	command := newWorktreeRescueCmd(&invocation{})
	command.SetContext(context.Background())
	command.SetOut(&cwWtFailWriter{Allow: 0})
	if err := runFleetRescueReport(&invocation{}, command, "text"); err == nil {
		t.Fatal("runFleetRescueReport did not propagate the write failure")
	}
}
