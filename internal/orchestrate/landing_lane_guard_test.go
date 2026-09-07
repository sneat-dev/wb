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
