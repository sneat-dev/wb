package agentrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
)

func TestPruneNeverRemovesRunningOrUnreviewedWork(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	service := fixture.service()
	store := agents.NewStore(fixture.home)
	old := fixture.now.Add(-90 * 24 * time.Hour)

	requiredID, _ := agents.NewID()
	optionalID, _ := agents.NewID()
	acceptedID, _ := agents.NewID()
	youngID, _ := agents.NewID()
	runningID, _ := agents.NewID()
	records := []agents.Record{
		{AgentID: requiredID, State: agents.StateCompleted, ExecutionStatus: agents.StatusCompleted, StartedAt: old, FinishedAt: old.Add(time.Hour), AcceptanceRequired: true},
		{AgentID: optionalID, State: agents.StateCompleted, ExecutionStatus: agents.StatusCompleted, StartedAt: old, FinishedAt: old.Add(time.Hour)},
		{AgentID: acceptedID, State: agents.StateCompleted, ExecutionStatus: agents.StatusCompleted, StartedAt: old, FinishedAt: old.Add(time.Hour), AcceptanceRequired: true},
		{AgentID: youngID, State: agents.StateCompleted, ExecutionStatus: agents.StatusCompleted, StartedAt: fixture.now, FinishedAt: fixture.now},
		{AgentID: runningID, State: agents.StateRunning, ExecutionStatus: agents.StatusRunning, StartedAt: old, OwnerPID: os.Getpid()},
	}
	for _, record := range records {
		if err := store.Create(record); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.AppendReview(acceptedID, agents.ReviewEntry{Status: agents.ReviewAccepted, Evaluator: "alex", At: fixture.now}); err != nil {
		t.Fatal(err)
	}

	if _, err := service.Prune(context.Background(), PruneRequest{ProjectsRoot: fixture.root}); err == nil || !agents.IsRequestError(err) {
		t.Fatalf("a missing bound = %v", err)
	}

	plan, err := service.Prune(context.Background(), PruneRequest{ProjectsRoot: fixture.root, OlderThan: 30 * 24 * time.Hour})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	actions := map[string]string{}
	for _, entry := range plan.Entries {
		actions[entry.AgentID] = entry.Action
	}
	if actions[optionalID] != "remove" {
		t.Fatalf("an old, unreviewed, optional run must be removable: %#v", plan.Entries)
	}
	for _, kept := range []string{requiredID, acceptedID, youngID, runningID} {
		if actions[kept] != "keep" {
			t.Fatalf("run %s must be kept: %#v", kept, plan.Entries)
		}
	}
	if plan.Removed != 0 {
		t.Fatalf("a plan must remove nothing: %#v", plan)
	}

	applied, err := service.Prune(context.Background(), PruneRequest{ProjectsRoot: fixture.root, OlderThan: 30 * 24 * time.Hour, Apply: true})
	if err != nil {
		t.Fatalf("Prune apply: %v", err)
	}
	if applied.Removed != 1 {
		t.Fatalf("removed = %d", applied.Removed)
	}
	if _, err := store.Load(optionalID); !agents.IsUnknownAgent(err) {
		t.Fatalf("the removable run must be gone: %v", err)
	}
	if _, err := store.Load(requiredID); err != nil {
		t.Fatalf("the required run must survive: %v", err)
	}
}

func TestPruneReportsARemovalFailureWithoutClaimingSuccess(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	store := agents.NewStore(fixture.home)
	id, _ := agents.NewID()
	if err := store.Create(agents.Record{AgentID: id, State: agents.StateCompleted, ExecutionStatus: agents.StatusCompleted, StartedAt: fixture.now.Add(-90 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	// A directory whose contents cannot be unlinked makes removal fail.
	locked := filepath.Join(store.Dir(id), "locked")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	plan, err := fixture.service().Prune(context.Background(), PruneRequest{ProjectsRoot: fixture.root, OlderThan: 24 * time.Hour, Apply: true})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if plan.Removed != 0 {
		t.Fatalf("a failed removal must not be counted: %#v", plan)
	}
	if len(plan.Entries) != 1 || plan.Entries[0].Action != "keep" || !strings.Contains(plan.Entries[0].Reason, "remove failed") {
		t.Fatalf("entries = %#v", plan.Entries)
	}
}

func TestPruneNeverRemovesARunWhoseWorktreeIsStillLeased(t *testing.T) {
	t.Parallel()
	fixture := newServiceFixture(t)
	store := agents.NewStore(fixture.home)
	worktree := t.TempDir()
	id, _ := agents.NewID()
	if err := store.Create(agents.Record{
		AgentID: id, State: agents.StateCompleted, ExecutionStatus: agents.StatusCompleted,
		StartedAt: fixture.now.Add(-90 * 24 * time.Hour), FinishedAt: fixture.now.Add(-90 * 24 * time.Hour),
		WorktreeDir: worktree,
	}); err != nil {
		t.Fatal(err)
	}
	lease, err := store.AcquireLease(worktree, "agt-other", "", "acme/app", fixture.now)
	if err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	t.Cleanup(func() { _ = lease.Release() })
	plan, err := fixture.service().Prune(context.Background(), PruneRequest{ProjectsRoot: fixture.root, OlderThan: 24 * time.Hour})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(plan.Entries) != 1 || plan.Entries[0].Action != "keep" || !strings.Contains(plan.Entries[0].Reason, "claimed") {
		t.Fatalf("entries = %#v", plan.Entries)
	}
}
