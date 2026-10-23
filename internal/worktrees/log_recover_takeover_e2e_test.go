//go:build e2e

package worktrees

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

//nolint:paralleltest // the Git fixture configures process-wide environment
func TestE2ELogRecoverRejectsMissingTakeoverActorBeforeProjectionWrite(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "recover-actor-preflight",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	projection, err := readWorkLogProjection(worktree)
	if err != nil {
		t.Fatal(err)
	}
	claimPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID,
		"claims", projection.ClaimID+".json")
	claimBefore, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory)
	eventsPath := filepath.Join(journal, localWorkLogEventsName)
	eventsBefore, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	localProjectionPath := filepath.Join(journal, localWorkLogProjectionName)
	// Distinct bytes make an unauthorized projection repair observable. A
	// valid takeover actor is required before recovery may replace them.
	const retained = "{\"last_seq\":9999,\"lifecycle\":\"active\"}\n"
	if err := os.WriteFile(localProjectionPath, []byte(retained), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := LogRecover(context.Background(), LogRecoverOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Apply: true, Takeover: true,
	})
	if err == nil || !strings.Contains(err.Error(), "--actor is required") || result.Applied {
		t.Fatalf("missing actor result = %#v, err=%v", result, err)
	}
	for path, want := range map[string][]byte{
		localProjectionPath: []byte(retained), eventsPath: eventsBefore, claimPath: claimBefore,
	} {
		got, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(got, want) {
			t.Fatalf("invalid takeover changed %s: bytes=%q, err=%v", path, got, readErr)
		}
	}
}
