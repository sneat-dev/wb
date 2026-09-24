package streams

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
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
		command.Env = testenv.GitAutoMaintenanceOffEnv(append(os.Environ(),
			"GIT_AUTHOR_NAME=wb", "GIT_AUTHOR_EMAIL=wb@example.test",
			"GIT_COMMITTER_NAME=wb", "GIT_COMMITTER_EMAIL=wb@example.test",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		))
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
	t.Parallel()
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
	t.Parallel()
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

func TestRunBoundedRedactsCredentialsFromCommandArguments(t *testing.T) {
	t.Setenv("WB_RUN_BOUNDED_CREDENTIAL_HELPER", "1")
	for _, test := range []struct {
		name        string
		url         string
		hidden      []string
		timeout     time.Duration
		wantTimeout bool
	}{
		{
			name: "username and password", url: "https://wb-user:super-secret-token@example.invalid/repository.git",
			hidden: []string{"wb-user", "super-secret-token"}, timeout: time.Second,
		},
		{
			name: "token as username", url: "https://glpat-opaque-review-credential@example.invalid/repository.git",
			hidden: []string{"glpat-opaque-review-credential"}, timeout: time.Second,
		},
		{
			name: "empty password", url: "https://glpat-opaque-empty:@example.invalid/repository.git",
			hidden: []string{"glpat-opaque-empty"}, timeout: time.Second,
		},
		{
			name: "token as username on timeout", url: "https://glpat-timeout-credential@example.invalid/repository.git",
			hidden: []string{"glpat-timeout-credential"}, timeout: 100 * time.Millisecond, wantTimeout: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mode := "fail"
			if test.wantTimeout {
				mode = "timeout"
			}
			_, err := runBounded(context.Background(), test.timeout, t.TempDir(), os.Args[0], "-test.run=^TestRunBoundedCredentialHelper$", "--", mode, test.url)
			if err == nil {
				t.Fatal("helper command reported success")
			}
			for _, secret := range test.hidden {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("command error leaked %q: %v", secret, err)
				}
			}
			if !strings.Contains(err.Error(), "[redacted]") {
				t.Fatalf("command error contains no redaction marker: %v", err)
			}
			if test.wantTimeout && !strings.Contains(err.Error(), "timed out") {
				t.Fatalf("timeout error = %v, want a timeout", err)
			}
		})
	}
}

func TestRunBoundedCredentialHelper(t *testing.T) {
	t.Parallel()
	if os.Getenv("WB_RUN_BOUNDED_CREDENTIAL_HELPER") != "1" {
		return
	}
	for _, arg := range os.Args {
		if arg == "timeout" {
			for {
				time.Sleep(time.Hour)
			}
		}
	}
	os.Exit(1)
}

func TestPullRequestJSONMapsOntoThePort(t *testing.T) {
	t.Parallel()
	raw := pullRequestJSON{
		Number: 7, URL: "https://example.test/pull/7", Title: "t",
		IsDraft: true, State: "OPEN", HeadRefName: "stream/x", BaseRefName: "main",
		HeadRefOID: "0123456789012345678901234567890123456789",
	}
	raw.MergeCommit.OID = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	pullRequest := raw.toPullRequest()
	if pullRequest.Number != 7 || !pullRequest.Draft || pullRequest.Head != "stream/x" || pullRequest.Base != "main" || pullRequest.HeadSHA != raw.HeadRefOID || pullRequest.MergeSHA != raw.MergeCommit.OID {
		t.Fatalf("toPullRequest = %#v", pullRequest)
	}
}

func TestPreflightChecksDeclaresItsPlanInRunOrder(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	checker := InstalledHooksChecker("/nonexistent/wb", t.TempDir())
	if _, err := checker(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("checking a checkout that does not exist reported no error")
	}
}

