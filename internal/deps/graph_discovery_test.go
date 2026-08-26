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

// TestBuildGraphFallsBackToDefaultBranchForNonGoRepositoryWithoutBaseRef pins
// that a fleet-wide graph survives — and correctly discovers — a repository
// whose default branch is "master" rather than the fleet's configured
// "main". Before the default-branch fallback existed, `orchestrate
// .EnsureCanonical` hard-failed on `origin/main` for every such repository;
// discovery only survived the fleet-wide run because a local scan could
// prove a website like this carries no go.mod at all. With the fallback,
// this repository is fully discovered at its actual default branch instead
// of merely being excused from failing the campaign.
func TestBuildGraphFallsBackToDefaultBranchForNonGoRepositoryWithoutBaseRef(t *testing.T) {
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
	// Nothing was skipped: the repository resolved via its default branch.
	if len(graph.DiscoverySkips) != 0 {
		t.Fatalf("discovery skips = %+v, want none — the repository should have been resolved via fallback, not skipped", graph.DiscoverySkips)
	}
	if len(graph.DefaultBranchFallbacks) != 1 || graph.DefaultBranchFallbacks[0].Repository != "acme/website" {
		t.Fatalf("default branch fallbacks = %+v, want exactly one for acme/website", graph.DefaultBranchFallbacks)
	}
	if graph.DefaultBranchFallbacks[0].Ref != "master" {
		t.Fatalf("fallback ref = %q, want master", graph.DefaultBranchFallbacks[0].Ref)
	}
}

// TestBuildGraphFallsBackToDefaultBranchForGoRepositoryWithoutBaseRef is the
// other half, and the more important one: a repository that DOES carry Go
// code must be fully discovered via its actual default branch rather than
// either being dropped or aborting the whole fleet. Silently dropping it
// would hide a real consumer from a dependency rollout; aborting the fleet
// over a routine default-branch mismatch is the exact production failure
// this fallback exists to fix (7 master-default fleet repositories: e.g.
// strongo/gamp, trakhimenok/badger).
func TestBuildGraphFallsBackToDefaultBranchForGoRepositoryWithoutBaseRef(t *testing.T) {
	t.Parallel()
	fixture := t.TempDir()
	app := seedGraphRepository(t, fixture, "app", "main", map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.24\n",
	})
	// A Go consumer whose default branch is master, not main.
	consumer := seedGraphRepository(t, fixture, "consumer", "master", map[string]string{
		"go.mod": "module example.com/consumer\n\ngo 1.24\n\nrequire example.com/app v0.1.0\n",
	})

	graph, err := BuildGraph(context.Background(), []Repository{
		{Slug: "acme/app", Path: app},
		{Slug: "acme/consumer", Path: consumer},
	}, GraphOptions{
		GitHubDir: filepath.Join(fixture, "projects"), Ref: "main",
		Parallel: 1, Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("a Go repository without origin/main must fall back to its default branch, not fail the graph: %v", err)
	}
	var modules []string
	for _, module := range graph.Modules {
		modules = append(modules, module.Path)
	}
	if !contains(modules, "example.com/consumer") {
		t.Fatalf("consumer module missing from graph; modules = %v", modules)
	}
	if len(graph.DiscoverySkips) != 0 {
		t.Fatalf("discovery skips = %+v, want none", graph.DiscoverySkips)
	}
	if len(graph.DefaultBranchFallbacks) != 1 || graph.DefaultBranchFallbacks[0].Repository != "acme/consumer" || graph.DefaultBranchFallbacks[0].Ref != "master" {
		t.Fatalf("default branch fallbacks = %+v, want exactly one acme/consumer -> master", graph.DefaultBranchFallbacks)
	}
}

