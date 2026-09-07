package locallink

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/streams"
)

// fakeGit answers the Git port without a repository, and records the excludes
// it was asked to write so the "never a tracked .gitignore" rule is provable.
type fakeGit struct {
	hash     string
	dirty    bool
	hashErr  error
	tracked  map[string][]string
	excludes map[string][]string
}

func newFakeGit() *fakeGit {
	return &fakeGit{hash: "treehash0123456789", dirty: true, tracked: map[string][]string{}, excludes: map[string][]string{}}
}

func (git *fakeGit) ContentHash(_ context.Context, dir string) (string, bool, error) {
	if git.hashErr != nil {
		return "", false, git.hashErr
	}
	return git.hash, git.dirty, nil
}

func (git *fakeGit) TrackedChanges(_ context.Context, dir string) ([]string, error) {
	return git.tracked[dir], nil
}

func (git *fakeGit) ExcludePath(_ context.Context, dir, pattern string) error {
	git.excludes[dir] = append(git.excludes[dir], pattern)
	return nil
}

func (git *fakeGit) ExcludedPatterns(_ context.Context, dir string) ([]string, error) {
	return git.excludes[dir], nil
}

// fakeNode records every build and link, and can fail the frozen install so
// the lockfile-baseline requirement is provable.
type fakeNode struct {
	installErr    map[string]error
	unlinkErr     map[string]error
	unlinkNote    map[string]string
	order         []string
	installed     []string
	builds        int
	buildRoots    []string
	packageDirs   []string
	buildErr      error
	dist          string
	linked        map[string]string
	unlinked      []string
	previousReal  string
	siblingGroups map[string][]string
}

func newFakeNode() *fakeNode {
	return &fakeNode{installErr: map[string]error{}, dist: "/cache/dist", linked: map[string]string{}, siblingGroups: map[string][]string{}}
}

func (node *fakeNode) FrozenInstall(_ context.Context, dir string) error {
	if err := node.installErr[dir]; err != nil {
		return err
	}
	node.installed = append(node.installed, dir)
	node.order = append(node.order, "install "+dir)
	return nil
}

func (node *fakeNode) Build(_ context.Context, libraryDir, packageDir string) (string, error) {
	if node.buildErr != nil {
		return "", node.buildErr
	}
	node.builds++
	node.buildRoots = append(node.buildRoots, libraryDir)
	node.packageDirs = append(node.packageDirs, packageDir)
	return node.dist, nil
}

func (node *fakeNode) Link(_ context.Context, consumerDir, packageName, dist string) (NodeLinkResult, error) {
	node.linked[consumerDir+" "+packageName] = dist
	node.order = append(node.order, "link "+consumerDir+" "+packageName)
	marker := linkAppliedMarkerPath(consumerDir, packageName)
	if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
		return NodeLinkResult{}, err
	}
	if err := os.WriteFile(marker, []byte("fake-owned-stage\n"), 0o644); err != nil {
		return NodeLinkResult{}, err
	}
	return NodeLinkResult{Previous: node.previousReal}, nil
}

func (node *fakeNode) Unlink(_ context.Context, consumerDir, packageName string) (string, error) {
	if err := node.unlinkErr[consumerDir+" "+packageName]; err != nil {
		return "", err
	}
	node.unlinked = append(node.unlinked, consumerDir+" "+packageName)
	if err := os.Remove(linkAppliedMarkerPath(consumerDir, packageName)); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	// A configured note simulates ExecNode finding the link already
	// superseded by a published package: Unlink succeeds, touches nothing
	// further, and reports why. See execports_test.go for the real
	// filesystem behaviour this stands in for.
	if node.unlinkNote != nil {
		if note, ok := node.unlinkNote[consumerDir+" "+packageName]; ok {
			return note, nil
		}
	}
	return "", nil
}

func (node *fakeNode) LinkSiblings(_ context.Context, consumerDir string, packageNames []string) error {
	node.siblingGroups[consumerDir] = append([]string(nil), packageNames...)
	return nil
}

type fakeVerifier struct {
	linked   map[string]VerificationRun
	baseline map[string]VerificationRun
	envSeen  map[string][]string
}

type failingBuildExecNode struct {
	ExecNode
}

func (node failingBuildExecNode) FrozenInstall(context.Context, string) error { return nil }
func (node failingBuildExecNode) Build(context.Context, string, string) (string, error) {
	return "", errors.New("provider build failed")
}

// refreshThenFailNode models pnpm reconciling the top-level package link back
// to the published peer-context package during the next frozen install. WB's
// marker, recovery record, and stage remain until undo.
type refreshThenFailNode struct {
	ExecNode
	dist        string
	builds      int
	consumerDir string
	packageName string
}

func (node *refreshThenFailNode) FrozenInstall(context.Context, string) error {
	if node.builds == 0 {
		return nil
	}
	target := filepath.Join(node.consumerDir, "node_modules", filepath.FromSlash(node.packageName))
	original, err := os.ReadFile(target + linkSymlinkBackupSuffix)
	if err != nil {
		return err
	}
	if err := os.Remove(target); err != nil {
		return err
	}
	return os.Symlink(strings.TrimSpace(string(original)), target)
}

func (node *refreshThenFailNode) Build(context.Context, string, string) (string, error) {
	node.builds++
	if node.builds > 1 {
		return "", errors.New("provider rebuild failed")
	}
	return node.dist, nil
}

// engineExecNode keeps the Engine journey deterministic while using the real
// filesystem Link, LinkSiblings, and Unlink implementations. The only seams
// are the provider build output and the frozen-install proof. Its first
// sibling call injects a staged conflict so the Engine's applied receipt and
// retry path are exercised through the production orchestration.
type engineExecNode struct {
	ExecNode
	dists           map[string]string
	failSiblingOnce bool
	siblingCalls    int
	conflictPath    string
}

func (node *engineExecNode) FrozenInstall(context.Context, string) error { return nil }

func (node *engineExecNode) Build(_ context.Context, _, packageDir string) (string, error) {
	dist := node.dists[packageDir]
	if dist == "" {
		return "", fmt.Errorf("missing test build output for %s", packageDir)
	}
	return dist, nil
}