func TestOpenResolvesTheStoreBelowTheProjectsRoot(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// WB_HOME no longer selects state; the store must derive from the root.
	decoy := filepath.Join(t.TempDir(), "wb-home")
	t.Setenv("WB_HOME", decoy)
	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, ".wb", "streams"); store.Root != want {
		t.Fatalf("store root = %q, want %q", store.Root, want)
	}
	if strings.HasPrefix(store.Root, decoy) {
		t.Fatalf("store root %q derives from WB_HOME", store.Root)
	}
}

// REQ: push-verifies-the-ref-it-pushed — the push exit code is not evidence
// the intended commit landed, so PushBranch compares the local SHA with
// origin's after pushing. Exercised against a real local bare remote.
func TestPushBranchVerifiesTheRefItPushed(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	base := t.TempDir()
	remote := filepath.Join(base, "origin.git")
	if output, err := exec.Command("git", "init", "--bare", "--initial-branch=main", remote).CombinedOutput(); err != nil {
		t.Fatalf("init bare remote: %v: %s", err, output)
	}
	testenv.ConfigureGitAutoMaintenanceOff(t, remote)
	work := filepath.Join(base, "work")
	runIn := func(dir string, args ...string) string {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = dir
		command.Env = testenv.GitAutoMaintenanceOffEnv(append(os.Environ(),
			"GIT_AUTHOR_NAME=wb", "GIT_AUTHOR_EMAIL=wb@example.test",
			"GIT_COMMITTER_NAME=wb", "GIT_COMMITTER_EMAIL=wb@example.test",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		))
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s in %s: %v: %s", strings.Join(args, " "), dir, err, output)
		}
		return string(output)
	}
	cloneCmd := exec.Command("git", "clone", remote, work)
	cloneCmd.Env = testenv.GitAutoMaintenanceOffEnv(os.Environ())
	if output, err := cloneCmd.CombinedOutput(); err != nil {
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

func TestDeleteRemoteBranchUsesAnAuthoritativeRereadAfterLeaseFailure(t *testing.T) {
	t.Parallel()
	const branch = "stream/recovery"
	t.Run("already absent is retired", func(t *testing.T) {
		t.Parallel()
		local, other := newPublishedStreamFixture(t)
		expected := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "refs/remotes/origin/"+branch))
		runStreamGit(t, other, "push", "origin", "--delete", branch)
		// Ordinary fetches do not prune a deleted branch. This stale tracking
		// ref is the exact interrupted-resume state that used to make the
		// failed leased deletion look like a live remote ref.
		if stale := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "refs/remotes/origin/"+branch)); stale != expected {
			t.Fatalf("stale tracking head = %s, want %s", stale, expected)
		}

		git := ExecGit{Timeout: time.Minute}
		if err := git.DeleteRemoteBranch(context.Background(), local, branch, expected); err != nil {
			t.Fatalf("retire an already-absent remote ref: %v", err)
		}
		if remote := strings.TrimSpace(runStreamGit(t, local, "ls-remote", "--heads", "origin", "refs/heads/"+branch)); remote != "" {
			t.Fatalf("remote ref survived: %s", remote)
		}
	})

	t.Run("advanced ref remains protected", func(t *testing.T) {
		t.Parallel()
		local, other := newPublishedStreamFixture(t)
		expected := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "refs/remotes/origin/"+branch))
		runStreamGit(t, other, "checkout", branch)
		commitStreamFile(t, other, "advanced.txt", "advanced\n", "feat: advance the stream")
		runStreamGit(t, other, "push", "origin", branch)
		advanced := strings.TrimSpace(runStreamGit(t, other, "rev-parse", "HEAD"))

		git := ExecGit{Timeout: time.Minute}
		err := git.DeleteRemoteBranch(context.Background(), local, branch, expected)
		if err == nil {
			t.Fatal("deleting an advanced remote ref succeeded; want the lease to fail closed")
		}
		if !strings.Contains(err.Error(), advanced) || !strings.Contains(err.Error(), expected) {
			t.Fatalf("advanced-ref error = %v, want observed %s and expected %s", err, advanced, expected)
		}
		remote := strings.TrimSpace(runStreamGit(t, local, "ls-remote", "--heads", "origin", "refs/heads/"+branch))
		if fields := strings.Fields(remote); len(fields) != 2 || fields[0] != advanced {
			t.Fatalf("advanced remote ref = %q, want %s", remote, advanced)
		}
	})

	t.Run("separate push destination remains protected", func(t *testing.T) {
		t.Parallel()
		local, fetchPeer := newPublishedStreamFixture(t)
		expected := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "refs/remotes/origin/"+branch))

		pushRemote := filepath.Join(t.TempDir(), "push.git")
		runStreamGit(t, "", "init", "--bare", "--initial-branch=main", pushRemote)
		testenv.ConfigureGitAutoMaintenanceOff(t, pushRemote)
		runStreamGit(t, local, "push", pushRemote, "main:main", branch+":"+branch)
		runStreamGit(t, local, "remote", "set-url", "--push", "origin", pushRemote)

		// The fetch destination no longer has the ref, while the actual push
		// destination advanced after the deletion lease was recorded.
		runStreamGit(t, fetchPeer, "push", "origin", "--delete", branch)
		if fetchSide := strings.TrimSpace(runStreamGit(t, local, "ls-remote", "--heads", "origin", "refs/heads/"+branch)); fetchSide != "" {
			t.Fatalf("fetch-side ref = %q, want absent", fetchSide)
		}
		pushPeer := filepath.Join(t.TempDir(), "push-peer")
		runStreamGit(t, "", "clone", pushRemote, pushPeer)
		runStreamGit(t, pushPeer, "checkout", branch)
		commitStreamFile(t, pushPeer, "advanced-push.txt", "advanced\n", "feat: advance the push destination")
		runStreamGit(t, pushPeer, "push", "origin", branch)
		advanced := strings.TrimSpace(runStreamGit(t, pushPeer, "rev-parse", "HEAD"))

		git := ExecGit{Timeout: time.Minute}
		err := git.DeleteRemoteBranch(context.Background(), local, branch, expected)
		if err == nil {
			t.Fatal("absent fetch-side ref hid an advanced push destination")
		}
		if !strings.Contains(err.Error(), advanced) || !strings.Contains(err.Error(), expected) {
			t.Fatalf("push-destination error = %v, want observed %s and expected %s", err, advanced, expected)
		}
		remote := strings.TrimSpace(runStreamGit(t, local, "ls-remote", "--heads", pushRemote, "refs/heads/"+branch))
		if fields := strings.Fields(remote); len(fields) != 2 || fields[0] != advanced {
			t.Fatalf("advanced push destination = %q, want %s", remote, advanced)
		}
	})

	t.Run("every configured push destination is verified", func(t *testing.T) {
		t.Parallel()
		local, _ := newPublishedStreamFixture(t)
		expected := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "refs/remotes/origin/"+branch))
		pushRemotes := []string{
			filepath.Join(t.TempDir(), "already-absent.git"),
			filepath.Join(t.TempDir(), "advanced.git"),
		}
		for _, remote := range pushRemotes {
			runStreamGit(t, "", "init", "--bare", "--initial-branch=main", remote)
			testenv.ConfigureGitAutoMaintenanceOff(t, remote)
			runStreamGit(t, local, "push", remote, "main:main", branch+":"+branch)
			runStreamGit(t, local, "remote", "set-url", "--add", "--push", "origin", remote)
		}
		runStreamGit(t, local, "push", pushRemotes[0], "--delete", branch)

		pushPeer := filepath.Join(t.TempDir(), "advanced-peer")
		runStreamGit(t, "", "clone", pushRemotes[1], pushPeer)
		runStreamGit(t, pushPeer, "checkout", branch)
		commitStreamFile(t, pushPeer, "advanced-mirror.txt", "advanced\n", "feat: advance one push destination")
		runStreamGit(t, pushPeer, "push", "origin", branch)
		advanced := strings.TrimSpace(runStreamGit(t, pushPeer, "rev-parse", "HEAD"))

		git := ExecGit{Timeout: time.Minute}
		err := git.DeleteRemoteBranch(context.Background(), local, branch, expected)
		if err == nil || !strings.Contains(err.Error(), advanced) {
			t.Fatalf("multi-destination deletion error = %v, want advanced SHA %s", err, advanced)
		}
		remote := strings.TrimSpace(runStreamGit(t, local, "ls-remote", "--heads", pushRemotes[1], "refs/heads/"+branch))
		if fields := strings.Fields(remote); len(fields) != 2 || fields[0] != advanced {
			t.Fatalf("advanced second push destination = %q, want %s", remote, advanced)
		}
	})
}

