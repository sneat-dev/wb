package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/landinglane"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// TestPrepareWorktreeMergeRefusesDifferentLiveLandingLane exercises the
// integration point added for the 2026-09-07 concurrent-landing incident: a
// different live WB session already driving this (repository, target) lane
// must refuse `wb worktree merge prepare` before it creates a candidate that
// would otherwise have to be re-prepared or stranded.
func TestPrepareWorktreeMergeRefusesDifferentLiveLandingLane(t *testing.T) {
	fixture := newEngineFixture(t)
	sourceA := createMergeSource(t, fixture, "lane-guard-source-a", "feature/lane-guard-a", "a.txt", "a\n")

	home, err := wbhome.EnsureRoot(fixture.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(home, session.DirName)
	// This test process's own PID is guaranteed live, so registering the
	// "other session" against it makes the guard's real session-registry
	// liveness check (never a bare PID) see a genuinely live owner.
	if _, err := session.Register(sessionDir, session.Record{
		PID: os.Getpid(), WBSessionID: "wbs-other-session", Runtime: "claude-code", Model: "claude-sonnet-5",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("register other session: %v", err)
	}
	if _, err := landinglane.Acquire(home, landinglane.AcquireRequest{
		Repository: "acme/app", Target: "main",
		Self: landinglane.Owner{WBSessionID: "wbs-other-session", PID: os.Getpid(), Command: "wb worktree merge"},
	}); err != nil {
		t.Fatalf("seed lane acquisition: %v", err)
	}

	_, err = PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir,
		Sources:      []string{sourceA.WorktreeDir},
		Target:       "main",
		Model:        "test-model",
		AgentRuntime: "test",
		Lane: LaneGuardRequest{
			Owner: landinglane.Owner{WBSessionID: "wbs-this-session", PID: os.Getpid(), Command: "wb worktree merge prepare"},
		},
	})
	var conflict *landinglane.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected a ConflictError refusing the held lane, got %v", err)
	}
	if conflict.Record.Owner.WBSessionID != "wbs-other-session" {
		t.Fatalf("conflict names %q, want wbs-other-session", conflict.Record.Owner.WBSessionID)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.githubDir, "acme", "app")); statErr != nil {
		t.Fatalf("canonical clone should be untouched by a refused prepare: %v", statErr)
	}
}

// TestPrepareWorktreeMergeAdmitsSameSessionLandingLane proves the other side
// of the same guard: this session already holding the lane (e.g. a retried
// prepare) proceeds rather than refusing itself.
func TestPrepareWorktreeMergeAdmitsSameSessionLandingLane(t *testing.T) {
	fixture := newEngineFixture(t)
	sourceA := createMergeSource(t, fixture, "lane-guard-source-b", "feature/lane-guard-b", "b.txt", "b\n")

	home, err := wbhome.EnsureRoot(fixture.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := landinglane.Acquire(home, landinglane.AcquireRequest{
		Repository: "acme/app", Target: "main",
		Self: landinglane.Owner{WBSessionID: "wbs-this-session", PID: os.Getpid(), Command: "wb worktree merge prepare"},
	}); err != nil {
		t.Fatalf("seed lane acquisition: %v", err)
	}

	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir,
		Sources:      []string{sourceA.WorktreeDir},
		Target:       "main",
		Model:        "test-model",
		AgentRuntime: "test",
		Lane: LaneGuardRequest{
			Owner: landinglane.Owner{WBSessionID: "wbs-this-session", PID: os.Getpid(), Command: "wb worktree merge prepare"},
		},
	})
	if err != nil {
		t.Fatalf("same-session prepare refused: %v", err)
	}
	if receipt.Status != WorktreeMergePrepared {
		t.Fatalf("receipt = %+v", receipt)
	}
}