func (node *engineExecNode) LinkSiblings(ctx context.Context, consumerDir string, packageNames []string) error {
	node.siblingCalls++
	if node.failSiblingOnce {
		node.failSiblingOnce = false
		contents, err := os.ReadFile(linkAppliedMarkerPath(consumerDir, "@acme/core"))
		if err != nil {
			return err
		}
		stage := strings.TrimSpace(string(contents))
		node.conflictPath = filepath.Join(stage, "node_modules", "@acme", "auth-core")
		if err := os.MkdirAll(filepath.Dir(node.conflictPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(node.conflictPath, []byte("conflicting staged path\n"), 0o644); err != nil {
			return err
		}
	}
	return node.ExecNode.LinkSiblings(ctx, consumerDir, packageNames)
}

func newFakeVerifier() *fakeVerifier {
	return &fakeVerifier{linked: map[string]VerificationRun{}, baseline: map[string]VerificationRun{}, envSeen: map[string][]string{}}
}

func (verifier *fakeVerifier) Verify(_ context.Context, dir string, env []string) (VerificationRun, error) {
	verifier.envSeen[dir] = env
	if run, ok := verifier.linked[dir]; ok {
		return run, nil
	}
	return VerificationRun{Passed: true, Command: "go test -p 1 ./..."}, nil
}

func (verifier *fakeVerifier) BuildAndVet(_ context.Context, dir string) (VerificationRun, error) {
	if run, ok := verifier.baseline[dir]; ok {
		return run, nil
	}
	return VerificationRun{Passed: true, Command: "go build ./...; go vet ./..."}, nil
}

func writeTree(t *testing.T, root string, files map[string]string) string {
	t.Helper()
	for name, contents := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

type fixture struct {
	engine   *Engine
	store    *streams.Store
	git      *fakeGit
	node     *fakeNode
	verifier *fakeVerifier
	library  string
	consumer string
}

func newFixture(t *testing.T, libraryFiles, consumerFiles map[string]string) fixture {
	t.Helper()
	base := t.TempDir()
	library := writeTree(t, filepath.Join(base, "library"), libraryFiles)
	consumer := writeTree(t, filepath.Join(base, "consumer"), consumerFiles)
	store := streams.OpenAt(filepath.Join(base, "wb-home", "streams"))
	if _, err := store.Create(streams.Stream{
		Name: "fixture",
		Members: []streams.Member{
			{Repository: "acme/library", Role: streams.RoleLibrary, Worktree: library, Branch: "stream/fixture", Base: "main"},
			{Repository: "acme/app", Role: streams.RoleConsumer, Worktree: consumer, Branch: "stream/fixture", Base: "main"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	git, node, verifier := newFakeGit(), newFakeNode(), newFakeVerifier()
	fixed := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	return fixture{
		engine: &Engine{
			Store: store, Git: git, Node: node, Verifier: verifier,
			CacheRoot: filepath.Join(base, "cache"),
			Now:       func() time.Time { return fixed },
		},
		store: store, git: git, node: node, verifier: verifier,
		library: library, consumer: consumer,
	}
}

const goLibraryModule = "module github.com/acme/library/backend\n\ngo 1.27\n"

// AC: go-consumer-builds-against-the-library-worktree — the go.work at the
// consumer worktree root carries `use` entries for every module in the consumer
// worktree AND the library; go.work and go.work.sum are both excluded; go.mod
// is unchanged.
func TestGoConsumerGetsAWorkspaceNamingEveryModuleAndTheLibrary(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{
			"backend/go.mod":    "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n",
			"tools/lint/go.mod": "module github.com/acme/app/tools/lint\n\ngo 1.26\n",
		})
	goModBefore := readFile(t, filepath.Join(fixture.consumer, "backend", "go.mod"))

	result, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	})
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	if result.Failed() {
		t.Fatalf("result = %#v", result.Consumers)
	}
	workspace := readFile(t, filepath.Join(fixture.consumer, "go.work"))
	for _, want := range []string{"./backend", "./tools/lint", filepath.ToSlash(filepath.Join(fixture.library, "backend"))} {
		if !strings.Contains(workspace, want) {
			t.Errorf("go.work does not use %q:\n%s", want, workspace)
		}
	}
	if !strings.Contains(workspace, "go 1.27") {
		t.Errorf("go.work does not carry the consumer's own go directive:\n%s", workspace)
	}
	excluded := fixture.git.excludes[fixture.consumer]
	if !containsAll(excluded, "/go.work", "/go.work.sum") {
		t.Errorf("excluded = %v, want both go.work and go.work.sum", excluded)
	}
	if after := readFile(t, filepath.Join(fixture.consumer, "backend", "go.mod")); after != goModBefore {
		t.Errorf("go.mod changed:\n%s", after)
	}
	if strings.Contains(goModBefore, "replace") {
		t.Error("fixture is wrong: the consumer already had a replace directive")
	}

	// The link is recorded in stream state with the version it replaced.
	stream, err := fixture.store.Load("fixture")
	if err != nil {
		t.Fatal(err)
	}
	member, _ := stream.Member("acme/app")
	if len(member.Links) != 1 {
		t.Fatalf("recorded links = %#v, want one", member.Links)
	}
	link := member.Links[0]
	if link.Mechanism != streams.MechanismGoWork || link.PreviousVersion != "v0.4.0" || link.ContentHash != fixture.git.hash {
		t.Fatalf("link = %#v", link)
	}
}

// REQ: local-link-discovers-what-the-library-publishes — a consumer that
// declares none of the discovered identities is reported and skipped, never
// linked to something it does not use.
func TestAConsumerThatDoesNotDependOnTheLibraryIsSkipped(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n"})
	result, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Consumers) != 1 || !result.Consumers[0].Skipped {
		t.Fatalf("consumers = %#v, want one skipped", result.Consumers)
	}
	if !strings.Contains(result.Consumers[0].Reason, "github.com/acme/library/backend") {
		t.Errorf("reason does not name what was looked for: %s", result.Consumers[0].Reason)
	}
	if _, err := os.Stat(filepath.Join(fixture.consumer, "go.work")); !os.IsNotExist(err) {
		t.Error("a skipped consumer was linked anyway")
	}
}

func TestALibraryPublishingNothingIsRefusedRatherThanGuessed(t *testing.T) {
	fixture := newFixture(t, map[string]string{"README.md": "no manifests\n"}, map[string]string{})
	_, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	})
	if err == nil || !strings.Contains(err.Error(), "publishes no discoverable") {
		t.Fatalf("error = %v, want a refusal to guess", err)
	}
}

// AC: npm-consumer-links-without-tracked-config — the library is built once
// with the repository's own build target and linked from its dist; every
// manifest stays byte-identical.
func TestNpmConsumerLinksFromABuiltDistWithoutTouchingTrackedConfig(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"libs/core/package.json": `{"name":"@acme/core","version":"1.0.0"}`},
		map[string]string{
			"package.json":        `{"name":"app","dependencies":{"@acme/core":"^1.0.0"}}`,
			"pnpm-workspace.yaml": "packages:\n  - apps/*\n",
			"pnpm-lock.yaml":      "lockfileVersion: '9.0'\n",
		})
	manifestBefore := readFile(t, filepath.Join(fixture.consumer, "package.json"))
	workspaceBefore := readFile(t, filepath.Join(fixture.consumer, "pnpm-workspace.yaml"))

	result, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed() {
		t.Fatalf("result = %#v", result.Consumers)
	}
	if fixture.node.builds != 1 {
		t.Errorf("builds = %d, want exactly one", fixture.node.builds)
	}
	if len(fixture.node.installed) != 1 || fixture.node.installed[0] != fixture.consumer {
		t.Errorf("frozen installs = %v, want the unlinked consumer proved once", fixture.node.installed)
	}
	if fixture.node.linked[fixture.consumer+" @acme/core"] != fixture.node.dist {
		t.Errorf("linked = %v, want the built dist", fixture.node.linked)
	}
	if readFile(t, filepath.Join(fixture.consumer, "package.json")) != manifestBefore {
		t.Error("package.json is no longer byte-identical to its committed contents")
	}
	if readFile(t, filepath.Join(fixture.consumer, "pnpm-workspace.yaml")) != workspaceBefore {
		t.Error("pnpm-workspace.yaml is no longer byte-identical to its committed contents")
	}
	for _, forbidden := range []string{"overrides", "workspace:", "link:"} {
		if strings.Contains(manifestBefore+workspaceBefore, forbidden) {
			t.Errorf("tracked config contains %q", forbidden)
		}
	}
}

