package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// TestClonePathInvertsToRemoteURL encodes
// projects-root-layout#ac:clone-path-inverts-to-url inside the worktree
// subsystem that owns canonical clone paths: a canonical clone at
// <root>/github.com/dal-go/dalgo inverts to https://github.com/dal-go/dalgo,
// and the answer cannot have come from the repository's own remote because
// that remote is a local bare repository here.
func TestClonePathInvertsToRemoteURL(t *testing.T) {
	fixture := newHostLevelFixture(t, "github.com", "dal-go", "dalgo")
	projectsRoot, canonical := fixture.projectsRoot, fixture.canonical

	got, err := ExpectedRemoteURL(projectsRoot, canonical)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://github.com/dal-go/dalgo" {
		t.Fatalf("ExpectedRemoteURL(%q) = %q, want https://github.com/dal-go/dalgo", canonical, got)
	}
	origin := gitTestOutput(t, canonical, "remote", "get-url", "origin")
	if strings.Contains(origin, "github.com") {
		t.Fatalf("fixture origin unexpectedly names the forge, so the test would prove nothing: %q", origin)
	}
}

// TestExpectedRemoteURLInvertsTheFleetSlugs proves the inverse holds for
// multi-segment hosted paths and for the owner/repository pairs actually
// present in this fleet, including an explicit-port forge.
func TestExpectedRemoteURLInvertsTheFleetSlugs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, slug := range []string{
		"github.com/dal-go/dalgo",
		"github.com/sneat-dev/wb",
		"github.com/sneat-co/backstage",
		"gitlab.example.test/sneat-dev/wb",
		"github.com:8443/acme/app",
	} {
		got, err := ExpectedRemoteURL(root, filepath.Join(root, filepath.FromSlash(slug)))
		if err != nil {
			t.Fatalf("ExpectedRemoteURL(%q): %v", slug, err)
		}
		if want := "https://" + slug; got != want {
			t.Fatalf("ExpectedRemoteURL(%q) = %q, want %q", slug, got, want)
		}
	}
}

// TestExpectedRemoteURLRefusesALegacyFirstLevel encodes the finding half of
// projects-root-layout#ac:clone-path-inverts-to-url at this boundary: the
// legacy two-level placement this fleet still uses has no literal hostname, so
// WB refuses to invent a remote for it. `wb layout audit` reports it as a
// layout finding.
func TestExpectedRemoteURLRefusesALegacyFirstLevel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, legacy := range []string{"sneat-dev/wb", "dal-go/dalgo", "acme/app"} {
		if remote, err := ExpectedRemoteURL(root, filepath.Join(root, filepath.FromSlash(legacy))); err == nil {
			t.Fatalf("ExpectedRemoteURL(%q) invented %q for a first level that is not a hostname", legacy, remote)
		}
	}
}

