package gitrepo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/remotestate"
)

// dqCovPushMalformedClaimChain commits two malformed claim files for task in a
// throwaway clone and pushes them to refs/staging/c1 and refs/staging/c2, with
// c2 a child of c1. It is the fixture for the "store keeps changing" retry
// paths: each promotion conflicts with the mutation without ever leaving the
// claim path readable.
func dqCovPushMalformedClaimChain(t *testing.T, origin, task string) (c1, c2 string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "dq-chain")
	gitIn(t, t.TempDir(), "clone", "-q", origin, work)
	abs := filepath.Join(work, filepath.FromSlash(ClaimPath(task)))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte("schema_version: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", "-A")
	gitIn(t, work, "commit", "-q", "-m", "malformed claim 1")
	gitIn(t, work, "push", "-q", "origin", "HEAD:refs/staging/c1")
	if err := os.WriteFile(abs, []byte("schema_version: 98\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", "-A")
	gitIn(t, work, "commit", "-q", "-m", "malformed claim 2")
	gitIn(t, work, "push", "-q", "origin", "HEAD:refs/staging/c2")
	return gitIn(t, origin, "rev-parse", "refs/staging/c1"), gitIn(t, origin, "rev-parse", "refs/staging/c2")
}

func TestDQCovClaimRejectsInvalidTaskName(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	_, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "bad task", time.Now().UTC()), remotestate.ClaimNormal, "")
	if err == nil || !strings.Contains(err.Error(), "must start with a letter or digit") {
		t.Fatalf("Claim with an invalid task name = %v, want a task-name error", err)
	}
}

func TestDQCovReleaseRejectsInvalidTaskName(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	_, err := p.Release(context.Background(), "bad task", "alice", "laptop", false)
	if err == nil || !strings.Contains(err.Error(), "must start with a letter or digit") {
		t.Fatalf("Release with an invalid task name = %v, want a task-name error", err)
	}
}

func TestDQCovClaimReleaseAndClaimsReportCloneFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-origin")
	p := New(Options{ClonePath: filepath.Join(t.TempDir(), "p", "wb-state"), CloneURL: missing})
	ctx := context.Background()

	if _, err := p.Claim(ctx, mkClaim("alice", "laptop", "task-1", time.Now().UTC()), remotestate.ClaimNormal, ""); err == nil {
		t.Fatal("Claim against an unusable clone succeeded, want an error")
	}
	if _, err := p.Release(ctx, "task-1", "alice", "laptop", false); err == nil {
		t.Fatal("Release against an unusable clone succeeded, want an error")
	}
	if _, err := p.Claims(ctx); err == nil {
		t.Fatal("Claims against an unusable clone succeeded, want an error")
	}
}

// dqCovClaimFileAsDirectory makes claims/<task>.yaml a directory in the clone,
// which turns a claim read into a real filesystem error rather than absence.
func dqCovClaimFileAsDirectory(t *testing.T, p *Provider, task string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(p.opts.ClonePath, filepath.FromSlash(ClaimPath(task))), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestDQCovClaimReportsClaimReadFailure(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	dqCovClaimFileAsDirectory(t, p, "task-7")

	_, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", time.Now().UTC()), remotestate.ClaimNormal, "")
	if err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("Claim with a directory at the claim path = %v, want the read error", err)
	}
}

func TestDQCovReleaseReportsClaimReadFailure(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	dqCovClaimFileAsDirectory(t, p, "task-7")

	_, err := p.Release(context.Background(), "task-7", "alice", "laptop", false)
	if err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("Release with a directory at the claim path = %v, want the read error", err)
	}
}