// A stream owns repository-root worktrees even when their npm workspaces live
// below frontend/. Discovery and every npm operation use that workspace while
// the link record and merge guard remain attached to the repository member.
func TestNestedFrontendWorkspaceLinksAndUndoesFromRepositoryRoot(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{
			"frontend/package.json":           `{"name":"provider","private":true}`,
			"frontend/pnpm-workspace.yaml":    "packages:\n  - libs/**\n",
			"frontend/pnpm-lock.yaml":         "lockfileVersion: '9.0'\n",
			"frontend/libs/core/package.json": `{"name":"@acme/core","version":"1.0.0"}`,
			"landings/package.json":           `{"name":"landing"}`,
			"landings/pnpm-workspace.yaml":    "packages: []\n",
			"landings/pnpm-lock.yaml":         "lockfileVersion: '9.0'\n",
		},
		map[string]string{
			"frontend/package.json":        `{"name":"consumer","private":true,"dependencies":{"@acme/core":"^1.0.0"}}`,
			"frontend/pnpm-workspace.yaml": "packages:\n  - apps/**\n",
			"frontend/pnpm-lock.yaml":      "lockfileVersion: '9.0'\n",
			"landings/package.json":        `{"name":"landing","dependencies":{"elsewhere":"1.0.0"}}`,
			"landings/pnpm-workspace.yaml": "packages: []\n",
			"landings/pnpm-lock.yaml":      "lockfileVersion: '9.0'\n",
		})
	manifestBefore := readFile(t, filepath.Join(fixture.consumer, "frontend", "package.json"))

	result, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed() || len(result.Consumers[0].Links) != 1 {
		t.Fatalf("result = %#v", result.Consumers)
	}
	providerWorkspace := filepath.Join(fixture.library, "frontend")
	consumerWorkspace := filepath.Join(fixture.consumer, "frontend")
	if len(fixture.node.installed) != 1 || fixture.node.installed[0] != consumerWorkspace {
		t.Fatalf("frozen installs = %v, want nested workspace %s once", fixture.node.installed, consumerWorkspace)
	}
	if len(fixture.node.buildRoots) != 1 || fixture.node.buildRoots[0] != providerWorkspace {
		t.Fatalf("build roots = %v, want %s", fixture.node.buildRoots, providerWorkspace)
	}
	if len(fixture.node.packageDirs) != 1 || fixture.node.packageDirs[0] != filepath.Join(providerWorkspace, "libs", "core") {
		t.Fatalf("package dirs = %v, want nested library package", fixture.node.packageDirs)
	}
	if fixture.node.linked[consumerWorkspace+" @acme/core"] != fixture.node.dist {
		t.Fatalf("linked = %v, want link in nested consumer workspace", fixture.node.linked)
	}
	if got := fixture.node.siblingGroups[consumerWorkspace]; len(got) != 1 || got[0] != "@acme/core" {
		t.Fatalf("sibling groups = %v, want the staged package in the nested workspace", fixture.node.siblingGroups)
	}
	link := result.Consumers[0].Links[0]
	if link.Workspace != "frontend" || len(link.Artifacts) == 0 || link.Artifacts[0] != "frontend/node_modules/@acme/core" {
		t.Fatalf("link record = %#v, want repository-relative nested workspace evidence", link)
	}
	if readFile(t, filepath.Join(fixture.consumer, "frontend", "package.json")) != manifestBefore {
		t.Fatal("nested package.json changed while linking")
	}
	if live, err := HasLiveLink(fixture.store, fixture.consumer); err != nil || len(live) != 1 {
		t.Fatalf("merge guard links = %#v, err = %v; want repository-root record", live, err)
	}

	undo, err := fixture.engine.Run(context.Background(), Options{Consumers: []string{fixture.consumer}, Undo: true})
	if err != nil || undo.Failed() {
		t.Fatalf("undo = %#v, err = %v", undo.Consumers, err)
	}
	if len(fixture.node.unlinked) != 1 || fixture.node.unlinked[0] != consumerWorkspace+" @acme/core" {
		t.Fatalf("unlinked = %v, want nested consumer workspace", fixture.node.unlinked)
	}
	if live, err := HasLiveLink(fixture.store, fixture.consumer); err != nil || len(live) != 0 {
		t.Fatalf("merge guard survived undo: %#v (err %v)", live, err)
	}
}

