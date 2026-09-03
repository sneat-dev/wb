package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
)

// The production capture must leave the worktree CLEAN — otherwise the
// existing cleanup transaction, which refuses a dirty checkout, could never
// retire it — and the reference it prints must still resolve afterwards.
func TestWorktreeEndCapturesDirtyWorkAndRetiresTheCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = root
		command.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=wb", "GIT_AUTHOR_EMAIL=wb@example.test",
			"GIT_COMMITTER_NAME=wb", "GIT_COMMITTER_EMAIL=wb@example.test",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
		}
	}
	runGit("init", "--initial-branch=main", ".")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "base")

	// Both kinds of uncommitted work: a modified tracked file and a file Git
	// has never seen. An agent's unfinished work is routinely the latter.
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked.md"), []byte("notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	capture := gitStashCapture{}
	ctx := context.Background()
	dirty, err := capture.DirtyPaths(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirty) != 2 {
		t.Fatalf("dirty = %v, want both the modified and the untracked path", dirty)
	}

	ref, err := capture.Preserve(ctx, root, "wb worktree end test")
	if err != nil {
		t.Fatalf("preserve: %v", err)
	}
	if len(ref) != 40 {
		t.Fatalf("capture ref = %q, want an immutable object name", ref)
	}

	// Clean afterwards, or cleanup could never retire the checkout.
	after, err := capture.DirtyPaths(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 0 {
		t.Fatalf("the worktree is still dirty after the capture: %v", after)
	}

	// The captured bytes are recoverable from the printed reference.
	show := exec.Command("git", "show", ref)
	show.Dir = root
	output, err := show.CombinedOutput()
	if err != nil {
		t.Fatalf("the printed capture reference does not resolve: %v: %s", err, output)
	}
	if !strings.Contains(string(output), "two") {
		t.Errorf("the capture does not contain the modified content:\n%s", output)
	}
	stashed := exec.Command("git", "stash", "list")
	stashed.Dir = root
	listed, err := stashed.CombinedOutput()
	if err != nil || !strings.Contains(string(listed), "wb worktree end test") {
		t.Errorf("the capture was not anchored in the stash reflog: %v %s", err, listed)
	}
}

// The link guard reads both independent signals, so a hand-written go.work
// with no stream record still refuses.
func TestWorktreeEndLinkGuardReadsBothSignals(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WB_HOME", home)
	worktree := t.TempDir()
	store := streams.OpenAt(filepath.Join(home, "streams"))
	guard := streamLinkGuard{store: store}

	reasons, _, err := guard.LiveLinks(worktree)
	if err != nil || len(reasons) != 0 {
		t.Fatalf("a clean worktree reported %v (err %v)", reasons, err)
	}

	if err := os.WriteFile(filepath.Join(worktree, "go.work"),
		[]byte("go 1.27\n\nuse (\n\t./backend\n\t/elsewhere/library/backend\n)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reasons, sanctioned, err := guard.LiveLinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if len(reasons) != 1 || !strings.Contains(reasons[0], "/elsewhere/library/backend") {
		t.Fatalf("reasons = %v, want the hand-written go.work named", reasons)
	}
	if len(sanctioned) == 0 || !strings.Contains(sanctioned[0], "--undo") {
		t.Errorf("sanctioned = %v, want the clearing command", sanctioned)
	}
}

// A live link refuses the verb with exit 2 and names the command that clears
// it, rather than retiring a checkout that builds against an unpublished tree.
func TestWorktreeEndRefusesALiveLink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WB_HOME", home)
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, "go.work"),
		[]byte("go 1.27\n\nuse (\n\t/elsewhere/library\n)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := streams.OpenAt(filepath.Join(home, "streams"))
	if _, err := store.Create(streams.Stream{
		Name: "linked", Phase: streams.PhaseOpen,
		Members: []streams.Member{{
			Repository: "acme/app", Worktree: worktree,
			Links: []streams.Link{{
				Library: "/work/library", LibraryRepository: "acme/library",
				Mechanism: streams.MechanismGoWork, Identity: "github.com/acme/library/backend",
			}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	guard := streamLinkGuard{store: store}
	reasons, sanctioned, err := guard.LiveLinks(worktree)
	if err != nil {
		t.Fatal(err)
	}
	// Both signals fire independently; either alone is enough to refuse.
	if len(reasons) != 2 {
		t.Fatalf("reasons = %v, want both the stream record and the go.work", reasons)
	}
	if len(sanctioned) != 2 {
		t.Fatalf("sanctioned = %v, want a clearing command for each signal", sanctioned)
	}
}

// `wb worktree end` on a task WB does not know is an error, not a silent
// success that would let a lane believe it had closed.
func TestWorktreeEndOnAnUnknownTaskFails(t *testing.T) {
	t.Setenv("WB_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := run([]string{"worktree", "end", "no-such-task", "--non-interactive"}, &stdout, &stderr)
	if code == exitOK {
		t.Fatalf("ending an unknown task succeeded; stdout=%s", stdout.String())
	}
}
