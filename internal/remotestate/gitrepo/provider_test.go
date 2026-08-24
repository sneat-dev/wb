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

	"github.com/sneat-dev/wb/internal/gitops"
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
// The GIT_QUARANTINE_PATH env var the hook script below un-sets is part of
// git's ref-update quarantine mechanism for pre-receive hooks, present since
// git 2.11 (released 2016). If this test starts failing after a git upgrade
// on the runner, check that assumption first: a later git may quarantine ref
// updates differently, which would make `env -u GIT_QUARANTINE_PATH git
// update-ref ...` no longer be enough to land the "concurrent" commit.
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

// TestEnsureCloneRejectsForeignRepository proves ensureClone does not treat
// any directory holding a `.git` as the state repo: a clone of a different
// origin already sitting at ClonePath must be rejected, not silently reused.
func TestEnsureCloneRejectsForeignRepository(t *testing.T) {
	origin := bareOrigin(t)
	foreign := bareOrigin(t)

	p := machine(t, origin)
	if err := os.MkdirAll(filepath.Dir(p.opts.ClonePath), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, filepath.Dir(p.opts.ClonePath), "clone", "-q", foreign, p.opts.ClonePath)

	err := p.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not the configured remote store") {
		t.Fatalf("Fetch = %v, want error mentioning 'not the configured remote store'", err)
	}
}

// TestEnsureCloneAcceptsExistingCorrectClone proves a second Provider built
// with the same options as one that already published reuses the existing
// clone instead of erroring, since its origin matches CloneURL.
func TestEnsureCloneAcceptsExistingCorrectClone(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}

	p2 := New(Options{ClonePath: p.opts.ClonePath, CloneURL: origin})
	if _, err := p2.List(context.Background()); err != nil {
		t.Fatalf("List with existing correct clone: %v", err)
	}
}

