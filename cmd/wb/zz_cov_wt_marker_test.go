package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/checkoutmarker"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestCwWtWorktreeMarkerWritesAndIsIdempotent(t *testing.T) {
	seeds := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	cwCovCloneWithOrigin(t, seeds, "app", clone)

	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeMarkerCmd(&invocation{projectsRoot: projects}) }, clone)
	if err != nil {
		t.Fatalf("marker: %v", err)
	}
	if !strings.Contains(stdout, "wrote marker + ignore rule") {
		t.Fatalf("first marker run = %q", stdout)
	}
	if _, err := os.Stat(filepath.Join(clone, checkoutmarker.FileName)); err != nil {
		t.Fatalf("marker file was not written: %v", err)
	}

	// A second run changes nothing.
	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeMarkerCmd(&invocation{projectsRoot: projects}) }, clone)
	if err != nil {
		t.Fatalf("second marker: %v", err)
	}
	if !strings.Contains(stdout, "current") {
		t.Fatalf("second marker run = %q", stdout)
	}

	// A dry run over a fresh clone says what it would change.
	clone2 := filepath.Join(projects, "acme", "app2")
	cwCovCloneWithOrigin(t, seeds, "app2", clone2)
	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeMarkerCmd(&invocation{projectsRoot: projects}) }, clone2, "--dry-run")
	if err != nil {
		t.Fatalf("marker --dry-run: %v", err)
	}
	if !strings.Contains(stdout, "would write marker") {
		t.Fatalf("marker --dry-run = %q", stdout)
	}
	if _, err := os.Stat(filepath.Join(clone2, checkoutmarker.FileName)); !os.IsNotExist(err) {
		t.Fatalf("--dry-run wrote a marker: %v", err)
	}

	// JSON emits the outcome rows.
	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeMarkerCmd(&invocation{projectsRoot: projects}) }, clone, "--format", "json")
	if err != nil {
		t.Fatalf("marker json: %v", err)
	}
	if !strings.Contains(stdout, "\"marker_written\"") {
		t.Fatalf("marker json = %q", stdout)
	}
}

func TestCwWtWorktreeMarkerFleetAndFailures(t *testing.T) {
	seeds := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	cwCovCloneWithOrigin(t, seeds, "app", clone)
	// A linked worktree registered to the clone is swept too.
	linked := filepath.Join(projects, "linked-checkout")
	runGit(t, clone, "worktree", "add", "-b", "linked-branch", linked)

	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeMarkerCmd(&invocation{projectsRoot: projects}) }, "--fleet")
	if err != nil {
		t.Fatalf("marker --fleet: %v", err)
	}
	if !strings.Contains(stdout, "2 checkout(s)") {
		t.Fatalf("fleet marker stdout = %q", stdout)
	}
	if _, err := os.Stat(filepath.Join(linked, checkoutmarker.FileName)); err != nil {
		t.Fatalf("fleet sweep did not mark the linked worktree: %v", err)
	}

	// --fleet with a named checkout is refused.
	if _, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeMarkerCmd(&invocation{projectsRoot: projects}) }, "--fleet", clone); err == nil || !strings.Contains(err.Error(), "do not also name one") {
		t.Fatalf("--fleet with an argument = %v", err)
	}

	// A --filter that matches nothing still completes.
	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeMarkerCmd(&invocation{projectsRoot: projects}) }, "--fleet", "--format", "json")
	if err != nil {
		t.Fatalf("fleet marker json: %v", err)
	}
	if !strings.Contains(stdout, "[") {
		t.Fatalf("fleet marker json = %q", stdout)
	}

	// A path that is not a checkout is a recorded failure, not a crash.
	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeMarkerCmd(&invocation{projectsRoot: projects}) }, filepath.Join(t.TempDir(), "not-a-repo"))
	if code := exitCodeOf(t, err); code != exitFindings {
		t.Fatalf("marker on a non-checkout exit = %d (%v)", code, err)
	}
	if !strings.Contains(stdout, "✗") {
		t.Fatalf("marker failure stdout = %q", stdout)
	}
}

