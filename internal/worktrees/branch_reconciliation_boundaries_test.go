package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
	home := t.TempDir()
	claim, _ := wtLogCovReconciliationClaim(t.TempDir())
	record := reconciliationRecordForClaim(claim)
	created, err := createBranchReconciliationRecord(home, claim, record)
	if err != nil {
		t.Fatal(err)
	}
	defer created.Close()
	got, replay, err := readBranchReconciliationRecord(home, claim, record.EventID)
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
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
	event := reconciliationEvent(context.Background(), claim.Worktree, got)
	completed := completedBranchReconciliationResult(claim.Worktree, event, *local)
	if !completed.ReadyForNormalCleanup || completed.Event == nil || completed.Event.ID != record.EventID ||
		completed.Projection == nil || completed.Projection.ClaimID != claim.ClaimID {
		t.Fatalf("replayed record produced incomplete cleanup receipt: %#v", completed)
	}
}

func TestBranchReconciliationDirectoryRejectsBrokenPrivatePath(t *testing.T) {
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
			defer run.Close()
			if err := os.WriteFile(filepath.Join(runPath, "branch-reconciliations"), []byte("not a directory"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "event is a file", setup: func(t *testing.T, home string) {
			run, runPath, err := openWorkLogRun(home, claim.EffortID, claim.RunID, true)
			if err != nil {
				t.Fatal(err)
			}
			defer run.Close()
			collection, err := openPrivateChild(run, "branch-reconciliations", true)
			if err != nil {
				t.Fatal(err)
			}
			defer collection.Close()
			if err := os.WriteFile(filepath.Join(runPath, "branch-reconciliations", "event-1"), []byte("not a directory"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "home")
			tc.setup(t, home)
			if directory, err := openBranchReconciliationEvent(home, claim, "event-1", true); err == nil {
				directory.Close()
				t.Fatal("broken private path was opened for creation")
			}
		})
	}
}

func TestBranchReconciliationRecordWritersRejectFailedStorage(t *testing.T) {
	claim, _ := wtLogCovReconciliationClaim(t.TempDir())
	record := reconciliationRecordForClaim(claim)
	homeFile := filepath.Join(t.TempDir(), "home-file")
	if err := os.WriteFile(homeFile, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if directory, err := createBranchReconciliationRecord(homeFile, claim, record); err == nil {
		directory.Close()
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
	run.Close()
	if err := os.Mkdir(filepath.Join(runPath, "branch-reconciliations", record.EventID, branchReconciliationRecordName), 0o700); err != nil {
		t.Fatal(err)
	}
	directory.Close()
	if created, err := createBranchReconciliationRecord(home, claim, record); err == nil {
		created.Close()
		t.Fatal("record creation replaced a directory at the record path")
	}
	if _, opened, err := readBranchReconciliationRecord(home, claim, record.EventID); err == nil {
		opened.Close()
		t.Fatal("record replay accepted a directory as JSON")
	}

	closed, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	if _, err := finishBranchReconciliation(closed, record, claim.Worktree, LocalWorkLogEvent{}, LocalWorkLogProjection{}); err == nil {
		t.Fatal("completion reported success without writing its terminal stage")
	}
}

func TestBranchReconciliationClaimReaderRejectsMissingAndAlteredClaims(t *testing.T) {
	fixture := newGitFixture(t)
	worktree := fixture.canonical
	claim, projection := wtLogCovReconciliationClaim(worktree)
	if err := writeWorkLogProjection(worktree, projection); err != nil {
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
	claims.Close()
	run.Close()
	if _, _, err := reconciliationClaim(home, worktree); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("altered immutable claim read error = %v", err)
	}
	if err := corroborateReconciliationClaimShape(worktree, projection, altered); err == nil {
		t.Fatal("altered claim passed direct identity validation")
	}
}

func TestBranchReconciliationGuardsPropagateCancelledGitQueries(t *testing.T) {
	fixture := newGitFixture(t)
	canonical, err := openCanonicalRepository(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := requireRemoteClaimHead(ctx, fixture.canonical, "wb/claim", strings.Repeat("a", 40)); err == nil {
		t.Fatal("remote head check ignored cancelled query")
	}
	if err := requireRemoteClaimAbsent(ctx, fixture.canonical, "wb/claim"); err == nil {
		t.Fatal("remote absence check ignored cancelled query")
	}
	if err := requireLocalClaimAbsent(ctx, canonical, "wb/claim"); err == nil {
		t.Fatal("local absence check ignored cancelled query")
	}
	if err := requireLocalClaimHead(ctx, canonical, "wb/claim", strings.Repeat("a", 40)); err == nil {
		t.Fatal("local head check ignored cancelled query")
	}
}

func TestBranchReconciliationRejectsAnUnrelatedRealGitBase(t *testing.T) {
	fixture := newGitFixture(t)
	base := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	gitTest(t, fixture.canonical, "checkout", "--orphan", "unrelated")
	gitTest(t, fixture.canonical, "commit", "--allow-empty", "-m", "unrelated root")
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	claim, _ := wtLogCovReconciliationClaim(fixture.canonical)
	claim.BaseSHA = base
	entry := ListResult{CanonicalDir: fixture.canonical, WorktreeDir: fixture.canonical,
		Branch: "wb/live", HeadSHA: head, Clean: true, IntegratedAtOrigin: true,
		RemoteTargetSHA: head, MergedPullRequest: &PullRequest{Number: 1}}
	options := LogRecoverOptions{ExpectedHead: head}
	if err := validateReconciliationLifecycleEvidence(context.Background(), entry, claim, options); err == nil ||
		!strings.Contains(err.Error(), "not descended") {
		t.Fatalf("unrelated root validation = %v", err)
	}
}

func TestBranchReconciliationRejectsInvalidInputBeforeReadingGit(t *testing.T) {
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

func TestBranchReconciliationBundleRejectsUnknownRef(t *testing.T) {
	fixture := newGitFixture(t)
	canonical, err := openCanonicalRepository(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	if err := bundleClaimHead(context.Background(), canonical, directory, "event-1", "local",
		"refs/heads/wb/missing", strings.Repeat("a", 40)); err == nil {
		t.Fatal("bundle for a nonexistent immutable claim ref was preserved")
	}
	if err := requireBundleAdvertisesClaimRef(context.Background(), canonical,
		filepath.Join(directory.Name(), "missing.bundle"), "refs/heads/wb/missing", strings.Repeat("a", 40)); err == nil {
		t.Fatal("missing recovery bundle was accepted as proof")
	}
}

func TestBranchReconciliationRemoteBundleInterruptionKeepsBothRefs(t *testing.T) {
	fixture, result, liveBranch, head, remoteHead, _ := prepareBranchReconciliationFixture(t)
	claimHead := gitTestOutput(t, fixture.canonical, "rev-parse", "refs/heads/"+result.Branch)
	options := reconcileOptions(fixture, result, liveBranch, head)
	options.Apply = true
	options.testFailAfterBundle = "remote"
	if _, err := LogRecover(context.Background(), options); err == nil || !strings.Contains(err.Error(), "after remote preservation") {
		t.Fatalf("remote bundle interruption = %v", err)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "refs/heads/"+result.Branch); got != claimHead {
		t.Fatalf("local claim ref changed to %s", got)
	}
	if got := remoteBranchForTest(t, fixture.canonical, result.Branch); got != remoteHead {
		t.Fatalf("remote claim ref changed to %s", got)
	}
}
