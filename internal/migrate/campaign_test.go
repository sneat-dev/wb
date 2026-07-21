package migrate

import (
	"path/filepath"
	"strings"
	"testing"
)

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
			Modules: []CampaignModuleReport{{Path: "github.com/acme/repo", Status: "planned", PlanState: "deferred"}},
		}},
	}
	markdown := report.Markdown()
	for _, want := range []string{"# WB hierarchical migration: example", "github.com/acme/repo", "file://", "planned", "unknown (worktree not created)"} {
		if !strings.Contains(markdown, want) {
			t.Errorf("Markdown missing %q:\n%s", want, markdown)
		}
	}
	yaml, err := report.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(yaml), "schema_version: 1") || !strings.Contains(string(yaml), "repositories:") || !strings.Contains(string(yaml), "plan_state: deferred") {
		t.Errorf("YAML = %s", yaml)
	}
	if strings.Contains(string(yaml), "changed_files:") {
		t.Errorf("deferred YAML must not invent a changed_files count: %s", yaml)
	}
}
