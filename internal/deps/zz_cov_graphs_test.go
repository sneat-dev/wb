package deps

// Coverage-focused tests for the fleet-graph, ordering, and projection source
// files. Every fixture is hermetic: repositories are seeded under t.TempDir()
// with local bare remotes, and nothing touches the network or the ambient
// environment. Package-level helpers are prefixed depsCov to keep this file
// collision-free while other coverage files are written into the same package.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
)

// depsCovSeedOriginRepository creates a git repository with a single configured
// origin remote, so remoteOriginSlug can be exercised without a real clone.
func depsCovSeedOriginRepository(t *testing.T, root, name, url string) string {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, directory, "init", "-b", "main")
	runTestGit(t, directory, "remote", "add", "origin", url)
	return directory
}

// depsCovGitlinkRepository builds a committed repository whose named tree
// entries are broken gitlinks: `git ls-tree -r --name-only` lists them, but
// `git show <ref>:<path>` cannot materialize them. That is the only portable
// way to make a Git-tree manifest read fail after the tree listing succeeded.
func depsCovGitlinkRepository(t *testing.T, directory string, gitlinks ...string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, directory, "init", "-b", "main")
	runTestGit(t, directory, "config", "user.name", "WB Test")
	runTestGit(t, directory, "config", "user.email", "wb@example.test")
	writeTestFile(t, filepath.Join(directory, "README.md"), "# fixture\n")
	runTestGit(t, directory, "add", "-A")
	runTestGit(t, directory, "commit", "-m", "initial")
	const absentObject = "1111111111111111111111111111111111111111"
	for _, gitlink := range gitlinks {
		runTestGit(t, directory, "update-index", "--add", "--cacheinfo", "160000,"+absentObject+","+gitlink)
	}
	runTestGit(t, directory, "commit", "-m", "broken gitlinks")
}

// depsCovCommitRepository commits a small repository from a path/contents map
// so the Git-tree inspection functions can be called directly.
func depsCovCommitRepository(t *testing.T, directory string, files map[string]string) {
	t.Helper()
	for path, body := range files {
		writeTestFile(t, filepath.Join(directory, path), body)
	}
	runTestGit(t, directory, "init", "-b", "main")
	runTestGit(t, directory, "config", "user.name", "WB Test")
	runTestGit(t, directory, "config", "user.email", "wb@example.test")
	runTestGit(t, directory, "add", "-A")
	runTestGit(t, directory, "commit", "-m", "initial")
}

// depsCovCarrierCase describes one pendingCarriersBlockTargets fixture in the
// ecosystem-neutral shape both fleet graphs share.
type depsCovCarrierCase struct {
	name         string
	packages     map[string]string
	requirements map[string][]string
	carriers     []ReleaseObservation
	targets      map[string][]Target
	want         bool
}

func TestDepsCovGraphsNormalizeGraphDependencies(t *testing.T) {
	t.Parallel()
	filters, err := normalizeGraphDependencies(nil)
	if err != nil || len(filters) != 0 {
		t.Fatalf("normalizeGraphDependencies(nil) = %v, %v; want empty and nil", filters, err)
	}
	filters, err = normalizeGraphDependencies([]string{" b ", "a", "a", " b"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(filters, []string{"a", "b"}) {
		t.Fatalf("filters = %v, want trimmed, de-duplicated, sorted [a b]", filters)
	}
	if _, err := normalizeGraphDependencies([]string{"a", "   "}); err == nil || !strings.Contains(err.Error(), "dependency filters must not be empty") {
		t.Fatalf("blank dependency filter error = %v", err)
	}
}

func TestDepsCovGraphsBuildGraphRejectsInvalidOptions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if _, err := BuildGraph(ctx, nil, GraphOptions{Ecosystem: "maven"}); err == nil || !strings.Contains(err.Error(), "supports only the go and npm ecosystems") {
		t.Fatalf("unknown ecosystem error = %v", err)
	}
	if _, err := BuildGraph(ctx, nil, GraphOptions{Ecosystem: EcosystemGo, Dependencies: []string{" "}}); err == nil || !strings.Contains(err.Error(), "dependency filters must not be empty") {
		t.Fatalf("blank dependency filter error = %v", err)
	}
	for _, ecosystem := range []Ecosystem{EcosystemGo, EcosystemNPM} {
		_, err := BuildGraph(ctx, nil, GraphOptions{Ecosystem: ecosystem, GitHubDir: t.TempDir(), Parallel: -1})
		if err == nil || !strings.Contains(err.Error(), "parallelism must be at least 1") {
			t.Fatalf("%s graph accepted negative parallelism: %v", ecosystem, err)
		}
	}
}

func TestDepsCovGraphsDiscoverNpmFleetGraphInvalidSlugArchivedAndPathFallback(t *testing.T) {
	fixture := t.TempDir()
	githubDir := filepath.Join(fixture, "projects")
	seedNpmGraphRepository(t, fixture, githubDir, "apps", map[string]string{
		"package.json": "{\n  \"name\": \"@acme/apps\",\n  \"version\": \"1.0.0\"\n}\n",
	})

	var (
		progressMu sync.Mutex
		progress   []graphDiscoveryProgress
	)
	graph, err := discoverNpmFleetGraph(context.Background(), []Repository{
		// An empty Path must resolve to <GitHubDir>/<owner>/<name>.
		{Slug: "sneat-co/apps"},
		{Slug: "sneat-co/retired", Archived: true},
		{Slug: "bogus"},
	}, orchestrate.Options{GitHubDir: githubDir, Operation: "deps-cov", Ref: "main", Parallel: 1, Timeout: time.Minute},
		npmGraphDiscoveryPolicy{},
		func(item graphDiscoveryProgress) {
			progressMu.Lock()
			progress = append(progress, item)
			progressMu.Unlock()
		})
	if err == nil || !strings.Contains(err.Error(), `invalid repository slug "bogus"`) {
		t.Fatalf("invalid slug error = %v", err)
	}
	if len(graph.packages) != 1 {
		t.Fatalf("packages = %+v, want only the healthy repository", graph.packages)
	}
	if pkg := graph.packages["@acme/apps"]; pkg.Repository != "sneat-co/apps" || pkg.Manifest != "package.json" {
		t.Fatalf("package = %+v, want the clone resolved from GitHubDir", pkg)
	}
	if len(graph.repositoryPackages["sneat-co/apps"]) != 1 {
		t.Fatalf("repository packages = %+v", graph.repositoryPackages)
	}
	progressMu.Lock()
	defer progressMu.Unlock()
	if len(progress) != 3 || progress[len(progress)-1].RepositoriesCompleted != 3 || progress[len(progress)-1].RepositoriesTotal != 3 {
		t.Fatalf("progress = %+v, want every repository counted exactly once", progress)
	}
}

func TestDepsCovGraphsDiscoverGoFleetGraphInvalidSlugArchivedAndPathFallback(t *testing.T) {
	t.Parallel()
	fixture := t.TempDir()
	githubDir := filepath.Join(fixture, "projects")
	seedGraphRepository(t, fixture, "app", "main", map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.24\n",
	})

	graph, err := discoverGoFleetGraph(context.Background(), []Repository{
		{Slug: "acme/app"},
		{Slug: "acme/retired", Archived: true},
		{Slug: "bogus"},
	}, orchestrate.Options{GitHubDir: githubDir, Operation: "deps-cov", Ref: "main", Parallel: 1, Timeout: time.Minute},
		goGraphDiscoveryPolicy{}, nil)
	if err == nil || !strings.Contains(err.Error(), `invalid repository slug "bogus"`) {
		t.Fatalf("invalid slug error = %v", err)
	}
	if module, exists := graph.modules["example.com/app"]; !exists || module.Repository != "acme/app" {
		t.Fatalf("modules = %+v, want the GitHubDir-derived canonical path honoured", graph.modules)
	}
	if len(graph.repositoryModules["acme/app"]) != 1 {
		t.Fatalf("repository modules = %+v", graph.repositoryModules)
	}
}