// TestDQCovReleaseRefusesUnreadableClaimWithoutForce proves Release refuses an
// undecodable claim file unless --force is given, and that force removes it.
func TestDQCovReleaseRefusesUnreadableClaimWithoutForce(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	dqCovPushFileToRef(t, origin, ClaimPath("task-5"), []byte("schema_version: 99\n"), "refs/heads/main", "corrupt claim")

	_, err := p.Release(context.Background(), "task-5", "alice", "laptop", false)
	if err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("Release without force on an unreadable claim = %v, want an unreadable error", err)
	}
	if tree := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main"); !strings.Contains(tree, "claims/task-5.yaml") {
		t.Fatalf("refused release removed the claim: %q", tree)
	}

	outcome, err := p.Release(context.Background(), "task-5", "alice", "laptop", true)
	if err != nil {
		t.Fatalf("Release --force on an unreadable claim: %v", err)
	}
	if outcome.Kind != remotestate.Released {
		t.Fatalf("Kind = %v, want released", outcome.Kind)
	}
	if tree := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main"); strings.Contains(tree, "claims/task-5.yaml") {
		t.Fatalf("forced release left the claim: %q", tree)
	}
}

// TestDQCovClaimReportsWriteFailure proves a forced claim refresh over a
// read-only claim file reports the write error instead of claiming success.
func TestDQCovClaimReportsWriteFailure(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	at := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(p.opts.ClonePath, filepath.FromSlash(ClaimPath("task-7")))
	if err := os.Chmod(abs, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(abs, 0o644) })

	_, err := p.Claim(context.Background(), mkClaim("bob", "vm", "task-7", at.Add(time.Hour)), remotestate.ClaimForce, "")
	if err == nil {
		t.Fatal("forced claim over a read-only file succeeded, want a write error")
	}
}

// TestDQCovReleaseReportsRemoveFailure proves a release that cannot unlink the
// claim file surfaces the removal error (as opposed to treating it as absent).
func TestDQCovReleaseReportsRemoveFailure(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	at := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}
	dqCovChmodDir(t, filepath.Join(p.opts.ClonePath, "claims"), 0o555)

	_, err := p.Release(context.Background(), "task-7", "alice", "laptop", false)
	if err == nil {
		t.Fatal("Release with an unwritable claims directory succeeded, want a removal error")
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("Release error = %v, want a permission error from unlinking", err)
	}
}

func TestDQCovClaimsEmptyWhenClaimsDirectoryMissing(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(p.opts.ClonePath, "claims")); err != nil {
		t.Fatal(err)
	}

	entries, err := p.Claims(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %+v, want none for a store with no claims directory", entries)
	}
}

func TestDQCovClaimsReportsDirectoryEntry(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	dqCovPushFileToRef(t, origin, "claims/nested/keep.txt", []byte("x\n"), "refs/heads/main", "nested claims dir")

	entries, err := p.Claims(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want one directory entry", entries)
	}
	if entries[0].Claim.Task != "nested" || !strings.Contains(entries[0].Error, "is a directory") {
		t.Fatalf("entry = %+v, want Task=nested and a directory error", entries[0])
	}
}

func TestDQCovClaimsReportsNonYAMLFile(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	dqCovPushFileToRef(t, origin, "claims/notes.txt", []byte("x\n"), "refs/heads/main", "stray claims file")

	entries, err := p.Claims(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want one error entry", entries)
	}
	if entries[0].Claim.Task != "notes.txt" || !strings.Contains(entries[0].Error, "unexpected file") {
		t.Fatalf("entry = %+v, want Task=notes.txt and an unexpected-file error", entries[0])
	}
}