func TestCwWtMarkerCheckoutsAndRegistration(t *testing.T) {
	seeds := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	cwCovCloneWithOrigin(t, seeds, "app", clone)
	linked := filepath.Join(projects, "linked")
	runGit(t, clone, "worktree", "add", "-b", "b", linked)

	checkouts, err := markerCheckouts(t.Context(), &invocation{projectsRoot: projects}, false, []string{"/one"})
	if err != nil || len(checkouts) != 1 || checkouts[0] != "/one" {
		t.Fatalf("single checkout = (%v, %v)", checkouts, err)
	}
	checkouts, err = markerCheckouts(t.Context(), &invocation{projectsRoot: projects}, false, nil)
	if err != nil || len(checkouts) != 1 || checkouts[0] != "." {
		t.Fatalf("default checkout = (%v, %v)", checkouts, err)
	}

	checkouts, err = markerCheckouts(t.Context(), &invocation{projectsRoot: projects}, true, nil)
	if err != nil {
		t.Fatalf("fleet checkouts: %v", err)
	}
	if len(checkouts) != 2 {
		t.Fatalf("fleet checkouts = %v", checkouts)
	}
	checkouts, err = markerCheckouts(t.Context(), &invocation{filterFlag: "nothing-matches"}, true, nil)
	if err != nil || len(checkouts) != 0 {
		t.Fatalf("filtered fleet checkouts = (%v, %v)", checkouts, err)
	}
	checkouts, err = markerCheckouts(t.Context(), &invocation{filterFlag: "acme/app"}, true, nil)
	if err != nil || len(checkouts) != 2 {
		t.Fatalf("matching fleet checkouts = (%v, %v)", checkouts, err)
	}

	registered := registeredWorktrees(t.Context(), clone)
	if len(registered) != 1 {
		t.Fatalf("registered worktrees = %v", registered)
	}
	if got := registeredWorktrees(t.Context(), filepath.Join(projects, "not-a-clone")); got != nil {
		t.Fatalf("registered worktrees of a non-clone = %v", got)
	}

	// resolvedPath follows symlinks where it can and cleans where it cannot.
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolvedPath(link); got != resolvedTarget {
		t.Fatalf("resolvedPath(link) = %q, want %q", got, resolvedTarget)
	}
	if got := resolvedPath(filepath.Join(t.TempDir(), "missing")); !filepath.IsAbs(got) {
		t.Fatalf("resolvedPath(missing) = %q", got)
	}
}

func TestCwWtApplyCheckoutMarkerAndWouldChange(t *testing.T) {
	seeds := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	cwCovCloneWithOrigin(t, seeds, "app", clone)

	options := checkoutmarker.DescribeOptions{ProjectsRoot: projects, BaseBranch: "main", Version: "wb test"}
	outcome := applyCheckoutMarker(clone, options, false)
	if outcome.Error != "" {
		t.Fatalf("applyCheckoutMarker: %s", outcome.Error)
	}
	if !outcome.MarkerWritten || !outcome.ExcludeWritten {
		t.Fatalf("first apply outcome = %+v", outcome)
	}
	outcome = applyCheckoutMarker(clone, options, false)
	if outcome.MarkerWritten || outcome.ExcludeWritten {
		t.Fatalf("second apply outcome = %+v", outcome)
	}
	outcome = applyCheckoutMarker(clone, options, true)
	if outcome.MarkerWritten || outcome.ExcludeWritten {
		t.Fatalf("dry run after a write = %+v", outcome)
	}
	outcome = applyCheckoutMarker(filepath.Join(t.TempDir(), "not-a-repo"), options, false)
	if outcome.Error == "" {
		t.Fatal("applyCheckoutMarker on a non-checkout must record an error")
	}
	if outcome.Path == "" {
		t.Fatalf("a failed outcome must still name the path: %+v", outcome)
	}
}

func TestCwWtMarkerWouldChangeAndSymbols(t *testing.T) {
	seeds := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	cwCovCloneWithOrigin(t, seeds, "app", clone)

	inspection, err := checkoutmarker.Describe(clone, checkoutmarker.DescribeOptions{ProjectsRoot: projects, BaseBranch: "main", Version: "wb test"})
	if err != nil {
		t.Fatal(err)
	}
	marker, exclude := markerWouldChange(inspection)
	if !marker || !exclude {
		t.Fatalf("fresh checkout would change = (%t, %t)", marker, exclude)
	}

	if outcome := applyCheckoutMarker(clone, checkoutmarker.DescribeOptions{ProjectsRoot: projects, BaseBranch: "main", Version: "wb test"}, false); outcome.Error != "" {
		t.Fatal(outcome.Error)
	}
	inspection, err = checkoutmarker.Describe(clone, checkoutmarker.DescribeOptions{ProjectsRoot: projects, BaseBranch: "main", Version: "wb test"})
	if err != nil {
		t.Fatal(err)
	}
	marker, exclude = markerWouldChange(inspection)
	if marker || exclude {
		t.Fatalf("a current checkout would change = (%t, %t)", marker, exclude)
	}

	// A marker whose contents drifted is reported as changed.
	if err := os.WriteFile(filepath.Join(clone, checkoutmarker.FileName), []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	marker, exclude = markerWouldChange(inspection)
	if !marker || exclude {
		t.Fatalf("drifted marker would change = (%t, %t)", marker, exclude)
	}

	if markerSymbol(markerOutcome{Kind: string(checkoutmarker.KindCanonical)}) != "🔒" {
		t.Fatal("a canonical checkout must use the lock symbol")
	}
	if markerSymbol(markerOutcome{Kind: "worktree"}) != "✎" {
		t.Fatal("a worktree must use the pencil symbol")
	}
}

func TestCwWtRenderMarkerOutcomes(t *testing.T) {
	outcomes := []markerOutcome{
		{Path: "/a", Kind: "canonical", MarkerWritten: true, ExcludeWritten: true},
		{Path: "/b", Kind: "worktree", MarkerWritten: true},
		{Path: "/c", Kind: "worktree", ExcludeWritten: true},
		{Path: "/d", Kind: "worktree"},
		{Path: "/e", Kind: "worktree", Error: "boom"},
	}
	var out strings.Builder
	if err := renderMarkerOutcomes(cwWtStringCmd(&out), "text", false, outcomes); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"🔒 /a: wrote marker + ignore rule", "✎ /b: wrote marker",
		"✎ /c: wrote ignore rule", "✎ /d: current", "✗ /e: boom",
		"5 checkout(s), 3 changed",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("marker text missing %q:\n%s", want, text)
		}
	}

	out.Reset()
	if err := renderMarkerOutcomes(cwWtStringCmd(&out), "text", true, outcomes); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "would write marker + ignore rule") {
		t.Fatalf("dry-run marker text = %q", out.String())
	}

	out.Reset()
	if err := renderMarkerOutcomes(cwWtStringCmd(&out), "text", false, outcomes[:1]); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "checkout(s),") {
		t.Fatalf("a single outcome must not print a footer: %q", out.String())
	}

	out.Reset()
	if err := renderMarkerOutcomes(cwWtStringCmd(&out), "json", false, outcomes); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\"marker_written\"") {
		t.Fatalf("marker json = %q", out.String())
	}

	for allow := 0; allow < 2; allow++ {
		command := &cobra.Command{}
		command.SetOut(&cwWtFailWriter{Allow: allow})
		if err := renderMarkerOutcomes(command, "text", false, outcomes); err == nil {
			t.Fatalf("renderMarkerOutcomes with %d writes allowed returned nil", allow)
		}
	}
}

