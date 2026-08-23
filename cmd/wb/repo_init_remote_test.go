package main

import (
	"strings"
	"testing"
)

// bareOrigin creates a bare repo and returns its path, for use as an origin
// that accepts pushes without touching the network.
func bareOrigin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	scratchGit(t, dir, "init", "-q", "--bare", "-b", "main")
	return dir
}

// repoWithOrigin returns an initialized repo with no commits, wired to a bare
// origin — the exact shape of the repos that motivated this command.
func repoWithOrigin(t *testing.T) (repo, origin string) {
	t.Helper()
	repo = scratchRepo(t)
	origin = bareOrigin(t)
	scratchGit(t, repo, "remote", "add", "origin", origin)
	return repo, origin
}

func TestRepoInitRemotePublishesUnbornBranch(t *testing.T) {
	repo, origin := repoWithOrigin(t)

	res := runWBIn(t, repo, "repo", "init-remote")
	if res.exitCode != 0 {
		t.Fatalf("wb repo init-remote: exit %d\n%s%s", res.exitCode, res.stdout, res.stderr)
	}

	if got := scratchGit(t, origin, "rev-parse", "--verify", "refs/heads/main"); got == "" {
		t.Fatal("origin has no main after init-remote")
	}
	if got := scratchGit(t, repo, "rev-parse", "--abbrev-ref", "main@{upstream}"); got != "origin/main" {
		t.Fatalf("upstream = %q, want origin/main", got)
	}
}

// After publishing, a plain pull must work — that is the whole point, since a
// failing pull is what surfaced this feature.
func TestRepoInitRemoteLeavesRepoPullable(t *testing.T) {
	repo, _ := repoWithOrigin(t)

	if res := runWBIn(t, repo, "repo", "init-remote"); res.exitCode != 0 {
		t.Fatalf("wb repo init-remote: exit %d\n%s%s", res.exitCode, res.stdout, res.stderr)
	}
	scratchGit(t, repo, "pull", "--quiet")
}

func TestRepoInitRemoteRejectsDetachedHead(t *testing.T) {
	repo, _ := repoWithOrigin(t)
	scratchGit(t, repo, "commit", "-q", "--allow-empty", "-m", "seed")
	scratchGit(t, repo, "checkout", "-q", "--detach")
	before := scratchGit(t, repo, "rev-parse", "HEAD")

	res := runWBIn(t, repo, "repo", "init-remote")
	if res.exitCode == 0 {
		t.Fatalf("wb repo init-remote succeeded on a detached HEAD\n%s", res.stdout)
	}
	if !strings.Contains(res.stderr, "detached HEAD") {
		t.Fatalf("stderr does not explain the detached HEAD: %q", res.stderr)
	}
	if after := scratchGit(t, repo, "rev-parse", "HEAD"); after != before {
		t.Fatalf("HEAD moved from %s to %s; a rejected run must not commit", before, after)
	}
}

func TestRepoInitRemoteRequiresOrigin(t *testing.T) {
	repo := scratchRepo(t)

	res := runWBIn(t, repo, "repo", "init-remote")
	if res.exitCode == 0 {
		t.Fatalf("wb repo init-remote succeeded with no origin\n%s", res.stdout)
	}
	if !strings.Contains(res.stderr, "origin") {
		t.Fatalf("stderr does not mention the missing origin: %q", res.stderr)
	}
	if has, _ := markerValue(t, repo); has != "" {
		t.Fatal("unexpected marker written")
	}
	// No commit may have been created for a push that could never run.
	if out := scratchGit(t, repo, "rev-list", "--all", "--count"); out != "0" {
		t.Fatalf("commit count = %s, want 0", out)
	}
}

// Publishing a marked repo would not make it syncable, so the command refuses
// rather than reporting a success that sync will ignore.
func TestRepoInitRemoteRefusesMarkedRepo(t *testing.T) {
	repo, _ := repoWithOrigin(t)
	if res := runWBIn(t, repo, "repo", "ignore"); res.exitCode != 0 {
		t.Fatalf("wb repo ignore: exit %d\n%s%s", res.exitCode, res.stdout, res.stderr)
	}

	res := runWBIn(t, repo, "repo", "init-remote")
	if res.exitCode == 0 {
		t.Fatalf("wb repo init-remote succeeded on a marked repo\n%s", res.stdout)
	}
	if !strings.Contains(res.stderr, "ignore --unset") {
		t.Fatalf("stderr does not point at the fix: %q", res.stderr)
	}
	if out := scratchGit(t, repo, "rev-list", "--all", "--count"); out != "0" {
		t.Fatalf("commit count = %s, want 0", out)
	}
}