func TestDepsCovGraphsClassifyNpmGraphDiscoveryFailure(t *testing.T) {
	t.Parallel()
	repository := "sneat-co/apps"
	cause := errors.New("origin/main is missing")
	withManifest := t.TempDir()
	writeTestFile(t, filepath.Join(withManifest, "package.json"), "{}\n")
	withoutManifest := t.TempDir()
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	skip, err := classifyNpmGraphDiscoveryFailure(repository, withManifest, cause, npmGraphDiscoveryPolicy{})
	if skip != nil || err == nil || !strings.Contains(err.Error(), repository) || !strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("policy disabled = %+v, %v; want the wrapped cause", skip, err)
	}

	skip, err = classifyNpmGraphDiscoveryFailure(repository, withManifest, errors.New("fatal: 'origin' does not appear to be a git repository"), npmGraphDiscoveryPolicy{SkipFailedNonNPM: true})
	if err != nil || skip == nil || skip.Repository != repository || !strings.Contains(skip.Reason, "unreadable") {
		t.Fatalf("unreadable clone = %+v, %v; want an unconditional skip", skip, err)
	}

	skip, err = classifyNpmGraphDiscoveryFailure(repository, missing, cause, npmGraphDiscoveryPolicy{SkipFailedNonNPM: true})
	if skip != nil || err == nil || !strings.Contains(err.Error(), "cannot prove failed repository is irrelevant to npm propagation") {
		t.Fatalf("unscannable local clone = %+v, %v; want an inspect failure", skip, err)
	}

	skip, err = classifyNpmGraphDiscoveryFailure(repository, withManifest, cause, npmGraphDiscoveryPolicy{SkipFailedNonNPM: true})
	if skip != nil || err == nil || !strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("manifest-bearing failure = %+v, %v; want a fatal error", skip, err)
	}

	skip, err = classifyNpmGraphDiscoveryFailure(repository, withoutManifest, cause, npmGraphDiscoveryPolicy{SkipFailedNonNPM: true})
	if err != nil || skip == nil || !strings.Contains(skip.Reason, "contains no package.json") {
		t.Fatalf("manifest-free failure = %+v, %v; want a reported skip", skip, err)
	}
}

func TestDepsCovGraphsClassifyGoGraphDiscoveryFailure(t *testing.T) {
	t.Parallel()
	repository := "acme/app"
	cause := errors.New("origin/main is missing")
	withManifest := t.TempDir()
	writeTestFile(t, filepath.Join(withManifest, "go.mod"), "module example.com/app\n")
	withoutManifest := t.TempDir()
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	skip, err := classifyGoGraphDiscoveryFailure(repository, withManifest, cause, goGraphDiscoveryPolicy{})
	if skip != nil || err == nil || !strings.Contains(err.Error(), repository) || !strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("policy disabled = %+v, %v; want the wrapped cause", skip, err)
	}

	skip, err = classifyGoGraphDiscoveryFailure(repository, withManifest, errors.New("fatal: not a git repository"), goGraphDiscoveryPolicy{SkipFailedNonGo: true})
	if err != nil || skip == nil || !strings.Contains(skip.Reason, "unreadable") {
		t.Fatalf("unreadable clone = %+v, %v; want an unconditional skip", skip, err)
	}

	skip, err = classifyGoGraphDiscoveryFailure(repository, missing, cause, goGraphDiscoveryPolicy{SkipFailedNonGo: true})
	if skip != nil || err == nil || !strings.Contains(err.Error(), "cannot prove failed repository is irrelevant to Go propagation") {
		t.Fatalf("unscannable local clone = %+v, %v; want an inspect failure", skip, err)
	}

	skip, err = classifyGoGraphDiscoveryFailure(repository, withManifest, cause, goGraphDiscoveryPolicy{SkipFailedNonGo: true})
	if skip != nil || err == nil {
		t.Fatalf("manifest-bearing failure = %+v, %v; want a fatal error", skip, err)
	}

	skip, err = classifyGoGraphDiscoveryFailure(repository, withoutManifest, cause, goGraphDiscoveryPolicy{SkipFailedNonGo: true})
	if err != nil || skip == nil || !strings.Contains(skip.Reason, "contains no go.mod") {
		t.Fatalf("manifest-free failure = %+v, %v; want a reported skip", skip, err)
	}
}

func TestDepsCovGraphsRepositoryContainsLocalManifests(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		files   []string
		wantNpm bool
		wantGo  bool
	}{
		{name: "root manifest", files: []string{"package.json", "go.mod"}, wantNpm: true, wantGo: true},
		{name: "nested manifest", files: []string{"sub/package.json", "sub/go.mod"}, wantNpm: true, wantGo: true},
		{name: "npm only", files: []string{"package.json"}, wantNpm: true, wantGo: false},
		{name: "node_modules skipped", files: []string{"node_modules/package.json", "node_modules/go.mod"}},
		{name: "vendor skipped", files: []string{"vendor/package.json", "vendor/go.mod"}},
		{name: ".git skipped", files: []string{".git/package.json", ".git/go.mod"}},
		{name: ".wb skipped", files: []string{".wb/package.json", ".wb/go.mod"}},
		{name: ".worktrees skipped", files: []string{".worktrees/package.json", ".worktrees/go.mod"}},
		{name: "testdata ignored", files: []string{"testdata/package.json", "testdata/go.mod"}},
		{name: "dist ignored", files: []string{"dist/package.json", "dist/go.mod"}},
		{name: "empty", files: nil},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			for _, name := range testCase.files {
				writeTestFile(t, filepath.Join(root, name), "fixture\n")
			}
			gotNpm, err := repositoryContainsLocalNpmManifest(root)
			if err != nil || gotNpm != testCase.wantNpm {
				t.Fatalf("repositoryContainsLocalNpmManifest() = %t, %v; want %t, nil", gotNpm, err, testCase.wantNpm)
			}
			gotGo, err := repositoryContainsLocalGoManifest(root)
			if err != nil || gotGo != testCase.wantGo {
				t.Fatalf("repositoryContainsLocalGoManifest() = %t, %v; want %t, nil", gotGo, err, testCase.wantGo)
			}
		})
	}
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := repositoryContainsLocalNpmManifest(missing); err == nil {
		t.Fatal("a missing root must be reported as an error for npm")
	}
	if _, err := repositoryContainsLocalGoManifest(missing); err == nil {
		t.Fatal("a missing root must be reported as an error for Go")
	}
}

func TestDepsCovGraphsInspectRepositoryManifestTreePaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	options := orchestrate.Options{Timeout: time.Minute}

	t.Run("npm base ref missing", func(t *testing.T) {
		t.Parallel()
		directory := filepath.Join(t.TempDir(), "repo")
		depsCovCommitRepository(t, directory, map[string]string{"package.json": "{}\n"})
		if _, err := inspectRepositoryNpmGraph(ctx, "acme/repo", directory, "origin/missing", options); err == nil {
			t.Fatal("an unresolvable base ref must fail npm inspection")
		}
	})
	t.Run("npm package.json cannot be shown", func(t *testing.T) {
		t.Parallel()
		directory := filepath.Join(t.TempDir(), "repo")
		depsCovGitlinkRepository(t, directory, "package.json")
		if _, err := inspectRepositoryNpmGraph(ctx, "acme/repo", directory, "main", options); err == nil {
			t.Fatal("a package.json tree entry git cannot materialize must fail npm inspection")
		}
	})
	t.Run("npm pnpm-workspace.yaml cannot be shown", func(t *testing.T) {
		t.Parallel()
		directory := filepath.Join(t.TempDir(), "repo")
		depsCovGitlinkRepository(t, directory, "pnpm-workspace.yaml")
		if _, err := inspectRepositoryNpmGraph(ctx, "acme/repo", directory, "main", options); err == nil {
			t.Fatal("a pnpm-workspace.yaml tree entry git cannot materialize must fail npm inspection")
		}
	})
	t.Run("npm package.json does not parse", func(t *testing.T) {
		t.Parallel()
		directory := filepath.Join(t.TempDir(), "repo")
		depsCovCommitRepository(t, directory, map[string]string{"package.json": "{ this is not JSON }\n"})
		_, err := inspectRepositoryNpmGraph(ctx, "acme/repo", directory, "main", options)
		if err == nil || !strings.Contains(err.Error(), "parse package.json") {
			t.Fatalf("unparseable package.json error = %v", err)
		}
	})
	t.Run("go base ref missing", func(t *testing.T) {
		t.Parallel()
		directory := filepath.Join(t.TempDir(), "repo")
		depsCovCommitRepository(t, directory, map[string]string{"go.mod": "module example.com/repo\n\ngo 1.24\n"})
		if _, err := inspectRepositoryGoGraph(ctx, "acme/repo", directory, "origin/missing", options); err == nil {
			t.Fatal("an unresolvable base ref must fail Go inspection")
		}
	})
	t.Run("go go.mod cannot be shown", func(t *testing.T) {
		t.Parallel()
		directory := filepath.Join(t.TempDir(), "repo")
		depsCovGitlinkRepository(t, directory, "go.mod")
		if _, err := inspectRepositoryGoGraph(ctx, "acme/repo", directory, "main", options); err == nil {
			t.Fatal("a go.mod tree entry git cannot materialize must fail Go inspection")
		}
	})
	t.Run("go root go.mod declares no module path", func(t *testing.T) {
		t.Parallel()
		directory := filepath.Join(t.TempDir(), "repo")
		depsCovCommitRepository(t, directory, map[string]string{"go.mod": "go 1.24\n"})
		_, err := inspectRepositoryGoGraph(ctx, "acme/repo", directory, "main", options)
		if err == nil || !strings.Contains(err.Error(), "has no module path") {
			t.Fatalf("module-less root go.mod error = %v", err)
		}
	})
}