func TestDQCovClaimsReportsUnreadableClaimFile(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	claimsDir := filepath.Join(p.opts.ClonePath, "claims")
	if err := os.MkdirAll(claimsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dqCovSymlink(t, filepath.Join(t.TempDir(), "gone.yaml"), filepath.Join(claimsDir, "task-3.yaml"))

	entries, err := p.Claims(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want one error entry", entries)
	}
	if entries[0].Claim.Task != "task-3" || entries[0].Error == "" {
		t.Fatalf("entry = %+v, want Task=task-3 and a read error", entries[0])
	}
}

func TestDQCovClaimsReportsClaimsPathReadFailure(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.opts.ClonePath, "claims"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := p.Claims(context.Background()); err == nil {
		t.Fatal("Claims with a file where the claims directory belongs succeeded, want an error")
	}
}

func TestDQCovMutateStoreReportsMutateError(t *testing.T) {
	sentinel := errors.New("dqCov mutate failed")
	p := New(Options{ClonePath: filepath.Join(t.TempDir(), "p", "wb-state"), CloneURL: "file:///nowhere"})

	sha, err := p.mutateStore("claims/task-1.yaml", func() (string, bool, []string, error) {
		return "", false, nil, sentinel
	}, func() error { return nil })
	if !errors.Is(err, sentinel) {
		t.Fatalf("mutateStore error = %v, want the mutate error", err)
	}
	if sha != "" {
		t.Fatalf("sha = %q, want empty on error", sha)
	}
}

func TestDQCovMutateStoreReportsAddCommitError(t *testing.T) {
	origin := dqCovEmptyBareOrigin(t)
	p := machine(t, origin)
	dqCovPreClone(t, origin, p.opts.ClonePath)
	if err := os.WriteFile(filepath.Join(p.opts.ClonePath, ".git", "index.lock"), []byte("locked"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := p.mutateStore("claims/task-1.yaml", func() (string, bool, []string, error) {
		abs := filepath.Join(p.opts.ClonePath, filepath.FromSlash(ClaimPath("task-1")))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return "", false, nil, err
		}
		if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
			return "", false, nil, err
		}
		return "wb: test", true, nil, nil
	}, func() error { return nil })
	if err == nil {
		t.Fatal("mutateStore with a stale index.lock succeeded, want an error")
	}
}

// TestDQCovMutateStoreSkipsCommitWhenNothingStaged proves a mutation that
// reports changed=true but stages no diff returns the current HEAD without
// making a commit or pushing.
func TestDQCovMutateStoreSkipsCommitWhenNothingStaged(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	at := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}
	before := gitIn(t, origin, "rev-parse", "main")
	want := gitIn(t, p.opts.ClonePath, "rev-parse", "HEAD")

	sha, err := p.mutateStore(ClaimPath("task-7"), func() (string, bool, []string, error) {
		return "wb: no-op", true, nil, nil
	}, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if sha != want {
		t.Fatalf("sha = %q, want current HEAD %q", sha, want)
	}
	if after := gitIn(t, origin, "rev-parse", "main"); after != before {
		t.Fatalf("origin main moved from %s to %s; an empty stage must not commit", before, after)
	}
}

// TestDQCovMutateStoreReportsRebaseFailureWithoutRebase proves a rejected push
// whose rebase fails before starting (an empty store with no branch to fetch)
// is reported as a plain rebase failure.
func TestDQCovMutateStoreReportsRebaseFailureWithoutRebase(t *testing.T) {
	origin := dqCovEmptyBareOrigin(t)
	p := machine(t, origin)
	dqCovPreClone(t, origin, p.opts.ClonePath)
	installRejectAlwaysPushHook(t, origin)

	_, err := p.mutateStore(ClaimPath("task-1"), func() (string, bool, []string, error) {
		abs := filepath.Join(p.opts.ClonePath, filepath.FromSlash(ClaimPath("task-1")))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return "", false, nil, err
		}
		if err := os.WriteFile(abs, []byte("schema_version: 1\n"), 0o644); err != nil {
			return "", false, nil, err
		}
		return "wb: claim task-1", true, nil, nil
	}, func() error { return nil })
	if err == nil || !strings.Contains(err.Error(), "push rejected and rebase failed") {
		t.Fatalf("mutateStore = %v, want 'push rejected and rebase failed'", err)
	}
}

