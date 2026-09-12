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

func TestFastForwardToRemoteAdvancesWhenTheFetchedStreamIsAhead(t *testing.T) {
	fixture := newRemoteStreamFixture(t)
	runGit(t, fixture.other, "checkout", "stream/fixture")
	commitFile(t, fixture.other, "remote.txt", "remote\n", "feat: remote stream advance")
	runGit(t, fixture.other, "push", "origin", "stream/fixture")

	git := ExecGit{Timeout: time.Minute}
	if err := git.Fetch(context.Background(), fixture.local); err != nil {
		t.Fatal(err)
	}
	head, present, advanced, err := git.FastForwardToRemote(context.Background(), fixture.local, "stream/fixture", "origin/stream/fixture")
	if err != nil {
		t.Fatalf("fast-forward: %v", err)
	}
	if !present || !advanced {
		t.Fatalf("remote state = head %q present=%t advanced=%t; want an applied remote advance", head, present, advanced)
	}
	if local := strings.TrimSpace(runGit(t, fixture.local, "rev-parse", "stream/fixture")); local != head {
		t.Fatalf("local stream head = %s, want fetched remote %s", local, head)
	}
}

func TestFastForwardToRemoteLeavesAnUnpushedLocalStreamAhead(t *testing.T) {
	fixture := newRemoteStreamFixture(t)
	commitFile(t, fixture.local, "local.txt", "local\n", "feat: local stream work")
	localBefore := strings.TrimSpace(runGit(t, fixture.local, "rev-parse", "stream/fixture"))

	git := ExecGit{Timeout: time.Minute}
	if err := git.Fetch(context.Background(), fixture.local); err != nil {
		t.Fatal(err)
	}
	head, present, advanced, err := git.FastForwardToRemote(context.Background(), fixture.local, "stream/fixture", "origin/stream/fixture")
	if err != nil {
		t.Fatalf("reconcile local-ahead branch: %v", err)
	}
	if !present || advanced {
		t.Fatalf("remote state = head %q present=%t advanced=%t; want a preserved local-ahead branch", head, present, advanced)
	}
	if local := strings.TrimSpace(runGit(t, fixture.local, "rev-parse", "stream/fixture")); local != localBefore {
		t.Fatalf("local stream head = %s, want unchanged %s", local, localBefore)
	}
}

func TestFastForwardToRemoteRefusesDivergedStreamBranchesWithoutChangingLocalHead(t *testing.T) {
	fixture := newRemoteStreamFixture(t)
	commitFile(t, fixture.local, "local.txt", "local\n", "feat: local stream work")
	localBefore := strings.TrimSpace(runGit(t, fixture.local, "rev-parse", "stream/fixture"))
	runGit(t, fixture.other, "checkout", "stream/fixture")
	commitFile(t, fixture.other, "remote.txt", "remote\n", "feat: remote stream work")
	runGit(t, fixture.other, "push", "origin", "stream/fixture")

	git := ExecGit{Timeout: time.Minute}
	if err := git.Fetch(context.Background(), fixture.local); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := git.FastForwardToRemote(context.Background(), fixture.local, "stream/fixture", "origin/stream/fixture")
	if err == nil || !strings.Contains(err.Error(), "diverged") {
		t.Fatalf("reconcile error = %v; want the divergence refusal", err)
	}
	if local := strings.TrimSpace(runGit(t, fixture.local, "rev-parse", "stream/fixture")); local != localBefore {
		t.Fatalf("local stream head = %s, want unchanged %s after refusal", local, localBefore)
	}
}

type remoteStreamFixture struct {
	local string
	other string
}

func newRemoteStreamFixture(t *testing.T) remoteStreamFixture {
	t.Helper()
	base := t.TempDir()
	remote := filepath.Join(base, "origin.git")
	runGit(t, "", "init", "--bare", "--initial-branch=main", remote)
	local := filepath.Join(base, "local")
	runGit(t, "", "clone", remote, local)
	commitFile(t, local, "one.txt", "one\n", "feat: initial")
	runGit(t, local, "push", "-u", "origin", "main")
	runGit(t, local, "checkout", "-b", "stream/fixture")
	runGit(t, local, "push", "-u", "origin", "stream/fixture")
	other := filepath.Join(base, "other")
	runGit(t, "", "clone", remote, other)
	return remoteStreamFixture{local: local, other: other}
}

func commitFile(t *testing.T, dir, name, contents, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-m", message)
}

func runGit(t *testing.T, dir string, args ...string) string {
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
	return string(output)
}
