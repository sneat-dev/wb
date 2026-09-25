package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// proposedTransitiveConsumers walks the dependency graph breadth-first and
// must not revisit a consumer reached through more than one path.
func TestPkp00ProposedTransitiveConsumersSkipsAlreadyReachedConsumer(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	home, err := wbhome.Root(root)
	if err != nil {
		t.Fatalf("resolve wbhome root: %v", err)
	}
	reportsDir := filepath.Join(home, "reports", "deps-graph-go")
	if err := os.MkdirAll(reportsDir, 0o755); err != nil {
		t.Fatalf("mkdir reports dir: %v", err)
	}
	graph := deps.Graph{Requirements: []deps.GraphRequirement{
		{ProviderRepository: "acme/lib", ConsumerRepository: "acme/app"},
		{ProviderRepository: "acme/app", ConsumerRepository: "acme/tool"},
		// acme/tool is also a direct consumer of acme/lib, so the walk
		// reaches it twice: once directly and once via acme/app.
		{ProviderRepository: "acme/lib", ConsumerRepository: "acme/tool"},
	}}
	raw, err := json.Marshal(graph)
	if err != nil {
		t.Fatalf("marshal graph: %v", err)
	}
	if err := os.WriteFile(filepath.Join(reportsDir, "deps-graph.json"), raw, 0o644); err != nil {
		t.Fatalf("write deps graph: %v", err)
	}
	consumers, found, err := proposedTransitiveConsumers(root, []string{"acme/lib"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatalf("want the graph to be found")
	}
	if len(consumers) != 2 || consumers[0] != "acme/app" || consumers[1] != "acme/tool" {
		t.Fatalf("want [acme/app acme/tool] with no duplicate, got %v", consumers)
	}
}