// TestUnqualifiedCoordinateResolvesTheLiteralHostLevel proves the derivation
// finds a clone that already sits at <root>/<host>/<org>/<repo> without
// requiring the fleet to move, and that it creates the task checkout there.
func TestUnqualifiedCoordinateResolvesTheLiteralHostLevel(t *testing.T) {
	fixture0 := newHostLevelFixture(t, "github.com", "dal-go", "dalgo")
	projectsRoot, canonical := fixture0.projectsRoot, fixture0.canonical

	resolved, err := CanonicalRepositoryPath(projectsRoot, "dal-go/dalgo")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != canonical {
		t.Fatalf("CanonicalRepositoryPath = %q, want the host-level clone %q", resolved, canonical)
	}

	results, err := Create(context.Background(), []string{"dal-go/dalgo"}, CreateOptions{
		ProjectsRoot: projectsRoot,
		Operation:    "host-level-task",
		WorkLog:      WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	wantWorktree := filepath.Join(canonical, ".worktrees", "host-level-task")
	if len(results) != 1 || results[0].CanonicalDir != canonical || results[0].WorktreeDir != wantWorktree {
		t.Fatalf("create results = %+v, want canonical %q worktree %q", results, canonical, wantWorktree)
	}
	if _, err := os.Stat(wantWorktree); err != nil {
		t.Fatalf("task checkout was not created at the host-level clone: %v", err)
	}
}

// TestUnqualifiedCoordinateStaysOnALegacyClone proves a fleet that has not
// adopted the host level keeps resolving to the exact clone it has, so nothing
// has to move to stay operable.
func TestUnqualifiedCoordinateStaysOnALegacyClone(t *testing.T) {
	fixture := newGitFixture(t)

	resolved, err := CanonicalRepositoryPath(fixture.projectsRoot, "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != fixture.canonical {
		t.Fatalf("CanonicalRepositoryPath = %q, want the legacy clone %q", resolved, fixture.canonical)
	}
	if _, err := ExpectedRemoteURL(fixture.projectsRoot, resolved); err == nil {
		t.Fatal("the legacy two-level placement must not be invertible to a remote")
	}
}

// TestCanonicalRepositoryPathPrefersTheHostLevelAndRefusesAmbiguity covers the
// one case the flat legacy root could not represent: the same owner/repository
// on two forges.
func TestCanonicalRepositoryPathPrefersTheHostLevelAndRefusesAmbiguity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	legacy := newLegacyClone(t, root, "acme", "app")
	if got, err := CanonicalRepositoryPath(root, "acme/app"); err != nil || got != legacy {
		t.Fatalf("CanonicalRepositoryPath(legacy only) = %q, %v", got, err)
	}
	hosted := newHostLevelClone(t, root, "github.com", "acme", "app")
	if got, err := CanonicalRepositoryPath(root, "acme/app"); err != nil || got != hosted {
		t.Fatalf("CanonicalRepositoryPath(host level present) = %q, %v; want %q", got, err, hosted)
	}
	newHostLevelClone(t, root, "gitlab.example.test", "acme", "app")
	if _, err := CanonicalRepositoryPath(root, "acme/app"); err == nil {
		t.Fatal("an owner/repository on two hosts must be refused rather than picked arbitrarily")
	} else if !strings.Contains(err.Error(), "more than one host") {
		t.Fatalf("ambiguity error = %v", err)
	}
}

// TestGuardAcceptsACanonicalCloneAtTheHostLevel proves the guard recognizes a
// clone placed at <root>/<host>/<org>/<repo> as canonical rather than foreign.
func TestGuardAcceptsACanonicalCloneAtTheHostLevel(t *testing.T) {
	fixture := newHostLevelFixture(t, "github.com", "dal-go", "dalgo")

	result, err := Guard(context.Background(), fixture.canonical, GuardOptions{ProjectsRoot: fixture.projectsRoot})
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != "canonical" || result.CanonicalDir != fixture.canonical {
		t.Fatalf("guard result = %+v", result)
	}
}

// TestCanonicalCoordinatesRecognizeBothPlacements pins the boundary-aware
// interpretation of a canonical clone path.
func TestCanonicalCoordinatesRecognizeBothPlacements(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, test := range []struct {
		relative string
		host     string
		owner    string
		name     string
		ok       bool
	}{
		{relative: "github.com/dal-go/dalgo", host: "github.com", owner: "dal-go", name: "dalgo", ok: true},
		{relative: "git.example.test/sneat-dev/wb", host: "git.example.test", owner: "sneat-dev", name: "wb", ok: true},
		{relative: "sneat-dev/wb", owner: "sneat-dev", name: "wb", ok: true},
		{relative: "sneat-dev/wb/extra", ok: false},
		{relative: "sneat-dev", ok: false},
		{relative: "..", ok: false},
	} {
		host, owner, name, err := canonicalCoordinates(root, filepath.Join(root, filepath.FromSlash(test.relative)))
		if test.ok && (err != nil || host != test.host || owner != test.owner || name != test.name) {
			t.Fatalf("canonicalCoordinates(%q) = (%q, %q, %q, %v), want (%q, %q, %q)",
				test.relative, host, owner, name, err, test.host, test.owner, test.name)
		}
		if !test.ok && err == nil {
			t.Fatalf("canonicalCoordinates(%q) accepted a path that is not a canonical clone", test.relative)
		}
	}
}

// TestSplitRepositoryAddressAcceptsTheLiteralHostLevel pins the extended
// coordinate parser: an optional literal hostname, then owner/repository.
func TestSplitRepositoryAddressAcceptsTheLiteralHostLevel(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		repository string
		host       string
		owner      string
		name       string
		ok         bool
	}{
		{repository: "acme/app", owner: "acme", name: "app", ok: true},
		{repository: "github.com/dal-go/dalgo", host: "github.com", owner: "dal-go", name: "dalgo", ok: true},
		{repository: "git.example.test/acme/app", host: "git.example.test", owner: "acme", name: "app", ok: true},
		{repository: "dal-go/dalgo/dalgo", ok: false},
		{repository: "a/b/c/d", ok: false},
		{repository: "acme", ok: false},
		{repository: " acme/app", ok: false},
	} {
		address, err := splitRepositoryAddress(test.repository)
		if test.ok && (err != nil || address.Host != test.host || address.Org != test.owner || address.Repo != test.name) {
			t.Fatalf("splitRepositoryAddress(%q) = %+v, %v, want %s/%s/%s",
				test.repository, address, err, test.host, test.owner, test.name)
		}
		if !test.ok && err == nil {
			t.Fatalf("splitRepositoryAddress(%q) accepted an invalid coordinate", test.repository)
		}
		// The owner/repository projection every claim and work log carries
		// keeps working unchanged.
		if test.ok {
			owner, name, err := splitRepository(test.repository)
			if err != nil || owner != test.owner || name != test.name {
				t.Fatalf("splitRepository(%q) = %q/%q, %v", test.repository, owner, name, err)
			}
		}
	}
}