// deleteOriginHeadSymref reproduces a long-lived fleet clone that never had
// (or lost) its cached origin/HEAD symref — most commonly because it was
// assembled with `git remote add` + `git fetch` rather than `git clone`, or
// because the remote's default branch was renamed after the local symref was
// cached. EnsureCanonical must still resolve the actual default branch by
// refreshing it from origin (`git remote set-head origin --auto`, or
// `git ls-remote --symref` if that also fails).
func deleteOriginHeadSymref(t *testing.T, canonical string) {
	t.Helper()
	runTestGit(t, canonical, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
}

// TestBuildGraphFallsBackToDefaultBranchWhenLocalSymrefIsMissing exercises
// the refresh path specifically: EnsureCanonical must not depend solely on
// a symref that `git clone` happened to cache; it must refresh it from
// origin when absent.
func TestBuildGraphFallsBackToDefaultBranchWhenLocalSymrefIsMissing(t *testing.T) {
	t.Parallel()
	fixture := t.TempDir()
	app := seedGraphRepository(t, fixture, "app", "main", map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.24\n",
	})
	consumer := seedGraphRepository(t, fixture, "consumer", "master", map[string]string{
		"go.mod": "module example.com/consumer\n\ngo 1.24\n",
	})
	deleteOriginHeadSymref(t, consumer)

	graph, err := BuildGraph(context.Background(), []Repository{
		{Slug: "acme/app", Path: app},
		{Slug: "acme/consumer", Path: consumer},
	}, GraphOptions{
		GitHubDir: filepath.Join(fixture, "projects"), Ref: "main",
		Parallel: 1, Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("a missing cached origin/HEAD symref must be refreshed from origin, not fail the graph: %v", err)
	}
	var modules []string
	for _, module := range graph.Modules {
		modules = append(modules, module.Path)
	}
	if !contains(modules, "example.com/consumer") {
		t.Fatalf("consumer module missing from graph; modules = %v", modules)
	}
	if len(graph.DefaultBranchFallbacks) != 1 || graph.DefaultBranchFallbacks[0].Ref != "master" {
		t.Fatalf("default branch fallbacks = %+v, want exactly one acme/consumer -> master", graph.DefaultBranchFallbacks)
	}
}

// TestBuildGraphFailsWhenNeitherConfiguredRefNorDefaultBranchResolve pins the
// floor of the fallback: a repository whose origin has no resolvable ref at
// all (nothing for `--ref`, and no default branch to fall back to) must
// still fail loudly for a repository proven relevant by a local scan. The
// fallback substitutes a known-good alternative; it is never license to swallow
// a repository WB genuinely cannot read.
func TestBuildGraphFailsWhenNeitherConfiguredRefNorDefaultBranchResolve(t *testing.T) {
	t.Parallel()
	fixture := t.TempDir()
	app := seedGraphRepository(t, fixture, "app", "main", map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.24\n",
	})
	// A canonical clone of a genuinely empty remote: no branches, no HEAD to
	// resolve at all. The working tree still carries an (uncommitted) go.mod,
	// exactly as a local scan would find on disk regardless of git history, so
	// the failure must stay loud rather than being excused as irrelevant.
	remote := filepath.Join(fixture, "remote-broken.git")
	runTestGit(t, fixture, "init", "--bare", remote)
	broken := filepath.Join(fixture, "projects", "acme", "broken")
	if err := os.MkdirAll(filepath.Dir(broken), 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, fixture, "clone", remote, broken)
	writeTestFile(t, filepath.Join(broken, "go.mod"), "module example.com/broken\n\ngo 1.24\n")

	_, err := BuildGraph(context.Background(), []Repository{
		{Slug: "acme/app", Path: app},
		{Slug: "acme/broken", Path: broken},
	}, GraphOptions{
		GitHubDir: filepath.Join(fixture, "projects"), Ref: "main",
		Parallel: 1, Timeout: time.Minute,
	})
	if err == nil {
		t.Fatal("a repository with no resolvable ref at all and a real go.mod must fail the graph")
	}
	if !strings.Contains(err.Error(), "acme/broken") {
		t.Fatalf("error must name the repository; got %v", err)
	}
}

