package streamrun

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
func TestProposedTransitiveConsumersSkipsAlreadyReachedConsumer(t *testing.T) {
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
func TestGraphUnreadableRootAndCycleEvidenceRemainExplicit(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	loop := filepath.Join(root, "loop")
	if err := os.Symlink(loop, loop); err != nil {
		t.Fatal(err)
	}
	if _, _, err := proposedTransitiveConsumers(loop, []string{"acme/lib"}); err == nil {
		t.Fatal("unresolvable root accepted")
	}
	home, err := wbhome.Root(root)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "reports", "deps-graph-npm")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(deps.Graph{Requirements: []deps.GraphRequirement{{ProviderRepository: "", ConsumerRepository: "acme/ignored"}, {ProviderRepository: "acme/lib", ConsumerRepository: ""}, {ProviderRepository: "acme/lib", ConsumerRepository: "acme/lib"}, {ProviderRepository: "acme/lib", ConsumerRepository: "acme/app"}, {ProviderRepository: "acme/app", ConsumerRepository: "acme/lib"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "deps-graph.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	got, found, err := proposedTransitiveConsumers(root, []string{"acme/lib"})
	if err != nil || !found || len(got) != 2 || got[0] != "acme/app" || got[1] != "acme/lib" {
		t.Fatal(got, found, err)
	}
}
