package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/worktreeend"
	"github.com/spf13/cobra"
)

func TestCwWtPrintWorktreeEndTextAndJSON(t *testing.T) {
	result := worktreeend.Result{
		Task:    "gc-cli",
		Applied: false,
		Members: []worktreeend.MemberResult{
			{
				Repository: "acme/app", Worktree: "/tmp/wt", Action: "would retire",
				Dirty: []string{"a.go", "b.go"}, CaptureRef: "refs/stash@{0}", Detail: "unmerged branch",
			},
			{Repository: "acme/other", Worktree: "/tmp/wt2", Action: "skip"},
		},
		ClaimOutcome: "claim retained",
	}
	var out bytes.Buffer
	command := newWorktreeEndCmd(&invocation{})
	command.SetOut(&out)
	if err := printWorktreeEnd(command, "text", result); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"would end task gc-cli",
		"acme/app", "uncommitted: a.go, b.go",
		"captured at refs/stash@{0} — recover with `git stash apply refs/stash@{0}`",
		"! unmerged branch", "claim: claim retained",
		"nothing was changed; re-run with --apply",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("end text missing %q:\n%s", want, text)
		}
	}

	out.Reset()
	result.Applied = true
	if err := printWorktreeEnd(command, "text", result); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "ended task gc-cli") || strings.Contains(got, "nothing was changed") {
		t.Fatalf("applied end text = %q", got)
	}

	out.Reset()
	if err := printWorktreeEnd(command, "json", result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "\"task\": \"gc-cli\"") {
		t.Fatalf("end json = %q", out.String())
	}

	// Every write failure is propagated.
	for allow := 0; allow < 6; allow++ {
		failing := newWorktreeEndCmd(&invocation{})
		failing.SetOut(&cwWtFailWriter{Allow: allow})
		if err := printWorktreeEnd(failing, "text", result); err == nil {
			t.Fatalf("printWorktreeEnd with %d writes allowed returned nil", allow)
		}
	}
}

func TestCwWtWorktreeInventoryAndRunGitIn(t *testing.T) {
	projects := cwCovProjectsRoot(t, "acme/app")

	found, err := worktreeInventory{}.Worktrees(context.Background(), projects, "absent-task", "")
	if err != nil {
		t.Fatalf("worktreeInventory: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("unexpected worktrees: %+v", found)
	}
	// A repository filter that matches nothing leaves the slice empty too.
	if found, err = (worktreeInventory{}).Worktrees(context.Background(), projects, "absent-task", "acme/app"); err != nil || len(found) != 0 {
		t.Fatalf("filtered inventory = (%+v, %v)", found, err)
	}
	// A root that does not exist is an empty inventory, not a failure.
	if found, err = (worktreeInventory{}).Worktrees(context.Background(), filepath.Join(t.TempDir(), "missing"), "absent-task", ""); err != nil || len(found) != 0 {
		t.Fatalf("inventory of a missing root = (%+v, %v)", found, err)
	}

	clone := filepath.Join(projects, "acme", "app")
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "rev-parse", "--is-inside-work-tree"}, runner.Result{Stdout: "true\n"}, nil)
	out, err := gitRunIn(context.Background(), fake, clone, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		t.Fatalf("gitRunIn: %v", err)
	}
	if strings.TrimSpace(out) != "true" {
		t.Fatalf("gitRunIn output = %q", out)
	}
	fake.ExpectArgv([]string{"git", "status"}, runner.Result{}, errors.New("not a git repository"))
	if _, err := gitRunIn(context.Background(), fake, filepath.Join(t.TempDir(), "missing"), "status"); err == nil {
		t.Fatal("gitRunIn in a missing directory must fail")
	}
}