// TestDQCovMutateStoreReportsResetFailureAfterConflict proves that when a
// conflicted rebase is followed by a failing reset to upstream, mutateStore
// reports both failures rather than masking the conflict.
func TestDQCovMutateStoreReportsResetFailureAfterConflict(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	at := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", at)); err != nil {
		t.Fatal(err)
	}
	dqCovPushFileToRef(t, origin, "shared.txt", []byte("theirs\n"), "refs/staging/compete", "competing change")
	competeSHA := gitIn(t, origin, "rev-parse", "refs/staging/compete")
	dqCovInstallClientMutatingRejectHook(t, origin, competeSHA, p.opts.ClonePath)

	lostRaceCalled := false
	_, err := p.mutateStore(ClaimPath("task-1"), func() (string, bool, []string, error) {
		shared := filepath.Join(p.opts.ClonePath, "shared.txt")
		if writeErr := os.WriteFile(shared, []byte("ours\n"), 0o644); writeErr != nil {
			return "", false, nil, writeErr
		}
		abs := filepath.Join(p.opts.ClonePath, filepath.FromSlash(ClaimPath("task-1")))
		if mkErr := os.MkdirAll(filepath.Dir(abs), 0o755); mkErr != nil {
			return "", false, nil, mkErr
		}
		if writeErr := os.WriteFile(abs, []byte("schema_version: 1\ntask: task-1\n"), 0o644); writeErr != nil {
			return "", false, nil, writeErr
		}
		return "wb: claim task-1", true, []string{"shared.txt"}, nil
	}, func() error {
		lostRaceCalled = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "reset to upstream also failed") {
		t.Fatalf("mutateStore = %v, want the reset failure", err)
	}
	if lostRaceCalled {
		t.Fatal("onLostRace ran even though the reset to upstream failed")
	}
}

// TestDQCovMutateStoreReportsSecondRejectionResetFailure proves a second push
// rejection followed by a failing reset reports both, rather than reporting a
// clean discard.
func TestDQCovMutateStoreReportsSecondRejectionResetFailure(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	// Unset the upstream so the final reset to @{u} cannot resolve, while the
	// pushes themselves still reach origin.
	for _, key := range []string{"branch.main.remote", "branch.main.merge"} {
		gitIn(t, p.opts.ClonePath, "config", "--unset", key)
	}
	installRejectAlwaysPushHook(t, origin)

	_, err := p.mutateStore(ClaimPath("task-1"), func() (string, bool, []string, error) {
		abs := filepath.Join(p.opts.ClonePath, filepath.FromSlash(ClaimPath("task-1")))
		if mkErr := os.MkdirAll(filepath.Dir(abs), 0o755); mkErr != nil {
			return "", false, nil, mkErr
		}
		if writeErr := os.WriteFile(abs, []byte("schema_version: 1\ntask: task-1\n"), 0o644); writeErr != nil {
			return "", false, nil, writeErr
		}
		return "wb: claim task-1", true, nil, nil
	}, func() error { return nil })
	if err == nil || !strings.Contains(err.Error(), "push rejected twice") || !strings.Contains(err.Error(), "reset to upstream also failed") {
		t.Fatalf("mutateStore = %v, want both the double rejection and the reset failure", err)
	}
}

// TestDQCovClaimReportsUnreadableAfterLostRace proves that losing the race to
// a malformed competing claim is reported as unreadable (with the --force
// hint) rather than as a phantom holder.
func TestDQCovClaimReportsUnreadableAfterLostRace(t *testing.T) {
	origin := bareOrigin(t)
	dqCovPushFileToRef(t, origin, ClaimPath("task-7"), []byte("schema_version: 99\n"), "refs/staging/bad", "malformed competing claim")
	badSHA := gitIn(t, origin, "rev-parse", "refs/staging/bad")
	installRejectFirstPushHook(t, origin, badSHA)

	p := machine(t, origin)
	_, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", time.Now().UTC()), remotestate.ClaimNormal, "")
	if err == nil || !strings.Contains(err.Error(), "unreadable after losing the race") {
		t.Fatalf("Claim = %v, want 'unreadable after losing the race'", err)
	}
}

