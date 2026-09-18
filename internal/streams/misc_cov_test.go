package streams

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileEventLogReportsUnwritableLocations(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(blocker, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	log := &FileEventLog{Path: filepath.Join(blocker, "events.jsonl")}
	if err := log.Append(Event{Verb: "stream start", Outcome: "success"}); err == nil || !strings.Contains(err.Error(), "create stream event directory") {
		t.Fatalf("append below a regular file = %v, want a directory failure", err)
	}

	destination := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	log = &FileEventLog{Path: destination}
	if err := log.Append(Event{Verb: "stream start", Outcome: "success"}); err == nil || !strings.Contains(err.Error(), "open stream event log") {
		t.Fatalf("append to a directory = %v, want an open failure", err)
	}
}

func TestReadEventsReportsUnreadableAndUnparseableLogs(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEvents(directory); err == nil || !strings.Contains(err.Error(), "read stream event log") {
		t.Fatalf("read of a directory = %v, want a read failure", err)
	}

	broken := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(broken, []byte("{not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEvents(broken); err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("read of a malformed log = %v, want a parse failure naming the line", err)
	}
	if events, err := ReadEvents(filepath.Join(t.TempDir(), "absent.jsonl")); err != nil || events != nil {
		t.Fatalf("ReadEvents of an absent log = %v, %v; want none", events, err)
	}
}

func TestStreamLookupsReportAbsence(t *testing.T) {
	if _, ok := (Stream{}).Library(); ok {
		t.Fatal("an empty stream reported a library member")
	}
	if _, ok := (Stream{}).LinkedConsumer("acme/app"); ok {
		t.Fatal("an empty stream reported a linked consumer")
	}
	if got := (Stream{Phase: PhaseOpen}).Lifecycle(); got != PhaseOpen {
		t.Fatalf("Lifecycle with an explicit phase = %q", got)
	}
	ended := time.Now().UTC()
	if got := (Stream{EndedAt: &ended}).Lifecycle(); got != PhaseEnded {
		t.Fatalf("Lifecycle with only EndedAt = %q, want %q", got, PhaseEnded)
	}
}

func TestValidateRepositoryRefusesANonSlug(t *testing.T) {
	if err := ValidateRepository("no-slash"); err == nil {
		t.Fatal("ValidateRepository accepted a repository without an owner")
	}
	if err := ValidateRepository("acme/app"); err != nil {
		t.Fatalf("ValidateRepository refused a valid slug: %v", err)
	}
}

func TestCanonicalPathHandlesARepositoryWithoutAnOwner(t *testing.T) {
	root := t.TempDir()
	if got := canonicalPath(root, "acme/app"); got != filepath.Join(root, "acme", "app") {
		t.Fatalf("canonicalPath = %q", got)
	}
	if got := canonicalPath(root, "bare"); got != filepath.Join(root, "bare") {
		t.Fatalf("canonicalPath without an owner = %q", got)
	}
}

func TestDiscoverPublishedReportsAnUnreadableGoManifest(t *testing.T) {
	root := t.TempDir()
	// A directory named go.mod passes the existence probe and then fails to read.
	if err := os.MkdirAll(filepath.Join(root, "backend", "go.mod"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverPublished(root); err == nil || !strings.Contains(err.Error(), "backend/go.mod") {
		t.Fatalf("DiscoverPublished = %v, want the unreadable manifest named", err)
	}
}

func TestDiscoverPublishedSortsGoBeforeNpm(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"go.mod":                    "module example.test/library\n\ngo 1.27\n",
		"libs/core/package.json":    `{"name":"@acme/core","version":"1.0.0"}`,
		"libs/private/package.json": `{"name":"@acme/private","private":true}`,
	})
	identities, err := DiscoverPublished(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 2 {
		t.Fatalf("identities = %#v, want the Go module and the publishable npm package", identities)
	}
	if identities[0].Ecosystem != EcosystemGo || identities[0].Name != "example.test/library" || identities[0].Directory != "." {
		t.Fatalf("identities[0] = %#v", identities[0])
	}
	if identities[1].Ecosystem != EcosystemNpm || identities[1].Name != "@acme/core" {
		t.Fatalf("identities[1] = %#v", identities[1])
	}
}

func TestDiscoverDeclarationsReportsUnreadableRoots(t *testing.T) {
	identity := Identity{Ecosystem: EcosystemNpm, Name: "@acme/core"}
	if _, err := DiscoverDeclarations(filepath.Join(t.TempDir(), "absent"), []Identity{identity}); err == nil {
		t.Fatal("DiscoverDeclarations accepted a root that does not exist")
	}

	broken := t.TempDir()
	writeFiles(t, broken, map[string]string{"package.json": "{not json"})
	if _, err := DiscoverDeclarations(broken, []Identity{identity}); err == nil {
		t.Fatal("DiscoverDeclarations accepted an unparseable package.json")
	}
}

func TestDiscoverDeclarationsSkipsANonObjectDependencySection(t *testing.T) {
	consumer := t.TempDir()
	writeFiles(t, consumer, map[string]string{
		"package.json": `{"name":"consumer","dependencies":{"@acme/core":123}}`,
	})
	declarations, err := DiscoverDeclarations(consumer, []Identity{{Ecosystem: EcosystemNpm, Name: "@acme/core"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(declarations) != 0 {
		t.Fatalf("declarations = %#v, want the malformed section skipped rather than reported", declarations)
	}
}

func TestDiscoverDeclarationsSortsByManifestThenIdentity(t *testing.T) {
	consumer := t.TempDir()
	writeFiles(t, consumer, map[string]string{
		"backend/go.mod": "module example.test/consumer/backend\n\ngo 1.27\n\nrequire example.test/library v1.0.0\n",
		"package.json":   `{"name":"consumer","dependencies":{"@acme/core":"1.0.0"}}`,
	})
	declarations, err := DiscoverDeclarations(consumer, []Identity{
		{Ecosystem: EcosystemGo, Name: "example.test/library"},
		{Ecosystem: EcosystemNpm, Name: "@acme/core"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(declarations) != 2 {
		t.Fatalf("declarations = %#v, want both declarations", declarations)
	}
	if declarations[0].Manifest != "backend/go.mod" || declarations[1].Manifest != "package.json" {
		t.Fatalf("declarations = %#v, want manifest order", declarations)
	}
}

func TestGoModulesReportsAnUnreadableManifestAndSkipsAModulelessOne(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"tooling/go.mod": "go 1.27\n"})
	modules, err := GoModules(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(modules) != 0 {
		t.Fatalf("modules = %#v, want a manifest without a module directive skipped", modules)
	}

	broken := t.TempDir()
	if err := os.Symlink(filepath.Join(broken, "missing"), filepath.Join(broken, "go.mod")); err != nil {
		t.Fatal(err)
	}
	if _, err := GoModules(broken); err == nil {
		t.Fatal("GoModules reported success for an unreadable go.mod")
	}
}

func TestGoModulePathRequiresAModuleDirective(t *testing.T) {
	if _, ok := goModulePath([]byte("go 1.27\n\nrequire example.test/x v1.0.0\n")); ok {
		t.Fatal("goModulePath found a module directive where there is none")
	}
	if module, ok := goModulePath([]byte("module \"example.test/quoted\"\n")); !ok || module != "example.test/quoted" {
		t.Fatalf("goModulePath = %q, %t; want the quoted module path", module, ok)
	}
}

func TestCheckHooksReportsAMissingOrUnreadableChecker(t *testing.T) {
	input := PreflightInput{Repository: "acme/app", Path: t.TempDir()}
	finding := checkHooks(input, nil)
	if finding.Status != PreflightUnknown || !strings.Contains(finding.Detail, "no hooks checker") {
		t.Fatalf("nil checker finding = %#v", finding)
	}
	finding = checkHooks(input, func(string) ([]string, error) { return nil, os.ErrPermission })
	if finding.Status != PreflightUnknown {
		t.Fatalf("failing checker finding = %#v", finding)
	}
	finding = checkHooks(input, func(string) ([]string, error) { return []string{"hook missing"}, nil })
	if finding.Status != PreflightFail || !strings.Contains(finding.Detail, "hook missing") {
		t.Fatalf("failing-hooks finding = %#v", finding)
	}
	finding = checkHooks(input, func(string) ([]string, error) { return nil, nil })
	if finding.Status != PreflightPass {
		t.Fatalf("healthy-hooks finding = %#v", finding)
	}
}

func TestCollectNpmPackageNamesReportsProviderIdentityProblems(t *testing.T) {
	broken := t.TempDir()
	writeFiles(t, broken, map[string]string{"package.json": "{not json"})
	if _, finding := collectNpmPackageNames(PreflightInput{Repository: "acme/app", Path: broken}); finding.Status != PreflightUnknown {
		t.Fatalf("unparseable manifest finding = %#v", finding)
	}

	private := t.TempDir()
	writeFiles(t, private, map[string]string{"package.json": `{"name":"private-app","private":true}`})
	if names, finding := collectNpmPackageNames(PreflightInput{Repository: "acme/app", Path: private}); len(names) != 0 || finding.Status != PreflightPass {
		t.Fatalf("private manifest = %v, %#v; want it skipped", names, finding)
	}

	unnamed := t.TempDir()
	writeFiles(t, unnamed, map[string]string{
		"package.json":           `{"name":"consumer"}`,
		"libs/core/package.json": `{"private":false}`,
	})
	names, finding := collectNpmPackageNames(PreflightInput{Repository: "acme/app", Path: unnamed})
	if finding.Status != PreflightFail || !strings.Contains(finding.Detail, "libs/core/package.json") {
		t.Fatalf("unnamed manifest = %v, %#v; want a provider-identity failure", names, finding)
	}
}

func TestCollectNpmPackageNamesSkipsANestedWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"frontend/pnpm-workspace.yaml": "packages:\n  - 'libs/*'\n",
		"frontend/package.json":        `{"name":"frontend-workspace","private":true}`,
		"package.json":                 `{"name":"repository-root","private":true}`,
	})
	names, finding := collectNpmPackageNames(PreflightInput{Repository: "acme/app", Path: root})
	if finding.Status != PreflightPass {
		t.Fatalf("finding = %#v", finding)
	}
	for _, name := range names {
		if name == "frontend-workspace" {
			t.Fatalf("names = %v; a nested workspace root describes the workspace, not a published package", names)
		}
	}
}

func TestCheckRedMainReportsEveryConclusion(t *testing.T) {
	ctx := context.Background()
	if finding := checkRedMain(ctx, nil, PreflightInput{Repository: "acme/app"}); finding.Status != PreflightUnknown {
		t.Fatalf("nil hub finding = %#v", finding)
	}

	hub := newFakeHub()
	dir := t.TempDir()
	input := PreflightInput{Repository: "acme/app", Path: dir, DefaultBranch: "main"}
	hub.mainErr[dir] = os.ErrPermission
	if finding := checkRedMain(ctx, hub, input); finding.Status != PreflightUnknown {
		t.Fatalf("unreadable status finding = %#v", finding)
	}
	delete(hub.mainErr, dir)

	for _, conclusion := range []string{"success", "skipped", "neutral"} {
		hub.mainStatus[dir] = conclusion
		if finding := checkRedMain(ctx, hub, input); finding.Status != PreflightPass {
			t.Fatalf("%s finding = %#v, want pass", conclusion, finding)
		}
	}
	hub.mainStatus[dir] = ""
	if finding := checkRedMain(ctx, hub, input); finding.Status != PreflightUnknown || !strings.Contains(finding.Detail, "no completed run") {
		t.Fatalf("no-run finding = %#v", finding)
	}
	hub.mainStatus[dir] = "failure"
	if finding := checkRedMain(ctx, hub, input); finding.Status != PreflightFail {
		t.Fatalf("red-main finding = %#v", finding)
	}
}

func TestCheckStreamConcurrencyReportsEveryRefusal(t *testing.T) {
	t.Run("unreadable workflows directory", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".github"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".github", "workflows"), []byte("not a directory\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		finding := checkStreamConcurrency(PreflightInput{Repository: "acme/app", Path: root})
		if finding.Status != PreflightUnknown {
			t.Fatalf("finding = %#v", finding)
		}
	})

	t.Run("no pull request workflow", func(t *testing.T) {
		root := t.TempDir()
		writeFiles(t, root, map[string]string{
			".github/workflows/push.yml": "name: Push\non:\n  push:\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n",
		})
		finding := checkStreamConcurrency(PreflightInput{Repository: "acme/app", Path: root})
		if finding.Status != PreflightUnknown || !strings.Contains(finding.Detail, "no pull_request workflow") {
			t.Fatalf("finding = %#v", finding)
		}
	})

	t.Run("group not keyed to the ref", func(t *testing.T) {
		root := t.TempDir()
		writeFiles(t, root, map[string]string{
			".github/workflows/ci.yml": "name: CI\non:\n  pull_request:\nconcurrency:\n  group: ci-static\n  cancel-in-progress: true\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n",
		})
		finding := checkStreamConcurrency(PreflightInput{Repository: "acme/app", Path: root})
		if finding.Status != PreflightFail || !strings.Contains(finding.Detail, "not keyed to the ref") {
			t.Fatalf("finding = %#v", finding)
		}
	})

	t.Run("no cancel-in-progress", func(t *testing.T) {
		root := t.TempDir()
		writeFiles(t, root, map[string]string{
			".github/workflows/ci.yml": "name: CI\non:\n  pull_request:\nconcurrency:\n  group: ci-${{ github.ref }}\njobs:\n  build:\n    runs-on: ubuntu-latest\n    steps:\n      - run: echo hi\n",
		})
		finding := checkStreamConcurrency(PreflightInput{Repository: "acme/app", Path: root})
		if finding.Status != PreflightFail || !strings.Contains(finding.Detail, "cancel-in-progress") {
			t.Fatalf("finding = %#v", finding)
		}
	})

	t.Run("healthy workflow", func(t *testing.T) {
		root := t.TempDir()
		writeFiles(t, root, map[string]string{".github/workflows/ci.yml": cancellingWorkflow})
		if finding := checkStreamConcurrency(PreflightInput{Repository: "acme/app", Path: root}); finding.Status != PreflightPass {
			t.Fatalf("finding = %#v, want pass", finding)
		}
	})
}

func TestNpmPackageManifestsReportsUnreadableAndUnparseableManifests(t *testing.T) {
	broken := t.TempDir()
	if err := os.Symlink(filepath.Join(broken, "missing"), filepath.Join(broken, "package.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := npmPackageManifests(broken); err == nil {
		t.Fatal("npmPackageManifests reported success for an unreadable manifest")
	}

	malformed := t.TempDir()
	writeFiles(t, malformed, map[string]string{"package.json": "{not json"})
	if _, err := npmPackageManifests(malformed); err == nil {
		t.Fatal("npmPackageManifests reported success for an unparseable manifest")
	}
}

func TestNpmPackageManifestsRecordsAWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"package.json":           `{"name":"root","workspaces":["libs/*"]}`,
		"libs/core/package.json": `{"name":"@acme/core"}`,
	})
	manifests, err := npmPackageManifests(root)
	if err != nil {
		t.Fatal(err)
	}
	byPath := map[string]npmPackageManifest{}
	for _, manifest := range manifests {
		byPath[manifest.Path] = manifest
	}
	core, ok := byPath["libs/core/package.json"]
	if !ok || core.Root || core.Workspace != "." {
		t.Fatalf("libs/core manifest = %#v", core)
	}
	rootManifest, ok := byPath["package.json"]
	if !ok || !rootManifest.Root || rootManifest.Workspace != "." {
		t.Fatalf("root manifest = %#v", rootManifest)
	}
}

func TestInstalledHooksCheckerReportsFindings(t *testing.T) {
	root, _ := gitFixture(t)
	checker := InstalledHooksChecker("", t.TempDir())
	messages, err := checker(root)
	if err != nil {
		t.Fatalf("InstalledHooksChecker: %v", err)
	}
	if len(messages) == 0 {
		t.Fatal("checker reported no findings for a repository with no WB hooks installed")
	}
}

func TestDiscoverPublishedSortsWithinOneEcosystem(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"libs/beta/package.json":  `{"name":"@acme/beta","version":"1.0.0"}`,
		"libs/alpha/package.json": `{"name":"@acme/alpha","version":"1.0.0"}`,
	})
	identities, err := DiscoverPublished(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 2 || identities[0].Name != "@acme/alpha" || identities[1].Name != "@acme/beta" {
		t.Fatalf("identities = %#v, want the npm packages sorted by name", identities)
	}
}

func TestDiscoverDeclarationsSortsWithinOneManifest(t *testing.T) {
	consumer := t.TempDir()
	writeFiles(t, consumer, map[string]string{
		"package.json": `{"name":"consumer","dependencies":{"@acme/beta":"1.0.0","@acme/alpha":"1.0.0"}}`,
	})
	declarations, err := DiscoverDeclarations(consumer, []Identity{
		{Ecosystem: EcosystemNpm, Name: "@acme/beta"},
		{Ecosystem: EcosystemNpm, Name: "@acme/alpha"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(declarations) != 2 || declarations[0].Identity.Name != "@acme/alpha" || declarations[1].Identity.Name != "@acme/beta" {
		t.Fatalf("declarations = %#v, want both sorted by identity", declarations)
	}
}

func TestAmbiguousProviderFindingsSkipUniquelyOwnedPackages(t *testing.T) {
	findings := ambiguousProviderFindings(map[string][]string{
		"solo":   {"acme/a"},
		"shared": {"acme/b", "acme/a"},
	})
	if len(findings) != 2 {
		t.Fatalf("findings = %#v, want one per owning repository of the shared package", findings)
	}
	for _, finding := range findings {
		if finding.Status != PreflightFail || !strings.Contains(finding.Detail, "shared") {
			t.Fatalf("finding = %#v", finding)
		}
	}
	if findings[0].Repository != "acme/a" || findings[1].Repository != "acme/b" {
		t.Fatalf("findings = %#v, want owners sorted", findings)
	}
}