// A stream member can be left without its draft pull request after another
// checkout has already advanced and pushed the shared stream branch. Retrying
// `wb stream join` must absorb that strictly newer remote head before it opens
// the missing pull request; a plain push only reports non-fast-forward forever.
func TestPushBranchFastForwardsABehindStreamCheckout(t *testing.T) {
	t.Parallel()
	local, other := newPublishedStreamFixture(t)
	runStreamGit(t, other, "checkout", "stream/recovery")
	commitStreamFile(t, other, "remote.txt", "remote\n", "feat: remote advance")
	runStreamGit(t, other, "push", "origin", "stream/recovery")
	remoteHead := strings.TrimSpace(runStreamGit(t, other, "rev-parse", "HEAD"))

	git := ExecGit{Timeout: time.Minute}
	pushed, err := git.PushBranch(context.Background(), local, "stream/recovery")
	if err != nil {
		t.Fatalf("recover behind stream branch: %v", err)
	}
	if pushed != remoteHead {
		t.Fatalf("published head = %s, want existing remote head %s", pushed, remoteHead)
	}
	if localHead := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "HEAD")); localHead != remoteHead {
		t.Fatalf("local stream head = %s, want fast-forwarded %s", localHead, remoteHead)
	}
}

// Recovery may publish local work when it extends the fetched remote branch;
// the remote-ahead repair must not turn that normal case into a no-op.
func TestPushBranchPublishesALocalAheadStreamCheckout(t *testing.T) {
	t.Parallel()
	local, _ := newPublishedStreamFixture(t)
	commitStreamFile(t, local, "local.txt", "local\n", "feat: local advance")
	localHead := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "HEAD"))

	git := ExecGit{Timeout: time.Minute}
	pushed, err := git.PushBranch(context.Background(), local, "stream/recovery")
	if err != nil {
		t.Fatalf("publish local-ahead stream branch: %v", err)
	}
	if pushed != localHead {
		t.Fatalf("published head = %s, want local head %s", pushed, localHead)
	}
}