// TestBuildGraphSkipsUnparseableNonRootGoModWithWarning pins the second
// discovery defect this fallback work fixed: a nested go.mod that is not a
// repository's root manifest — most commonly an EJS code-generator template
// such as sneat-co/sneat-ext-contracts'
// tools/contract-generator/src/generators/contract/files-go/go.mod, which
// contains a literal `module module/path` placeholder — must not abort the
// whole fleet. It is skipped with a warning naming the exact file and
// repository instead.
func TestBuildGraphSkipsUnparseableNonRootGoModWithWarning(t *testing.T) {
	t.Parallel()
	fixture := t.TempDir()
	app := seedGraphRepository(t, fixture, "app", "main", map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.24\n",
	})
	// A repository with NO root go.mod at all, only a nested EJS template
	// that is not valid Go module syntax.
	generator := seedGraphRepository(t, fixture, "contracts", "main", map[string]string{
		"tools/contract-generator/src/generators/contract/files-go/go.mod": "module github.com/acme/contracts/<%= family %>\n",
	})

	graph, err := BuildGraph(context.Background(), []Repository{
		{Slug: "acme/app", Path: app},
		{Slug: "acme/contracts", Path: generator},
	}, GraphOptions{
		GitHubDir: filepath.Join(fixture, "projects"), Ref: "main",
		Parallel: 1, Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("an unparseable NON-root go.mod must not fail the graph: %v", err)
	}
	if len(graph.ManifestWarnings) != 1 {
		t.Fatalf("manifest warnings = %+v, want exactly one", graph.ManifestWarnings)
	}
	warning := graph.ManifestWarnings[0]
	if warning.Repository != "acme/contracts" {
		t.Fatalf("warning repository = %q, want acme/contracts", warning.Repository)
	}
	if warning.Manifest != "tools/contract-generator/src/generators/contract/files-go/go.mod" {
		t.Fatalf("warning manifest = %q, want the exact template path", warning.Manifest)
	}
	if len(graph.DiscoverySkips) != 0 {
		t.Fatalf("discovery skips = %+v, want none — the repository was discovered, only one manifest was skipped", graph.DiscoverySkips)
	}
}

// TestBuildGraphFailsForUnparseableRootGoMod is the fatal counterpart: an
// unparseable ROOT go.mod must never be downgraded to a warning. WB cannot
// safely assume irrelevance about a repository's own module declaration the
// way it can about a nested generator template.
func TestBuildGraphFailsForUnparseableRootGoMod(t *testing.T) {
	t.Parallel()
	fixture := t.TempDir()
	app := seedGraphRepository(t, fixture, "app", "main", map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.24\n",
	})
	broken := seedGraphRepository(t, fixture, "broken-root", "main", map[string]string{
		"go.mod": "module <%= family %>\n",
	})

	_, err := BuildGraph(context.Background(), []Repository{
		{Slug: "acme/app", Path: app},
		{Slug: "acme/broken-root", Path: broken},
	}, GraphOptions{
		GitHubDir: filepath.Join(fixture, "projects"), Ref: "main",
		Parallel: 1, Timeout: time.Minute,
	})
	if err == nil {
		t.Fatal("an unparseable ROOT go.mod must fail the graph, not be downgraded to a warning")
	}
	if !strings.Contains(err.Error(), "acme/broken-root") {
		t.Fatalf("error must name the repository; got %v", err)
	}
	if !strings.Contains(err.Error(), "go.mod") {
		t.Fatalf("error must name the manifest; got %v", err)
	}
}

// seedUnreadableCanonicalRepository creates a canonical clone directory that
// is a real git repository with committed content but deliberately has no
// 'origin' remote configured, reproducing the exact production failure this
// package now treats as a skip: `git fetch --quiet origin` fails with
// "fatal: 'origin' does not appear to be a git repository" rather than a
// missing-ref error, because there is no remote to check a ref against at
// all.
func seedUnreadableCanonicalRepository(t *testing.T, fixture, name string, files map[string]string) string {
	t.Helper()
	canonical := filepath.Join(fixture, "projects", "acme", name)
	for path, body := range files {
		writeTestFile(t, filepath.Join(canonical, path), body)
	}
	runTestGit(t, canonical, "init", "-b", "main")
	runTestGit(t, canonical, "config", "user.name", "WB Test")
	runTestGit(t, canonical, "config", "user.email", "wb@example.test")
	runTestGit(t, canonical, "add", "-A")
	runTestGit(t, canonical, "commit", "-m", "initial")
	return canonical
}

