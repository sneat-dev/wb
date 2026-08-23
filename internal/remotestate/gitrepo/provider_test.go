package gitrepo

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
)

// setGitIdentity gives every git command spawned by this test process an
// author/committer identity. CI runners have no global gitconfig, and
// gitops.run's child processes inherit the process env (via
// internal/console.Env, which wraps os.Environ), so t.Setenv here is enough
// to make Provider.Publish's own commits succeed without any repo-local
// config.
func setGitIdentity(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// No explicit cmd.Env: the process env (including the GIT_* identity
	// vars set via t.Setenv in setGitIdentity) is inherited by default.
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// bareOrigin creates an empty bare store with a seed commit on main, the way
// a freshly created team state repo looks after `git init` + first push.
func bareOrigin(t *testing.T) string {
	t.Helper()
	setGitIdentity(t)
	origin := t.TempDir()
	gitIn(t, origin, "init", "-q", "--bare", "-b", "main")
	seed := filepath.Join(t.TempDir(), "seed")
	gitIn(t, t.TempDir(), "clone", "-q", origin, seed)
	gitIn(t, seed, "commit", "-q", "--allow-empty", "-m", "init")
	gitIn(t, seed, "push", "-q", "origin", "main")
	return origin
}

func machine(t *testing.T, origin string) *Provider {
	t.Helper()
	return New(Options{ClonePath: filepath.Join(t.TempDir(), "projects", "team", "wb-state"), CloneURL: origin})
}

func snap(login, machine string, at time.Time) remotestate.Snapshot {
	return remotestate.Build(remotestate.Snapshot{Login: login, Machine: machine, PublishedAt: at, WBVersion: "test", ProjectsRoot: "/p"}, nil, nil, remotestate.RedactNone)
}

func TestPublishClonesWritesCommitsAndPushes(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	result, err := p.Publish(context.Background(), snap("alice", "laptop", at))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Location) != 40 {
		t.Fatalf("Location = %q, want commit sha", result.Location)
	}
	files := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main")
	for _, want := range []string{"README.md", "machines/alice/laptop/snapshot.yaml"} {
		if !strings.Contains(files, want) {
			t.Fatalf("origin tree %q lacks %s", files, want)
		}
	}
	if msg := gitIn(t, origin, "log", "-1", "--format=%s", "main"); msg != "wb: publish alice/laptop @ 2026-08-23T12:00:00Z" {
		t.Fatalf("commit message = %q", msg)
	}
}

func TestPublishUnchangedSnapshotMakesNoCommit(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	first, err := p.Publish(context.Background(), snap("alice", "laptop", at))
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Publish(context.Background(), snap("alice", "laptop", at))
	if err != nil {
		t.Fatal(err)
	}
	if first.Location != second.Location {
		t.Fatalf("identical snapshot produced a new commit %s (was %s)", second.Location, first.Location)
	}
}

// pushSnapshotToRef commits a machine's snapshot (plus README.md, matching
// what Provider.Publish itself writes on a first publish) in a throwaway
// clone of origin and pushes it to ref instead of main. It synthesizes
// "another machine already has this commit ready" without going through a
// Provider and without touching main, so the caller controls exactly when
// that commit becomes visible on main.
func pushSnapshotToRef(t *testing.T, origin string, snapshot remotestate.Snapshot, ref string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "prep")
	gitIn(t, t.TempDir(), "clone", "-q", origin, work)

	data, err := remotestate.Encode(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	rel := SnapshotPath(snapshot.Login, snapshot.Machine)
	abs := filepath.Join(work, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte(readme), 0o644); err != nil {
		t.Fatal(err)
	}
	message := fmt.Sprintf("wb: publish %s @ %s", snapshot.Key(), snapshot.PublishedAt.UTC().Format("2006-01-02T15:04:05Z07:00"))
	gitIn(t, work, "add", "-A")
	gitIn(t, work, "commit", "-q", "-m", message)
	gitIn(t, work, "push", "-q", "origin", "HEAD:"+ref)
}

// installRejectFirstPushHook rigs origin's bare repo so that the *first*
// push it receives is rejected after promoting promoteSHA onto main, and
// every push after that is accepted. This deterministically reproduces, in a
// single-process test, what a genuinely concurrent second machine would
// cause: a push that lands on origin in the narrow window between our
// Fetch and our own Push. A pre-receive hook runs server-side even for a
// same-machine (file path) remote, so this needs no real concurrency and no
// timing assumptions.
func installRejectFirstPushHook(t *testing.T, origin, promoteSHA string) {
	t.Helper()
	hooksDir := filepath.Join(origin, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
set -e
gitdir="$(git rev-parse --git-dir)"
marker="$gitdir/wb-test-hook-fired"
if [ ! -f "$marker" ]; then
  touch "$marker"
  # git forbids ref updates from inside a pre-receive hook while the
  # incoming objects are still quarantined; -u drops that env var for this
  # one command so the "concurrent" commit can land on main immediately.
  env -u GIT_QUARANTINE_PATH git update-ref refs/heads/main %s
  echo "wb-test: simulating a concurrent publish landing first" >&2
  exit 1
fi
exit 0
`, promoteSHA)
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-receive"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestTwoMachinesPublishConcurrentlyViaRebase proves the "push rejected ->
// PullRebase -> push again" branch inside Publish: bob's own push is
// rejected by origin (rigged via installRejectFirstPushHook to simulate
// alice's commit landing concurrently), so Publish must rebase and retry
// before it succeeds. This exercises that exact retry branch deterministically
// rather than a leading-Fetch race that a sequential test cannot force (see
// the task report for why the naive two-Provider version does not hit it).
func TestTwoMachinesPublishConcurrentlyViaRebase(t *testing.T) {
	origin := bareOrigin(t)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	aliceSnap := snap("alice", "laptop", at)
	pushSnapshotToRef(t, origin, aliceSnap, "refs/staging/alice")
	aliceSHA := gitIn(t, origin, "rev-parse", "refs/staging/alice")
	installRejectFirstPushHook(t, origin, aliceSHA)

	b := machine(t, origin)
	if _, err := b.Publish(context.Background(), snap("bob", "vm", at.Add(time.Minute))); err != nil {
		t.Fatalf("bob publish with induced push rejection: %v", err)
	}

	entries, err := b.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		keys = append(keys, e.Snapshot.Key())
	}
	if strings.Join(keys, " ") != "alice/laptop bob/vm" {
		t.Fatalf("List keys = %v (List must Fetch first and sort by key)", keys)
	}
	// The hook only allows a push through on its second invocation, so
	// reaching here at all proves Publish executed PullRebase after a
	// rejected Push and pushed again successfully.
	if msg := gitIn(t, origin, "log", "-1", "--format=%s", "main"); !strings.Contains(msg, "bob/vm") {
		t.Fatalf("main HEAD after retry = %q, want bob's commit", msg)
	}
}

func TestListSurfacesCorruptEntryAsError(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	// Corrupt another machine's file directly in a second clone.
	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, t.TempDir(), "clone", "-q", origin, other)
	bad := filepath.Join(other, "machines", "carol", "desk", "snapshot.yaml")
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("schema_version: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, other, "add", "-A")
	gitIn(t, other, "commit", "-q", "-m", "corrupt")
	gitIn(t, other, "push", "-q", "origin", "main")

	entries, err := p.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	carol := entries[1]
	if carol.Snapshot.Login != "carol" || carol.Snapshot.Machine != "desk" || !strings.Contains(carol.Error, "schema_version 99") {
		t.Fatalf("corrupt entry = %+v", carol)
	}
}