// TestDQCovClaimReportsReadErrorAfterLostRace proves that when the competing
// claim that wins the race is itself unreadable at the filesystem level (a
// symlink to a directory), the lost-race path reports the read failure.
func TestDQCovClaimReportsReadErrorAfterLostRace(t *testing.T) {
	origin := bareOrigin(t)
	dqCovPushSymlinkToRef(t, origin, ClaimPath("task-7"), ".", "refs/staging/link", "symlink claim")
	linkSHA := gitIn(t, origin, "rev-parse", "refs/staging/link")
	installRejectFirstPushHook(t, origin, linkSHA)

	p := machine(t, origin)
	_, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", time.Now().UTC()), remotestate.ClaimNormal, "")
	if err == nil || !strings.Contains(err.Error(), "lost the race for task-7") {
		t.Fatalf("Claim = %v, want a lost-race error carrying the read failure", err)
	}
	if strings.Contains(err.Error(), "to /") {
		t.Fatalf("Claim = %v, want no phantom holder name", err)
	}
}

// TestDQCovReleaseReportsLostRaceToHolder proves losing a release race to a
// readable competing claim names the winner instead of deleting it.
func TestDQCovReleaseReportsLostRaceToHolder(t *testing.T) {
	origin := bareOrigin(t)
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	p := machine(t, origin)
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}
	pushClaimToRef(t, origin, mkClaim("bob", "vm", "task-7", at.Add(time.Minute)), "refs/staging/bob")
	bobSHA := gitIn(t, origin, "rev-parse", "refs/staging/bob")
	installRejectFirstPushHook(t, origin, bobSHA)

	_, err := p.Release(context.Background(), "task-7", "alice", "laptop", false)
	if err == nil || !strings.Contains(err.Error(), "lost the race for task-7 to bob/vm") {
		t.Fatalf("Release = %v, want 'lost the race for task-7 to bob/vm'", err)
	}
	if data, ok, _ := gitops.ShowFile(origin, "main", ClaimPath("task-7")); !ok || !strings.Contains(data, "bob") {
		t.Fatalf("origin claim = %q (ok=%v), want bob's claim preserved", data, ok)
	}
}

// TestDQCovReleaseReportsUnreadableAfterLostRace proves a forced release that
// loses the race to a malformed claim reports the unreadable winner.
func TestDQCovReleaseReportsUnreadableAfterLostRace(t *testing.T) {
	origin := bareOrigin(t)
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	p := machine(t, origin)
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}
	dqCovPushFileToRef(t, origin, ClaimPath("task-7"), []byte("schema_version: 99\n"), "refs/staging/bad", "malformed competing claim")
	badSHA := gitIn(t, origin, "rev-parse", "refs/staging/bad")
	installRejectFirstPushHook(t, origin, badSHA)

	_, err := p.Release(context.Background(), "task-7", "alice", "laptop", false)
	if err == nil || !strings.Contains(err.Error(), "unreadable after losing the race") {
		t.Fatalf("Release = %v, want 'unreadable after losing the race'", err)
	}
}

// TestDQCovReleaseRetriesWhenStorePathFreed proves the bounded retry after a
// release loses its race to a competing release (path freed): a forced release
// whose conflict leaves the path gone retries once, and a second such loss
// reports the store as changing too fast.
func TestDQCovReleaseRetriesWhenStorePathFreed(t *testing.T) {
	origin := bareOrigin(t)
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	p := machine(t, origin)
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}
	c1, c2 := dqCovPushMalformedClaimChain(t, origin, "task-7")
	dqCovInstallRejectTwiceHook(t, origin, c1, c2)

	_, err := p.Release(context.Background(), "task-7", "alice", "laptop", true)
	if err == nil || !strings.Contains(err.Error(), "store is changing too fast for task-7") {
		t.Fatalf("Release = %v, want 'store is changing too fast for task-7'", err)
	}
}

