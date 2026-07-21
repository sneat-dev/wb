package migrate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunRepositoriesParallelErrorsPreservesRepositoryOrder(t *testing.T) {
	repositories := []*campaignRepository{
		{repository: "github.com/acme/first"},
		{repository: "github.com/acme/second"},
	}
	errs := runRepositoriesParallelErrors(repositories, 2, func(repo *campaignRepository) error {
		if repo == repositories[0] {
			time.Sleep(10 * time.Millisecond)
		}
		return errors.New(repo.repository)
	})
	if len(errs) != 2 || errs[0].Error() != repositories[0].repository || errs[1].Error() != repositories[1].repository {
		t.Fatalf("errors = %v, want repository order", errs)
	}
}

func TestReadyRepositoryComponentsContinuesIndependentPeersAtomically(t *testing.T) {
	blocked := &campaignRepository{repository: "github.com/acme/blocked"}
	cyclicPeer := &campaignRepository{repository: "github.com/acme/cyclic-peer"}
	ready := &campaignRepository{repository: "github.com/acme/ready"}
	components := [][]*campaignRepository{{blocked, cyclicPeer}, {ready}}

	gotReady, gotBootstraps, gotBlocked := readyRepositoryComponents(components, func(
		repo *campaignRepository,
		_ map[string]bool,
	) (map[string]bool, error) {
		if repo == blocked {
			return nil, errors.New("missing release")
		}
		return nil, nil
	})

	if len(gotReady) != 1 || gotReady[0] != ready {
		t.Fatalf("ready = %+v, want only independent ready repository", gotReady)
	}
	if len(gotBlocked) != 1 || gotBlocked[0].Error() != "missing release" {
		t.Fatalf("blocked errors = %v", gotBlocked)
	}
	if len(gotBootstraps) != 0 {
		t.Fatalf("bootstraps = %+v, want none for blocked component", gotBootstraps)
	}
}

func TestReadyRepositoryComponentsBootstrapsMissingCycleReleases(t *testing.T) {
	cycleA := &campaignRepository{repository: "github.com/acme/cycle-a"}
	cycleB := &campaignRepository{repository: "github.com/acme/cycle-b"}
	cycleA.modules = []*campaignModule{{path: cycleA.repository, migrate: true}}
	cycleB.modules = []*campaignModule{{path: cycleB.repository, migrate: true}}

	ready, bootstraps, blocked := readyRepositoryComponents(
		[][]*campaignRepository{{cycleA, cycleB}},
		func(repo *campaignRepository, allowed map[string]bool) (map[string]bool, error) {
			if !allowed[cycleA.repository] || !allowed[cycleB.repository] {
				t.Fatalf("allowed cycle modules = %v", allowed)
			}
			if repo == cycleA {
				return map[string]bool{cycleB.repository: true}, nil
			}
			return map[string]bool{cycleA.repository: true}, nil
		},
	)

	if len(blocked) != 0 || len(ready) != 2 || len(bootstraps) != 1 {
		t.Fatalf("ready = %v, bootstraps = %+v, blocked = %v", ready, bootstraps, blocked)
	}
	if !bootstraps[0].modulePaths[cycleA.repository] || !bootstraps[0].modulePaths[cycleB.repository] {
		t.Fatalf("bootstrap paths = %v", bootstraps[0].modulePaths)
	}
}

func TestPseudoVersionForCommitUsesCurrentTaggedVersion(t *testing.T) {
	repository := t.TempDir()
	runCampaignGit(t, repository, "init", "--initial-branch=main")
	writeCampaignFile(t, filepath.Join(repository, "go.mod"), "module github.com/acme/module\n\ngo 1.24\n")
	runCampaignGit(t, repository, "add", ".")
	runCampaignGit(t, repository, "-c", "user.name=WB Test", "-c", "user.email=wb@example.test", "commit", "-m", "seed")
	revision := strings.TrimSpace(runCampaignGit(t, repository, "rev-parse", "HEAD"))

	version, err := pseudoVersionForCommit(repository, "github.com/acme/module", "v0.53.3", revision)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(version, "v0.53.4-0.") || !strings.HasSuffix(version, revision[:12]) {
		t.Fatalf("pseudo-version = %q", version)
	}
}

