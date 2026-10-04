package worktreerun

import (
	"context"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/testenv"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeEndCapturesDirtyWorkAndRetiresTheCheckout(t *testing.T) {
	t.Parallel()
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

func TestWorktreeEndLinkGuardReadsBothSignals(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	home := filepath.Join(root, ".wb")
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

func TestWorktreeEndRefusesALiveLink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	home := filepath.Join(root, ".wb")
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

func TestCwWtWorktreeInventoryAndRunGitIn(t *testing.T) {
	t.Parallel()
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

	out, err := runGitIn(context.Background(), filepath.Join(projects, "acme", "app"), "rev-parse", "--is-inside-work-tree")
	if err != nil {
		t.Fatalf("runGitIn: %v", err)
	}
	if strings.TrimSpace(out) != "true" {
		t.Fatalf("runGitIn output = %q", out)
	}
	if _, err := runGitIn(context.Background(), filepath.Join(t.TempDir(), "missing"), "status"); err == nil {
		t.Fatal("runGitIn in a missing directory must fail")
	}
}

func TestCwWtStreamLinkGuard(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	checkout := cwWtGitRepo(t, filepath.Join(t.TempDir(), "checkout"))
	if err := os.WriteFile(filepath.Join(checkout, "modified.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	capture := gitStashCapture{}
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

}

func initTestRepository(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", path, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("init %s: %v\n%s", path, err, output)
	}
	return path
}

func runGit(t *testing.T, dir string, args ...string) { t.Helper(); testenv.Git(t, dir, args...) }

func cwCovProjectsRoot(t *testing.T, repos ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, slug := range repos {
		initTestRepository(t, filepath.Join(root, filepath.FromSlash(slug)))
	}
	return root
}

func cwWtGitRepo(t *testing.T, path string) string {
	t.Helper()
	initTestRepository(t, path)
	runGit(t, path, "config", "user.email", "wb-cwwt@example.test")
	runGit(t, path, "config", "user.name", "WB CwWt")
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("cwWt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, path, "add", ".")
	runGit(t, path, "commit", "-m", "cwWt init")
	return path
}