// TestBuildGraphSkipsUnreadableCloneEvenWithGoManifest is the end-to-end
// version of TestGoGraphDiscoveryFailureSkipsUnreadableCloneRegardlessOfGoManifest:
// it drives the real EnsureCanonical/git fetch failure path rather than
// calling the classifier directly, pinning that `wb deps graph --fleet`
// (and, by the same discovery path, `wb deps bump go --fleet`) no longer
// aborts the whole fleet over one repository whose local clone has no
// 'origin' remote configured — even though that repository has a go.mod and
// would otherwise be a hard blocker.
func TestBuildGraphSkipsUnreadableCloneEvenWithGoManifest(t *testing.T) {
	t.Parallel()
	fixture := t.TempDir()
	app := seedGraphRepository(t, fixture, "app", "main", map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.24\n",
	})
	broken := seedUnreadableCanonicalRepository(t, fixture, "payments", map[string]string{
		"go.mod": "module example.com/payments\n\ngo 1.24\n",
	})

	graph, err := BuildGraph(context.Background(), []Repository{
		{Slug: "acme/app", Path: app},
		{Slug: "acme/payments", Path: broken},
	}, GraphOptions{
		GitHubDir: filepath.Join(fixture, "projects"), Ref: "main",
		Parallel: 1, Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("an unreadable/remote-less clone must not abort the fleet graph: %v", err)
	}
	var modules []string
	for _, module := range graph.Modules {
		modules = append(modules, module.Path)
	}
	if !contains(modules, "example.com/app") {
		t.Fatalf("healthy repository missing from graph; modules = %v", modules)
	}
	if len(graph.DiscoverySkips) != 1 || graph.DiscoverySkips[0].Repository != "acme/payments" {
		t.Fatalf("discovery skips = %+v, want acme/payments skipped", graph.DiscoverySkips)
	}
	if !strings.Contains(graph.DiscoverySkips[0].Reason, "unreadable") {
		t.Fatalf("skip reason = %q, want it to explain the clone is unreadable", graph.DiscoverySkips[0].Reason)
	}
}

// TestBuildGraphSkipsUnreadableNpmCloneEvenWithPackageJSON is the npm
// ecosystem's half of the same regression.
func TestBuildGraphSkipsUnreadableNpmCloneEvenWithPackageJSON(t *testing.T) {
	t.Parallel()
	fixture := t.TempDir()
	app := seedGraphRepository(t, fixture, "app", "main", map[string]string{
		"package.json": `{"name": "@acme/app", "version": "1.0.0"}` + "\n",
	})
	broken := seedUnreadableCanonicalRepository(t, fixture, "payments", map[string]string{
		"package.json": `{"name": "@acme/payments", "version": "1.0.0"}` + "\n",
	})

	graph, err := BuildGraph(context.Background(), []Repository{
		{Slug: "acme/app", Path: app},
		{Slug: "acme/payments", Path: broken},
	}, GraphOptions{
		Ecosystem: EcosystemNPM, GitHubDir: filepath.Join(fixture, "projects"), Ref: "main",
		Parallel: 1, Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("an unreadable/remote-less clone must not abort the fleet npm graph: %v", err)
	}
	var modules []string
	for _, module := range graph.Modules {
		modules = append(modules, module.Path)
	}
	if !contains(modules, "@acme/app") {
		t.Fatalf("healthy repository missing from graph; modules = %v", modules)
	}
	if len(graph.DiscoverySkips) != 1 || graph.DiscoverySkips[0].Repository != "acme/payments" {
		t.Fatalf("discovery skips = %+v, want acme/payments skipped", graph.DiscoverySkips)
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