// TestPublishAbortsRebaseOnConflict proves that when a rebase inside Publish
// hits a real conflict, the clone is not left mid-rebase: Publish's error
// reports the abort, and a subsequent Fetch never trips over a leftover
// rebase state.
func TestPublishAbortsRebaseOnConflict(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)

	// First publish creates p's clone (with README.md written and pushed).
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}

	// A second clone changes README.md - the one file every clone shares -
	// and pushes, so origin's main diverges from p's clone.
	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, t.TempDir(), "clone", "-q", origin, other)
	if err := os.WriteFile(filepath.Join(other, "README.md"), []byte("# remote change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, other, "commit", "-q", "-am", "remote readme change")
	gitIn(t, other, "push", "-q", "origin", "main")

	// p's own clone edits the same line of README.md differently and
	// commits locally, without fetching first, so the rebase that Publish
	// (via Fetch) is about to attempt collides.
	readmePath := filepath.Join(p.opts.ClonePath, "README.md")
	if err := os.WriteFile(readmePath, []byte("# local change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, p.opts.ClonePath, "commit", "-q", "-am", "local readme change")

	_, err := p.Publish(context.Background(), snap("bob", "vm", time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "rebase aborted") {
		t.Fatalf("Publish = %v, want error mentioning 'rebase aborted'", err)
	}

	// The clone must not be left mid-rebase: a subsequent Fetch may still
	// fail on the same underlying conflict, but never with a "rebase in
	// progress" style error.
	if err := p.Fetch(context.Background()); err != nil {
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "rebase-merge") || strings.Contains(lower, "in progress") {
			t.Fatalf("Fetch after aborted rebase = %v, want no rebase-in-progress error", err)
		}
	}
	if _, err := os.Stat(filepath.Join(p.opts.ClonePath, ".git", "rebase-merge")); !os.IsNotExist(err) {
		t.Fatalf(".git/rebase-merge still present after abort: err=%v", err)
	}
}

// TestPublishIntoEmptyStoreCreatesMain proves a first publish works against
// a genuinely empty store: a bare repository with no seed commit at all, the
// shape `gh repo create` leaves behind. `git clone` of that produces a clone
// with no branches and no upstream to rebase onto, so Publish must push
// (with -u) rather than assume a rebase is possible. A second, independent
// provider instance (simulating a second machine) must then be able to
// clone that same store and publish alongside the first entry.
func TestPublishIntoEmptyStoreCreatesMain(t *testing.T) {
	setGitIdentity(t)
	origin := t.TempDir()
	gitIn(t, origin, "init", "-q", "--bare", "-b", "main")
	p := machine(t, origin)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	if _, err := p.Publish(context.Background(), snap("alice", "laptop", at)); err != nil {
		t.Fatalf("first publish into an empty store: %v", err)
	}
	files := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main")
	for _, want := range []string{"README.md", "machines/alice/laptop/snapshot.yaml"} {
		if !strings.Contains(files, want) {
			t.Fatalf("origin tree %q lacks %s", files, want)
		}
	}

	p2 := New(Options{ClonePath: filepath.Join(t.TempDir(), "projects", "team", "wb-state"), CloneURL: origin})
	if _, err := p2.Publish(context.Background(), snap("bob", "vm", at.Add(time.Minute))); err != nil {
		t.Fatalf("second machine's publish against the now-non-empty store: %v", err)
	}

	entries, err := p2.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		keys = append(keys, e.Snapshot.Key())
	}
	if strings.Join(keys, " ") != "alice/laptop bob/vm" {
		t.Fatalf("List keys = %v, want both machines", keys)
	}
}

// TestFetchReportsRealCauseWhenNoRebaseInProgress proves Fetch does not
// blame a rebase-abort failure for an ordinary PullRebase failure that never
// started a rebase at all: a dirty tracked file makes `git pull --rebase`
// refuse before it creates .git/rebase-merge, so RebaseAbort must not be
// attempted (it would itself fail with an unrelated "no rebase in progress"
// error, masking the real cause).
func TestFetchReportsRealCauseWhenNoRebaseInProgress(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}

	// A second clone changes the one file every clone shares and pushes, so
	// there is something new for Fetch to rebase onto.
	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, t.TempDir(), "clone", "-q", origin, other)
	if err := os.WriteFile(filepath.Join(other, "README.md"), []byte("# remote change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, other, "commit", "-q", "-am", "remote readme change")
	gitIn(t, other, "push", "-q", "origin", "main")

	// Dirty the tracked file locally without committing: `git pull --rebase`
	// refuses immediately, before ever starting a rebase.
	if err := os.WriteFile(filepath.Join(p.opts.ClonePath, "README.md"), []byte("dirty, uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := p.Fetch(context.Background())
	if err == nil {
		t.Fatal("Fetch should fail: uncommitted local changes block the pull")
	}
	if strings.Contains(err.Error(), "rebase abort also failed") {
		t.Fatalf("Fetch = %v, want the real cause, not a misattributed rebase-abort failure", err)
	}
	if inProgress, ierr := gitops.RebaseInProgress(p.opts.ClonePath); ierr != nil || inProgress {
		t.Fatalf("RebaseInProgress = (%v, %v), want (false, nil): no rebase should have started", inProgress, ierr)
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

// TestSameRemoteEquivalence proves sameRemote treats the https, ssh:// and
// scp-style spellings of the same GitHub remote as equal (ensureClone relies
// on this so a clone made with one spelling is not rejected as "foreign" when
// the configured remote.repo happens to use another), while still telling
// genuinely different remotes apart.
func TestSameRemoteEquivalence(t *testing.T) {
	equal := [][2]string{
		{"https://github.com/o/r", "git@github.com:o/r.git"},
		{"ssh://git@github.com/o/r", "git@github.com:o/r"},
		{"https://github.com/o/r.git", "https://github.com/o/r"},
		{"https://github.com/o/r/", "https://github.com/o/r"},
		{"HTTPS://GitHub.com/o/r", "https://github.com/o/r"},
		{"http://github.com/o/r", "git://github.com/o/r"},
	}
	for _, pair := range equal {
		if !sameRemote(pair[0], pair[1]) {
			t.Errorf("sameRemote(%q, %q) = false, want true", pair[0], pair[1])
		}
	}

	notEqual := [][2]string{
		{"https://github.com/o/r", "https://github.com/o/other"},
		{"https://github.com/o/r", "https://gitlab.com/o/r"},
		{"/tmp/local/r", "https://github.com/o/r"},
		{"git@github.com:o/r.git", "git@github.com:o2/r.git"},
	}
	for _, pair := range notEqual {
		if sameRemote(pair[0], pair[1]) {
			t.Errorf("sameRemote(%q, %q) = true, want false", pair[0], pair[1])
		}
	}
}

// TestListSurfacesWrongDepthSnapshot proves a snapshot.yaml that does not
// sit at machines/<login>/<machine>/snapshot.yaml (e.g. a stray file dropped
// one level too shallow) is reported as an error entry rather than silently
// skipped, so a malformed store is visible instead of quietly losing data.
func TestListSurfacesWrongDepthSnapshot(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	// Drop a snapshot.yaml at the wrong depth (machines/orphan/snapshot.yaml,
	// depth 2) directly in a second clone.
	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, t.TempDir(), "clone", "-q", origin, other)
	bad := filepath.Join(other, "machines", "orphan", "snapshot.yaml")
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("schema_version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, other, "add", "-A")
	gitIn(t, other, "commit", "-q", "-m", "orphan snapshot")
	gitIn(t, other, "push", "-q", "origin", "main")

	entries, err := p.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 (good machine + bad-path entry)", len(entries))
	}
	var found bool
	for _, e := range entries {
		if e.Error != "" && strings.Contains(e.Error, "unexpected path") {
			found = true
			if !strings.Contains(e.Error, "orphan/snapshot.yaml") {
				t.Errorf("error %q does not mention the bad relative path", e.Error)
			}
		}
	}
	if !found {
		t.Fatalf("entries = %+v, want one entry with an 'unexpected path' error", entries)
	}
}

// TestEnsureCloneAcceptsInsteadOfRewrite proves that ensureClone accepts an
// existing clone whose configured remote URL matches CloneURL even when git's
// url.<base>.insteadOf rewriting has transformed the effective URL. This tests
// the fix for the bug where OriginURL (which returns the rewritten URL) was
// compared instead of ConfiguredOriginURL (which returns the original).
func TestEnsureCloneAcceptsInsteadOfRewrite(t *testing.T) {
	// Create a bare origin and clone it with the actual origin path
	origin := bareOrigin(t)
	clonePath := filepath.Join(t.TempDir(), "projects", "team", "wb-state")
	if err := os.MkdirAll(filepath.Dir(clonePath), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, filepath.Dir(clonePath), "clone", "-q", origin, clonePath)

	// The configured remote URL points to the actual origin path
	configuredBefore := gitIn(t, clonePath, "config", "remote.origin.url")
	if configuredBefore != origin {
		t.Fatalf("configured URL = %q, want %q", configuredBefore, origin)
	}

	// Now set up a url.<sometmpdir>.insteadOf rewrite in the clone's local config
	// so that git operations on the clone will be redirected to the actual origin
	tmpRewrite := filepath.Join(t.TempDir(), "elsewhere")
	gitIn(t, clonePath, "config", "url."+tmpRewrite+".insteadOf", origin)

	// Verify that OriginURL now returns the rewritten URL
	rewritten := gitIn(t, clonePath, "remote", "get-url", "origin")
	if rewritten != tmpRewrite {
		t.Fatalf("OriginURL (rewritten) = %q, want %q", rewritten, tmpRewrite)
	}

	// Create a Provider with the *fake* URL from the test (what a user with a
	// global insteadOf config might have), not the actual origin path
	fakeURL := "git@example.com:team/wb-state.git"
	gitIn(t, clonePath, "remote", "set-url", "origin", fakeURL)
	gitIn(t, clonePath, "config", "url."+origin+".insteadOf", fakeURL)

	// Construct a Provider with the fake URL
	p := New(Options{ClonePath: clonePath, CloneURL: fakeURL})

	// List must succeed without a "not the configured remote store" error.
	// This proves ensureClone is using ConfiguredOriginURL (which compares against
	// the fake URL) rather than OriginURL (which would see the rewritten path).
	_, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List with insteadOf rewrite: %v", err)
	}

	// Publish must succeed, and git push must successfully route to the actual
	// origin via the insteadOf rewrite.
	_, err = p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC()))
	if err != nil {
		t.Fatalf("Publish with insteadOf rewrite: %v", err)
	}

	// Verify that the commit landed on the actual origin
	files := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main")
	if !strings.Contains(files, "machines/alice/laptop/snapshot.yaml") {
		t.Fatalf("publish via insteadOf rewrite did not land on origin: %q", files)
	}
}