// TestPrepareWorktreeMergeReleasesLaneOnEarlyFailure proves the finding-2
// fix: an error path after the lane is acquired but before a receipt is
// created must release the lane immediately, rather than leaving it to clear
// only via the 30-minute stale timeout while a different session is wrongly
// refused in the meantime.
func TestPrepareWorktreeMergeReleasesLaneOnEarlyFailure(t *testing.T) {
	fixture := newEngineFixture(t)
	sourceA := createMergeSource(t, fixture, "lane-guard-source-early-fail", "feature/lane-guard-early-fail", "d.txt", "d\n")

	home, err := wbhome.EnsureRoot(fixture.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(home, session.DirName)
	if _, err := session.Register(sessionDir, session.Record{
		PID: os.Getpid(), WBSessionID: "wbs-early-fail", Runtime: "claude-code", Model: "claude-sonnet-5",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("register session: %v", err)
	}

	// A rebatch receipt path that does not exist triggers an error after the
	// lane guard runs (acquireLandingLane) but before PrepareWorktreeMerge
	// constructs any receipt of its own — exactly the gap finding 2 closed.
	_, err = PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot:   fixture.githubDir,
		Sources:        []string{sourceA.WorktreeDir},
		Target:         "main",
		Model:          "test-model",
		AgentRuntime:   "test",
		RebatchReceipt: filepath.Join(fixture.githubDir, "no-such-receipt.json"),
		Lane: LaneGuardRequest{
			Owner: landinglane.Owner{WBSessionID: "wbs-early-fail", PID: os.Getpid(), Command: "wb worktree merge prepare"},
		},
	})
	if err == nil {
		t.Fatalf("expected the missing rebatch receipt to fail")
	}

	// A different, live session must be admitted immediately: the failed
	// session's lane must already be gone, not merely stale.
	if _, err := session.Register(sessionDir, session.Record{
		PID: os.Getpid() + 1, WBSessionID: "wbs-different-session", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("register other session: %v", err)
	}
	record, err := landinglane.Acquire(home, landinglane.AcquireRequest{
		Repository: "acme/app", Target: "main",
		Self:       landinglane.Owner{WBSessionID: "wbs-different-session", PID: os.Getpid() + 1},
		SessionDir: sessionDir,
	})
	if err != nil {
		t.Fatalf("a different session should acquire the released lane immediately, got %v", err)
	}
	if record.Owner.WBSessionID != "wbs-different-session" {
		t.Fatalf("lane not admitted to the new session: %+v", record)
	}
}

// TestStartLandingLaneHeartbeatKeepsLiveOwnerLaneAcrossASimulatedLongWait
// proves the finding-1 fix: a lane held across a long CI wait (routinely
// 30-60 minutes for this fleet) must never go stale out from under a
// still-live session. It scales the real 30-minute stale window down for
// speed, starts the background heartbeat the way pr_land.go and
// worktree_merge.go now do around a CI wait, and proves a different live
// session is refused for the whole simulated wait — then, once the wait ends
// and the heartbeat stops, that the record's HeartbeatAt actually advanced
// (proving the ticker really ran, not merely that liveness alone happened to
// carry the refusal).
func TestStartLandingLaneHeartbeatKeepsLiveOwnerLaneAcrossASimulatedLongWait(t *testing.T) {
	// startLandingLaneHeartbeat takes a *projectsRoot* (like every other
	// orchestrate-level lane helper) and resolves the actual WB home from it
	// via wbhome.Root, exactly as acquireLandingLane/releaseLandingLane do.
	// Seeding and reading the record must go through that same resolved
	// home, not a bare temp dir, or the heartbeat writes land somewhere this
	// test never looks at.
	projectsRoot := t.TempDir()
	home, err := wbhome.EnsureRoot(projectsRoot)
	if err != nil {
		t.Fatalf("resolve wb home: %v", err)
	}
	const staleAfter = 60 * time.Millisecond
	const heartbeatInterval = 10 * time.Millisecond
	const simulatedWait = 250 * time.Millisecond // several stale windows

	initial, err := landinglane.Acquire(home, landinglane.AcquireRequest{
		Repository: "acme/app", Target: "main",
		Self:        landinglane.Owner{WBSessionID: "wbs-landing", PID: 111, Command: "wb pr land"},
		IsOwnerLive: func(landinglane.Owner) bool { return true },
		StaleAfter:  staleAfter,
	})
	if err != nil {
		t.Fatalf("seed acquire: %v", err)
	}

	stop := startLandingLaneHeartbeat(projectsRoot, "acme/app", "main", "wbs-landing", heartbeatInterval)
	deadline := time.Now().Add(simulatedWait)
	refusals := 0
	for time.Now().Before(deadline) {
		_, acquireErr := landinglane.Acquire(home, landinglane.AcquireRequest{
			Repository: "acme/app", Target: "main",
			Self:        landinglane.Owner{WBSessionID: "wbs-other", PID: 222, Command: "wb pr land"},
			IsOwnerLive: func(landinglane.Owner) bool { return true },
			StaleAfter:  staleAfter,
		})
		var conflict *landinglane.ConflictError
		if !errors.As(acquireErr, &conflict) {
			t.Fatalf("a different live session must be refused throughout the wait, got %v", acquireErr)
		}
		refusals++
		time.Sleep(heartbeatInterval)
	}
	stop()
	if refusals == 0 {
		t.Fatalf("expected at least one refusal during the simulated wait")
	}

	final, found, err := landinglane.Read(home, "acme/app", "main")
	if err != nil || !found {
		t.Fatalf("Read after wait: found=%v err=%v", found, err)
	}
	if !final.Owner.HeartbeatAt.After(initial.Owner.HeartbeatAt) {
		t.Fatalf("heartbeat should have advanced during the wait: initial=%v final=%v",
			initial.Owner.HeartbeatAt, final.Owner.HeartbeatAt)
	}
}

// TestStartLandingLaneHeartbeatNoOpsWithoutASession proves the guard's
// deliberate no-op contract: a call that never acquired a lane (empty
// wbSessionID) must not panic or write anything, exactly like
// acquireLandingLane/releaseLandingLane's own no-op contract.
func TestStartLandingLaneHeartbeatNoOpsWithoutASession(t *testing.T) {
	home := t.TempDir()
	stop := startLandingLaneHeartbeat(home, "acme/app", "main", "", time.Millisecond)
	stop()
	if _, found, err := landinglane.Read(home, "acme/app", "main"); err != nil || found {
		t.Fatalf("no-op heartbeat must not create a lane record: found=%v err=%v", found, err)
	}
}

// TestLaneGuardRequestZeroValueSkipsGuard proves the compatibility contract
// every existing direct caller relies on: leaving Lane unset never engages
// the guard, so no existing PrepareWorktreeMerge/LandWorktreeMerge caller (or
// test) is affected by its addition.
func TestLaneGuardRequestZeroValueSkipsGuard(t *testing.T) {
	fixture := newEngineFixture(t)
	sourceA := createMergeSource(t, fixture, "lane-guard-source-c", "feature/lane-guard-c", "c.txt", "c\n")

	home, err := wbhome.EnsureRoot(fixture.githubDir)
	if err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(home, session.DirName)
	if _, err := session.Register(sessionDir, session.Record{
		PID: os.Getpid(), WBSessionID: "wbs-other-session", StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("register other session: %v", err)
	}
	if _, err := landinglane.Acquire(home, landinglane.AcquireRequest{
		Repository: "acme/app", Target: "main",
		Self: landinglane.Owner{WBSessionID: "wbs-other-session", PID: os.Getpid()},
	}); err != nil {
		t.Fatalf("seed lane acquisition: %v", err)
	}

	// No Lane set at all: PrepareWorktreeMerge must proceed exactly as it did
	// before this guard existed.
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir,
		Sources:      []string{sourceA.WorktreeDir},
		Target:       "main",
		Model:        "test-model",
		AgentRuntime: "test",
	})
	if err != nil {
		t.Fatalf("prepare without Lane set must not be guarded: %v", err)
	}
	if receipt.Status != WorktreeMergePrepared {
		t.Fatalf("receipt = %+v", receipt)
	}
}