type hostLevelFixture struct {
	projectsRoot string
	canonical    string
}

// newHostLevelFixture builds a hermetic projects root whose canonical clone
// sits at <root>/<host>/<org>/<repo>, with a local bare origin so no test needs
// the network.
func newHostLevelFixture(t *testing.T, host, org, name string) hostLevelFixture {
	t.Helper()
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	t.Setenv(wbhome.EnvOverride, projectsRoot)
	t.Setenv(wbhome.EnvMigrationCompat, "")
	t.Setenv("HOME", filepath.Join(root, "home"))
	// These fixtures exercise the literal host level in clone derivation, not
	// the store mode: select repository-local mode explicitly so the checkout
	// path stays the canonical clone's own .worktrees. The central store's host
	// level is covered by store_mode_test.go.
	configHome := filepath.Join(root, "config")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	mustWriteBranchConfig(t, filepath.Join(configHome, "wb", "worktrees.yaml"), "version: 1\nworktrees:\n  store: repository-local\n")
	canonical := newHostLevelCloneIn(t, root, projectsRoot, host, org, name)
	return hostLevelFixture{projectsRoot: projectsRoot, canonical: canonical}
}

func newHostLevelClone(t *testing.T, projectsRoot, host, org, name string) string {
	t.Helper()
	return newHostLevelCloneIn(t, t.TempDir(), projectsRoot, host, org, name)
}

func newHostLevelCloneIn(t *testing.T, root, projectsRoot, host, org, name string) string {
	t.Helper()
	return seedCloneAt(t, root, filepath.Join(projectsRoot, host, org, name))
}

func newLegacyClone(t *testing.T, projectsRoot, org, name string) string {
	t.Helper()
	return seedCloneAt(t, t.TempDir(), filepath.Join(projectsRoot, org, name))
}