func TestDepsCovGraphsParseNpmManifests(t *testing.T) {
	t.Parallel()
	if _, _, err := parseNpmPackageJSONManifest("acme/app", "package.json", []byte("{ not json")); err == nil {
		t.Fatal("invalid package.json must fail to parse")
	}
	contents := `{
  "name": "@acme/app",
  "version": "1.2.3",
  "dependencies": {"@acme/zeta": "1.0.0", "@acme/alpha": "2.0.0"},
  "devDependencies": {"@acme/dev": "3.0.0"},
  "peerDependencies": {"@acme/peer": "4.0.0"},
  "optionalDependencies": {"@acme/optional": "5.0.0"}
}`
	pkg, requirements, err := parseNpmPackageJSONManifest("acme/app", "package.json", []byte(contents))
	if err != nil {
		t.Fatal(err)
	}
	if pkg == nil || pkg.Name != "@acme/app" || pkg.Version != "1.2.3" || pkg.Repository != "acme/app" || pkg.Manifest != "package.json" {
		t.Fatalf("package = %+v", pkg)
	}
	want := []struct{ dependency, version, field string }{
		{"@acme/alpha", "2.0.0", "dependencies"},
		{"@acme/zeta", "1.0.0", "dependencies"},
		{"@acme/dev", "3.0.0", "devDependencies"},
		{"@acme/peer", "4.0.0", "peerDependencies"},
		{"@acme/optional", "5.0.0", "optionalDependencies"},
	}
	if len(requirements) != len(want) {
		t.Fatalf("requirements = %+v, want %d", requirements, len(want))
	}
	for index, expected := range want {
		got := requirements[index]
		if got.Dependency != expected.dependency || got.Version != expected.version || got.Field != expected.field {
			t.Errorf("requirement %d = %+v, want %+v", index, got, expected)
		}
		if got.ConsumerPackage != "@acme/app" || got.Repository != "acme/app" || got.Manifest != "package.json" {
			t.Errorf("requirement %d ownership = %+v", index, got)
		}
	}

	privatePackage, privateRequirements, err := parseNpmPackageJSONManifest("acme/private", "package.json",
		[]byte(`{"name":"@acme/private","private":true,"dependencies":{"@acme/core":"1.0.0"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if privatePackage != nil || len(privateRequirements) != 1 {
		t.Fatalf("private package = %+v, requirements = %+v; want no publish identity but the requirement", privatePackage, privateRequirements)
	}
	unnamed, unnamedRequirements, err := parseNpmPackageJSONManifest("acme/unnamed", "package.json",
		[]byte(`{"dependencies":{"@acme/core":"1.0.0"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if unnamed != nil || len(unnamedRequirements) != 1 {
		t.Fatalf("unnamed package = %+v, requirements = %+v; want no publish identity but the requirement", unnamed, unnamedRequirements)
	}
}

func TestDepsCovGraphsParseNpmWorkspaceRequirements(t *testing.T) {
	t.Parallel()
	manifest := "overrides:\n" +
		"  \"@acme/core\": \"1.0.0\"\n" +
		"catalog:\n" +
		"  \"@acme/ui\": \"2.0.0\"\n" +
		"catalogs:\n" +
		"  legacy:\n" +
		"    \"@acme/legacy\": \"3.0.0\"\n"
	requirements := parseNpmWorkspaceRequirements("sneat-co/ops", "pnpm-workspace.yaml", []byte(manifest))
	want := []struct{ dependency, version, field string }{
		{"@acme/core", "1.0.0", "pnpm-override"},
		{"@acme/ui", "2.0.0", "pnpm-catalog"},
		{"@acme/legacy", "3.0.0", "pnpm-catalog:legacy"},
	}
	if len(requirements) != len(want) {
		t.Fatalf("requirements = %+v, want %d", requirements, len(want))
	}
	for index, expected := range want {
		got := requirements[index]
		if got.Dependency != expected.dependency || got.Version != expected.version || got.Field != expected.field {
			t.Errorf("requirement %d = %+v, want %+v", index, got, expected)
		}
		if got.ConsumerPackage != "" || got.Repository != "sneat-co/ops" || got.Manifest != "pnpm-workspace.yaml" {
			t.Errorf("requirement %d identity = %+v", index, got)
		}
	}
}

func TestDepsCovGraphsNpmFleetGraphHelpers(t *testing.T) {
	t.Parallel()
	dependencyGraph := npmFleetGraph{requirements: map[string][]npmFleetRequirement{
		"@acme/core": {
			{Dependency: "@acme/core", Version: "1.0.0", ConsumerPackage: "@acme/app", Repository: "sneat-co/apps", Manifest: "package.json"},
			{Dependency: "@acme/core", Version: "1.0.0", ConsumerPackage: "", Repository: "sneat-co/ops", Manifest: "pnpm-workspace.yaml"},
		},
	}}
	got := dependencyGraph.requirementsForDependency("@acme/core")
	wantFleet := []fleetRequirement{
		{ConsumerModule: "@acme/app", Repository: "sneat-co/apps", Version: "1.0.0"},
		{ConsumerModule: "sneat-co/ops (pnpm workspace)", Repository: "sneat-co/ops", Version: "1.0.0"},
	}
	if !reflect.DeepEqual(got, wantFleet) {
		t.Fatalf("requirementsForDependency = %+v, want %+v", got, wantFleet)
	}

	eventGraph := npmFleetGraph{requirements: map[string][]npmFleetRequirement{
		"@acme/a": {{Dependency: "@acme/a", Version: "0.9.0", Repository: "sneat-co/apps", Manifest: "package.json"}},
		"@acme/b": {{Dependency: "@acme/b", Version: "0.9.0", Repository: "sneat-co/apps", Manifest: "package.json"}},
		"@acme/c": {{Dependency: "@acme/c", Version: "2.0.0", Repository: "sneat-co/apps", Manifest: "package.json"}},
	}}
	targets := eventGraph.repositoriesForEvents([]ReleaseEvent{
		{Dependency: "@acme/a", Version: "1.0.0"},
		{Dependency: "@acme/b", Version: "1.0.0"},
		{Dependency: "@acme/c", Version: "2.0.0"},
	})
	if len(targets) != 1 || len(targets["sneat-co/apps"]) != 2 {
		t.Fatalf("targets = %+v, want the already-current dependency skipped", targets)
	}
	if dependencies := []string{targets["sneat-co/apps"][0].Dependency, targets["sneat-co/apps"][1].Dependency}; !reflect.DeepEqual(dependencies, []string{"@acme/a", "@acme/b"}) {
		t.Fatalf("target order = %v, want sorted dependencies", dependencies)
	}

	adjacencyGraph := npmFleetGraph{
		packages: map[string]npmFleetPackage{"@acme/core": {Name: "@acme/core", Repository: "sneat-co/libs", Manifest: "package.json"}},
		requirements: map[string][]npmFleetRequirement{
			"@acme/core": {
				{Dependency: "@acme/core", Repository: "sneat-co/libs", Manifest: "package.json"},
				{Dependency: "@acme/core", Repository: "sneat-co/apps", Manifest: "package.json"},
			},
			"@acme/external": {{Dependency: "@acme/external", Repository: "sneat-co/apps", Manifest: "package.json"}},
		},
	}
	adjacency := adjacencyGraph.repositoryAdjacency()
	if !reflect.DeepEqual(adjacency, map[string][]string{"sneat-co/libs": {"sneat-co/apps"}}) {
		t.Fatalf("repositoryAdjacency = %+v, want only the cross-repository internal edge", adjacency)
	}

	versionGraph := npmFleetGraph{requirements: map[string][]npmFleetRequirement{
		"@acme/core": {
			{Dependency: "@acme/core", Version: "2.0.0", Repository: "sneat-co/apps", Manifest: "package.json"},
			{Dependency: "@acme/core", Version: "1.0.0", Repository: "sneat-co/other", Manifest: "package.json"},
		},
	}}
	affected := versionGraph.affectedModules(map[string][]Target{
		"sneat-co/apps": {{Ecosystem: EcosystemNPM, Dependency: "@acme/core", Version: "2.0.0"}},
	})
	if !reflect.DeepEqual(affected, map[string]map[string]bool{}) {
		t.Fatalf("affectedModules = %+v, want same-version and other-repository requirements skipped", affected)
	}
	workspaceGraph := npmFleetGraph{requirements: map[string][]npmFleetRequirement{
		"@acme/core": {
			{Dependency: "@acme/core", Version: "1.0.0", Repository: "sneat-co/apps", Manifest: "pnpm-workspace.yaml"},
			{Dependency: "@acme/core", Version: "1.0.0", Repository: "sneat-co/other", Manifest: "pnpm-workspace.yaml"},
		},
	}}
	affected = workspaceGraph.affectedModules(map[string][]Target{
		"sneat-co/apps": {{Ecosystem: EcosystemNPM, Dependency: "@acme/core", Version: "2.0.0"}},
	})
	if !reflect.DeepEqual(affected, map[string]map[string]bool{"sneat-co/apps": {"sneat-co/apps (pnpm workspace)": true}}) {
		t.Fatalf("workspace affectedModules = %+v", affected)
	}
}

func TestDepsCovGraphsGoFleetGraphHelpers(t *testing.T) {
	t.Parallel()
	declaration := func(repository string) goFleetModule {
		return goFleetModule{Path: "github.com/acme/lib", Repository: repository, Manifest: "go.mod"}
	}
	single := declaration("acme/lib")
	if got, ok := canonicalGoModuleDeclaration("github.com/acme/lib", []goFleetModule{single}); !ok || got.Repository != "acme/lib" {
		t.Fatalf("single declaration = %+v, %t", got, ok)
	}
	if _, ok := canonicalGoModuleDeclaration("example.com/local", []goFleetModule{declaration("acme/one"), declaration("acme/two")}); ok {
		t.Fatal("a non-github module path has no owner/repository convention to resolve")
	}
	if _, ok := canonicalGoModuleDeclaration("github.com/acme", []goFleetModule{declaration("acme/one"), declaration("acme/two")}); ok {
		t.Fatal("a module path with no repository segment must not resolve")
	}
	if got, ok := canonicalGoModuleDeclaration("github.com/acme/lib", []goFleetModule{declaration("acme/other"), declaration("acme/lib")}); !ok || got.Repository != "acme/lib" {
		t.Fatalf("matching declaration = %+v, %t", got, ok)
	}
	if _, ok := canonicalGoModuleDeclaration("github.com/acme/lib", []goFleetModule{declaration("acme/other"), declaration("acme/third")}); ok {
		t.Fatal("a module no declaration names must stay unresolved")
	}

	adjacencyGraph := goFleetGraph{
		modules: map[string]goFleetModule{"example.com/a": {Path: "example.com/a", Repository: "acme/a", Manifest: "go.mod"}},
		requirements: map[string][]goFleetRequirement{
			"example.com/a": {
				{Dependency: "example.com/a", Repository: "acme/a", Manifest: "go.mod"},
				{Dependency: "example.com/a", Repository: "acme/b", Manifest: "go.mod"},
			},
			"example.com/unknown": {{Dependency: "example.com/unknown", Repository: "acme/c", Manifest: "go.mod"}},
		},
	}
	if adjacency := adjacencyGraph.repositoryAdjacency(); !reflect.DeepEqual(adjacency, map[string][]string{"acme/a": {"acme/b"}}) {
		t.Fatalf("repositoryAdjacency = %+v, want only the cross-repository internal edge", adjacency)
	}

	consumerGraph := goFleetGraph{requirements: map[string][]goFleetRequirement{
		"example.com/a": {{Dependency: "example.com/a", Repository: "acme/a", Manifest: "go.mod"}},
	}}
	if consumerGraph.hasExternalConsumers("example.com/absent", "acme/a") {
		t.Fatal("an unknown module has no external consumers")
	}
	if consumerGraph.hasExternalConsumers("example.com/a", "acme/a") {
		t.Fatal("a module required only by its own repository has no external consumers")
	}
	consumerGraph.requirements["example.com/a"] = append(consumerGraph.requirements["example.com/a"], goFleetRequirement{Dependency: "example.com/a", Repository: "acme/other", Manifest: "go.mod"})
	if !consumerGraph.hasExternalConsumers("example.com/a", "acme/a") {
		t.Fatal("a module required by another repository has external consumers")
	}
}

func TestDepsCovGraphsRemoteOriginSlug(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	options := orchestrate.Options{Timeout: time.Minute}
	cases := []struct {
		name string
		url  string
		want string
	}{
		{name: "ssh github", url: "git@github.com:acme/lib.git", want: "acme/lib"},
		{name: "https github", url: "https://github.com/acme/lib.git", want: "acme/lib"},
		{name: "ssh url github", url: "ssh://git@github.com/acme/lib.git", want: "acme/lib"},
		{name: "filesystem remote", url: filepath.Join(root, "remotes", "acme", "lib.git"), want: "acme/lib"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			directory := depsCovSeedOriginRepository(t, root, testCase.name, testCase.url)
			got, err := remoteOriginSlug(context.Background(), directory, options)
			if err != nil || got != testCase.want {
				t.Fatalf("remoteOriginSlug(%q) = %q, %v; want %q", testCase.url, got, err, testCase.want)
			}
		})
	}
	for _, testCase := range []struct {
		name string
		url  string
		want string
	}{
		{name: "single segment", url: "justname", want: "cannot derive owner/repository from origin"},
		{name: "empty owner", url: "//lib", want: "origin remote does not identify owner/repository"},
		{name: "github without repository", url: "git@github.com:lib", want: "origin remote does not identify owner/repository"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			directory := depsCovSeedOriginRepository(t, root, testCase.name, testCase.url)
			if _, err := remoteOriginSlug(context.Background(), directory, options); err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("remoteOriginSlug(%q) error = %v, want %q", testCase.url, err, testCase.want)
			}
		})
	}
	t.Run("not a repository", func(t *testing.T) {
		t.Parallel()
		if _, err := remoteOriginSlug(context.Background(), filepath.Join(root, "absent"), options); err == nil {
			t.Fatal("a directory that is not a git repository must fail remote resolution")
		}
	})
}

func TestDepsCovGraphsResolveDuplicateCloneModuleDeclaration(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	files := map[string]string{"go.mod": "module github.com/acme/lib\n\ngo 1.24\n"}
	// One self-consistent clone and one stale duplicate of the same remote,
	// cloned under a directory that no longer matches its remote identity.
	seedBumpRemoteClone(t, root, githubDir, "acme", "lib", "acme", "lib", files)
	seedBumpRemoteClone(t, root, githubDir, "acme", "lib", "acme", "lib-old", files)
	seedBumpRemoteClone(t, root, githubDir, "acme", "other", "acme", "other", map[string]string{"go.mod": "module github.com/acme/other\n\ngo 1.24\n"})
	options := orchestrate.Options{GitHubDir: githubDir, Timeout: time.Minute}
	ctx := context.Background()

	resolved, ok := resolveDuplicateCloneModuleDeclaration(ctx, []goFleetModule{
		{Path: "github.com/acme/lib", Repository: "acme/lib", Manifest: "go.mod"},
		{Path: "github.com/acme/lib", Repository: "acme/lib-old", Manifest: "go.mod"},
		{Path: "github.com/acme/lib", Repository: "no-slash", Manifest: "go.mod"},
		{Path: "github.com/acme/lib", Repository: "acme/absent", Manifest: "go.mod"},
	}, options)
	if !ok || resolved.Repository != "acme/lib" {
		t.Fatalf("stale duplicate resolution = %+v, %t; want the self-consistent acme/lib", resolved, ok)
	}

	if _, ok := resolveDuplicateCloneModuleDeclaration(ctx, []goFleetModule{
		{Path: "github.com/acme/lib", Repository: "acme/lib", Manifest: "go.mod"},
		{Path: "github.com/acme/lib", Repository: "acme/other", Manifest: "go.mod"},
	}, options); ok {
		t.Fatal("two self-consistent declarations are a genuine coincidence, not a stale duplicate")
	}
	if _, ok := resolveDuplicateCloneModuleDeclaration(ctx, []goFleetModule{
		{Path: "github.com/acme/lib", Repository: "no-slash", Manifest: "go.mod"},
		{Path: "github.com/acme/lib", Repository: "acme/absent", Manifest: "go.mod"},
	}, options); ok {
		t.Fatal("no self-consistent declaration must leave the tie unresolved")
	}
}

func TestDepsCovGraphsPendingCarriersBlockTargets(t *testing.T) {
	t.Parallel()
	targetList := []Target{{Ecosystem: EcosystemGo, Dependency: "example.com/dep", Version: "v1.0.0"}}
	cases := []depsCovCarrierCase{
		{
			name:     "no targets always blocks",
			carriers: []ReleaseObservation{{Repository: "acme/app"}},
			want:     true,
		},
		{
			name:    "no carriers never blocks",
			targets: map[string][]Target{"acme/x": targetList},
			want:    false,
		},
		{
			name:     "released carrier with a successor is skipped",
			carriers: []ReleaseObservation{{Repository: "acme/app", Status: "released", After: "v1.0.0"}},
			targets:  map[string][]Target{"acme/x": targetList},
			want:     false,
		},
		{
			name:     "released carrier without a successor is still pending",
			carriers: []ReleaseObservation{{Repository: "acme/app", Status: "released"}},
			targets:  map[string][]Target{"acme/app": targetList},
			want:     false,
		},
		{
			name:         "carrier upstream of a target blocks downstream work",
			packages:     map[string]string{"example.com/a": "acme/app", "example.com/b": "acme/x"},
			requirements: map[string][]string{"example.com/a": {"acme/x"}},
			carriers:     []ReleaseObservation{{Repository: "acme/app"}},
			targets:      map[string][]Target{"acme/x": targetList},
			want:         true,
		},
		{
			name:     "carrier owning a target but nothing downstream does not block",
			carriers: []ReleaseObservation{{Repository: "acme/app"}},
			targets:  map[string][]Target{"acme/app": targetList},
			want:     false,
		},
		{
			name:         "a cycle that never reaches a target does not block",
			packages:     map[string]string{"example.com/p1": "acme/a", "example.com/p2": "acme/b"},
			requirements: map[string][]string{"example.com/p1": {"acme/b"}, "example.com/p2": {"acme/a"}},
			carriers:     []ReleaseObservation{{Repository: "acme/a"}},
			targets:      map[string][]Target{"acme/c": targetList},
			want:         false,
		},
	}
	npmFleet := func(packages map[string]string, requirements map[string][]string) npmFleetGraph {
		graph := npmFleetGraph{packages: map[string]npmFleetPackage{}, requirements: map[string][]npmFleetRequirement{}}
		for name, repository := range packages {
			graph.packages[name] = npmFleetPackage{Name: name, Repository: repository, Manifest: "package.json"}
		}
		for dependency, repositories := range requirements {
			for _, repository := range repositories {
				graph.requirements[dependency] = append(graph.requirements[dependency], npmFleetRequirement{
					Dependency: dependency, Repository: repository, Manifest: "package.json",
				})
			}
		}
		return graph
	}
	goFleet := func(modules map[string]string, requirements map[string][]string) goFleetGraph {
		graph := goFleetGraph{modules: map[string]goFleetModule{}, requirements: map[string][]goFleetRequirement{}}
		for path, repository := range modules {
			graph.modules[path] = goFleetModule{Path: path, Repository: repository, Manifest: "go.mod"}
		}
		for dependency, repositories := range requirements {
			for _, repository := range repositories {
				graph.requirements[dependency] = append(graph.requirements[dependency], goFleetRequirement{
					Dependency: dependency, Repository: repository, Manifest: "go.mod",
				})
			}
		}
		return graph
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := npmFleet(testCase.packages, testCase.requirements).pendingCarriersBlockTargets(testCase.carriers, testCase.targets); got != testCase.want {
				t.Errorf("npm pendingCarriersBlockTargets = %t, want %t", got, testCase.want)
			}
			if got := goFleet(testCase.packages, testCase.requirements).pendingCarriersBlockTargets(testCase.carriers, testCase.targets); got != testCase.want {
				t.Errorf("go pendingCarriersBlockTargets = %t, want %t", got, testCase.want)
			}
		})
	}
}

