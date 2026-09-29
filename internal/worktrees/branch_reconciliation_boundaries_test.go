package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

// cancellationAwareReconciliationRunner makes a dropped context observable
// without starting Git. A canned cancellation error would pass even if a
// caller accidentally replaced the canceled context with Background.
type cancellationAwareReconciliationRunner struct{ runner.Runner }

func (r cancellationAwareReconciliationRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if err := ctx.Err(); err != nil {
		return runner.Result{}, err
	}
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}

func reconciliationFakeCanonical(t *testing.T) *canonicalRepository {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, err := openCanonicalRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(canonical.close)
	return canonical
}

func reconciliationRecordForClaim(claim workLogClaim) branchReconciliationRecord {
	return branchReconciliationRecord{
		Version: 1, EventID: "event-1", ClaimID: claim.ClaimID,
		Worktree: claim.Worktree, Repository: claim.Repository,
		ClaimBranch: claim.Branch, LiveBranch: "wb/live",
		ExpectedHead: strings.Repeat("a", 40), LocalHead: strings.Repeat("b", 40),
		RemoteHead: strings.Repeat("c", 40), TargetHead: strings.Repeat("d", 40),
		Actor: "operator", Reason: "restore branch identity",
		Stage: reconciliationStagePlanned, CreatedAt: time.Now().UTC(),
	}
}

func TestBranchReconciliationRecordCreateAndReplayUseSamePrivateDirectory(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	claim, _ := wtLogCovReconciliationClaim(t.TempDir())
	record := reconciliationRecordForClaim(claim)
	created, err := createBranchReconciliationRecord(home, claim, record)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = created.Close() })
	got, replay, err := readBranchReconciliationRecord(home, claim, record.EventID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = replay.Close() })
	createdInfo, err := created.Stat()
	if err != nil {
		t.Fatal(err)
	}
	replayInfo, err := replay.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(createdInfo, replayInfo) || got.ClaimID != record.ClaimID || got.Stage != record.Stage {
		t.Fatalf("replayed record %#v from a different private directory", got)
	}
	if err := corroborateReconciliationRecord(got, claim, claim.Worktree, LogRecoverOptions{
		EventID: record.EventID, ReconcileBranch: record.LiveBranch, ExpectedHead: record.ExpectedHead,
		Actor: record.Actor, Reason: record.Reason,
	}); err != nil {
		t.Fatalf("replayed record lost immutable identity: %v", err)
	}
	_, projection := wtLogCovReconciliationClaim(claim.Worktree)
	local := localProjectionForReconciliation(projection)
	gitQueries := runnertest.New(t)
	gitQueries.ExpectArgv([]string{"git", "-C", claim.Worktree, "branch", "--show-current"},
		runner.Result{CombinedOutput: record.LiveBranch + "\n"}, nil)
	gitQueries.ExpectArgv([]string{"git", "-C", claim.Worktree, "rev-parse", "HEAD"},
		runner.Result{CombinedOutput: record.ExpectedHead + "\n"}, nil)
	gitQueries.ExpectArgv([]string{"git", "-C", claim.Worktree, "status", "--porcelain"},
		runner.Result{}, nil)
	event := reconciliationEvent(withGitRunner(context.Background(), gitQueries), claim.Worktree, got)
	if gitQueries.CallCount() != 3 || event.Git == nil || event.Git.Branch != record.LiveBranch || event.Git.Head != record.ExpectedHead {
		t.Fatalf("replayed event did not retain fake-observed Git evidence: %#v", event.Git)
	}
	completed := completedBranchReconciliationResult(claim.Worktree, event, *local)
	if !completed.ReadyForNormalCleanup || completed.Event == nil || completed.Event.ID != record.EventID ||
		completed.Projection == nil || completed.Projection.ClaimID != claim.ClaimID {
		t.Fatalf("replayed record produced incomplete cleanup receipt: %#v", completed)
	}
}