func TestPushBranchAcceptsAnAlreadyPublishedStreamCheckout(t *testing.T) {
	t.Parallel()
	local, _ := newPublishedStreamFixture(t)
	headBefore := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "HEAD"))

	git := ExecGit{Timeout: time.Minute}
	published, err := git.PushBranch(context.Background(), local, "stream/recovery")
	if err != nil {
		t.Fatalf("accept equal stream heads: %v", err)
	}
	if published != headBefore {
		t.Fatalf("published head = %s, want unchanged %s", published, headBefore)
	}
}

func TestPushBranchRefusesToFastForwardOverDirtyWork(t *testing.T) {
	t.Parallel()
	local, other := newPublishedStreamFixture(t)
	runStreamGit(t, other, "checkout", "stream/recovery")
	commitStreamFile(t, other, "remote.txt", "remote\n", "feat: remote advance")
	runStreamGit(t, other, "push", "origin", "stream/recovery")
	localHead := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(local, "dirty.txt"), []byte("do not lose\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	git := ExecGit{Timeout: time.Minute}
	_, err := git.PushBranch(context.Background(), local, "stream/recovery")
	if err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("dirty recovery error = %v, want an explicit dirty-worktree refusal", err)
	}
	if after := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "HEAD")); after != localHead {
		t.Fatalf("dirty local head changed from %s to %s", localHead, after)
	}
	contents, readErr := os.ReadFile(filepath.Join(local, "dirty.txt"))
	if readErr != nil || string(contents) != "do not lose\n" {
		t.Fatalf("dirty work was changed: contents=%q error=%v", contents, readErr)
	}
}

