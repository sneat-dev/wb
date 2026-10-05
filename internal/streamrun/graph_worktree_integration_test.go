package streamrun

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/deps" // The deps-graph report lives in the projects root's state home, so the
	// fixture writes it under <projectsRoot>/.wb rather than an ambient WB_HOME.
	// No graph evidence at all: found is false rather than guessing.
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestProposedTransitiveConsumersWalksTheRecordedGraph(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	consumers, found, err := proposedTransitiveConsumers(projectsRoot, []string{"acme/lib"})
	if err != nil || found || consumers != nil {
		t.Fatalf("missing graph = (%v, %t, %v)", consumers, found, err)
	}

	graph := deps.Graph{
		SchemaVersion: 1,
		Requirements: []deps.GraphRequirement{
			{ProviderRepository: "acme/lib", ConsumerRepository: "acme/mid"},
			{ProviderRepository: "acme/mid", ConsumerRepository: "acme/top"},
			{ProviderRepository: "acme/top", ConsumerRepository: "acme/other"},
			// Self-edges and half-edges are not dependencies.
			{ProviderRepository: "acme/lib", ConsumerRepository: "acme/lib"},
			{ProviderRepository: "", ConsumerRepository: "acme/ignored"},
			{ProviderRepository: "acme/ignored", ConsumerRepository: ""},
		},
	}
	path := filepath.Join(projectsRoot, ".wb", "reports", "deps-graph-go", "deps-graph.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}

	consumers, found, err = proposedTransitiveConsumers(projectsRoot, []string{"acme/lib"})
	if err != nil || !found {
		t.Fatalf("graph walk = (%v, %t, %v)", consumers, found, err)
	}
	want := []string{"acme/mid", "acme/other", "acme/top"}
	if strings.Join(consumers, ",") != strings.Join(want, ",") {
		t.Fatalf("consumers = %v, want the transitive closure %v", consumers, want)
	}

	// A malformed graph is not evidence.
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, found, err = proposedTransitiveConsumers(projectsRoot, []string{"acme/lib"}); err != nil || found {
		t.Fatalf("malformed graph = (found=%t, err=%v), want no evidence", found, err)
	}
}

func TestStreamWorktreesPlannedWorktreeAndRemove(t *testing.T) {
	t.Parallel()
	adapter := &streamWorktrees{projectsRoot: t.TempDir(), cleanup: worktrees.Cleanup}
	if _, err := adapter.PlannedWorktree("task", "not-a-slug"); err == nil ||
		!strings.Contains(err.Error(), "must be owner/name") {
		t.Fatalf("malformed repository error = %v", err)
	}
	planned, err := adapter.PlannedWorktree("cw-task", "acme/app")
	if err != nil {
		t.Fatalf("PlannedWorktree: %v", err)
	}
	if !strings.Contains(planned, "cw-task") || !strings.Contains(planned, filepath.FromSlash("acme/app")) {
		t.Fatalf("planned worktree = %q, want a task directory under the canonical clone", planned)
	}

	// Remove with a receipt that matches nothing reports the missing candidate.
	err = adapter.Remove(context.Background(), "cw-task", "acme/app", planned, &streams.SquashAbsorptionReceipt{
		Target: "main", SourceBranch: "task/cw-task", SourceSHA: "s", CandidateSHA: "c", LandingSHA: "l",
	})
	if err == nil || !strings.Contains(err.Error(), "cw-task") || !strings.Contains(err.Error(), "acme/app") {
		t.Fatalf("Remove error = %v, want a refusal naming the task and repository", err)
	}
}
