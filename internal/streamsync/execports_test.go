package streamsync

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A stream member is persisted before its first push is proven. This exercises
// the real Git port against a bare origin: the local stream branch must remain
// usable when origin has no matching remote-tracking ref yet.
func TestFastForwardToRemoteAllowsAMissingRemoteStreamBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	base := t.TempDir()
	remote := filepath.Join(base, "origin.git")
	runGit(t, "", "init", "--bare", "--initial-branch=main", remote)
	work := filepath.Join(base, "work")
	runGit(t, "", "clone", remote, work)
	if err := os.WriteFile(filepath.Join(work, "one.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-m", "feat: initial")
	runGit(t, work, "push", "-u", "origin", "main")
	runGit(t, work, "checkout", "-b", "stream/first-publication")

	git := ExecGit{Timeout: time.Minute}
	if err := git.Fetch(context.Background(), work); err != nil {
		t.Fatal(err)
	}
	head, present, advanced, err := git.FastForwardToRemote(context.Background(), work, "stream/first-publication", "origin/stream/first-publication")
	if err != nil {
		t.Fatalf("first stream reconciliation: %v", err)
	}
	if head != "" || present || advanced {
		t.Fatalf("remote state = head %q present=%t advanced=%t; want no remote lease yet", head, present, advanced)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=wb", "GIT_AUTHOR_EMAIL=wb@example.test",
		"GIT_COMMITTER_NAME=wb", "GIT_COMMITTER_EMAIL=wb@example.test",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v: %s", strings.Join(args, " "), dir, err, output)
	}
}
