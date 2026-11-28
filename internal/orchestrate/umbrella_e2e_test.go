//go:build e2e

package orchestrate

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/umbrella"
	"github.com/sneat-dev/wb/internal/worktrees"
)

//nolint:paralleltest // engine fixture installs process-wide hosted metadata scripts and t.Setenv.
func TestE2EWorktreeMergeIntegrationCommitTrailerAndLandWithoutCleanupCloseEligible(t *testing.T) {
	fixture := newEngineFixture(t)
	first := createMergeSource(t, fixture, "umb-merge-a", "feature/umb-merge-a", "a.txt", "a\n")
	second := createMergeSource(t, fixture, "umb-merge-b", "feature/umb-merge-b", "b.txt", "b\n")

	ref := umbrella.Ref{Registry: "github", ID: "sneat-co/backstage#845"}
	if _, err := worktrees.SetTaskUmbrella(context.Background(), fixture.githubDir, "umb-merge-a", ref); err != nil {
		t.Fatalf("SetTaskUmbrella(a): %v", err)
	}
	if _, err := worktrees.SetTaskUmbrella(context.Background(), fixture.githubDir, "umb-merge-b", ref); err != nil {
		t.Fatalf("SetTaskUmbrella(b): %v", err)
	}

	// Integrating two divergent umbrella-bound sources produces a WB merge commit with Wb-Umbrella trailer.
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir,
		Sources:      []string{first.WorktreeDir, second.WorktreeDir},
		Target:       "main",
		Model:        "test-model",
		AgentRuntime: "test",
	})
	if err != nil {
		t.Fatalf("PrepareWorktreeMerge: %v", err)
	}
	mergeMsg := runEngineGit(t, receipt.Candidate.Worktree, "log", "-1", "--format=%B", receipt.Candidate.SHA)
	if !strings.Contains(mergeMsg, "Wb-Umbrella: github:sneat-co/backstage#845") {
		t.Fatalf("integration merge commit lacks Wb-Umbrella trailer:\n%s", mergeMsg)
	}

	// Land without cleanup (--cleanup=false) and with NO preceding PR landing:
	// umbrella must still become close-eligible immediately!
	var posted []string
	providers := umbrella.DefaultProviders(func(_ context.Context, args ...string) umbrella.CommandResult {
		posted = append(posted, strings.Join(args, " "))
		return umbrella.CommandResult{Stdout: []byte(`{"id":1}`)}
	})
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_TARGET_SHA", receipt.Candidate.SHA)
	landed, err := LandWorktreeMerge(context.Background(), WorktreeMergeLandOptions{
		ProjectsRoot:      fixture.githubDir,
		Receipt:           receipt.ReceiptPath,
		Route:             WorktreeMergeRouteAuto,
		Cleanup:           false,
		Timeout:           5 * time.Second,
		CheckPollInterval: time.Millisecond,
		UmbrellaProviders: providers,
	})
	if err != nil || landed.Status != WorktreeMergeLanded {
		t.Fatalf("LandWorktreeMerge(--cleanup=false) = (%+v, %v)", landed, err)
	}
	if landed.UmbrellaCloseCommand != "wb umbrella close sneat-co/backstage#845 --apply" {
		t.Fatalf("UmbrellaCloseCommand = %q, want close offer after --cleanup=false", landed.UmbrellaCloseCommand)
	}
	if len(posted) != 2 {
		t.Fatalf("expected 2 umbrella status comments, got %d: %v", len(posted), posted)
	}
	ready, members, err := worktrees.UmbrellaReadyToClose(fixture.githubDir, ref)
	if err != nil || !ready || len(members) != 2 {
		t.Fatalf("UmbrellaReadyToClose after --cleanup=false = (ready=%v, members=%+v, err=%v)", ready, members, err)
	}
	// Verify the landed target commit itself carries the trailer.
	targetMsg := runEngineGit(t, fixture.canonical, "log", "-1", "--format=%B", landed.LandingSHA)
	if !strings.Contains(targetMsg, "Wb-Umbrella: github:sneat-co/backstage#845") {
		t.Fatalf("landed target merge commit lacks Wb-Umbrella trailer:\n%s", targetMsg)
	}
}