func TestCampaignGraphSelectsDependentsDependencyFirst(t *testing.T) {
	children := map[string][]string{
		"github.com/sneat-co/bots": {"github.com/sneat-co/core"},
		"github.com/sneat-co/core": {"github.com/dal-go/dalgo"},
	}
	targets := map[string]bool{"github.com/dal-go/dalgo": true}
	closure := reverseClosure(targets, children)
	order := dependencyOrder(closure, children)
	want := []string{"github.com/dal-go/dalgo", "github.com/sneat-co/core", "github.com/sneat-co/bots"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestMigrationTargetModulesUsesLongestGoModulePrefix(t *testing.T) {
	modules := map[string]listedModule{
		"github.com/dal-go/dalgo":         {Path: "github.com/dal-go/dalgo"},
		"github.com/dal-go/dalgo/adapter": {Path: "github.com/dal-go/dalgo/adapter"},
	}
	spec := Spec{Steps: []Step{{Kind: "import.replace", From: "github.com/dal-go/dalgo/adapter/sql"}}}
	targets := migrationTargetModules(spec, modules)
	if !targets["github.com/dal-go/dalgo/adapter"] || len(targets) != 1 {
		t.Fatalf("targets = %v", targets)
	}
}

func TestAddGoModRequirementEdgesRestoresPrunedDependency(t *testing.T) {
	root := t.TempDir()
	adapterMod := filepath.Join(root, "adapter.mod")
	if err := os.WriteFile(adapterMod, []byte("module github.com/acme/adapter\n\ngo 1.24\n\nrequire github.com/acme/core v1.2.3\n\nfuture_directive example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	modules := map[string]listedModule{
		"github.com/acme/adapter": {Path: "github.com/acme/adapter", GoMod: adapterMod},
		"github.com/acme/core":    {Path: "github.com/acme/core"},
	}
	edges := map[string]map[string]bool{}
	if err := addGoModRequirementEdges(modules, edges); err != nil {
		t.Fatal(err)
	}
	if !edges["github.com/acme/adapter"]["github.com/acme/core"] {
		t.Fatalf("requirement edge was not restored: %+v", edges)
	}
}

func TestCachedGoModPathUsesOfficialModuleEscaping(t *testing.T) {
	got, err := cachedGoModPath("/cache", "github.com/RoaringBitmap/roaring/v2", "v2.21.0")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/cache", "cache", "download", "github.com", "!roaring!bitmap", "roaring", "v2", "@v", "v2.21.0.mod")
	if got != want {
		t.Fatalf("cached go.mod path = %q, want %q", got, want)
	}
}

func TestCampaignOptionsDefaultVerificationAndPush(t *testing.T) {
	options, err := normalizeCampaignOptions(CampaignOptions{GitHubDir: t.TempDir(), Apply: true, Push: true})
	if err != nil {
		t.Fatal(err)
	}
	if options.Verify != VerifyFull || !options.Commit || !options.Push {
		t.Fatalf("options = %+v", options)
	}
	if _, err := normalizeCampaignOptions(CampaignOptions{GitHubDir: t.TempDir(), Commit: true}); err == nil {
		t.Fatal("commit without apply should fail")
	}
	if _, err := normalizeCampaignOptions(CampaignOptions{GitHubDir: t.TempDir(), Resume: true}); err == nil {
		t.Fatal("resume without apply should fail")
	}
}

func TestCampaignChangeTitle(t *testing.T) {
	if got, want := campaignChangeTitle(Spec{ID: "dalgo-record-v1", Title: "Extract DALgo records."}), "chore: Extract DALgo records"; got != want {
		t.Fatalf("campaign title = %q, want %q", got, want)
	}
	if got, want := campaignChangeTitle(Spec{ID: "rename-types"}), "chore: migrate rename-types"; got != want {
		t.Fatalf("fallback campaign title = %q, want %q", got, want)
	}
}

func TestCampaignOptionsImplyPRAndMergePhases(t *testing.T) {
	options, err := normalizeCampaignOptions(CampaignOptions{GitHubDir: t.TempDir(), Apply: true, Merge: true, Parallel: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !options.Commit || !options.Push || !options.PR || !options.Merge || options.Parallel != 2 {
		t.Fatalf("options = %+v", options)
	}
}

func TestRepositoryLayersRunDependenciesFirst(t *testing.T) {
	provider := &campaignRepository{repository: "github.com/acme/provider"}
	consumer := &campaignRepository{repository: "github.com/acme/consumer"}
	c := campaign{
		repos: []*campaignRepository{consumer, provider},
		modules: map[string]*campaignModule{
			"github.com/acme/provider": {repository: provider.repository},
			"github.com/acme/consumer": {repository: consumer.repository},
		},
		children: map[string][]string{
			"github.com/acme/consumer": {"github.com/acme/provider"},
		},
	}
	layers, err := c.repositoryLayers()
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 2 || layers[0][0] != provider || layers[1][0] != consumer {
		t.Fatalf("layers = %+v", layers)
	}
}

func TestRepositoryLayersCollapseDependencyCycles(t *testing.T) {
	provider := &campaignRepository{repository: "github.com/acme/provider"}
	cycleA := &campaignRepository{repository: "github.com/acme/cycle-a"}
	cycleB := &campaignRepository{repository: "github.com/acme/cycle-b"}
	consumer := &campaignRepository{repository: "github.com/acme/consumer"}
	c := campaign{
		repos: []*campaignRepository{consumer, cycleB, provider, cycleA},
		modules: map[string]*campaignModule{
			provider.repository: {repository: provider.repository},
			cycleA.repository:   {repository: cycleA.repository},
			cycleB.repository:   {repository: cycleB.repository},
			consumer.repository: {repository: consumer.repository},
		},
		children: map[string][]string{
			cycleA.repository:   {cycleB.repository},
			cycleB.repository:   {cycleA.repository, provider.repository},
			consumer.repository: {cycleA.repository},
		},
	}
	layers, err := c.repositoryLayers()
	if err != nil {
		t.Fatal(err)
	}
	if len(layers) != 3 {
		t.Fatalf("len(layers) = %d, want 3: %+v", len(layers), layers)
	}
	if len(layers[0]) != 1 || layers[0][0] != provider {
		t.Fatalf("provider layer = %+v", layers[0])
	}
	if len(layers[1]) != 2 || layers[1][0] != cycleA || layers[1][1] != cycleB {
		t.Fatalf("cycle layer = %+v", layers[1])
	}
	if len(layers[2]) != 1 || layers[2][0] != consumer {
		t.Fatalf("consumer layer = %+v", layers[2])
	}
}

func TestGitHubRepositoryAndCampaignReport(t *testing.T) {
	owner, name, repository, err := githubRepository("github.com/acme/repo/submodule")
	if err != nil {
		t.Fatal(err)
	}
	if owner != "acme" || name != "repo" || repository != "github.com/acme/repo" {
		t.Fatalf("repository = %q/%q/%q", owner, name, repository)
	}
	report := CampaignReport{
		SchemaVersion: 1,
		Migration:     ReportMigration{ID: "example", Format: MigrationFormatV1},
		Status:        "planned",
		Repositories: []CampaignRepositoryReport{{
			Repository: "github.com/acme/repo", WorktreeDir: filepath.Join(t.TempDir(), "worktree"), Ref: "main",
			Modules: []CampaignModuleReport{{
				Path: "github.com/acme/repo", Status: "planned", PlanState: "deferred",
				DependencyDecisions: []GoDependencyDecision{{
					Phase: "local_verification", Path: "github.com/acme/stable",
					RequiredAtCheck: true, VersionAtCheck: "v1.2.3",
					RequiredAfter: true, VersionAfter: "v1.2.3",
					VersionAction: "unchanged", ReplacementAction: "unchanged",
					Reason: "no target version configured; WB preserved the selected version",
				}},
			}},
		}},
	}
	markdown := report.Markdown()
	for _, want := range []string{
		"# WB hierarchical migration: example", "github.com/acme/repo", "file://", "planned",
		"unknown (worktree not created)", "github.com/acme/stable", "checked `v1.2.3`",
		"no target version configured",
	} {
		if !strings.Contains(markdown, want) {
			t.Errorf("Markdown missing %q:\n%s", want, markdown)
		}
	}
	yaml, err := report.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(yaml), "schema_version: 1") ||
		!strings.Contains(string(yaml), "repositories:") ||
		!strings.Contains(string(yaml), "plan_state: deferred") ||
		!strings.Contains(string(yaml), "version_at_check: v1.2.3") ||
		!strings.Contains(string(yaml), "reason: no target version configured") {
		t.Errorf("YAML = %s", yaml)
	}
	if strings.Contains(string(yaml), "changed_files:") {
		t.Errorf("deferred YAML must not invent a changed_files count: %s", yaml)
	}
}

func TestCampaignReportIndexesCumulativeRepositoryChanges(t *testing.T) {
	changedFiles := []string{"go.mod", "pkg/example.go"}
	report := CampaignReport{
		SchemaVersion: 1,
		Migration:     ReportMigration{ID: "example", Format: MigrationFormatV1},
		Status:        "applied",
		Repositories: []CampaignRepositoryReport{{
			Repository: "github.com/acme/repo", WorktreeDir: filepath.Join(t.TempDir(), "worktree"), Ref: "main", ChangedFiles: &changedFiles,
		}},
	}
	markdown := report.Markdown()
	for _, want := range []string{"pkg/example.go", "git -C", "origin/main"} {
		if !strings.Contains(markdown, want) {
			t.Errorf("Markdown missing %q:\n%s", want, markdown)
		}
	}
	yaml, err := report.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(yaml), "changed_files:\n") || !strings.Contains(string(yaml), "- pkg/example.go") {
		t.Errorf("YAML missing deterministic change index: %s", yaml)
	}
}