func TestEngineRealPnpmSiblingFailureRetryAndUndoJourney(t *testing.T) {
	type packageSpec struct {
		name         string
		directory    string
		version      string
		dependencies string
	}
	packages := []packageSpec{
		{
			name:         "@acme/app",
			directory:    "libs/app",
			version:      "1.0.0",
			dependencies: `"dependencies":{"@acme/core":"1.0.0"},"optionalDependencies":{"@acme/auth-core":"1.0.0"},"peerDependencies":{"@angular/core":"^18.0.0"}`,
		},
		{
			name:         "@acme/core",
			directory:    "libs/core",
			version:      "1.0.0",
			dependencies: `"peerDependencies":{"@acme/auth-core":"1.0.0"}`,
		},
		{
			name:         "@acme/auth-core",
			directory:    "libs/auth-core",
			version:      "1.0.0",
			dependencies: `"peerDependencies":{"@angular/core":"^18.0.0"}`,
		},
	}
	base := t.TempDir()
	libraryFiles := map[string]string{"package.json": `{"private":true}`}
	consumerFiles := map[string]string{
		"package.json": `{"name":"consumer","dependencies":{"@acme/app":"1.0.0","@acme/auth-core":"1.0.0","@acme/core":"1.0.0"}}`,
	}
	for _, pkg := range packages {
		libraryFiles[filepath.ToSlash(filepath.Join(pkg.directory, "package.json"))] = fmt.Sprintf(`{"name":%q,"version":%q}`, pkg.name, pkg.version)
	}
	library := writeTree(t, filepath.Join(base, "library"), libraryFiles)
	consumer := writeTree(t, filepath.Join(base, "consumer"), consumerFiles)

	original := map[string]string{}
	installed := map[string]string{}
	for _, pkg := range packages {
		storePackage := filepath.Join(consumer, "node_modules", ".pnpm", pnpmStoreKey(pkg.name, pkg.version), "node_modules", filepath.FromSlash(filepath.Dir(pkg.name)), filepath.Base(pkg.name))
		if err := os.MkdirAll(storePackage, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(storePackage, "package.json"), []byte(fmt.Sprintf(`{"name":%q,"version":%q}`, pkg.name, pkg.version)), 0o644); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(consumer, "node_modules", filepath.FromSlash(pkg.name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		relative, err := filepath.Rel(filepath.Dir(target), storePackage)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(relative, target); err != nil {
			t.Fatal(err)
		}
		original[pkg.name] = relative
		installed[pkg.name] = storePackage
	}
	angular := filepath.Join(consumer, "node_modules", "@angular", "core")
	if err := os.MkdirAll(angular, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(angular, "package.json"), []byte(`{"name":"@angular/core","version":"18.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	dists := map[string]string{}
	for _, pkg := range packages {
		dist := filepath.Join(t.TempDir(), "dist")
		if err := os.MkdirAll(dist, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dist, "package.json"), []byte(fmt.Sprintf(`{"name":%q,"version":"1.0.0-dev",%s}`, pkg.name, pkg.dependencies)), 0o644); err != nil {
			t.Fatal(err)
		}
		dists[filepath.Join(library, filepath.FromSlash(pkg.directory))] = dist
	}

	store := streams.OpenAt(filepath.Join(base, "wb-home", "streams"))
	if _, err := store.Create(streams.Stream{
		Name: "fixture",
		Members: []streams.Member{
			{Repository: "acme/library", Role: streams.RoleLibrary, Worktree: library, Branch: "stream/fixture", Base: "main"},
			{Repository: "acme/app", Role: streams.RoleConsumer, Worktree: consumer, Branch: "stream/fixture", Base: "main"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	git := newFakeGit()
	node := &engineExecNode{
		ExecNode:        ExecNode{CacheRoot: filepath.Join(base, "cache"), ContentHash: git.hash, Timeout: time.Second},
		dists:           dists,
		failSiblingOnce: true,
	}
	engine := &Engine{
		Store: store, Git: git, Node: node, CacheRoot: filepath.Join(base, "cache"),
		Now: func() time.Time { return time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC) },
	}

	first, err := engine.Run(context.Background(), Options{Library: library, Consumers: []string{consumer}})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Failed() || len(first.Consumers) != 1 || len(first.Consumers[0].Links) != len(packages) {
		t.Fatalf("first link = %#v, want sibling reconciliation failure with all applied links", first.Consumers)
	}
	if !strings.Contains(strings.Join(first.Consumers[0].Errors, " "), "refuse to replace existing staged sibling path") {
		t.Fatalf("first errors = %v, want the exact sibling conflict", first.Consumers[0].Errors)
	}
	if node.siblingCalls != 1 || node.conflictPath == "" || !fileExists(node.conflictPath) {
		t.Fatalf("sibling failure receipt = calls:%d conflict:%q exists:%t", node.siblingCalls, node.conflictPath, fileExists(node.conflictPath))
	}
	appStage := strings.TrimSpace(readFile(t, linkAppliedMarkerPath(consumer, "@acme/app")))
	if _, err := os.Lstat(filepath.Join(appStage, "node_modules", "@acme", "core")); !os.IsNotExist(err) {
		t.Fatalf("failed Engine reconciliation left an app sibling edge: %v", err)
	}
	stream, err := store.Load("fixture")
	if err != nil {
		t.Fatal(err)
	}
	member, ok := stream.Member("acme/app")
	if !ok || len(member.Links) != len(packages) {
		t.Fatalf("first receipt = %#v, want all package links retained", member.Links)
	}
	for _, link := range member.Links {
		if link.State != streams.LinkStateApplied {
			t.Fatalf("first receipt link = %#v, want applied recovery state", link)
		}
	}

	second, err := engine.Run(context.Background(), Options{Library: library, Consumers: []string{consumer}})
	if err != nil || second.Failed() {
		t.Fatalf("retry = %#v, err = %v", second.Consumers, err)
	}
	if node.siblingCalls != 2 {
		t.Fatalf("retry receipt = calls:%d, want one failed and one successful sibling reconciliation", node.siblingCalls)
	}
	stages := map[string]string{}
	for _, pkg := range packages {
		stages[pkg.name] = strings.TrimSpace(readFile(t, linkAppliedMarkerPath(consumer, pkg.name)))
	}
	conflictInfo, err := os.Lstat(node.conflictPath)
	if err != nil || conflictInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("retry conflict path = %s, want the reconciled sibling symlink: %v", node.conflictPath, err)
	}
	if got := resolveNodePackage(t, stages["@acme/app"], "@acme/core"); got != resolvePath(t, stages["@acme/core"]) {
		t.Fatalf("retry app/core identity = %s, want %s", got, stages["@acme/core"])
	}
	if got := resolveNodePackage(t, stages["@acme/app"], "@acme/auth-core"); got != resolvePath(t, stages["@acme/auth-core"]) {
		t.Fatalf("retry app/auth identity = %s, want %s", got, stages["@acme/auth-core"])
	}
	if got := resolveNodePackage(t, stages["@acme/core"], "@acme/auth-core"); got != resolvePath(t, stages["@acme/auth-core"]) {
		t.Fatalf("retry core/auth identity = %s, want %s", got, stages["@acme/auth-core"])
	}
	if got := resolveNodePackage(t, stages["@acme/auth-core"], "@angular/core"); got != resolvePath(t, angular) {
		t.Fatalf("retry external peer identity = %s, want consumer-installed %s", got, angular)
	}

	undo, err := engine.Run(context.Background(), Options{Consumers: []string{consumer}, Undo: true})
	if err != nil || undo.Failed() {
		t.Fatalf("undo = %#v, err = %v", undo.Consumers, err)
	}
	for _, pkg := range packages {
		target := filepath.Join(consumer, "node_modules", filepath.FromSlash(pkg.name))
		got, err := os.Readlink(target)
		if err != nil || got != original[pkg.name] {
			t.Fatalf("undo %s restored %q, want %q (err %v)", pkg.name, got, original[pkg.name], err)
		}
		if _, err := os.Stat(filepath.Join(installed[pkg.name], "package.json")); err != nil {
			t.Fatalf("undo removed published %s: %v", pkg.name, err)
		}
		if fileExists(linkAppliedMarkerPath(consumer, pkg.name)) || fileExists(stages[pkg.name]) {
			t.Fatalf("undo left recovery artefacts for %s", pkg.name)
		}
	}
	stream, err = store.Load("fixture")
	if err != nil {
		t.Fatal(err)
	}
	member, _ = stream.Member("acme/app")
	if len(member.Links) != 0 {
		t.Fatalf("undo receipt = %#v, want no live links", member.Links)
	}
}

func TestNestedConsumerPathDoesNotBypassRepositoryRootStreamMembership(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{
			"frontend/pnpm-workspace.yaml":    "packages:\n  - libs/**\n",
			"frontend/package.json":           `{"private":true}`,
			"frontend/libs/core/package.json": `{"name":"@acme/core"}`,
		},
		map[string]string{
			"frontend/pnpm-workspace.yaml": "packages: []\n",
			"frontend/package.json":        `{"dependencies":{"@acme/core":"1.0.0"}}`,
		})
	_, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{filepath.Join(fixture.consumer, "frontend")},
	})
	if refusal, ok := Refused(err); !ok || refusal.Code != RefusalNotRecordable {
		t.Fatalf("error = %v, want exact repository-root membership refusal", err)
	}
	if len(fixture.node.installed) != 0 || len(fixture.node.linked) != 0 {
		t.Fatalf("nested-path bypass caused side effects: installs=%v links=%v", fixture.node.installed, fixture.node.linked)
	}
}

func TestSamePackageInTwoConsumerWorkspacesKeepsTwoUndoRecords(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{
			"libs/core/package.json": `{"name":"@acme/core"}`,
			"package.json":           `{"private":true}`,
		},
		map[string]string{
			"package.json":                 `{"dependencies":{"@acme/core":"1.0.0"}}`,
			"pnpm-lock.yaml":               "lockfileVersion: '9.0'\n",
			"frontend/package.json":        `{"dependencies":{"@acme/core":"1.0.0"}}`,
			"frontend/pnpm-workspace.yaml": "packages: []\n",
			"frontend/pnpm-lock.yaml":      "lockfileVersion: '9.0'\n",
		})
	result, err := fixture.engine.Run(context.Background(), Options{Library: fixture.library, Consumers: []string{fixture.consumer}})
	if err != nil || result.Failed() {
		t.Fatalf("result = %#v, err = %v", result.Consumers, err)
	}
	if len(fixture.node.installed) != 2 || fixture.node.installed[0] != fixture.consumer || fixture.node.installed[1] != filepath.Join(fixture.consumer, "frontend") {
		t.Fatalf("frozen installs = %v, want each independent workspace", fixture.node.installed)
	}
	stream, err := fixture.store.Load("fixture")
	if err != nil {
		t.Fatal(err)
	}
	member, _ := stream.Member("acme/app")
	if len(member.Links) != 2 {
		t.Fatalf("recorded links = %#v, want one per workspace", member.Links)
	}
	if _, err := fixture.engine.Run(context.Background(), Options{Consumers: []string{fixture.consumer}, Undo: true}); err != nil {
		t.Fatal(err)
	}
	wantUnlinked := map[string]bool{
		fixture.consumer + " @acme/core":                            true,
		filepath.Join(fixture.consumer, "frontend") + " @acme/core": true,
	}
	for _, unlinked := range fixture.node.unlinked {
		delete(wantUnlinked, unlinked)
	}
	if len(wantUnlinked) != 0 {
		t.Fatalf("unlinked = %v, missing %v", fixture.node.unlinked, wantUnlinked)
	}
	if live, err := HasLiveLink(fixture.store, fixture.consumer); err != nil || len(live) != 0 {
		t.Fatalf("merge guard survived complete multi-workspace undo: %#v (err %v)", live, err)
	}
}

func TestUndoRejectsWorkspaceSymlinkEscapeAndKeepsMergeGuardClosed(t *testing.T) {
	fixture := newFixture(t, map[string]string{"backend/go.mod": goLibraryModule}, map[string]string{})
	outside := t.TempDir()
	if err := os.MkdirAll(fixture.consumer, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(fixture.consumer, "frontend")); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.Update("fixture", func(stream *streams.Stream) error {
		member, _ := stream.Member("acme/app")
		for index := range stream.Members {
			if stream.Members[index].Repository == member.Repository {
				stream.Members[index].Links = []streams.Link{{
					Library: fixture.library, Mechanism: streams.MechanismPnpmLink,
					Identity: "@acme/core", Workspace: "frontend", CreatedAt: time.Now(),
				}}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.engine.Run(context.Background(), Options{Consumers: []string{fixture.consumer}, Undo: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Failed() || !strings.Contains(strings.Join(result.Consumers[0].Errors, " "), "outside worktree") {
		t.Fatalf("result = %#v, want symlink escape refusal", result.Consumers)
	}
	if live, err := HasLiveLink(fixture.store, fixture.consumer); err != nil || len(live) != 1 {
		t.Fatalf("merge guard opened after refused undo: %#v (err %v)", live, err)
	}
}

func TestFailedBuildUndoPreservesPublishedPackageFilesystem(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{
			"frontend/package.json":           `{"private":true}`,
			"frontend/pnpm-workspace.yaml":    "packages:\n  - libs/**\n",
			"frontend/libs/core/package.json": `{"name":"@acme/core"}`,
		},
		map[string]string{
			"frontend/package.json":        `{"dependencies":{"@acme/core":"1.0.0"}}`,
			"frontend/pnpm-workspace.yaml": "packages: []\n",
		})
	consumerWorkspace := filepath.Join(fixture.consumer, "frontend")
	storePackage := filepath.Join(consumerWorkspace, "node_modules", ".pnpm", "@acme+core@1.0.0", "node_modules", "@acme", "core")
	if err := os.MkdirAll(storePackage, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storePackage, "package.json"), []byte(`{"name":"@acme/core","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(consumerWorkspace, "node_modules", "@acme", "core")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(filepath.Dir(target), storePackage)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(relative, target); err != nil {
		t.Fatal(err)
	}
	fixture.engine.Node = failingBuildExecNode{ExecNode{CacheRoot: t.TempDir(), ContentHash: "hash", Timeout: time.Second}}

	result, err := fixture.engine.Run(context.Background(), Options{Library: fixture.library, Consumers: []string{fixture.consumer}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Failed() {
		t.Fatalf("failed provider build reported success: %#v", result.Consumers)
	}
	assertPublished := func(stage string) {
		t.Helper()
		contents, readErr := os.ReadFile(filepath.Join(target, "package.json"))
		if readErr != nil || !strings.Contains(string(contents), `"version":"1.0.0"`) {
			t.Fatalf("%s: published package unavailable: %s (err %v)", stage, contents, readErr)
		}
	}
	assertPublished("after failed build")
	stream, err := fixture.store.Load("fixture")
	if err != nil {
		t.Fatal(err)
	}
	member, _ := stream.Member("acme/app")
	if len(member.Links) != 1 || member.Links[0].State != streams.LinkStateIntent {
		t.Fatalf("links = %#v, want one unapplied intent", member.Links)
	}

	undo, err := fixture.engine.Run(context.Background(), Options{Consumers: []string{fixture.consumer}, Undo: true})
	if err != nil || undo.Failed() {
		t.Fatalf("undo = %#v, err = %v", undo.Consumers, err)
	}
	assertPublished("after undo")
	if live, err := HasLiveLink(fixture.store, fixture.consumer); err != nil || len(live) != 0 {
		t.Fatalf("intent record survived safe undo: %#v (err %v)", live, err)
	}
}

func TestAppliedRecordWithoutOwnershipMarkerFailsClosed(t *testing.T) {
	fixture := newFixture(t, map[string]string{"backend/go.mod": goLibraryModule}, map[string]string{})
	if _, err := fixture.store.Update("fixture", func(stream *streams.Stream) error {
		for index := range stream.Members {
			if stream.Members[index].Repository == "acme/app" {
				stream.Members[index].Links = []streams.Link{{
					Library: fixture.library, Mechanism: streams.MechanismPnpmLink,
					State: streams.LinkStateApplied, Identity: "@acme/core", CreatedAt: time.Now(),
				}}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.engine.Run(context.Background(), Options{Consumers: []string{fixture.consumer}, Undo: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Failed() || !strings.Contains(strings.Join(result.Consumers[0].Errors, " "), "has no ownership marker") {
		t.Fatalf("undo = %#v, want missing-marker refusal", result.Consumers)
	}
	if len(fixture.node.unlinked) != 0 {
		t.Fatalf("unlink was invoked without ownership proof: %v", fixture.node.unlinked)
	}
	if live, err := HasLiveLink(fixture.store, fixture.consumer); err != nil || len(live) != 1 {
		t.Fatalf("merge guard opened after missing-marker refusal: %#v (err %v)", live, err)
	}
}

func TestRefreshBuildFailureKeepsAppliedRecoveryUntilUndo(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{
			"package.json":           `{"private":true}`,
			"libs/core/package.json": `{"name":"@acme/core"}`,
		},
		map[string]string{
			"package.json":   `{"dependencies":{"@acme/core":"1.0.0"}}`,
			"pnpm-lock.yaml": "lockfileVersion: '9.0'\n",
		})
	storePackage := filepath.Join(fixture.consumer, "node_modules", ".pnpm", "@acme+core@1.0.0", "node_modules", "@acme", "core")
	if err := os.MkdirAll(storePackage, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storePackage, "package.json"), []byte(`{"name":"@acme/core","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(fixture.consumer, "node_modules", "@acme", "core")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(filepath.Dir(target), storePackage)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(relative, target); err != nil {
		t.Fatal(err)
	}
	dist := filepath.Join(t.TempDir(), "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "package.json"), []byte(`{"name":"@acme/core","version":"1.1.0-dev"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	node := &refreshThenFailNode{
		ExecNode:    ExecNode{CacheRoot: t.TempDir(), ContentHash: "hash", Timeout: time.Second},
		dist:        dist,
		consumerDir: fixture.consumer,
		packageName: "@acme/core",
	}
	fixture.engine.Node = node

	first, err := fixture.engine.Run(context.Background(), Options{Library: fixture.library, Consumers: []string{fixture.consumer}})
	if err != nil || first.Failed() {
		t.Fatalf("initial link = %#v, err = %v", first.Consumers, err)
	}
	marker := linkAppliedMarkerPath(fixture.consumer, "@acme/core")
	stageBytes, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	stage := strings.TrimSpace(string(stageBytes))

	second, err := fixture.engine.Run(context.Background(), Options{Library: fixture.library, Consumers: []string{fixture.consumer}})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Failed() || !strings.Contains(strings.Join(second.Consumers[0].Errors, " "), "provider rebuild failed") {
		t.Fatalf("refresh = %#v, want failed provider rebuild", second.Consumers)
	}
	stream, err := fixture.store.Load("fixture")
	if err != nil {
		t.Fatal(err)
	}
	member, _ := stream.Member("acme/app")
	if len(member.Links) != 1 || member.Links[0].State != streams.LinkStateApplied {
		t.Fatalf("links = %#v, want prior applied recovery record retained", member.Links)
	}
	if !fileExists(marker) || !fileExists(stage) || !fileExists(target+linkSymlinkBackupSuffix) {
		t.Fatal("failed refresh discarded the prior marker, stage, or recovery record")
	}
	if contents, err := os.ReadFile(filepath.Join(target, "package.json")); err != nil || !strings.Contains(string(contents), `"version":"1.0.0"`) {
		t.Fatalf("frozen install did not restore published package: %s (err %v)", contents, err)
	}

	undo, err := fixture.engine.Run(context.Background(), Options{Consumers: []string{fixture.consumer}, Undo: true})
	if err != nil || undo.Failed() {
		t.Fatalf("undo = %#v, err = %v", undo.Consumers, err)
	}
	if contents, err := os.ReadFile(filepath.Join(target, "package.json")); err != nil || !strings.Contains(string(contents), `"version":"1.0.0"`) {
		t.Fatalf("published package unavailable after undo: %s (err %v)", contents, err)
	}
	for _, artifact := range []string{marker, stage, target + linkSymlinkBackupSuffix} {
		if fileExists(artifact) {
			t.Fatalf("recovery artifact survived undo: %s", artifact)
		}
	}
	if live, err := HasLiveLink(fixture.store, fixture.consumer); err != nil || len(live) != 0 {
		t.Fatalf("link records survived undo: %#v (err %v)", live, err)
	}
}

// REQ: npm-link-preserves-a-frozen-lockfile-baseline — a consumer whose frozen
// install fails is never linked, so a link cannot mask a lockfile mismatch.
func TestNpmLinkRefusesWhenTheFrozenInstallFails(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"libs/core/package.json": `{"name":"@acme/core","version":"1.0.0"}`},
		map[string]string{
			"package.json":   `{"name":"app","dependencies":{"@acme/core":"^1.0.0"}}`,
			"pnpm-lock.yaml": "lockfileVersion: '9.0'\n",
		})
	fixture.node.installErr[fixture.consumer] = errors.New("lockfile is out of date")
	result, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Failed() {
		t.Fatal("a failed frozen install did not fail the link")
	}
	if fixture.node.builds != 0 || len(fixture.node.linked) != 0 {
		t.Errorf("the library was built or linked despite an unproved baseline: builds=%d linked=%v", fixture.node.builds, fixture.node.linked)
	}
}

// AC: verify-reports-every-consumer-single-worker — both consumers are
// verified, the failure is attributed to its consumer, and the passing consumer
// is still reported.
func TestVerifyReportsEveryConsumerAndDoesNotStopAtTheFirstFailure(t *testing.T) {
	base := t.TempDir()
	library := writeTree(t, filepath.Join(base, "library"), map[string]string{"backend/go.mod": goLibraryModule})
	consumerModule := "module github.com/acme/%s/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"
	failing := writeTree(t, filepath.Join(base, "failing"), map[string]string{"backend/go.mod": strings.Replace(consumerModule, "%s", "failing", 1)})
	passing := writeTree(t, filepath.Join(base, "passing"), map[string]string{"backend/go.mod": strings.Replace(consumerModule, "%s", "passing", 1)})
	store := streams.OpenAt(filepath.Join(base, "wb-home", "streams"))
	if _, err := store.Create(streams.Stream{
		Name: "verify",
		Members: []streams.Member{
			{Repository: "acme/library", Role: streams.RoleLibrary, Worktree: library},
			{Repository: "acme/failing", Role: streams.RoleConsumer, Worktree: failing},
			{Repository: "acme/passing", Role: streams.RoleConsumer, Worktree: passing},
		},
	}); err != nil {
		t.Fatal(err)
	}
	git, verifier := newFakeGit(), newFakeVerifier()
	verifier.linked[failing] = VerificationRun{Passed: false, Command: "go test -p 1 ./...", Details: []string{"backend test: compilation failed"}}
	engine := &Engine{Store: store, Git: git, Node: newFakeNode(), Verifier: verifier, CacheRoot: filepath.Join(base, "cache")}

	result, err := engine.Run(context.Background(), Options{
		Library: library, Consumers: []string{failing, passing}, Verify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Consumers) != 2 {
		t.Fatalf("consumers = %#v, want both reported", result.Consumers)
	}
	byPath := map[string]ConsumerResult{}
	for _, consumer := range result.Consumers {
		byPath[consumer.Consumer] = consumer
	}
	if byPath[failing].Verification == nil || byPath[failing].Verification.Passed {
		t.Fatalf("the failing consumer = %#v, want a failed verification", byPath[failing].Verification)
	}
	if byPath[passing].Verification == nil || !byPath[passing].Verification.Passed {
		t.Fatalf("the passing consumer = %#v, want it reported as passing", byPath[passing].Verification)
	}
	statement := byPath[passing].Verification.Statement
	if !strings.Contains(statement, "verified against unpublished") || !strings.Contains(statement, git.hash) || !strings.Contains(statement, "(dirty)") {
		t.Errorf("statement = %q, want the unpublished/content-hash/dirty sentence", statement)
	}
	if len(byPath[passing].Verification.ActiveLinks) == 0 {
		t.Error("the verification did not print its active links")
	}
	if !strings.Contains(byPath[passing].Verification.ActiveLinks[0], "replaces v0.4.0") {
		t.Errorf("active link does not name the published version it replaced: %q", byPath[passing].Verification.ActiveLinks[0])
	}
	if byPath[passing].Verification.PublishedBaseline.Command == "" {
		t.Error("the GOWORK=off pre-landing check did not run")
	}
	env := verifier.envSeen[passing]
	if !containsAll(env, "NX_DAEMON=false", "NX_SKIP_NX_CACHE=true") {
		t.Errorf("verification env = %v, want the single-worker Node environment", env)
	}
}

// AC: undo-restores-published-versions — undo succeeds without reading the
// removed library worktree, leaves no go.work behind, and clears the record.
func TestUndoRestoresPublishedVersionsAfterTheLibraryIsGone(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})
	if _, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(fixture.library); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.engine.Run(context.Background(), Options{
		Consumers: []string{fixture.consumer}, Undo: true,
	})
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if result.Failed() {
		t.Fatalf("undo reported errors: %#v", result.Consumers)
	}
	if _, err := os.Stat(filepath.Join(fixture.consumer, "go.work")); !os.IsNotExist(err) {
		t.Errorf("go.work survived undo: %v", err)
	}
	stream, err := fixture.store.Load("fixture")
	if err != nil {
		t.Fatal(err)
	}
	member, _ := stream.Member("acme/app")
	if len(member.Links) != 0 {
		t.Fatalf("links survived undo: %#v", member.Links)
	}
}

func TestUndoOnAConsumerWithNoRecordedLinkIsReportedNotFailed(t *testing.T) {
	fixture := newFixture(t, map[string]string{"backend/go.mod": goLibraryModule}, map[string]string{})
	result, err := fixture.engine.Run(context.Background(), Options{
		Consumers: []string{fixture.consumer}, Undo: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Consumers[0].Skipped || !strings.Contains(result.Consumers[0].Reason, "nothing to undo") {
		t.Fatalf("consumers = %#v", result.Consumers)
	}
}

// Re-linking after the library moves must replace the record rather than append
// a second one, and must keep the ORIGINAL published version — that is what
// undo has to restore.
func TestRelinkingKeepsTheOriginalPublishedVersion(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})
	if _, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	}); err != nil {
		t.Fatal(err)
	}
	fixture.git.hash = "movedhash9876543210"
	if _, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	}); err != nil {
		t.Fatal(err)
	}
	stream, err := fixture.store.Load("fixture")
	if err != nil {
		t.Fatal(err)
	}
	member, _ := stream.Member("acme/app")
	if len(member.Links) != 1 {
		t.Fatalf("links = %#v, want the record replaced rather than appended", member.Links)
	}
	if member.Links[0].ContentHash != "movedhash9876543210" {
		t.Errorf("content hash = %q, want the moved tree", member.Links[0].ContentHash)
	}
	if member.Links[0].PreviousVersion != "v0.4.0" {
		t.Errorf("previous version = %q, want the version that was published before any link existed", member.Links[0].PreviousVersion)
	}
}

// A link that changes a tracked file is a defect, and the verb says so rather
// than reporting success.
func TestLinkingThatChangesATrackedFileIsReportedAsAFailure(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})
	fixture.git.tracked[fixture.consumer] = nil
	original := fixture.git.TrackedChanges
	_ = original
	// The second read reports a tracked change the first did not, exactly as a
	// manifest-mutating link mechanism would.
	fixture.engine.Git = &trackedChangeInjector{fakeGit: fixture.git, after: []string{"package.json"}}
	result, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Failed() {
		t.Fatal("a tracked-file change was reported as success")
	}
	if !strings.Contains(strings.Join(result.Consumers[0].Errors, " "), "package.json") {
		t.Errorf("errors = %v, want the tracked file named", result.Consumers[0].Errors)
	}
}

type trackedChangeInjector struct {
	*fakeGit
	after []string
	reads int
}

func (git *trackedChangeInjector) TrackedChanges(ctx context.Context, dir string) ([]string, error) {
	git.reads++
	if git.reads == 1 {
		return nil, nil
	}
	return git.after, nil
}

func TestPlanStatesTheChecksBeforeTheyRun(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})
	result, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer}, Verify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(result.Plan, " | ")
	for _, want := range []string{"discover the library's published identities", "excluded go.work", "single-worker", "GOWORK=off"} {
		if !strings.Contains(plan, want) {
			t.Errorf("plan does not state %q: %s", want, plan)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func containsAll(values []string, wanted ...string) bool {
	present := map[string]bool{}
	for _, value := range values {
		present[value] = true
	}
	for _, want := range wanted {
		if !present[want] {
			return false
		}
	}
	return true
}

// MF-1. The record is written BEFORE the filesystem changes, so a record that
// cannot be written leaves nothing on disk to strand the worktree.
//
// Derived from the reviewer's probe A, which showed `go.work` written with
// zero links recorded and `--undo` reporting "nothing to undo".
func TestNoLinkIsWrittenWhenTheRecordCannotBe(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})

	directory := fixture.store.Dir("fixture")
	if err := os.Chmod(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })

	result, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	})
	if err == nil && !result.Failed() {
		t.Fatal("an unwritable record reported success")
	}
	if _, statErr := os.Stat(filepath.Join(fixture.consumer, "go.work")); !os.IsNotExist(statErr) {
		t.Fatalf("go.work was written even though the link could not be recorded: %v", statErr)
	}
}

