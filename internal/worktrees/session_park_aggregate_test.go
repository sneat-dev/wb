package worktrees

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionpark"
)

func TestCaptureParkedSessionAggregateRetainsEveryCustodyLockThroughPersistence(t *testing.T) {
	fixture, first, source := newSessionCheckpointFixture(t, "park-aggregate-first")
	useIdentityRemote(t, fixture, first)
	secondResult, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "park-aggregate-second",
		WorkLog: WorkLogOptions{
			RunID: "park-aggregate-second-run", Model: source.Model, AgentRuntime: source.Runtime,
			AgentID: source.NativeHarnessID, OriginalPrompt: writeWorkLogPromptFile(t, "second parked member\n"), RequireOriginalPrompt: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	second := secondResult[0].WorktreeDir
	listed := make([]ListResult, 0, 2)
	for _, worktree := range []string{first, second} {
		branch := gitTestOutput(t, worktree, "branch", "--show-current")
		gitTest(t, worktree, "push", "origin", branch)
		guard, guardErr := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot, Admission: AdmissionEnforce})
		if guardErr != nil {
			t.Fatal(guardErr)
		}
		listed = append(listed, ListResult{Repository: "acme/app", CanonicalDir: guard.CanonicalDir, WorktreeDir: worktree, WorktreesRoot: guard.WorktreesRoot, Branch: branch})
	}

	mutationStarted := make(chan struct{})
	mutationDone := make(chan error, 1)
	persisted := false
	err = CaptureParkedSessionAggregate(context.Background(), fixture.projectsRoot, listed, source, func(members []sessionpark.Worktree) error {
		if len(members) != 2 {
			t.Fatalf("captured members = %d, want 2", len(members))
		}
		go func() {
			close(mutationStarted)
			mutationDone <- RecordCustody(first, "", "competing successor", AgentIdentity{Runtime: "codex", AgentID: "newer", Model: "gpt-5", PID: os.Getpid() + 1000})
		}()
		<-mutationStarted
		select {
		case mutationErr := <-mutationDone:
			t.Fatalf("member-1 custody mutation slipped before aggregate persistence: %v", mutationErr)
		case <-time.After(100 * time.Millisecond):
		}
		persisted = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !persisted {
		t.Fatal("aggregate persistence callback did not run")
	}
	select {
	case mutationErr := <-mutationDone:
		if mutationErr != nil {
			t.Fatal(mutationErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("custody mutation did not proceed after aggregate authority released")
	}
}