func TestBranchReconciliationDirectoryRejectsBrokenPrivatePath(t *testing.T) {
	t.Parallel()

	claim, _ := wtLogCovReconciliationClaim(t.TempDir())
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, home string)
	}{
		{name: "home is a file", setup: func(t *testing.T, home string) {
			if err := os.WriteFile(home, []byte("not a directory"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "reconciliation collection is a file", setup: func(t *testing.T, home string) {
			run, runPath, err := openWorkLogRun(home, claim.EffortID, claim.RunID, true)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = run.Close() }()
			if err := os.WriteFile(filepath.Join(runPath, "branch-reconciliations"), []byte("not a directory"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "event is a file", setup: func(t *testing.T, home string) {
			run, runPath, err := openWorkLogRun(home, claim.EffortID, claim.RunID, true)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = run.Close() }()
			collection, err := openPrivateChild(run, "branch-reconciliations", true)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = collection.Close() }()
			if err := os.WriteFile(filepath.Join(runPath, "branch-reconciliations", "event-1"), []byte("not a directory"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			home := filepath.Join(t.TempDir(), "home")
			tc.setup(t, home)
			if directory, err := openBranchReconciliationEvent(home, claim, "event-1", true); err == nil {
				_ = directory.Close()
				t.Fatal("broken private path was opened for creation")
			}
		})
	}
}

func TestBranchReconciliationRecordWritersRejectFailedStorage(t *testing.T) {
	t.Parallel()

	claim, _ := wtLogCovReconciliationClaim(t.TempDir())
	record := reconciliationRecordForClaim(claim)
	homeFile := filepath.Join(t.TempDir(), "home-file")
	if err := os.WriteFile(homeFile, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if directory, err := createBranchReconciliationRecord(homeFile, claim, record); err == nil {
		_ = directory.Close()
		t.Fatal("record creation accepted a file as its home")
	}

	home := t.TempDir()
	directory, err := openBranchReconciliationEvent(home, claim, record.EventID, true)
	if err != nil {
		t.Fatal(err)
	}
	run, runPath, err := openWorkLogRun(home, claim.EffortID, claim.RunID, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = run.Close()
	if err := os.Mkdir(filepath.Join(runPath, "branch-reconciliations", record.EventID, branchReconciliationRecordName), 0o700); err != nil {
		t.Fatal(err)
	}
	_ = directory.Close()
	if created, err := createBranchReconciliationRecord(home, claim, record); err == nil {
		_ = created.Close()
		t.Fatal("record creation replaced a directory at the record path")
	}
	if _, opened, err := readBranchReconciliationRecord(home, claim, record.EventID); err == nil {
		_ = opened.Close()
		t.Fatal("record replay accepted a directory as JSON")
	}

	closed, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	if _, err := finishBranchReconciliation(closed, record, claim.Worktree, LocalWorkLogEvent{}, LocalWorkLogProjection{}); err == nil {
		t.Fatal("completion reported success without writing its terminal stage")
	}
}

func TestBranchReconciliationClaimReaderRejectsMissingAndAlteredClaims(t *testing.T) {
	t.Parallel()

	worktree := t.TempDir()
	claim, projection := wtLogCovReconciliationClaim(worktree)
	projectionDir, err := openWorkLogProjectionDirectory(worktree, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomicAt(projectionDir, workLogProjectionName, projection, 0o600); err != nil {
		_ = projectionDir.Close()
		t.Fatal(err)
	}
	if err := projectionDir.Close(); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if _, _, err := reconciliationClaim(home, worktree); err == nil {
		t.Fatal("claim read succeeded without a private run")
	}
	run, _, err := openWorkLogRun(home, claim.EffortID, claim.RunID, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := reconciliationClaim(home, worktree); err == nil {
		t.Fatal("claim read succeeded without immutable claim JSON")
	}
	claims, err := openPrivateChild(run, "claims", true)
	if err != nil {
		t.Fatal(err)
	}
	altered := claim
	altered.Branch = "wb/other"
	if err := writeJSONImmutableAt(claims, claim.ClaimID+".json", altered, false); err != nil {
		t.Fatal(err)
	}
	_ = claims.Close()
	_ = run.Close()
	if _, _, err := reconciliationClaim(home, worktree); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("altered immutable claim read error = %v", err)
	}
	if err := corroborateReconciliationClaimShape(worktree, projection, altered); err == nil {
		t.Fatal("altered claim passed direct identity validation")
	}
}

func TestBranchReconciliationGuardsPropagateCancelledGitQueries(t *testing.T) {
	t.Parallel()

	canonical := reconciliationFakeCanonical(t)
	head := strings.Repeat("a", 40)
	ref := "refs/heads/wb/claim"
	remote := runnertest.New(t)
	remoteQuery := []string{"git", "-C", canonical.path, "ls-remote", "--heads", "origin", ref}
	remote.ExpectArgv(remoteQuery, runner.Result{CombinedOutput: head + "\t" + ref + "\n"}, nil)
	// If cancellation stops propagating, the fake still returns a present ref,
	// which cannot satisfy the cancellation assertion below.
	remote.ExpectArgv(remoteQuery, runner.Result{CombinedOutput: head + "\t" + ref + "\n"}, nil)
	remote.ExpectArgv(remoteQuery, runner.Result{CombinedOutput: head + "\t" + ref + "\n"}, nil)
	remoteCtx := withGitRunner(context.Background(), cancellationAwareReconciliationRunner{remote})
	localCtx := withCanonicalGitInterceptor(context.Background(), func(ctx context.Context, args []string, _ func() ([]byte, error)) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(args) == 2 && args[0] == "rev-parse" && args[1] == ref {
			return []byte(head + "\n"), nil
		}
		if len(args) == 3 && args[0] == "for-each-ref" && args[2] == ref {
			return []byte(head + "\n"), nil
		}
		return nil, errors.New("unexpected canonical Git query")
	})
	if err := requireRemoteClaimHead(remoteCtx, canonical.path, "wb/claim", head); err != nil {
		t.Fatalf("fake remote claim ref was not readable: %v", err)
	}
	if err := requireLocalClaimHead(localCtx, canonical, "wb/claim", head); err != nil {
		t.Fatalf("fake local claim ref was not readable: %v", err)
	}
	remoteCanceled, cancel := context.WithCancel(remoteCtx)
	cancel()
	localCanceled, cancelLocal := context.WithCancel(localCtx)
	cancelLocal()
	for name, check := range map[string]func() error{
		"remote head":   func() error { return requireRemoteClaimHead(remoteCanceled, canonical.path, "wb/claim", head) },
		"remote absent": func() error { return requireRemoteClaimAbsent(remoteCanceled, canonical.path, "wb/claim") },
		"local head":    func() error { return requireLocalClaimHead(localCanceled, canonical, "wb/claim", head) },
		"local absent":  func() error { return requireLocalClaimAbsent(localCanceled, canonical, "wb/claim") },
	} {
		if err := check(); err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
			t.Errorf("%s returned %v, want context cancellation", name, err)
		}
	}
}

func TestBranchReconciliationRejectsUnrelatedBaseWithFakeGit(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	base, head := strings.Repeat("a", 40), strings.Repeat("b", 40)
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "-C", repository, "merge-base", "--is-ancestor", base, head},
		runner.Result{ExitCode: 1}, errors.New("exit status 1"))
	claim, _ := wtLogCovReconciliationClaim(repository)
	claim.BaseSHA = base
	entry := ListResult{CanonicalDir: repository, WorktreeDir: repository,
		Branch: "wb/live", HeadSHA: head, Clean: true, IntegratedAtOrigin: true,
		RemoteTargetSHA: head, MergedPullRequest: &PullRequest{Number: 1}}
	options := LogRecoverOptions{ExpectedHead: head}
	if err := validateReconciliationLifecycleEvidence(withGitRunner(context.Background(), fake), entry, claim, options); err == nil ||
		!strings.Contains(err.Error(), "not descended") {
		t.Fatalf("unrelated root validation = %v", err)
	}
	if fake.CallCount() != 1 {
		t.Fatalf("ancestry guard made %d fake Git calls, want one exact merge-base query", fake.CallCount())
	}
}

func TestBranchReconciliationRejectsInvalidInputBeforeReadingGit(t *testing.T) {
	t.Parallel()

	if _, err := reconcileClaimBranch(context.Background(), LogRecoverOptions{}); err == nil {
		t.Fatal("reconciliation accepted an empty request")
	}
	claim, _ := wtLogCovReconciliationClaim(t.TempDir())
	if _, err := reconciliationLifecycleEvidence(context.Background(), t.TempDir(), t.TempDir(), claim); err == nil {
		t.Fatal("unmanaged worktree had lifecycle evidence")
	}
	if err := revalidateReconciliationStage(context.Background(), LogRecoverOptions{ProjectsRoot: t.TempDir()},
		t.TempDir(), claim, reconciliationRecordForClaim(claim)); err == nil {
		t.Fatal("unmanaged worktree passed stage revalidation")
	}
}

func TestBranchReconciliationBundleAdvertisementRejectsUnknownRef(t *testing.T) {
	t.Parallel()

	canonical := reconciliationFakeCanonical(t)
	wantRef := "refs/heads/wb/missing"
	otherRef := "refs/heads/wb/other"
	bundlePath := filepath.Join(t.TempDir(), "missing.bundle")
	calls := 0
	ctx := withCanonicalGitInterceptor(context.Background(), func(_ context.Context, args []string, _ func() ([]byte, error)) ([]byte, error) {
		calls++
		if len(args) != 3 || args[0] != "bundle" || args[1] != "list-heads" || args[2] != bundlePath {
			return nil, errors.New("unexpected canonical bundle query")
		}
		return []byte(strings.Repeat("b", 40) + " " + otherRef + "\n"), nil
	})
	if err := requireBundleAdvertisesClaimRef(ctx, canonical,
		bundlePath, wantRef, strings.Repeat("a", 40)); err == nil ||
		!strings.Contains(err.Error(), "does not advertise expected ref") {
		t.Fatalf("unrelated bundle advertisement = %v", err)
	}
	if calls != 1 {
		t.Fatalf("bundle advertisement made %d canonical Git queries, want one list-heads", calls)
	}
}
