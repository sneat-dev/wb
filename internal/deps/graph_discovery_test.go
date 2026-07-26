package deps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedGraphRepository creates a remote plus a canonical clone containing files,
// on the given branch, and returns the canonical path. A repository seeded
// without a go.mod is what the discovery policy can prove irrelevant to Go.
func seedGraphRepository(t *testing.T, fixture, name, branch string, files map[string]string) string {
	t.Helper()
	seed := filepath.Join(fixture, "seed-"+name)
	remote := filepath.Join(fixture, "remote-"+name+".git")
	canonical := filepath.Join(fixture, "projects", "acme", name)
	for path, body := range files {
		writeTestFile(t, filepath.Join(seed, path), body)
	}
	runTestGit(t, seed, "init", "-b", branch)
	runTestGit(t, seed, "config", "user.name", "WB Test")
	runTestGit(t, seed, "config", "user.email", "wb@example.test")
	runTestGit(t, seed, "add", "-A")
	runTestGit(t, seed, "commit", "-m", "initial")
	runTestGit(t, fixture, "clone", "--bare", seed, remote)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, fixture, "clone", remote, canonical)
	return canonical
}

// TestBuildGraphSkipsProvenNonGoRepositoryWithoutBaseRef pins that a fleet-wide
// graph survives a repository that has no origin/main and no go.mod. Before the
// discovery policy was applied to this path, one such repository made
// `wb deps graph --fleet` exit 1 with no output at all: every other
// repository's evidence was discarded along with it.
func TestBuildGraphSkipsProvenNonGoRepositoryWithoutBaseRef(t *testing.T) {
	t.Parallel()
	fixture := t.TempDir()
	app := seedGraphRepository(t, fixture, "app", "main", map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.24\n",
	})
	// A website still on master: exists, fine, simply not a Go repository.
	site := seedGraphRepository(t, fixture, "website", "master", map[string]string{
		"README.md": "# website\n",
	})

	graph, err := BuildGraph(context.Background(), []Repository{
		{Slug: "acme/app", Path: app},
		{Slug: "acme/website", Path: site},
	}, GraphOptions{
		GitHubDir: filepath.Join(fixture, "projects"), Ref: "main",
		Parallel: 1, Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("a non-Go repository without origin/%s must not fail the graph: %v", "main", err)
	}

	var modules []string
	for _, module := range graph.Modules {
		modules = append(modules, module.Path)
	}
	if !contains(modules, "example.com/app") {
		t.Fatalf("healthy repository missing from graph; modules = %v", modules)
	}
	// The skip is reported, so a shrunken fleet cannot read as a complete one.
	if len(graph.DiscoverySkips) != 1 {
		t.Fatalf("discovery skips = %+v, want exactly one", graph.DiscoverySkips)
	}
	if graph.DiscoverySkips[0].Repository != "acme/website" {
		t.Fatalf("skipped repository = %q, want acme/website", graph.DiscoverySkips[0].Repository)
	}
	if !strings.Contains(graph.DiscoverySkips[0].Reason, "no go.mod") {
		t.Fatalf("skip reason = %q, want it to say why the skip was safe", graph.DiscoverySkips[0].Reason)
	}
}

// TestBuildGraphFailsForGoRepositoryWithoutBaseRef is the other half, and the
// more important one: a repository that DOES carry Go code must never be
// skipped quietly. Silently dropping it would hide a real consumer from a
// dependency rollout, which is worse than the abort this policy replaced.
func TestBuildGraphFailsForGoRepositoryWithoutBaseRef(t *testing.T) {
	t.Parallel()
	fixture := t.TempDir()
	app := seedGraphRepository(t, fixture, "app", "main", map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.24\n",
	})
	// A Go consumer whose default branch was renamed. This must be loud.
	stranded := seedGraphRepository(t, fixture, "consumer", "master", map[string]string{
		"go.mod": "module example.com/consumer\n\ngo 1.24\n",
	})

	_, err := BuildGraph(context.Background(), []Repository{
		{Slug: "acme/app", Path: app},
		{Slug: "acme/consumer", Path: stranded},
	}, GraphOptions{
		GitHubDir: filepath.Join(fixture, "projects"), Ref: "main",
		Parallel: 1, Timeout: time.Minute,
	})
	if err == nil {
		t.Fatal("a Go repository without the base ref must fail the graph, not be skipped")
	}
	if !strings.Contains(err.Error(), "acme/consumer") {
		t.Fatalf("error must name the repository; got %v", err)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