// Two independently advanced stream heads require an owner's decision. WB
// must neither force-push nor silently choose one while recovering a missing
// pull request.
func TestPushBranchRefusesADivergedStreamCheckout(t *testing.T) {
	t.Parallel()
	local, other := newPublishedStreamFixture(t)
	commitStreamFile(t, local, "local.txt", "local\n", "feat: local advance")
	localHead := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "HEAD"))
	runStreamGit(t, other, "checkout", "stream/recovery")
	commitStreamFile(t, other, "remote.txt", "remote\n", "feat: remote advance")
	runStreamGit(t, other, "push", "origin", "stream/recovery")
	remoteHead := strings.TrimSpace(runStreamGit(t, other, "rev-parse", "HEAD"))

	git := ExecGit{Timeout: time.Minute}
	_, err := git.PushBranch(context.Background(), local, "stream/recovery")
	if err == nil || !strings.Contains(err.Error(), "diverged") {
		t.Fatalf("divergence error = %v; want an explicit divergence refusal", err)
	}
	if after := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "HEAD")); after != localHead {
		t.Fatalf("local head changed from %s to %s", localHead, after)
	}
	if after := strings.TrimSpace(runStreamGit(t, other, "rev-parse", "origin/stream/recovery")); after != remoteHead {
		t.Fatalf("remote head changed from %s to %s", remoteHead, after)
	}
}

func newPublishedStreamFixture(t *testing.T) (local, other string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	base := t.TempDir()
	remote := filepath.Join(base, "origin.git")
	runStreamGit(t, "", "init", "--bare", "--initial-branch=main", remote)
	testenv.ConfigureGitAutoMaintenanceOff(t, remote)
	local = filepath.Join(base, "local")
	runStreamGit(t, "", "clone", remote, local)
	commitStreamFile(t, local, "base.txt", "base\n", "feat: base")
	runStreamGit(t, local, "push", "-u", "origin", "main")
	runStreamGit(t, local, "checkout", "-b", "stream/recovery")
	runStreamGit(t, local, "push", "-u", "origin", "stream/recovery")
	other = filepath.Join(base, "other")
	runStreamGit(t, "", "clone", remote, other)
	return local, other
}

func runStreamGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = testenv.GitAutoMaintenanceOffEnv(append(os.Environ(),
		"GIT_AUTHOR_NAME=wb", "GIT_AUTHOR_EMAIL=wb@example.test",
		"GIT_COMMITTER_NAME=wb", "GIT_COMMITTER_EMAIL=wb@example.test",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
	))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v: %s", strings.Join(args, " "), dir, err, output)
	}
	return string(output)
}

func commitStreamFile(t *testing.T, dir, name, contents, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	runStreamGit(t, dir, "add", name)
	runStreamGit(t, dir, "commit", "-m", message)
}

// A push that cannot reach the remote is a failure, not a silently reported
// success — and the error carries no credential.
func TestPushBranchFailsWhenTheRemoteIsUnreachable(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root, git := gitFixture(t)
	secret := "ghp_0123456789abcdefghijklmnopqrstuvwx"
	command := exec.Command("git", "remote", "add", "origin",
		"https://x-access-token:"+secret+"@127.0.0.1:1/acme/library.git")
	command.Dir = root
	command.Env = testenv.GitAutoMaintenanceOffEnv(append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null"))
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
