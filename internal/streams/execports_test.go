package streams

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// gitFixture builds a real repository with a base branch and a feature branch,
// so the production Git port is proven against Git itself rather than only
// against a fake. Everything here is local: no test contacts a network.
func gitFixture(t *testing.T) (root string, git ExecGit) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root = t.TempDir()
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
	if err := os.WriteFile(filepath.Join(root, "one.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "feat: the base commit")
	runGit("tag", "backend/v0.4.0")
	runGit("checkout", "-b", "stream/fixture")
	if err := os.WriteFile(filepath.Join(root, "two.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", ".")
	runGit("commit", "-m", "feat: the stream commit")
	runGit("tag", "backend/v0.5.0")
	return root, ExecGit{Timeout: 30 * time.Second}
}

func TestExecGitReadsBranchesTagsAndUnabsorbedCommits(t *testing.T) {
	root, git := gitFixture(t)
	ctx := context.Background()

	branch, err := git.CurrentBranch(ctx, root)
	if err != nil || branch != "stream/fixture" {
		t.Fatalf("CurrentBranch = %q, %v", branch, err)
	}
	head, err := git.LocalHead(ctx, root)
	if err != nil || len(head) != 40 {
		t.Fatalf("LocalHead = %q, %v", head, err)
	}
	tags, err := git.Tags(ctx, root, "backend/*")
	if err != nil || len(tags) != 2 || tags[0] != "backend/v0.5.0" {
		t.Fatalf("Tags = %v, %v; want newest first", tags, err)
	}
	commits, err := git.CommitsNotIn(ctx, root, "stream/fixture", "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 1 || commits[0].Subject != "feat: the stream commit" {
		t.Fatalf("CommitsNotIn = %#v; want the one unabsorbed commit", commits)
	}
	if len(commits[0].SHA) != 40 {
		t.Errorf("commit SHA = %q, want a full object name", commits[0].SHA)
	}
	if commits[0].PatchID == "" {
		t.Error("commit carries no patch id, so patch-identity clustering cannot work")
	}
	absorbed, err := git.CommitsNotIn(ctx, root, "main", "stream/fixture")
	if err != nil || len(absorbed) != 0 {
		t.Fatalf("CommitsNotIn(main, stream) = %v, %v; want nothing unabsorbed", absorbed, err)
	}
	log, err := git.LogSubjects(ctx, root, "backend/v0.4.0", "stream/fixture")
	if err != nil || len(log) != 1 {
		t.Fatalf("LogSubjects = %v, %v", log, err)
	}
	if _, ok, err := git.RemoteHead(ctx, root, "main"); err != nil || ok {
		t.Fatalf("RemoteHead on a repository with no origin = ok %t, err %v; want not found", ok, err)
	}
	if _, err := git.DefaultBranch(ctx, root); err == nil {
		t.Fatal("DefaultBranch resolved a repository with no origin; an unresolvable default branch must be an error, not a guess")
	}

	if err := os.WriteFile(filepath.Join(root, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, err := git.DirtyPaths(ctx, root)
	if err != nil || len(dirty) != 1 || dirty[0] != "dirty.txt" {
		t.Fatalf("DirtyPaths = %v, %v", dirty, err)
	}
}

// Every child a stream verb starts is bounded: a hang is reported as a
// failure, never left to hold the captured output pipe forever.
func TestRunBoundedReportsATimeoutRatherThanHanging(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep is not installed")
	}
	start := time.Now()
	_, err := runBounded(context.Background(), 50*time.Millisecond, t.TempDir(), "sleep", "30")
	if err == nil {
		t.Fatal("a command that outlived its bound reported success")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v, want a timeout", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("runBounded took %s; the bound was not enforced", elapsed)
	}
}

func TestPullRequestJSONMapsOntoThePort(t *testing.T) {
	raw := pullRequestJSON{
		Number: 7, URL: "https://example.test/pull/7", Title: "t",
		IsDraft: true, State: "OPEN", HeadRefName: "stream/x", BaseRefName: "main",
	}
	pullRequest := raw.toPullRequest()
	if pullRequest.Number != 7 || !pullRequest.Draft || pullRequest.Head != "stream/x" || pullRequest.Base != "main" {
		t.Fatalf("toPullRequest = %#v", pullRequest)
	}
}

func TestPreflightChecksDeclaresItsPlanInRunOrder(t *testing.T) {
	checks := PreflightChecks()
	want := []string{CheckHooks, CheckNpmProviderIdentity, CheckRedMain, CheckStreamConcurrency}
	if len(checks) != len(want) {
		t.Fatalf("checks = %v, want %v", checks, want)
	}
	for index := range want {
		if checks[index] != want[index] {
			t.Fatalf("checks = %v, want %v", checks, want)
		}
	}
}

func TestInstalledHooksCheckerReportsAnUnreadableCheckoutAsAnError(t *testing.T) {
	checker := InstalledHooksChecker("/nonexistent/wb", t.TempDir())
	if _, err := checker(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("checking a checkout that does not exist reported no error")
	}
}

func TestOpenResolvesTheStoreBelowWBHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WB_HOME", home)
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if store.Root != filepath.Join(home, "streams") {
		t.Fatalf("store root = %q, want %q", store.Root, filepath.Join(home, "streams"))
	}
}

// REQ: push-verifies-the-ref-it-pushed — the push exit code is not evidence
// the intended commit landed, so PushBranch compares the local SHA with
// origin's after pushing. Exercised against a real local bare remote.
func TestPushBranchVerifiesTheRefItPushed(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	base := t.TempDir()
	remote := filepath.Join(base, "origin.git")
	if output, err := exec.Command("git", "init", "--bare", "--initial-branch=main", remote).CombinedOutput(); err != nil {
		t.Fatalf("init bare remote: %v: %s", err, output)
	}
	work := filepath.Join(base, "work")
	runIn := func(dir string, args ...string) string {
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
	if output, err := exec.Command("git", "clone", remote, work).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(work, "one.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn(work, "add", ".")
	runIn(work, "commit", "-m", "feat: the stream commit")
	runIn(work, "checkout", "-b", "stream/pushed")

	git := ExecGit{Timeout: 60 * time.Second}
	ctx := context.Background()
	pushed, err := git.PushBranch(ctx, work, "stream/pushed")
	if err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	local := strings.TrimSpace(runIn(work, "rev-parse", "HEAD"))
	if pushed != local {
		t.Fatalf("PushBranch reported %s, local HEAD is %s", pushed, local)
	}
	// The verification is against origin, not against the local ref it just
	// wrote: the remote must actually carry the commit.
	onRemote := strings.TrimSpace(runIn(remote, "rev-parse", "refs/heads/stream/pushed"))
	if onRemote != local {
		t.Fatalf("origin carries %s, want %s", onRemote, local)
	}
	head, present, err := git.RemoteHead(ctx, work, "stream/pushed")
	if err != nil || !present || head != local {
		t.Fatalf("RemoteHead = %q present=%t err=%v", head, present, err)
	}
}

// A push that cannot reach the remote is a failure, not a silently reported
// success — and the error carries no credential.
func TestPushBranchFailsWhenTheRemoteIsUnreachable(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root, git := gitFixture(t)
	secret := "ghp_0123456789abcdefghijklmnopqrstuvwx"
	command := exec.Command("git", "remote", "add", "origin",
		"https://x-access-token:"+secret+"@127.0.0.1:1/acme/library.git")
	command.Dir = root
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("add remote: %v: %s", err, output)
	}
	_, err := git.PushBranch(context.Background(), root, "stream/fixture")
	if err == nil {
		t.Fatal("pushing to an unreachable remote reported success")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the push error carries the credential: %s", err)
	}
}