// seedCloneAt creates one canonical clone at dest, complete with a local bare
// origin, a pushed main branch, and a committed README.
func seedCloneAt(t *testing.T, root, dest string) string {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// The bare origin lives in a randomly named directory so a clone's origin
	// URL cannot accidentally mention the hostname under test.
	remoteHolder, err := os.MkdirTemp(root, "origin-")
	if err != nil {
		t.Fatal(err)
	}
	remote := filepath.Join(remoteHolder, "remote.git")
	gitTest(t, root, "init", "--bare", "--initial-branch=main", remote)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, root, "clone", remote, dest)
	configureGitUser(t, dest)
	if err := os.WriteFile(filepath.Join(dest, "README.md"), []byte("# app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dest, "add", "README.md")
	gitTest(t, dest, "commit", "-m", "initial")
	gitTest(t, dest, "push", "-u", "origin", "main")
	resolved, err := filepath.EvalSymlinks(dest)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// TestHostLevelCloneLocalWorktreesAreDiscoverable proves the inventory walk
// reads through the literal host level. A host-level clone whose default-local
// task checkout the walk could not see would be invisible to `wb worktree
// list`, cleanup and guard even though it is the canonical placement.
func TestHostLevelCloneLocalWorktreesAreDiscoverable(t *testing.T) {
	fixture := newHostLevelFixture(t, "github.com", "dal-go", "dalgo")
	if _, err := Create(context.Background(), []string{"dal-go/dalgo"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "list-task",
		WorkLog:      WorkLogOptions{Model: "unknown"},
	}); err != nil {
		t.Fatal(err)
	}

	listed, err := List(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Task: "list-task", Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(fixture.canonical, ".worktrees", "list-task")
	if len(listed) != 1 {
		t.Fatalf("List found %d worktrees, want the host-level clone's one checkout", len(listed))
	}
	if listed[0].WorktreeDir != want || listed[0].CanonicalDir != fixture.canonical {
		t.Fatalf("listed = %+v, want worktree %q canonical %q", listed[0], want, fixture.canonical)
	}
	if listed[0].Repository != "dal-go/dalgo" {
		t.Fatalf("repository identity = %q, want dal-go/dalgo", listed[0].Repository)
	}
}

// TestCanonicalDirMatchesRepositoryAcceptsBothPlacements pins the durable-record
// check: a lifecycle backlog written before the host level existed must keep
// validating, and a host-qualified coordinate must require its own host.
func TestCanonicalDirMatchesRepositoryAcceptsBothPlacements(t *testing.T) {
	t.Parallel()
	root := filepath.Join(string(filepath.Separator), "projects")
	for _, test := range []struct {
		repository string
		dir        string
		ok         bool
	}{
		{repository: "acme/app", dir: filepath.Join(root, "acme", "app"), ok: true},
		{repository: "acme/app", dir: filepath.Join(root, "github.com", "acme", "app"), ok: true},
		{repository: "acme/app", dir: filepath.Join(root, "acme", "other"), ok: false},
		{repository: "acme/app", dir: filepath.Join(root, "acme"), ok: false},
		{repository: "github.com/acme/app", dir: filepath.Join(root, "github.com", "acme", "app"), ok: true},
		{repository: "github.com/acme/app", dir: filepath.Join(root, "acme", "app"), ok: false},
		{repository: "github.com/acme/app", dir: filepath.Join(root, "gitlab.example.test", "acme", "app"), ok: false},
		{repository: "not-a-host/acme/app", dir: filepath.Join(root, "not-a-host", "acme", "app"), ok: false},
		{repository: "ACME/App", dir: filepath.Join(root, "acme", "app"), ok: true},
	} {
		if got := canonicalDirMatchesRepository(root, test.repository, test.dir); got != test.ok {
			t.Fatalf("canonicalDirMatchesRepository(%q, %q) = %t, want %t", test.repository, test.dir, got, test.ok)
		}
	}
}

// TestCentralStoreInventoryAcceptsAPortHostedForgeLevel pins the two
// first-level predicates staying in step. A forge with an explicit port is a
// valid host level for placement, so a checkout at
// <root>/.worktrees/<task>/github.com:8443/{org}/{repo} must be inventoried —
// not skipped with a false "invalid owner or legacy repository directory name"
// diagnostic before the host branch is ever consulted.
func TestCentralStoreInventoryAcceptsAPortHostedForgeLevel(t *testing.T) {
	base := t.TempDir()
	projectsRoot := filepath.Join(base, "projects")
	t.Setenv(wbhome.EnvOverride, projectsRoot)
	t.Setenv(wbhome.EnvMigrationCompat, "")
	t.Setenv("HOME", filepath.Join(base, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))

	canonical := seedCloneAt(t, base, filepath.Join(projectsRoot, "github.com:8443", "acme", "app"))
	checkout := filepath.Join(projectsRoot, ".worktrees", "port-task", "github.com:8443", "acme", "app")
	if err := os.MkdirAll(filepath.Dir(checkout), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, canonical, "worktree", "add", "-q", "-b", "port-task", checkout)

	outcome, err := ListWithDiagnostics(context.Background(), ListOptions{ProjectsRoot: projectsRoot, Task: "port-task", Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, result := range outcome.Results {
		if filepath.Clean(result.WorktreeDir) == filepath.Clean(checkout) {
			found = true
			if result.CanonicalDir != canonical {
				t.Fatalf("canonical for the port-hosted checkout = %q, want %q", result.CanonicalDir, canonical)
			}
		}
	}
	if !found {
		t.Fatalf("the port-hosted checkout was not inventoried: results=%+v diagnostics=%+v", outcome.Results, outcome.Diagnostics)
	}
	for _, diagnostic := range outcome.Diagnostics {
		if strings.Contains(diagnostic.Message, "invalid owner") {
			t.Fatalf("a port-hosted forge level was rejected as an invalid owner: %+v", diagnostic)
		}
	}
}
