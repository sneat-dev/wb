package deps

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
)

// seedGoRepository creates a remote plus a canonical clone containing goMod, on
// the given branch, and returns the canonical path.
func seedGoRepository(t *testing.T, fixture, slug, branch, goMod string) string {
	t.Helper()
	seed := filepath.Join(fixture, "seed-"+branch+"-"+filepath.Base(slug))
	remote := filepath.Join(fixture, "remote-"+filepath.Base(slug)+".git")
	canonical := filepath.Join(fixture, "projects", filepath.Dir(slug), filepath.Base(slug))
	writeTestFile(t, filepath.Join(seed, "go.mod"), goMod)
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

// TestGoFleetGraphSkipsRepositoryWithoutBaseRef pins the rule that a repository
// lacking the base ref is skipped and reported, never fatal. A fleet contains
// empty repositories and repositories still on master; before this, one of them
// aborted the walk and every other repository's evidence was discarded, so the
// command returned an empty graph and a non-zero exit.
func TestGoFleetGraphSkipsRepositoryWithoutBaseRef(t *testing.T) {
	t.Parallel()
	fixture := t.TempDir()
	healthy := seedGoRepository(t, fixture, "acme/app", "main", "module example.com/app\n\ngo 1.24\n")
	// A real case from the fleet: the repository exists and is fine, it simply
	// has no origin/main because it never migrated off master.
	stranded := seedGoRepository(t, fixture, "acme/website", "master", "module example.com/website\n\ngo 1.24\n")

	lifecycle, err := orchestrate.Normalize(orchestrate.Options{
		GitHubDir: filepath.Join(fixture, "projects"), Operation: "deps-graph-go-test",
		Ref: "main", Parallel: 1, Timeout: time.Minute, DryRun: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	graph, err := discoverGoFleetGraph(context.Background(), []Repository{
		{Slug: "acme/app", Path: healthy},
		{Slug: "acme/website", Path: stranded},
	}, lifecycle, nil)
	if err != nil {
		t.Fatalf("one repository without origin/main must not fail the walk: %v", err)
	}

	// The healthy repository's evidence survives.
	if _, ok := graph.modules["example.com/app"]; !ok {
		t.Fatalf("healthy repository missing from graph; modules = %v", graph.modules)
	}
	// The stranded one contributes nothing.
	if _, ok := graph.modules["example.com/website"]; ok {
		t.Fatal("repository without the base ref must not contribute modules")
	}
	// And it is reported, so a shrunken fleet cannot look like a complete one.
	if len(graph.skipped) != 1 {
		t.Fatalf("skipped = %+v, want exactly one entry", graph.skipped)
	}
	if graph.skipped[0].Repository != "acme/website" {
		t.Fatalf("skipped repository = %q, want acme/website", graph.skipped[0].Repository)
	}
	if graph.skipped[0].Reason != "no origin/main" {
		t.Fatalf("skip reason = %q, want %q", graph.skipped[0].Reason, "no origin/main")
	}
}
