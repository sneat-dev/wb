package gitrepo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/remotestate"
)

// TestDQCovListAndStatusReportCloneFailure proves List and Status surface a
// failed Fetch (here: an unusable clone URL) instead of reading a stale or
// missing working tree.
func TestDQCovListAndStatusReportCloneFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-origin")
	p := New(Options{ClonePath: filepath.Join(t.TempDir(), "p", "wb-state"), CloneURL: missing})

	if entries, err := p.List(context.Background()); err == nil {
		t.Fatalf("List = %+v, want an error from the failed clone", entries)
	}
	if status, err := p.Status(context.Background()); err == nil {
		t.Fatalf("Status = %+v, want an error from the failed clone", status)
	}
}

// TestDQCovEnsureCloneReportsUnreadableOrigin proves a directory that merely
// holds a `.git` entry but has no readable origin configuration is reported as
// an error, not silently treated as the store.
func TestDQCovEnsureCloneReportsUnreadableOrigin(t *testing.T) {
	p := New(Options{ClonePath: filepath.Join(t.TempDir(), "p", "wb-state"), CloneURL: "file:///nowhere"})
	if err := os.MkdirAll(filepath.Join(p.opts.ClonePath, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := p.Fetch(context.Background()); err == nil {
		t.Fatal("Fetch against a .git with no origin config succeeded, want an error")
	}
}

// TestDQCovFetchReportsUpstreamCheckFailure proves Fetch surfaces a git error
// from the upstream check rather than treating it as "no upstream".
func TestDQCovFetchReportsUpstreamCheckFailure(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	// Garbage in packed-refs makes ref resolution fail with a real git error
	// (exit 128), distinct from the clean "no upstream" exit 1.
	if err := os.WriteFile(filepath.Join(p.opts.ClonePath, ".git", "packed-refs"), []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := p.Fetch(context.Background()); err == nil {
		t.Fatal("Fetch with unreadable refs succeeded, want an error")
	}
}

// TestDQCovPushReportsUpstreamCheckFailure proves push propagates a git error
// from the upstream probe instead of falling through to a push.
func TestDQCovPushReportsUpstreamCheckFailure(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.opts.ClonePath, ".git", "packed-refs"), []byte("garbage\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := p.push(); err == nil {
		t.Fatal("push with unreadable refs succeeded, want an error")
	}
}

// TestDQCovPushReportsMissingRepository proves push invoked against a clone
// path that does not exist fails at the upstream probe.
func TestDQCovPushReportsMissingRepository(t *testing.T) {
	p := New(Options{ClonePath: filepath.Join(t.TempDir(), "does", "not", "exist"), CloneURL: "file:///nowhere"})
	if err := p.push(); err == nil {
		t.Fatal("push against a missing repository succeeded, want an error")
	}
}

// TestDQCovScpHostPathRejectsNonScpForms proves the scp-like split refuses an
// "@" with no following ":" and a candidate host that is not a bare hostname.
func TestDQCovScpHostPathRejectsNonScpForms(t *testing.T) {
	for _, in := range []string{"user@host", "user@ho/st:path", "@:path", "", "no-at-sign"} {
		host, path, ok := scpHostPath(in)
		if ok {
			t.Errorf("scpHostPath(%q) = (%q, %q, true), want ok=false", in, host, path)
		}
	}
	if host, path, ok := scpHostPath("git@github.com:o/r.git"); !ok || host != "github.com" || path != "o/r.git" {
		t.Fatalf("scpHostPath(git@github.com:o/r.git) = (%q, %q, %v), want github.com / o.r.git / true", host, path, ok)
	}
}

// TestDQCovProviderMethodsReportLockFailure proves every locked entry point
// reports an uncreatable lock directory rather than proceeding unlocked.
func TestDQCovProviderMethodsReportLockFailure(t *testing.T) {
	p := New(Options{ClonePath: dqCovBlockedLockClonePath(t), CloneURL: "file:///nowhere"})
	ctx := context.Background()
	at := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	checks := []struct {
		name string
		run  func() error
	}{
		{"Claim", func() error {
			_, err := p.Claim(ctx, mkClaim("alice", "laptop", "task-1", at), remotestate.ClaimNormal, "")
			return err
		}},
		{"Release", func() error {
			_, err := p.Release(ctx, "task-1", "alice", "laptop", false)
			return err
		}},
		{"Claims", func() error {
			_, err := p.Claims(ctx)
			return err
		}},
		{"Publish", func() error {
			_, err := p.Publish(ctx, snap("alice", "laptop", at))
			return err
		}},
		{"List", func() error {
			_, err := p.List(ctx)
			return err
		}},
		{"Status", func() error {
			_, err := p.Status(ctx)
			return err
		}},
	}
	for _, check := range checks {
		err := check.run()
		if err == nil || !strings.Contains(err.Error(), "create wb-state lock directory") {
			t.Errorf("%s error = %v, want the lock-directory cause", check.name, err)
		}
	}
}

// TestDQCovPublishReportsSnapshotMkdirFailure proves Publish reports a failure
// to create the snapshot's parent directory.
func TestDQCovPublishReportsSnapshotMkdirFailure(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	dqCovPreClone(t, origin, p.opts.ClonePath)
	// A regular file where the machines directory must be makes MkdirAll fail.
	if err := os.WriteFile(filepath.Join(p.opts.ClonePath, "machines"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := p.Publish(context.Background(), snap("bob", "vm", time.Now().UTC())); err == nil {
		t.Fatal("Publish with a file where machines/ belongs succeeded, want an error")
	}
}

// TestDQCovPublishReportsSnapshotWriteFailure proves Publish reports a failure
// to write the snapshot file itself (here: the target path is a directory).
func TestDQCovPublishReportsSnapshotWriteFailure(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	dqCovPreClone(t, origin, p.opts.ClonePath)
	abs := filepath.Join(p.opts.ClonePath, filepath.FromSlash(SnapshotPath("bob", "vm")))
	if err := os.MkdirAll(abs, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := p.Publish(context.Background(), snap("bob", "vm", time.Now().UTC())); err == nil {
		t.Fatal("Publish with a directory at the snapshot path succeeded, want an error")
	}
}

// TestDQCovPublishReportsReadmeWriteFailure proves the first publish into a
// store fails loudly when the shared README cannot be created.
func TestDQCovPublishReportsReadmeWriteFailure(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	dqCovPreClone(t, origin, p.opts.ClonePath)
	dqCovSymlink(t, filepath.Join(t.TempDir(), "missing-parent", "README.md"), filepath.Join(p.opts.ClonePath, "README.md"))

	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err == nil {
		t.Fatal("Publish with an unwritable README path succeeded, want an error")
	}
}

// TestDQCovPublishReportsAddCommitFailure proves Publish reports a git staging
// failure (a stale index.lock) instead of reporting a successful publish.
func TestDQCovPublishReportsAddCommitFailure(t *testing.T) {
	origin := dqCovEmptyBareOrigin(t)
	p := machine(t, origin)
	dqCovPreClone(t, origin, p.opts.ClonePath)
	if err := os.WriteFile(filepath.Join(p.opts.ClonePath, ".git", "index.lock"), []byte("locked"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err == nil {
		t.Fatal("Publish with a stale index.lock succeeded, want an error")
	}
}

// TestDQCovAbortDetailReportsFailedAbort proves abortDetailIfRebasing reports
// that a rebase was in progress when the abort itself failed, instead of
// returning a false negative that would mask the conflict.
func TestDQCovAbortDetailReportsFailedAbort(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	dqCovPreClone(t, origin, p.opts.ClonePath)
	// An incomplete rebase-merge directory makes git see a rebase in progress
	// that `git rebase --abort` cannot itself finish.
	if err := os.MkdirAll(filepath.Join(p.opts.ClonePath, ".git", "rebase-merge"), 0o755); err != nil {
		t.Fatal(err)
	}

	detail, wasRebasing := abortDetailIfRebasing(p.opts.ClonePath)
	if !wasRebasing {
		t.Fatal("abortDetailIfRebasing reported no rebase although one was present")
	}
	if !strings.Contains(detail, "rebase abort also failed") {
		t.Fatalf("detail = %q, want the failed-abort message", detail)
	}
}

// TestDQCovPublishAbortsRebaseAfterRejectedPush proves that when a rejected
// push leads to a genuine rebase conflict, Publish reports the abort and does
// not leave the clone mid-rebase.
func TestDQCovPublishAbortsRebaseAfterRejectedPush(t *testing.T) {
	origin := bareOrigin(t)
	at := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

	competing := snap("bob", "vm", at)
	pushSnapshotToRef(t, origin, competing, "refs/staging/competing")
	competingSHA := gitIn(t, origin, "rev-parse", "refs/staging/competing")
	installRejectFirstPushHook(t, origin, competingSHA)

	p := machine(t, origin)
	_, err := p.Publish(context.Background(), snap("bob", "vm", at.Add(time.Hour)))
	if err == nil || !strings.Contains(err.Error(), "rebase aborted") {
		t.Fatalf("Publish = %v, want an error mentioning the aborted rebase", err)
	}
	if inProgress, ierr := gitops.RebaseInProgress(p.opts.ClonePath); ierr != nil || inProgress {
		t.Fatalf("RebaseInProgress = (%v, %v), want (false, nil)", inProgress, ierr)
	}
}

// TestDQCovPublishReportsRebaseFailureWithoutRebase proves a push rejection
// whose rebase fails before ever starting a rebase (an empty store with no
// branch to fetch) is reported as a plain rebase failure, not an abort.
func TestDQCovPublishReportsRebaseFailureWithoutRebase(t *testing.T) {
	origin := dqCovEmptyBareOrigin(t)
	installRejectAlwaysPushHook(t, origin)
	p := machine(t, origin)

	_, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "push rejected and rebase failed") {
		t.Fatalf("Publish = %v, want 'push rejected and rebase failed'", err)
	}
	if strings.Contains(err.Error(), "rebase aborted") {
		t.Fatalf("Publish = %v, want no abort claim when no rebase started", err)
	}
}

// TestDQCovPublishKeepsCommitAfterSecondRejection proves a persistently
// rejected push is reported after the retry, and the local commit is kept for
// the next attempt (unlike a claims mutation, a snapshot publish owns a
// private per-machine file, so keeping it is safe).
func TestDQCovPublishKeepsCommitAfterSecondRejection(t *testing.T) {
	origin := bareOrigin(t)
	installRejectAlwaysPushHook(t, origin)
	p := machine(t, origin)

	_, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC()))
	if err == nil || !strings.Contains(err.Error(), "push rejected twice") {
		t.Fatalf("Publish = %v, want 'push rejected twice'", err)
	}
	if subject := gitIn(t, p.opts.ClonePath, "log", "-1", "--format=%s"); !strings.Contains(subject, "wb: publish alice/laptop") {
		t.Fatalf("local HEAD subject = %q, want the kept publish commit", subject)
	}
}

// TestDQCovPublishReportsHeadSHAFailure proves Publish surfaces a failure to
// read the resulting HEAD instead of reporting a bogus empty location.
func TestDQCovPublishReportsHeadSHAFailure(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	dqCovPreClone(t, origin, p.opts.ClonePath)
	dqCovInstallRemoveClientBranchHook(t, origin, p.opts.ClonePath)

	_, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC()))
	if err == nil {
		t.Fatal("Publish succeeded although HEAD was unreadable after the push")
	}
}