// MF-2. `--undo` clears a `go.work` that stream state has no record of, so the
// command the merge guard names can actually satisfy the guard.
func TestUndoRemovesAnUnrecordedGoWork(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n"})
	workspace := filepath.Join(fixture.consumer, "go.work")
	if err := os.WriteFile(workspace, []byte("go 1.27\n\nuse (\n\t./backend\n\t/elsewhere/library\n)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The guard fires and names exactly this command.
	links, err := HasLiveLink(fixture.store, fixture.consumer)
	if err != nil || len(links) != 1 {
		t.Fatalf("links = %#v, err = %v; want the go.work signal", links, err)
	}

	result, err := fixture.engine.Run(context.Background(), Options{
		Consumers: []string{fixture.consumer}, Undo: true,
	})
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if result.Failed() {
		t.Fatalf("undo reported errors: %#v", result.Consumers)
	}
	if _, statErr := os.Stat(workspace); !os.IsNotExist(statErr) {
		t.Fatalf("the unrecorded go.work survived --undo: %v", statErr)
	}
	// The guard is now satisfied — which is what makes the refusal's named
	// command a real next step rather than a dead end.
	after, err := HasLiveLink(fixture.store, fixture.consumer)
	if err != nil || len(after) != 0 {
		t.Fatalf("the guard still fires after --undo: %#v (err %v)", after, err)
	}
}

// go.work already lost its `use` entry for the library — a hand edit, or a
// rebase — before --undo ever ran. There is nothing on disk this undo owns
// any more, so it clears the stale record without touching the file at all:
// deleting go.work here could take other entries this undo has no business
// owning down with it.
func TestUndoClearsARecordWhenGoWorkNoLongerReferencesTheLibrary(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})

	if _, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	}); err != nil {
		t.Fatalf("link: %v", err)
	}
	workspace := filepath.Join(fixture.consumer, "go.work")
	before := readFile(t, workspace)
	if !strings.Contains(before, filepath.ToSlash(filepath.Join(fixture.library, "backend"))) {
		t.Fatalf("fixture is wrong: go.work does not yet use the library:\n%s", before)
	}

	// A hand edit (or a rebase) drops the library's use entry but leaves the
	// consumer's own entries, and the rest of the file, untouched.
	edited := "go 1.27\n\nuse (\n\t./backend\n)\n"
	if err := os.WriteFile(workspace, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := fixture.engine.Run(context.Background(), Options{
		Consumers: []string{fixture.consumer}, Undo: true,
	})
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if result.Failed() {
		t.Fatalf("undo reported errors: %#v", result.Consumers)
	}
	if after := readFile(t, workspace); after != edited {
		t.Fatalf("undo touched go.work despite the library entry already being gone:\n%s", after)
	}
	var notes []string
	for _, consumer := range result.Consumers {
		notes = append(notes, consumer.Notes...)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "record cleared") {
		t.Fatalf("notes = %#v, want one note saying the record was cleared", notes)
	}
	stream, err := fixture.store.Load("fixture")
	if err != nil {
		t.Fatal(err)
	}
	member, _ := stream.Member("acme/app")
	if len(member.Links) != 0 {
		t.Fatalf("recorded links = %#v, want the go.work record cleared", member.Links)
	}
}