func cwWtStringCmd(out *strings.Builder) *cobra.Command {
	command := &cobra.Command{}
	command.SetOut(out)
	return command
}

func TestCwWtMarkCreatedRenamedRelocatedAndSynced(t *testing.T) {
	seeds := t.TempDir()
	projects := t.TempDir()
	clone := filepath.Join(projects, "acme", "app")
	cwCovCloneWithOrigin(t, seeds, "app", clone)

	var errOut strings.Builder
	command := &cobra.Command{}
	command.SetOut(&strings.Builder{})
	command.SetErr(&errOut)

	// markCreatedCheckouts marks both the worktree and its canonical clone,
	// and warns for a path it cannot describe.
	markCreatedCheckouts(&invocation{projectsRoot: projects}, command, "main", []worktrees.CreateResult{
		{Repository: "acme/app", WorktreeDir: clone, Base: "main"},
		{Repository: "acme/app", WorktreeDir: filepath.Join(t.TempDir(), "missing")},
	})
	if _, err := os.Stat(filepath.Join(clone, checkoutmarker.FileName)); err != nil {
		t.Fatalf("markCreatedCheckouts did not mark the clone: %v", err)
	}
	if !strings.Contains(errOut.String(), "warning: could not write") {
		t.Fatalf("markCreatedCheckouts warnings = %q", errOut.String())
	}

	// A repository slug with no owner is skipped for the canonical path but
	// the worktree itself is still marked.
	errOut.Reset()
	markCreatedCheckouts(&invocation{projectsRoot: projects}, command, "main", []worktrees.CreateResult{{Repository: "app", WorktreeDir: clone}})
	if errOut.Len() != 0 {
		t.Fatalf("markCreatedCheckouts errOut = %q", errOut.String())
	}

	// refreshSyncedCheckoutMarkers skips failed and unnamed results, and
	// counts a clone it cannot describe.
	errOut.Reset()
	refreshSyncedCheckoutMarkers([]fleetsync.Result{
		{Repo: discover.Repo{Org: "acme", Name: "app"}, Status: fleetsync.Failed},
		{Repo: discover.Repo{Org: "", Name: ""}},
		{Repo: discover.Repo{Org: "acme", Name: "app"}},
		{Repo: discover.Repo{Org: "acme", Name: "absent"}},
	}, projects, &errOut)
	if errOut.Len() != 0 {
		t.Fatalf("refreshSyncedCheckoutMarkers warnings = %q", errOut.String())
	}

	// markRenamedCheckouts skips unapplied results and warns on failure.
	errOut.Reset()
	markRenamedCheckouts(&invocation{projectsRoot: projects}, command, "main", []worktrees.RenameResult{
		{Applied: false, NewWorktreeDir: filepath.Join(t.TempDir(), "missing")},
		{Applied: true, NewWorktreeDir: ""},
		{Applied: true, NewWorktreeDir: filepath.Join(t.TempDir(), "missing")},
	})
	if !strings.Contains(errOut.String(), "warning: could not refresh") {
		t.Fatalf("markRenamedCheckouts warnings = %q", errOut.String())
	}

	// markRelocatedCheckouts skips unapplied results and warns on failure.
	errOut.Reset()
	markRelocatedCheckouts(&invocation{projectsRoot: projects}, command, []worktrees.RelocateResult{
		{Applied: false, Destination: filepath.Join(t.TempDir(), "missing")},
		{Applied: true, Destination: filepath.Join(t.TempDir(), "missing")},
	})
	if !strings.Contains(errOut.String(), "warning: relocated worktree marker") {
		t.Fatalf("markRelocatedCheckouts warnings = %q", errOut.String())
	}
}