// TestDQCovClaimReportsMkdirFailureForBrokenClaimsLink proves that when the
// claims directory itself is a dangling link (so the claim reads as absent but
// cannot be created under), the claim reports the directory-creation failure.
func TestDQCovClaimReportsMkdirFailureForBrokenClaimsLink(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(p.opts.ClonePath, "claims")); err != nil {
		t.Fatal(err)
	}
	dqCovSymlink(t, filepath.Join(t.TempDir(), "gone-claims"), filepath.Join(p.opts.ClonePath, "claims"))

	_, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", time.Now().UTC()), remotestate.ClaimNormal, "")
	if err == nil {
		t.Fatal("Claim with an uncreatable claims directory succeeded, want an error")
	}
	if tree := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main"); strings.Contains(tree, "claims/task-7.yaml") {
		t.Fatalf("failed claim landed anyway: %q", tree)
	}
}

// TestDQCovClaimLeavesSnapshotUnchangedWhenStampWriteFails proves the
// best-effort last-seen stamp never fails the claim: with the claimant's own
// snapshot read-only, the claim still lands and the snapshot keeps its bytes.
func TestDQCovClaimLeavesSnapshotUnchangedWhenStampWriteFails(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	published := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", published)); err != nil {
		t.Fatal(err)
	}
	snapshotAbs := filepath.Join(p.opts.ClonePath, filepath.FromSlash(SnapshotPath("alice", "laptop")))
	if err := os.Chmod(snapshotAbs, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(snapshotAbs, 0o644) })
	before := gitIn(t, origin, "rev-parse", "main")

	claimedAt := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	outcome, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", claimedAt), remotestate.ClaimNormal, "")
	if err != nil {
		t.Fatalf("Claim with an unstampable snapshot: %v", err)
	}
	if outcome.Kind != remotestate.ClaimAcquired {
		t.Fatalf("Kind = %v, want acquired", outcome.Kind)
	}
	if n := gitIn(t, origin, "rev-list", "--count", before+"..main"); n != "1" {
		t.Fatalf("commits after claim = %s, want 1 (claim only; no snapshot stamp)", n)
	}
	data := gitIn(t, origin, "show", "main:"+SnapshotPath("alice", "laptop"))
	got, err := remotestate.Decode([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if !got.LastSeenAt.IsZero() {
		t.Fatalf("LastSeenAt = %v, want it untouched when the stamp write fails", got.LastSeenAt)
	}
	if !got.PublishedAt.Equal(published) {
		t.Fatalf("PublishedAt = %v, want unchanged %v", got.PublishedAt, published)
	}
}

// TestDQCovReleaseReportsReadErrorAfterLostRace proves a release that loses to
// a competing claim which is unreadable at the filesystem level reports the
// read failure on the lost-race path.
func TestDQCovReleaseReportsReadErrorAfterLostRace(t *testing.T) {
	origin := bareOrigin(t)
	at := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	p := machine(t, origin)
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}
	dqCovPushSymlinkReplacingToRef(t, origin, ClaimPath("task-7"), ".", "refs/staging/link", "symlink claim")
	linkSHA := gitIn(t, origin, "rev-parse", "refs/staging/link")
	installRejectFirstPushHook(t, origin, linkSHA)

	_, err := p.Release(context.Background(), "task-7", "alice", "laptop", false)
	if err == nil || !strings.Contains(err.Error(), "lost the race for task-7") {
		t.Fatalf("Release = %v, want a lost-race error carrying the read failure", err)
	}
}

// TestDQCovClaimRetriesWhenStoreForceOverwritable proves the bounded retry for
// a forced claim that keeps losing to unreadable competing claims reports the
// store as changing too fast on the second loss.
func TestDQCovClaimRetriesWhenStoreForceOverwritable(t *testing.T) {
	origin := bareOrigin(t)
	c1, c2 := dqCovPushMalformedClaimChain(t, origin, "task-7")
	dqCovInstallRejectTwiceHook(t, origin, c1, c2)

	p := machine(t, origin)
	_, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", time.Now().UTC()), remotestate.ClaimForce, "")
	if err == nil || !strings.Contains(err.Error(), "store is changing too fast for task-7") {
		t.Fatalf("Claim = %v, want 'store is changing too fast for task-7'", err)
	}
}