// MF-3. A failed removal KEEPS its record, so the guard stays closed and
// `stream end` keeps refusing while the artefact is still on disk.
func TestUndoKeepsTheRecordWhenRemovalFails(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"libs/core/package.json": `{"name":"@acme/core","version":"1.0.0"}`},
		map[string]string{
			"package.json":   `{"name":"app","dependencies":{"@acme/core":"^1.0.0"}}`,
			"pnpm-lock.yaml": "lockfileVersion: '9.0'\n",
		})
	if _, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	}); err != nil {
		t.Fatal(err)
	}
	fixture.node.unlinkErr = map[string]error{
		fixture.consumer + " @acme/core": errors.New("EACCES: node_modules is read-only"),
	}

	result, err := fixture.engine.Run(context.Background(), Options{
		Consumers: []string{fixture.consumer}, Undo: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Failed() {
		t.Fatal("a failed removal was reported as a successful undo")
	}
	stream, err := fixture.store.Load("fixture")
	if err != nil {
		t.Fatal(err)
	}
	member, _ := stream.Member("acme/app")
	if len(member.Links) != 1 {
		t.Fatalf("links = %#v; a failed removal must keep its record so the guard stays closed", member.Links)
	}
	// The merge guard must still refuse.
	links, err := HasLiveLink(fixture.store, fixture.consumer)
	if err != nil || len(links) == 0 {
		t.Fatalf("the guard stopped firing while the link is still live: %#v (err %v)", links, err)
	}
}

// MF-5. The frozen install proves the UNLINKED tree, so it runs once per
// consumer regardless of how many identities that consumer declares.
func TestFrozenInstallRunsOncePerConsumerNotPerIdentity(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{
			"libs/core/package.json": `{"name":"@acme/core","version":"1.0.0"}`,
			"libs/ui/package.json":   `{"name":"@acme/ui","version":"1.0.0"}`,
		},
		map[string]string{
			"package.json":   `{"name":"app","dependencies":{"@acme/core":"^1.0.0","@acme/ui":"^1.0.0"}}`,
			"pnpm-lock.yaml": "lockfileVersion: '9.0'\n",
		})
	result, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed() {
		t.Fatalf("result = %#v", result.Consumers)
	}
	if len(fixture.node.installed) != 1 {
		t.Fatalf("frozen installs = %v, want exactly one against the unlinked tree", fixture.node.installed)
	}
	if len(fixture.node.linked) != 2 {
		t.Fatalf("linked = %v, want both identities linked", fixture.node.linked)
	}
	// The single install must precede every link.
	if fixture.node.order[0] != "install "+fixture.consumer {
		t.Fatalf("order = %v, want the frozen install first", fixture.node.order)
	}
}

