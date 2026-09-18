package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func migCovInstallFakeGo(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	writeCampaignFile(t, filepath.Join(binDir, "go"), `#!/bin/sh
if [ "$1" = "list" ]; then
  if [ "$GO_FAKE_MODE" = "bad-json" ]; then
    printf 'not json'
    exit 0
  fi
  printf '%s' "$GO_FAKE_LIST"
  exit 0
fi
if [ "$1" = "env" ]; then
  if [ "$GO_FAKE_MODE" = "env-fail" ]; then
    exit 1
  fi
  printf '%s\n' "${GOMODCACHE:-}"
  exit 0
fi
if [ "$1" = "mod" ] && [ "$2" = "graph" ]; then
  if [ "$GO_FAKE_MODE" = "graph-fail" ]; then
    exit 1
  fi
  printf '%s' "$GO_FAKE_GRAPH"
  exit 0
fi
exit 1
`)
	if err := os.Chmod(filepath.Join(binDir, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestMigCovInspectGoModuleGraphReportsEachFailure(t *testing.T) {
	root := t.TempDir()
	migCovInstallFakeGo(t)

	t.Setenv("GO_FAKE_MODE", "bad-json")
	if _, _, err := inspectGoModuleGraph(root, false); err == nil || !strings.Contains(err.Error(), "decode go module list") {
		t.Fatalf("inspectGoModuleGraph(bad json) = %v", err)
	}

	t.Setenv("GO_FAKE_MODE", "env-fail")
	t.Setenv("GO_FAKE_LIST", `{"Path":"example.com/app","Version":"v1.0.0"}`)
	if _, _, err := inspectGoModuleGraph(root, false); err == nil {
		t.Fatal("inspectGoModuleGraph(unreadable module cache) succeeded")
	}

	t.Setenv("GO_FAKE_MODE", "graph-fail")
	t.Setenv("GO_FAKE_LIST", `{"Path":"example.com/app","Main":true}`)
	if _, _, err := inspectGoModuleGraph(root, false); err == nil {
		t.Fatal("inspectGoModuleGraph(unreadable graph) succeeded")
	}

	// A malformed graph line is skipped rather than trusted as an edge.
	t.Setenv("GO_FAKE_MODE", "graph-ok")
	t.Setenv("GO_FAKE_GRAPH", "a b c")
	modules, children, err := inspectGoModuleGraph(root, false)
	if err != nil {
		t.Fatalf("inspectGoModuleGraph(malformed graph line) = %v", err)
	}
	if len(children) != 0 || len(modules) != 1 {
		t.Fatalf("modules = %+v, children = %+v", modules, children)
	}

	// A module whose recorded go.mod is unreadable aborts requirement-edge
	// recovery.
	directory := t.TempDir()
	t.Setenv("GO_FAKE_MODE", "graph-ok")
	t.Setenv("GO_FAKE_LIST", `{"Path":"example.com/app","Main":true,"GoMod":"`+directory+`"}`)
	t.Setenv("GO_FAKE_GRAPH", "example.com/app example.com/dep")
	if _, _, err := inspectGoModuleGraph(root, false); err == nil {
		t.Fatal("inspectGoModuleGraph(unreadable recorded go.mod) succeeded")
	}
}

func TestMigCovPopulateGoModPathsUsesCacheAndProxy(t *testing.T) {
	// A working directory that does not exist cannot report its module cache.
	if err := populateGoModPaths(filepath.Join(t.TempDir(), "absent"), map[string]listedModule{}); err == nil {
		t.Fatal("populateGoModPaths(missing directory) succeeded")
	}

	// A module already present in the local module cache is used as-is.
	cache := t.TempDir()
	t.Setenv("GOMODCACHE", cache)
	t.Setenv("GOPROXY", "off")
	cached, err := cachedGoModPath(cache, "example.com/cached", "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cached), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cached, []byte("module example.com/cached\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	modules := map[string]listedModule{"example.com/cached": {Path: "example.com/cached", Version: "v1.0.0"}}
	if err := populateGoModPaths(t.TempDir(), modules); err != nil {
		t.Fatalf("populateGoModPaths(cached) = %v", err)
	}
	if modules["example.com/cached"].GoMod != cached {
		t.Fatalf("cached go.mod = %q, want %q", modules["example.com/cached"].GoMod, cached)
	}

	// A module that cannot be resolved is reported with its version.
	unresolved := map[string]listedModule{"example.com/absent": {Path: "example.com/absent", Version: "v9.9.9"}}
	if err := populateGoModPaths(t.TempDir(), unresolved); err == nil ||
		!strings.Contains(err.Error(), "resolve go.mod for example.com/absent@v9.9.9") {
		t.Fatalf("populateGoModPaths(unresolved) = %v", err)
	}
}

func TestMigCovPopulateGoModPathsResolvesThroughAModuleProxy(t *testing.T) {
	proxy := migCovModuleProxy(t, "example.com/proxied", "v1.2.3", map[string]string{
		"go.mod": "module example.com/proxied\n\ngo 1.24\n",
	})
	t.Setenv("GOMODCACHE", t.TempDir())
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(proxy))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GONOSUMDB", "*")

	modules := map[string]listedModule{
		"example.com/proxied":  {Path: "example.com/proxied", Version: "v1.2.3"},
		"example.com/complete": {Path: "example.com/complete", Version: "v1.0.0", GoMod: "/already/known/go.mod"},
		"example.com/main":     {Path: "example.com/main"},
	}
	if err := populateGoModPaths(t.TempDir(), modules); err != nil {
		t.Fatalf("populateGoModPaths(proxy) = %v", err)
	}
	if !strings.HasSuffix(modules["example.com/proxied"].GoMod, filepath.Join("@v", "v1.2.3.mod")) {
		t.Fatalf("resolved go.mod = %q", modules["example.com/proxied"].GoMod)
	}
	if modules["example.com/complete"].GoMod != "/already/known/go.mod" {
		t.Fatalf("already-known go.mod was overwritten: %+v", modules["example.com/complete"])
	}
	if modules["example.com/main"].GoMod != "" {
		t.Fatalf("versionless module gained a go.mod: %+v", modules["example.com/main"])
	}
}

func TestMigCovPlanCampaignRejectsUnusableRootsAndTargets(t *testing.T) {
	t.Parallel()
	spec := Spec{
		Format: MigrationFormatV1, ID: "plan",
		Steps: []Step{{Kind: "text.replace", From: "github.com/acme/app/old", To: "github.com/acme/app/new"}},
	}
	options := CampaignOptions{GitHubDir: t.TempDir(), Ref: "main", Verify: VerifyFull, Parallel: 1}

	// --resume needs a readable source manifest to discover the campaign
	// worktree.
	if _, err := planCampaign(spec, t.TempDir(), CampaignOptions{GitHubDir: t.TempDir(), Ref: "main", Resume: true}); err == nil ||
		!strings.Contains(err.Error(), "read source module for resume discovery") {
		t.Fatalf("planCampaign(resume without manifest) = %v", err)
	}

	// Without a Go module graph nothing can be planned.
	if _, err := planCampaign(spec, t.TempDir(), options); err == nil {
		t.Fatal("planCampaign(no module) succeeded")
	}

	root := t.TempDir()
	migCovWriteGoMod(t, root, "module github.com/acme/app\n\ngo 1.24\n")

	// A migration that touches no module in the graph is refused.
	unrelated := spec
	unrelated.Steps = []Step{{Kind: "text.replace", From: "example.com/other/old", To: "example.com/other/new"}}
	if _, err := planCampaign(unrelated, root, options); err == nil ||
		!strings.Contains(err.Error(), "does not reference a Go module") {
		t.Fatalf("planCampaign(unrelated) = %v", err)
	}

	// A non-GitHub module cannot be resolved to a repository.
	nonGitHub := t.TempDir()
	migCovWriteGoMod(t, nonGitHub, "module example.com/local\n\ngo 1.24\n")
	localSpec := Spec{
		Format: MigrationFormatV1, ID: "plan",
		Steps: []Step{{Kind: "text.replace", From: "example.com/local/pkg", To: "example.com/local/new"}},
	}
	if _, err := planCampaign(localSpec, nonGitHub, options); err == nil ||
		!strings.Contains(err.Error(), "not a resolvable GitHub module") {
		t.Fatalf("planCampaign(non-GitHub) = %v", err)
	}

	// A configured per-module ref overrides the campaign ref, and a declared
	// requirement joins the campaign even when the graph never selects it.
	withRequirement := spec
	withRequirement.GoModuleRequires = []GoModuleRequire{{Path: "github.com/acme/extra", Version: "v1.0.0"}}
	refOptions := options
	refOptions.ModuleRefs = map[string]string{"github.com/acme/app": "release"}
	c, err := planCampaign(withRequirement, root, refOptions)
	if err != nil {
		t.Fatalf("planCampaign(requirement) = %v", err)
	}
	if len(c.repos) != 2 {
		t.Fatalf("repositories = %+v", c.repos)
	}
	byModule := map[string]*campaignModule{}
	for _, repo := range c.repos {
		if repo.repository == "github.com/acme/app" && repo.ref != "release" {
			t.Fatalf("app repository ref = %q, want release", repo.ref)
		}
		for _, module := range repo.modules {
			byModule[module.path] = module
		}
	}
	if module, ok := byModule["github.com/acme/extra"]; !ok || module.migrate {
		t.Fatalf("declared requirement module = %+v", byModule)
	}
	if module := byModule["github.com/acme/app"]; module == nil || module.migrate {
		t.Fatalf("migration target module = %+v", byModule)
	}
}

func TestMigCovPlanCampaignRejectsConflictingRefsInOneRepository(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	appDir := filepath.Join(root, "app")
	migCovWriteGoMod(t, appDir, "module github.com/acme/app\n\ngo 1.24\n\nrequire (\n\tgithub.com/acme/multi v0.0.0\n\tgithub.com/acme/multi/sub v0.0.0\n)\n\nreplace github.com/acme/multi => ../multi\n\nreplace github.com/acme/multi/sub => ../multi/sub\n")
	migCovWriteGoMod(t, filepath.Join(root, "multi"), "module github.com/acme/multi\n\ngo 1.24\n")
	migCovWriteGoMod(t, filepath.Join(root, "multi", "sub"), "module github.com/acme/multi/sub\n\ngo 1.24\n")

	spec := Spec{
		Format: MigrationFormatV1, ID: "conflicting-refs",
		Steps: []Step{
			{Kind: "text.replace", From: "github.com/acme/multi/pkg", To: "github.com/acme/multi/new"},
			{Kind: "text.replace", From: "github.com/acme/multi/sub/pkg", To: "github.com/acme/multi/sub/new"},
		},
	}
	options := CampaignOptions{
		GitHubDir:  t.TempDir(),
		Ref:        "main",
		Verify:     VerifyFull,
		Parallel:   1,
		ModuleRefs: map[string]string{"github.com/acme/multi/sub": "release"},
	}
	_, err := planCampaign(spec, appDir, options)
	if err == nil || !strings.Contains(err.Error(), "needs both refs") {
		t.Fatalf("planCampaign(conflicting refs) = %v", err)
	}
}

func TestMigCovFinalizeGoModuleReportsGateFailures(t *testing.T) {
	// The module's own path and direct dependencies without a campaign
	// replacement are not publication gates.
	parent := t.TempDir()
	root := filepath.Join(parent, "app")
	depRoot := filepath.Join(parent, "dep")
	migCovWriteGoMod(t, root, "module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n")
	migCovWriteGoMod(t, depRoot, "module example.com/dep\n\ngo 1.24\n")
	update, err := finalizeGoModule(root, Spec{}, "example.com/app", map[string]string{
		"example.com/app": root,
		"example.com/dep": depRoot,
	}, nil)
	if err != nil {
		t.Fatalf("finalizeGoModule(no campaign replacement) = %v", err)
	}
	if update.Changed {
		t.Fatal("an unmodified manifest was reported as changed")
	}

	// A foreign replacement is refused.
	foreign := filepath.Join(parent, "foreign")
	migCovWriteGoMod(t, foreign, "module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v1.0.0\n\nreplace example.com/dep => ../elsewhere\n")
	if _, err := finalizeGoModule(foreign, Spec{}, "example.com/app", map[string]string{"example.com/dep": depRoot}, nil); err == nil ||
		!strings.Contains(err.Error(), "non-campaign replacement") {
		t.Fatalf("finalizeGoModule(foreign replacement) = %v", err)
	}

	// An invalid release version cannot be pinned.
	invalid := filepath.Join(parent, "invalid")
	migCovWriteGoMod(t, invalid, "module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v0.0.0\n\nreplace example.com/dep => ../dep\n")
	if _, err := finalizeGoModule(
		invalid, Spec{}, "example.com/app",
		map[string]string{"example.com/dep": depRoot},
		map[string]string{"example.com/dep": "not a version"},
	); err == nil {
		t.Fatal("finalizeGoModule(invalid release version) succeeded")
	}

	// A release that cannot be resolved aborts publication.
	t.Setenv("GOPROXY", "off")
	unresolvable := filepath.Join(parent, "unresolvable")
	migCovWriteGoMod(t, unresolvable, "module example.com/app\n\ngo 1.24\n\nrequire example.com/dep v0.0.0\n\nreplace example.com/dep => ../dep\n")
	writeCampaignFile(t, filepath.Join(unresolvable, "app.go"), "package app\n\nimport _ \"example.com/dep\"\n")
	if _, err := finalizeGoModule(
		unresolvable, Spec{}, "example.com/app",
		map[string]string{"example.com/dep": depRoot},
		map[string]string{"example.com/dep": "v9.9.9"},
	); err == nil {
		t.Fatal("finalizeGoModule(unresolvable release) succeeded")
	}
}

func TestMigCovPseudoVersionForCommitHandlesPseudoAndGopkgInBases(t *testing.T) {
	repository := migCovClone(t, "pseudo", "module github.com/acme/module\n\ngo 1.24\n")
	revision := strings.TrimSpace(runCampaignGit(t, repository, "rev-parse", "HEAD"))

	// A pseudo-version base is unwrapped before the new pseudo-version is
	// derived.
	version, err := pseudoVersionForCommit(repository, "github.com/acme/module", "v0.53.4-0.20240101000000-abcdef123456", revision)
	if err != nil {
		t.Fatalf("pseudoVersionForCommit(pseudo base) = %v", err)
	}
	if !strings.HasPrefix(version, "v0.53.4-0.") {
		t.Fatalf("pseudo-version = %q", version)
	}

	// A gopkg.in path carries its major version in a ".vN" suffix.
	gopkg, err := pseudoVersionForCommit(repository, "gopkg.in/yaml.v3", "", revision)
	if err != nil {
		t.Fatalf("pseudoVersionForCommit(gopkg.in) = %v", err)
	}
	if !strings.HasPrefix(gopkg, "v3.0.0-") {
		t.Fatalf("gopkg.in pseudo-version = %q", gopkg)
	}

	// An unparseable commit timestamp is reported rather than ignored.
	binDir := t.TempDir()
	writeCampaignFile(t, filepath.Join(binDir, "git"), "#!/bin/sh\nprintf 'not-a-time\\n'\n")
	if err := os.Chmod(filepath.Join(binDir, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := pseudoVersionForCommit(t.TempDir(), "github.com/acme/module", "v1.0.0", "abcdef123456"); err == nil ||
		!strings.Contains(err.Error(), "parse commit time") {
		t.Fatalf("pseudoVersionForCommit(unparseable time) = %v", err)
	}
}

func TestMigCovPlanCampaignReportsPlacementFailures(t *testing.T) {
	root := t.TempDir()
	migCovWriteGoMod(t, root, "module github.com/acme/app\n\ngo 1.24\n")
	spec := Spec{
		Format: MigrationFormatV1, ID: "placement",
		Steps: []Step{{Kind: "text.replace", From: "github.com/acme/app/old", To: "github.com/acme/app/new"}},
	}
	options := CampaignOptions{GitHubDir: t.TempDir(), Ref: "main", Verify: VerifyFull, Parallel: 1}

	// A malformed worktrees configuration is reported instead of ignored.
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	writeCampaignFile(t, filepath.Join(configHome, "wb", "worktrees.yaml"), "worktrees: [this is not a mapping\n")
	if _, err := planCampaign(spec, root, options); err == nil {
		t.Fatal("planCampaign() ignored a malformed worktrees configuration")
	}

	// A migration id whose slug is empty cannot name a worktree.
	if err := os.Remove(filepath.Join(configHome, "wb", "worktrees.yaml")); err != nil {
		t.Fatal(err)
	}
	punctuation := spec
	punctuation.ID = "!!!"
	if _, err := planCampaign(punctuation, root, options); err == nil ||
		!strings.Contains(err.Error(), "invalid worktree task") {
		t.Fatalf("planCampaign(punctuated id) = %v", err)
	}
}