func TestDepsCovGraphsBuildGraphNpmSortsDiscoveryEvidence(t *testing.T) {
	t.Parallel()
	fixture := t.TempDir()
	githubDir := filepath.Join(fixture, "projects")
	deadA := seedUnreadableCanonicalRepository(t, fixture, "dead-a", map[string]string{
		"package.json": "{\n  \"name\": \"@acme/dead-a\"\n}\n",
	})
	deadB := seedUnreadableCanonicalRepository(t, fixture, "dead-b", map[string]string{
		"package.json": "{\n  \"name\": \"@acme/dead-b\"\n}\n",
	})
	legacyA := seedNpmGraphRepository(t, fixture, githubDir, "legacy-a", map[string]string{
		"package.json": "{\n  \"name\": \"@acme/legacy-a\"\n}\n",
	})
	legacyB := seedGraphRepository(t, fixture, "legacy-b", "master", map[string]string{
		"package.json": "{\n  \"name\": \"@acme/legacy-b\"\n}\n",
	})
	legacyC := seedGraphRepository(t, fixture, "legacy-c", "master", map[string]string{
		"package.json": "{\n  \"name\": \"@acme/legacy-c\"\n}\n",
	})
	libs := seedNpmGraphRepository(t, fixture, githubDir, "libs", map[string]string{
		"package.json":                "{\n  \"name\": \"@acme/dup\"\n}\n",
		"packages/inner/package.json": "{\n  \"name\": \"@acme/dup\"\n}\n",
	})
	libsCopy := seedNpmGraphRepository(t, fixture, githubDir, "libs-copy", map[string]string{
		"package.json": "{\n  \"name\": \"@acme/dup\"\n}\n",
	})
	apps := seedNpmGraphRepository(t, fixture, githubDir, "apps", map[string]string{
		"package.json": "{\n  \"name\": \"@acme/apps\",\n  \"dependencies\": {\n    \"@acme/dup\": \"1.0.0\"\n  }\n}\n",
	})

	graph, err := BuildGraph(context.Background(), []Repository{
		{Slug: "acme/dead-a", Path: deadA},
		{Slug: "acme/dead-b", Path: deadB},
		{Slug: "sneat-co/legacy-a", Path: legacyA},
		{Slug: "acme/legacy-b", Path: legacyB},
		{Slug: "acme/legacy-c", Path: legacyC},
		{Slug: "sneat-co/libs", Path: libs},
		{Slug: "sneat-co/libs-copy", Path: libsCopy},
		{Slug: "sneat-co/apps", Path: apps},
	}, GraphOptions{Ecosystem: EcosystemNPM, GitHubDir: githubDir, Ref: "main", Parallel: 1, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.DiscoverySkips) != 2 || graph.DiscoverySkips[0].Repository != "acme/dead-a" || graph.DiscoverySkips[1].Repository != "acme/dead-b" {
		t.Fatalf("discovery skips = %+v, want two sorted unreadable clones", graph.DiscoverySkips)
	}
	if len(graph.DefaultBranchFallbacks) != 2 ||
		graph.DefaultBranchFallbacks[0].Repository != "acme/legacy-b" || graph.DefaultBranchFallbacks[0].Ref != "master" ||
		graph.DefaultBranchFallbacks[1].Repository != "acme/legacy-c" || graph.DefaultBranchFallbacks[1].Ref != "master" {
		t.Fatalf("default branch fallbacks = %+v, want two sorted master-default repositories", graph.DefaultBranchFallbacks)
	}
	if graph.Summary.AmbiguousProviders != 1 {
		t.Fatalf("summary = %+v, want exactly one ambiguous npm provider", graph.Summary)
	}
	var duplicated *GraphRequirement
	for index := range graph.Requirements {
		if graph.Requirements[index].Dependency == "@acme/dup" {
			duplicated = &graph.Requirements[index]
		}
	}
	if duplicated == nil || duplicated.ProviderRepository != "" {
		t.Fatalf("duplicated requirement = %+v, want an unresolved provider", duplicated)
	}
	wantCandidates := []string{
		"sneat-co/libs-copy:package.json",
		"sneat-co/libs:package.json",
		"sneat-co/libs:packages/inner/package.json",
	}
	if !reflect.DeepEqual(duplicated.ProviderCandidates, wantCandidates) {
		t.Fatalf("provider candidates = %v, want %v", duplicated.ProviderCandidates, wantCandidates)
	}
}

func TestDepsCovGraphsBuildGraphNpmFailsOnUnparseableRootManifest(t *testing.T) {
	t.Parallel()
	fixture := t.TempDir()
	githubDir := filepath.Join(fixture, "projects")
	broken := seedGraphRepository(t, fixture, "bad-pkg", "main", map[string]string{
		"package.json": "{ this is not JSON }\n",
	})
	_, err := BuildGraph(context.Background(), []Repository{{Slug: "acme/bad-pkg", Path: broken}}, GraphOptions{
		Ecosystem: EcosystemNPM, GitHubDir: githubDir, Ref: "main", Parallel: 1, Timeout: time.Minute,
	})
	if err == nil || !strings.Contains(err.Error(), "acme/bad-pkg") || !strings.Contains(err.Error(), "parse package.json") {
		t.Fatalf("unparseable root package.json error = %v, want the repository and manifest named", err)
	}
}

func TestDepsCovGraphsBuildGraphGoSortsDiscoveryEvidence(t *testing.T) {
	t.Parallel()
	fixture := t.TempDir()
	githubDir := filepath.Join(fixture, "projects")
	healthy := seedGraphRepository(t, fixture, "healthy", "main", map[string]string{
		"go.mod": "module example.com/healthy\n\ngo 1.24\n",
	})
	multiWarn := seedGraphRepository(t, fixture, "warn-multi", "main", map[string]string{
		"go.mod":         "module example.com/warn-multi\n\ngo 1.24\n",
		"tools/a/go.mod": "module <%= family %>\n",
		"tools/b/go.mod": "module <%= family %>\n",
	})
	singleWarn := seedGraphRepository(t, fixture, "warn-single", "main", map[string]string{
		"go.mod":         "module example.com/warn-single\n\ngo 1.24\n",
		"tools/c/go.mod": "module <%= family %>\n",
	})
	legacyA := seedGraphRepository(t, fixture, "legacy-a", "master", map[string]string{
		"go.mod": "module example.com/legacy-a\n\ngo 1.24\n",
	})
	legacyB := seedGraphRepository(t, fixture, "legacy-b", "master", map[string]string{
		"go.mod": "module example.com/legacy-b\n\ngo 1.24\n",
	})
	deadA := seedUnreadableCanonicalRepository(t, fixture, "dead-a", map[string]string{
		"go.mod": "module example.com/dead-a\n\ngo 1.24\n",
	})
	deadB := seedUnreadableCanonicalRepository(t, fixture, "dead-b", map[string]string{
		"go.mod": "module example.com/dead-b\n\ngo 1.24\n",
	})

	graph, err := BuildGraph(context.Background(), []Repository{
		{Slug: "acme/healthy", Path: healthy},
		{Slug: "acme/warn-multi", Path: multiWarn},
		{Slug: "acme/warn-single", Path: singleWarn},
		{Slug: "acme/legacy-a", Path: legacyA},
		{Slug: "acme/legacy-b", Path: legacyB},
		{Slug: "acme/dead-a", Path: deadA},
		{Slug: "acme/dead-b", Path: deadB},
	}, GraphOptions{GitHubDir: githubDir, Ref: "main", Parallel: 1, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.DiscoverySkips) != 2 || graph.DiscoverySkips[0].Repository != "acme/dead-a" || graph.DiscoverySkips[1].Repository != "acme/dead-b" {
		t.Fatalf("discovery skips = %+v, want two sorted unreadable clones", graph.DiscoverySkips)
	}
	if len(graph.DefaultBranchFallbacks) != 2 || graph.DefaultBranchFallbacks[0].Repository != "acme/legacy-a" || graph.DefaultBranchFallbacks[1].Repository != "acme/legacy-b" {
		t.Fatalf("default branch fallbacks = %+v, want two sorted master-default repositories", graph.DefaultBranchFallbacks)
	}
	if len(graph.ManifestWarnings) != 3 {
		t.Fatalf("manifest warnings = %+v, want three", graph.ManifestWarnings)
	}
	wantWarnings := []struct{ repository, manifest string }{
		{"acme/warn-multi", "tools/a/go.mod"},
		{"acme/warn-multi", "tools/b/go.mod"},
		{"acme/warn-single", "tools/c/go.mod"},
	}
	for index, expected := range wantWarnings {
		got := graph.ManifestWarnings[index]
		if got.Repository != expected.repository || got.Manifest != expected.manifest {
			t.Errorf("warning %d = %+v, want %+v", index, got, expected)
		}
		if !strings.Contains(got.Reason, "code-generator template") {
			t.Errorf("warning %d reason = %q", index, got.Reason)
		}
	}
}

func TestDepsCovGraphsBuildGraphGoResolvesDuplicateModuleDeclarations(t *testing.T) {
	t.Parallel()
	fixture := t.TempDir()
	githubDir := filepath.Join(fixture, "projects")
	seed := func(name, module string) string {
		return seedGraphRepository(t, fixture, name, "main", map[string]string{
			"go.mod": "module " + module + "\n\ngo 1.24\n",
		})
	}
	lib := seed("lib", "github.com/acme/lib")
	libCopy := seed("lib-copy", "github.com/acme/lib")
	other := seed("other", "github.com/acme/other")
	otherCopy := seed("other-copy", "github.com/acme/other")
	graph, err := BuildGraph(context.Background(), []Repository{
		{Slug: "acme/lib", Path: lib},
		{Slug: "acme/lib-copy", Path: libCopy},
		{Slug: "acme/other", Path: other},
		{Slug: "acme/other-copy", Path: otherCopy},
	}, GraphOptions{GitHubDir: githubDir, Ref: "main", Parallel: 1, Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(graph.AmbiguousModules) != 2 {
		t.Fatalf("ambiguous modules = %+v, want two resolved conflicts", graph.AmbiguousModules)
	}
	if first := graph.AmbiguousModules[0]; first.Module != "github.com/acme/lib" || first.Repository != "acme/lib" || !reflect.DeepEqual(first.Duplicates, []string{"acme/lib-copy:go.mod"}) {
		t.Fatalf("first ambiguous module = %+v", first)
	}
	if second := graph.AmbiguousModules[1]; second.Module != "github.com/acme/other" || second.Repository != "acme/other" {
		t.Fatalf("second ambiguous module = %+v", second)
	}
}

func TestDepsCovGraphsBuildGraphGoSortsSameRepositoryManifests(t *testing.T) {
	t.Parallel()
	fixture := t.TempDir()
	githubDir := filepath.Join(fixture, "projects")
	twin := seedGraphRepository(t, fixture, "twin", "main", map[string]string{
		"go.mod":     "module example.com/twin\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n",
		"sub/go.mod": "module example.com/twin\n\ngo 1.24\n\nrequire example.com/dep v1.2.0\n",
	})
	graph, err := BuildGraph(context.Background(), []Repository{{Slug: "acme/twin", Path: twin}}, GraphOptions{
		GitHubDir: githubDir, Ref: "main", Parallel: 1, Timeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	var requirements []GraphRequirement
	for _, requirement := range graph.Requirements {
		if requirement.Dependency == "example.com/dep" {
			requirements = append(requirements, requirement)
		}
	}
	if len(requirements) != 2 || requirements[0].Manifest != "go.mod" || requirements[1].Manifest != "sub/go.mod" {
		t.Fatalf("requirements = %+v, want the same repository's manifests sorted", requirements)
	}
	modules := map[string]string{}
	for _, module := range graph.Modules {
		modules[module.Manifest] = module.Path
	}
	if modules["go.mod"] != "example.com/twin" || modules["sub/go.mod"] != "example.com/twin" {
		t.Fatalf("modules = %+v, want both declarations of the same repository", graph.Modules)
	}
}

func TestDepsCovGraphsGraphFromFleetTieBreakers(t *testing.T) {
	t.Parallel()
	goRequirement := func(version, repository, module, manifest string) goFleetRequirement {
		return goFleetRequirement{Dependency: "example.com/x", Version: version, Repository: repository, ConsumerModule: module, Manifest: manifest}
	}
	goCases := []struct {
		name string
		got  func(Graph) string
		want string
	}{
		{name: "version", got: func(graph Graph) string { return graph.Requirements[0].Version }, want: "v1.0.0"},
		{name: "consumer repository", got: func(graph Graph) string { return graph.Requirements[0].ConsumerRepository }, want: "acme/a"},
		{name: "consumer module", got: func(graph Graph) string { return graph.Requirements[0].ConsumerModule }, want: "example.com/a"},
		{name: "manifest", got: func(graph Graph) string { return graph.Requirements[0].Manifest }, want: "a/go.mod"},
	}
	goRequests := [][]goFleetRequirement{
		{goRequirement("v2.0.0", "acme/app", "example.com/app", "go.mod"), goRequirement("v1.0.0", "acme/app", "example.com/app", "go.mod")},
		{goRequirement("v1.0.0", "acme/b", "example.com/app", "go.mod"), goRequirement("v1.0.0", "acme/a", "example.com/app", "go.mod")},
		{goRequirement("v1.0.0", "acme/app", "example.com/b", "go.mod"), goRequirement("v1.0.0", "acme/app", "example.com/a", "go.mod")},
		{goRequirement("v1.0.0", "acme/app", "example.com/a", "b/go.mod"), goRequirement("v1.0.0", "acme/app", "example.com/a", "a/go.mod")},
	}
	for index, testCase := range goCases {
		discovered := goFleetGraph{
			modules: map[string]goFleetModule{}, moduleDeclarations: map[string][]goFleetModule{},
			requirements: map[string][]goFleetRequirement{"example.com/x": goRequests[index]},
		}
		graph := graphFromGoFleet(discovered, nil, "main", nil)
		if got := testCase.got(graph); got != testCase.want {
			t.Errorf("go %s tie-breaker left %q first, want %q", testCase.name, got, testCase.want)
		}
	}

	kept := graphFromGoFleet(goFleetGraph{
		modules: map[string]goFleetModule{"example.com/keep": {Path: "example.com/keep", Repository: "acme/keep", Manifest: "go.mod"}},
		moduleDeclarations: map[string][]goFleetModule{
			"example.com/keep": {{Path: "example.com/keep", Repository: "acme/keep", Manifest: "go.mod"}},
			"example.com/drop": {{Path: "example.com/drop", Repository: "acme/drop", Manifest: "go.mod"}},
		},
		requirements: map[string][]goFleetRequirement{
			"example.com/keep": {{Dependency: "example.com/keep", Version: "v1.0.0", ConsumerModule: "example.com/app", Repository: "acme/app", Manifest: "go.mod"}},
		},
	}, nil, "main", []string{"example.com/keep"})
	if len(kept.Modules) != 1 || kept.Modules[0].Path != "example.com/keep" {
		t.Fatalf("filtered modules = %+v, want only the filtered declaration", kept.Modules)
	}

	npmRequirement := func(version, consumer, manifest string) npmFleetRequirement {
		return npmFleetRequirement{Dependency: "@acme/x", Version: version, ConsumerPackage: consumer, Repository: "sneat-co/app", Manifest: manifest}
	}
	npmCases := []struct {
		name string
		got  func(Graph) string
		want string
	}{
		{name: "version", got: func(graph Graph) string { return graph.Requirements[0].Version }, want: "1.0.0"},
		{name: "manifest", got: func(graph Graph) string { return graph.Requirements[0].Manifest }, want: "a/package.json"},
	}
	npmRequests := [][]npmFleetRequirement{
		{npmRequirement("2.0.0", "@acme/app", "package.json"), npmRequirement("1.0.0", "@acme/app", "package.json")},
		{npmRequirement("1.0.0", "@acme/app", "b/package.json"), npmRequirement("1.0.0", "@acme/app", "a/package.json")},
	}
	for index, testCase := range npmCases {
		discovered := npmFleetGraph{
			packages: map[string]npmFleetPackage{}, packageDeclarations: map[string][]npmFleetPackage{},
			requirements: map[string][]npmFleetRequirement{"@acme/x": npmRequests[index]},
		}
		graph := graphFromNpmFleet(discovered, nil, "main", nil)
		if got := testCase.got(graph); got != testCase.want {
			t.Errorf("npm %s tie-breaker left %q first, want %q", testCase.name, got, testCase.want)
		}
	}

	keptNpm := graphFromNpmFleet(npmFleetGraph{
		packages: map[string]npmFleetPackage{"@acme/keep": {Name: "@acme/keep", Repository: "sneat-co/keep", Manifest: "package.json"}},
		packageDeclarations: map[string][]npmFleetPackage{
			"@acme/keep": {{Name: "@acme/keep", Repository: "sneat-co/keep", Manifest: "package.json"}},
			"@acme/drop": {{Name: "@acme/drop", Repository: "sneat-co/drop", Manifest: "package.json"}},
		},
		requirements: map[string][]npmFleetRequirement{
			"@acme/keep": {{Dependency: "@acme/keep", Version: "1.0.0", ConsumerPackage: "@acme/app", Repository: "sneat-co/app", Manifest: "package.json"}},
		},
	}, nil, "main", []string{"@acme/keep"})
	if len(keptNpm.Modules) != 1 || keptNpm.Modules[0].Path != "@acme/keep" {
		t.Fatalf("filtered npm modules = %+v, want only the filtered declaration", keptNpm.Modules)
	}
}

func TestDepsCovGraphsRepositoryOrderAndCyclePaths(t *testing.T) {
	graph := Graph{
		Repositories: []GraphRepository{{Slug: "acme/unrelated"}},
		Requirements: []GraphRequirement{internalRequirement("acme/provider", "acme/consumer")},
	}
	order := graph.RepositoryOrder()
	layerOf := map[string]int{}
	for _, layer := range order.Layers {
		for _, repository := range layer.Repositories {
			layerOf[repository] = layer.Index
		}
	}
	if len(layerOf) != 3 {
		t.Fatalf("layering = %+v, want repositories introduced by requirements retained", order.Layers)
	}
	if layerOf["acme/provider"] >= layerOf["acme/consumer"] {
		t.Fatalf("layer of provider %d must precede consumer %d", layerOf["acme/provider"], layerOf["acme/consumer"])
	}
	if layerOf["acme/unrelated"] != 0 {
		t.Fatalf("unrelated repository layer = %d, want 0", layerOf["acme/unrelated"])
	}

	components := topologicalLayers(map[string][]string{"a": {"b", "c"}, "b": {"c"}, "c": {"a"}})
	if len(components) != 1 || !reflect.DeepEqual(components[0].nodes, []string{"a", "b", "c"}) || components[0].level != 0 {
		t.Fatalf("topologicalLayers = %+v, want one strongly connected component at level 0", components)
	}

	if path := repositoryCyclePath([]string{"acme/a", "acme/b"}, map[string][]string{
		"acme/a": {"acme/b"},
		"acme/b": {"acme/0-unrelated", "acme/a"},
	}); path != "acme/a -> acme/b -> acme/a" {
		t.Fatalf("cycle path over a non-member edge = %q", path)
	}
	if path := repositoryCyclePath([]string{"acme/a", "acme/b", "acme/c"}, map[string][]string{
		"acme/a": {"acme/b", "acme/c"},
		"acme/b": {"acme/c"},
		"acme/c": {"acme/a"},
	}); path != "acme/a -> acme/c -> acme/a" {
		t.Fatalf("cycle path with an already-visited member = %q", path)
	}
	if path := repositoryCyclePath([]string{"acme/a", "acme/b"}, map[string][]string{
		"acme/a": {},
		"acme/b": {},
	}); path != "acme/a -> acme/b" {
		t.Fatalf("cycle path fallback = %q, want the component listing", path)
	}
}

func TestDepsCovGraphsCoalescedCampaignTargets(t *testing.T) {
	t.Parallel()
	targetList := []Target{{Ecosystem: EcosystemGo, Dependency: "example.com/dep", Version: "v1.0.0"}}
	if selected, deferred := coalescedCampaignTargets(
		map[string][]Target{"acme/seed": targetList},
		nil, nil, map[string]bool{"acme/seed": true}); selected != nil || deferred != nil {
		t.Fatalf("fixed-root-only targets = %+v, %v; want nil, nil", selected, deferred)
	}

	selected, deferred := coalescedCampaignTargets(
		map[string][]Target{"acme/r1": targetList, "acme/r3": targetList},
		map[string][]string{"acme/r0": {"acme/r1"}},
		map[string]bool{"acme/r0": true},
		map[string]bool{"acme/seed": true},
	)
	if len(selected) != 1 || len(selected["acme/r3"]) != 1 {
		t.Fatalf("selected = %+v, want the defaulted level-0 repository only", selected)
	}
	if !reflect.DeepEqual(deferred, []string{"acme/r1"}) {
		t.Fatalf("deferred = %v, want the deeper repository", deferred)
	}

	// A repository reachable from two consumer roots is visited once; the
	// second visit must short-circuit rather than traverse it again.
	selected, deferred = coalescedCampaignTargets(
		map[string][]Target{"acme/r2": targetList},
		map[string][]string{"acme/r0": {"acme/r2"}, "acme/r1": {"acme/r2"}},
		map[string]bool{"acme/r0": true, "acme/r1": true},
		map[string]bool{"acme/seed": true},
	)
	if len(selected) != 1 || len(selected["acme/r2"]) != 1 || deferred != nil {
		t.Fatalf("diamond selection = %+v, %v; want the shared downstream repository", selected, deferred)
	}
}

func TestDepsCovGraphsProjectionSubtitlesAndStatuses(t *testing.T) {
	t.Parallel()
	graph := Graph{
		Repositories: []GraphRepository{{Slug: "acme/multi", Organization: "acme", Modules: []string{"a", "b"}}},
		Requirements: []GraphRequirement{
			{
				Dependency: "dep-one", Version: "1.0.0", ConsumerModule: "m", ConsumerRepository: "acme/consumer",
				Manifest: "package.json", ProviderRepository: "acme/multi",
				ProviderCandidates: []string{"acme/multi:package.json", "acme/other:package.json"},
			},
			{
				Dependency: "dep-two", Version: "1.0.0", ConsumerModule: "m", ConsumerRepository: "acme/consumer",
				Manifest: "package.json", ProviderCandidates: []string{"acme/a:package.json", "acme/b:package.json"},
			},
		},
	}
	if _, err := graph.Project(GraphView("bogus")); err == nil || !strings.Contains(err.Error(), "unknown dependency graph view") {
		t.Fatalf("unknown view error = %v", err)
	}
	repositories, err := graph.Project(GraphViewRepositories)
	if err != nil {
		t.Fatal(err)
	}
	if subtitle := projectionNode(repositories, "acme/multi").Subtitle; subtitle != "2 modules" {
		t.Fatalf("repository subtitle = %q, want the module count", subtitle)
	}
	dependencies, err := graph.Project(GraphViewDependencies)
	if err != nil {
		t.Fatal(err)
	}
	if subtitle := projectionNode(dependencies, "dep-one").Subtitle; !strings.Contains(subtitle, "provided by acme/multi · 2 declarations") {
		t.Fatalf("single-provider subtitle = %q", subtitle)
	}
	if subtitle := projectionNode(dependencies, "dep-two").Subtitle; !strings.Contains(subtitle, "ambiguous provider · 2 declarations") {
		t.Fatalf("ambiguous-provider subtitle = %q", subtitle)
	}

	highest := graphHighestVersions([]GraphRequirement{
		{Dependency: "a", Version: "not-semver"},
		{Dependency: "a", Version: "v1.0.0"},
		{Dependency: "a", Version: "v1.2.0"},
		{Dependency: "b", Version: "v0.1.0"},
	})
	if !reflect.DeepEqual(highest, map[string]string{"a": "v1.2.0", "b": "v0.1.0"}) {
		t.Fatalf("graphHighestVersions = %+v", highest)
	}
	for _, testCase := range []struct {
		requirement GraphRequirement
		highest     map[string]string
		want        string
	}{
		{requirement: GraphRequirement{Dependency: "a", Version: "not-semver"}, highest: map[string]string{"a": "v1.0.0"}, want: "selected"},
		{requirement: GraphRequirement{Dependency: "a", Version: "v1.0.0"}, highest: map[string]string{}, want: "selected"},
		{requirement: GraphRequirement{Dependency: "a", Version: "v0.9.0"}, highest: map[string]string{"a": "v1.0.0"}, want: "behind"},
		{requirement: GraphRequirement{Dependency: "a", Version: "v1.0.0"}, highest: map[string]string{"a": "v1.0.0"}, want: "fleet-highest"},
		{requirement: GraphRequirement{Dependency: "a", Version: "v2.0.0"}, highest: map[string]string{"a": "v1.0.0"}, want: "fleet-highest"},
	} {
		if got := graphRequirementStatus(testCase.requirement, testCase.highest); got != testCase.want {
			t.Errorf("graphRequirementStatus(%+v) = %q, want %q", testCase.requirement, got, testCase.want)
		}
	}
}

func TestDepsCovGraphsOrderForRepositoriesAndPlanOrderedLayers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if _, err := orderForRepositories(ctx, nil, Target{Ecosystem: EcosystemNPM}, orchestrate.Options{GitHubDir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "only for the go ecosystem") {
		t.Fatalf("non-Go ordering error = %v", err)
	}
	if _, err := orderForRepositories(ctx, []Repository{{Slug: "bogus"}}, Target{Ecosystem: EcosystemGo}, orchestrate.Options{
		GitHubDir: t.TempDir(), Ref: "main", Parallel: 1, Timeout: time.Minute,
	}); err == nil || !strings.Contains(err.Error(), `invalid repository slug "bogus"`) {
		t.Fatalf("failing graph scan error = %v", err)
	}

	layers := planOrderedLayers(GraphOrder{}, []Repository{{Slug: "acme/x"}}, LayerSelection{})
	if len(layers) != 1 || layers[0].index != 0 || !layers[0].selected {
		t.Fatalf("layers = %+v, want a synthesized selected layer 0", layers)
	}
	if got := layerSlugs(layers[0]); !reflect.DeepEqual(got, []string{"acme/x"}) {
		t.Fatalf("unplaced repositories = %v, want the selected repository", got)
	}
}