// MF-7. A consumer no open stream holds is REFUSED before anything is written:
// an unrecorded link cannot be undone and the guard's state signal cannot see
// it.
func TestALinkThatCannotBeRecordedIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	base := t.TempDir()
	library := writeTree(t, filepath.Join(base, "library"), map[string]string{"backend/go.mod": goLibraryModule})
	consumer := writeTree(t, filepath.Join(base, "consumer"), map[string]string{
		"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n",
	})
	store := streams.OpenAt(filepath.Join(base, "wb-home", "streams"))
	engine := &Engine{Store: store, Git: newFakeGit(), Node: newFakeNode(), CacheRoot: filepath.Join(base, "cache")}

	_, err := engine.Run(context.Background(), Options{Library: library, Consumers: []string{consumer}})
	refusal, refused := Refused(err)
	if !refused || refusal.Code != RefusalNotRecordable {
		t.Fatalf("error = %v, want a %s refusal", err, RefusalNotRecordable)
	}
	if !strings.Contains(strings.Join(refusal.Sanctioned, " "), "wb stream join") {
		t.Errorf("refusal does not name how to make the consumer recordable: %v", refusal.Sanctioned)
	}
	if _, statErr := os.Stat(filepath.Join(consumer, "go.work")); !os.IsNotExist(statErr) {
		t.Fatalf("a refused link still wrote go.work: %v", statErr)
	}
}