func TestCwWtStreamLinkGuard(t *testing.T) {
	worktree := t.TempDir()
	guard := streamLinkGuard{}
	reasons, sanctioned, err := guard.LiveLinks(worktree)
	if err != nil || len(reasons) != 0 || len(sanctioned) != 0 {
		t.Fatalf("no go.work = (%v, %v, %v)", reasons, sanctioned, err)
	}

	if err := os.WriteFile(filepath.Join(worktree, streams.GoWorkFile), []byte("go 1.24\n\nuse (\n\t./lib\n\t../other\n)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reasons, sanctioned, err = guard.LiveLinks(worktree)
	if err != nil {
		t.Fatalf("guard.LiveLinks: %v", err)
	}
	if len(reasons) != 1 || !strings.Contains(reasons[0], "go.work carries use entries") {
		t.Fatalf("go.work reasons = %v", reasons)
	}
	if len(sanctioned) != 1 || !strings.Contains(sanctioned[0], "--undo") {
		t.Fatalf("go.work sanctioned = %v", sanctioned)
	}

	// A store that cannot be read is an error, not an empty result.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := streamLinkGuard{store: &streams.Store{Root: blocker}}
	if _, _, err := broken.LiveLinks(worktree); err == nil {
		t.Fatal("a store that cannot be read must be reported")
	}

	// GoWorkUseEntries reads the file through a path; a directory named
	// go.work is an error rather than "no entries".
	badWorktree := t.TempDir()
	if err := os.Mkdir(filepath.Join(badWorktree, streams.GoWorkFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (streamLinkGuard{}).LiveLinks(badWorktree); err == nil {
		t.Fatal("a go.work directory must be reported as unreadable")
	}
}

func TestCwWtGitStashCaptureAndNotesAndRetirer(t *testing.T) {
	// gitStashCapture now runs every git call through internal/runner
	// (task-8), and this test's whole point is to observe real git's
	// stash/status behaviour. This file is already on
	// internal/quality/testdata/unit_tier.pending (task-22).
	runnertest.AllowRealProcess(t)
	checkout := cwWtGitRepo(t, filepath.Join(t.TempDir(), "checkout"))
	if err := os.WriteFile(filepath.Join(checkout, "modified.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	capture := gitStashCapture{runner: runner.New()}
	paths, err := capture.DirtyPaths(context.Background(), checkout)
	if err != nil {
		t.Fatalf("DirtyPaths: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("dirty paths = %v", paths)
	}

	if _, err := capture.DirtyPaths(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("DirtyPaths in a missing directory must fail")
	}

	ref, err := capture.Preserve(context.Background(), checkout, "cwWt capture")
	if err != nil {
		t.Fatalf("Preserve: %v", err)
	}
	if len(ref) != 40 {
		t.Fatalf("capture ref = %q, want a 40-character SHA", ref)
	}
	// The capture leaves the worktree clean, which is what lets cleanup retire it.
	paths, err = capture.DirtyPaths(context.Background(), checkout)
	if err != nil || len(paths) != 0 {
		t.Fatalf("post-capture dirty paths = (%v, %v)", paths, err)
	}

	if _, err := capture.Preserve(context.Background(), filepath.Join(t.TempDir(), "missing"), "cwWt capture"); err == nil {
		t.Fatal("Preserve in a missing directory must fail")
	}

	// workLogNotes seals a note into the checkout's journal.
	notePath, err := workLogNotes{}.Seal(checkout, "task ended by cwWt")
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if notePath == "" {
		t.Fatal("Seal reported an empty path")
	}

	// cleanupRetirer reports when cleanup found no candidate at all.
	if err := (cleanupRetirer{}).Retire(context.Background(), t.TempDir(), "absent-task", "acme/app", "/tmp/wt"); err == nil {
		t.Fatal("cleanupRetirer on an empty root must fail")
	}

	// claimReleaser always reports the path it took.
	if got := (claimReleaser{writer: io.Discard}).Release(t.TempDir(), "absent-task"); got == "" {
		t.Fatal("claimReleaser returned an empty outcome")
	}
}

func TestCwWtWorktreeEndInProcess(t *testing.T) {
	// gitStashCapture now runs every git call through internal/runner
	// (task-8), and the end engine calls it (DirtyPaths) even on a dry
	// run. This file is already on
	// internal/quality/testdata/unit_tier.pending (task-22).
	runnertest.AllowRealProcess(t)
	projects, _, _ := initGCFixture(t)
	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeEndCmd(&invocation{projectsRoot: projects}) }, "gc-cli")
	if err != nil {
		t.Fatalf("worktree end dry run: %v", err)
	}
	if !strings.Contains(stdout, "would end task gc-cli") || !strings.Contains(stdout, "nothing was changed") {
		t.Fatalf("worktree end stdout = %q", stdout)
	}

	stdout, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeEndCmd(&invocation{projectsRoot: projects}) }, "gc-cli", "--format", "json")
	if err != nil {
		t.Fatalf("worktree end json: %v", err)
	}
	if !strings.Contains(stdout, "\"task\"") {
		t.Fatalf("worktree end json stdout = %q", stdout)
	}

	// A task that does not exist is reported as an errfindings-free error.
	_, _, err = cwCovExec(t, projects, func() *cobra.Command { return newWorktreeEndCmd(&invocation{projectsRoot: projects}) }, "absent-task")
	if err == nil || !strings.Contains(err.Error(), "has no worktrees") {
		t.Fatalf("worktree end of an absent task = %v", err)
	}
}

func TestCwWtWorktreeEndRefusesLiveLink(t *testing.T) {
	projects, _, worktree := initGCFixture(t)
	// A go.work with use entries is the guard that stops end (exit 2).
	if err := os.WriteFile(filepath.Join(worktree, streams.GoWorkFile), []byte("go 1.24\n\nuse ./local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := cwCovExec(t, projects, func() *cobra.Command { return newWorktreeEndCmd(&invocation{projectsRoot: projects}) }, "gc-cli", "--apply")
	if code := exitCodeOf(t, err); code != exitUsage {
		t.Fatalf("worktree end with a live link exit = %d (%v)\n%s", code, err, stdout)
	}
	if err == nil || !strings.Contains(err.Error(), "go.work carries use entries") {
		t.Fatalf("live-link refusal = %v", err)
	}
}
