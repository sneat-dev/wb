package gitrepo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/remotestate"
)

// TestAcquireCloneLockSerializesConcurrentHolders proves acquireCloneLock
// actually blocks a second acquirer until the first releases — the primary
// wb#321 fix. Without this, two WB processes sharing one wb-state clone can
// each run git commands against the same working directory at once, which
// is exactly how the issue's "cannot rebase: Your index contains
// uncommitted changes" and "cannot lock ref ... but expected ..." failures
// happened.
func TestAcquireCloneLockSerializesConcurrentHolders(t *testing.T) {
	clonePath := filepath.Join(t.TempDir(), "team", "wb-state")

	first, err := acquireCloneLock(clonePath)
	if err != nil {
		t.Fatal(err)
	}

	acquired := make(chan struct{})
	go func() {
		second, err := acquireCloneLock(clonePath)
		if err != nil {
			t.Errorf("second acquireCloneLock: %v", err)
			return
		}
		close(acquired)
		_ = second.release()
	}()

	select {
	case <-acquired:
		t.Fatal("second acquireCloneLock returned before the first lock was released")
	case <-time.After(150 * time.Millisecond):
	}

	if err := first.release(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("second acquireCloneLock never returned after the first released")
	}
}

// TestFetchRetriesTransientFailureThenSucceeds proves Fetch recovers from a
// dirty working tree that reproduces one of wb#321's two observed failures:
// `git pull --rebase` refuses because of an uncommitted local change. In
// production cloneLock is what stops another WB process from causing this
// in the first place; this test exercises the retry as the remaining
// defense in depth, for whatever transient condition reaches Fetch anyway.
// onFetchRetry fires deterministically between the failed first attempt and
// the retry — restoring the tracked file there, rather than racing a timer
// against a background goroutine, is what keeps this test non-flaky.
func TestFetchRetriesTransientFailureThenSucceeds(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}

	// A second clone changes the one file every clone shares and pushes, so
	// there is something for Fetch to rebase onto.
	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, t.TempDir(), "clone", "-q", origin, other)
	if err := os.WriteFile(filepath.Join(other, "README.md"), []byte("# remote change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, other, "commit", "-q", "-am", "remote readme change")
	gitIn(t, other, "push", "-q", "origin", "main")

	readmePath := filepath.Join(p.opts.ClonePath, "README.md")
	original, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatal(err)
	}
	// Dirty the tracked file without committing: the first PullRebase
	// attempt refuses immediately, before starting a rebase (matching
	// TestFetchReportsRealCauseWhenNoRebaseInProgress).
	if err := os.WriteFile(readmePath, []byte("dirty, uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fired := 0
	restoreHook := onFetchRetry
	onFetchRetry = func() {
		fired++
		if err := os.WriteFile(readmePath, original, 0o644); err != nil {
			t.Errorf("restore tracked file between retries: %v", err)
		}
	}
	t.Cleanup(func() { onFetchRetry = restoreHook })
	restoreBackoff := fetchRetryBackoff
	fetchRetryBackoff = time.Millisecond
	t.Cleanup(func() { fetchRetryBackoff = restoreBackoff })

	if err := p.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch did not recover from a transient dirty index: %v", err)
	}
	if fired != 1 {
		t.Fatalf("onFetchRetry fired %d times, want exactly 1 (one failed attempt before the retry that succeeded)", fired)
	}
}

// TestFetchExhaustsRetriesOnPersistentFailure proves Fetch does not retry
// forever: a dirty tracked file that never clears (a genuinely broken
// clone, not a transient race) must still surface as an error once
// fetchRetryAttempts is spent, and onFetchRetry must fire exactly
// fetchRetryAttempts-1 times — once before every retry, never after the
// final, non-retried attempt.
func TestFetchExhaustsRetriesOnPersistentFailure(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, t.TempDir(), "clone", "-q", origin, other)
	if err := os.WriteFile(filepath.Join(other, "README.md"), []byte("# remote change\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, other, "commit", "-q", "-am", "remote readme change")
	gitIn(t, other, "push", "-q", "origin", "main")

	if err := os.WriteFile(filepath.Join(p.opts.ClonePath, "README.md"), []byte("dirty, uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	restoreBackoff := fetchRetryBackoff
	fetchRetryBackoff = time.Millisecond
	t.Cleanup(func() { fetchRetryBackoff = restoreBackoff })
	fired := 0
	restoreHook := onFetchRetry
	onFetchRetry = func() { fired++ }
	t.Cleanup(func() { onFetchRetry = restoreHook })

	err := p.Fetch(context.Background())
	if err == nil {
		t.Fatal("Fetch should fail: the dirty tracked file never clears")
	}
	if !strings.Contains(err.Error(), "pull --rebase failed") {
		t.Fatalf("Fetch err = %v, want a 'pull --rebase failed' message", err)
	}
	if fired != fetchRetryAttempts-1 {
		t.Fatalf("onFetchRetry fired %d times, want %d (once before each retry, never after the last attempt)", fired, fetchRetryAttempts-1)
	}
}

// TestConcurrentReleasesDoNotCorruptClone is wb#321's repro shape: several
// WB processes (here, independent *Provider values built the same way
// openRemote builds one per invocation) sharing one wb-state clone
// directory, each releasing a different task's claim at the same time.
// Without cloneLock this reliably corrupts the shared clone or drops
// releases; with it, every release must succeed and the store must end up
// exactly empty, with the clone left in a clean, non-mid-rebase state.
func TestConcurrentReleasesDoNotCorruptClone(t *testing.T) {
	origin := bareOrigin(t)
	clonePath := filepath.Join(t.TempDir(), "projects", "team", "wb-state")
	const n = 8

	seed := New(Options{ClonePath: clonePath, CloneURL: origin})
	at := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		claim := remotestate.Claim{
			SchemaVersion: remotestate.ClaimSchemaVersion,
			Task:          fmt.Sprintf("task-%d", i),
			Login:         "alice",
			Machine:       "laptop",
			ClaimedAt:     at,
		}
		if _, err := seed.Claim(context.Background(), claim, remotestate.ClaimNormal, ""); err != nil {
			t.Fatalf("seed claim task-%d: %v", i, err)
		}
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// A fresh Provider per goroutine, exactly like a fresh WB
			// process would build one, so this exercises independent
			// clone-lock acquisition against the same clonePath rather
			// than one Provider's in-process state.
			p := New(Options{ClonePath: clonePath, CloneURL: origin})
			_, err := p.Release(context.Background(), fmt.Sprintf("task-%d", i), "alice", "laptop", false)
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent release of task-%d: %v", i, err)
		}
	}

	entries, err := seed.Claims(context.Background())
	if err != nil {
		t.Fatalf("Claims after concurrent releases: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("claims remaining after concurrent releases = %v, want none", entries)
	}

	inProgress, err := gitops.RebaseInProgress(clonePath)
	if err != nil {
		t.Fatalf("check rebase state: %v", err)
	}
	if inProgress {
		t.Fatal("clone left mid-rebase after concurrent releases")
	}
}