// MF (round 2). Membership is resolved per CONSUMER, not from the library.
//
// The reviewer's probe R2-F inverted: a stream holds the library and one app,
// and a THIRD worktree no member names is linked. Resolving from the library
// made this look recordable, so go.work was written, nothing was recorded, and
// the verb exited 0 — the same un-undoable link round 1 rejected.
func TestAConsumerOutsideTheStreamIsRefusedEvenWhenTheStreamHoldsTheLibrary(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})

	// A third worktree that is a real consumer but no stream member.
	outsider := writeTree(t, filepath.Join(t.TempDir(), "outsider"), map[string]string{
		"backend/go.mod": "module github.com/acme/outsider/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n",
	})

	_, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{outsider},
	})
	refusal, refused := Refused(err)
	if !refused || refusal.Code != RefusalNotRecordable {
		t.Fatalf("error = %v, want a %s refusal even though the stream holds the library", err, RefusalNotRecordable)
	}
	if !strings.Contains(refusal.Message, outsider) {
		t.Errorf("refusal does not name the unrecordable consumer: %s", refusal.Message)
	}
	if _, statErr := os.Stat(filepath.Join(outsider, "go.work")); !os.IsNotExist(statErr) {
		t.Fatalf("go.work was written into a worktree no stream records: %v", statErr)
	}
}

// All fences run before the first side effect: one unrecordable consumer stops
// the whole invocation, so a recordable sibling is not half-linked.
func TestOneUnrecordableConsumerLinksNothingAtAll(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n"})
	outsider := writeTree(t, filepath.Join(t.TempDir(), "outsider"), map[string]string{
		"backend/go.mod": "module github.com/acme/outsider/backend\n\ngo 1.27\n\nrequire github.com/acme/library/backend v0.4.0\n",
	})

	_, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer, outsider},
	})
	if _, refused := Refused(err); !refused {
		t.Fatalf("error = %v, want a refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.consumer, "go.work")); !os.IsNotExist(statErr) {
		t.Fatalf("the recordable consumer was linked despite the refusal: %v", statErr)
	}
}

// recordLinks refuses rather than silently writing nothing when its update
// matches no member — the second half of the same defect.
func TestRecordLinksFailsWhenItMatchesNoMember(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n"})
	err := fixture.engine.recordLinks("fixture", filepath.Join(t.TempDir(), "not-a-member"), []streams.Link{
		{Library: fixture.library, Mechanism: streams.MechanismGoWork, Identity: "github.com/acme/library/backend"},
	})
	if err == nil {
		t.Fatal("recording against a path no member names reported success")
	}
	if !strings.Contains(err.Error(), "no member at") {
		t.Errorf("error = %v, want it to say the stream has no such member", err)
	}
	if err := fixture.engine.recordLinks("", fixture.consumer, []streams.Link{{Identity: "x"}}); err == nil {
		t.Fatal("recording with no stream reported success")
	}
}

// SHOULD-FIX (d). A consumer that was skipped is not verified, so it must not
// be told a verifier was unavailable for a run it was never part of.
func TestSkippedConsumersAreNotToldTheVerifierWasUnavailable(t *testing.T) {
	fixture := newFixture(t,
		map[string]string{"backend/go.mod": goLibraryModule},
		map[string]string{"backend/go.mod": "module github.com/acme/app/backend\n\ngo 1.27\n"})
	fixture.engine.Verifier = nil
	result, err := fixture.engine.Run(context.Background(), Options{
		Library: fixture.library, Consumers: []string{fixture.consumer}, Verify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Consumers) != 1 || !result.Consumers[0].Skipped {
		t.Fatalf("consumers = %#v, want the one consumer skipped", result.Consumers)
	}
	if len(result.Consumers[0].Errors) != 0 {
		t.Fatalf("a skipped consumer was given verification errors: %v", result.Consumers[0].Errors)
	}
}
