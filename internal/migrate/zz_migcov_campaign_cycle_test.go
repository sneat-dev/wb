package migrate

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// migCovCycleFixture builds four repositories whose migrating modules form a
// dependency cycle: the target module t is provided, x and y require each
// other and t, and the source root depends on all three. When conflictRepo is
// non-empty, that repository's campaign branch already exists on its remote
// with divergent history, so publishing its seed is rejected.
func migCovCycleFixture(t *testing.T, conflictRepo string) campaignIntegrationFixture {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "WB Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "wb@example.test")
	t.Setenv("GIT_COMMITTER_NAME", "WB Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "wb@example.test")
	root := t.TempDir()
	t.Setenv(wbhome.EnvOverride, filepath.Join(root, "github", ".wb"))
	remotes := filepath.Join(root, "remotes")

	type repo struct {
		name   string
		goMod  string
		source string
	}
	repositories := []repo{
		{
			name:  "t",
			goMod: "module github.com/acme/t\n\ngo 1.24\n",
		},
		{
			name:  "x",
			goMod: "module github.com/acme/x\n\ngo 1.24\n\nrequire (\n\tgithub.com/acme/t v0.0.0\n\tgithub.com/acme/y v0.0.0\n)\n",
		},
		{
			name:  "y",
			goMod: "module github.com/acme/y\n\ngo 1.24\n\nrequire (\n\tgithub.com/acme/t v0.0.0\n\tgithub.com/acme/x v0.0.0\n)\n",
		},
		{
			name:  "root",
			goMod: "module github.com/acme/root\n\ngo 1.24\n\nrequire (\n\tgithub.com/acme/t v0.0.0\n\tgithub.com/acme/x v0.0.0\n\tgithub.com/acme/y v0.0.0\n)\n",
		},
	}
	for index := range repositories {
		entry := &repositories[index]
		entry.source = filepath.Join(root, "remote-sources", entry.name)
		writeCampaignFile(t, filepath.Join(entry.source, "go.mod"), entry.goMod)
		if entry.name == "t" {
			writeCampaignFile(t, filepath.Join(entry.source, "t.go"), "package t\n\nconst Package = \"github.com/acme/t/pkg\"\n")
		}
		commitCampaignRepository(t, entry.source, filepath.Join(remotes, "acme", entry.name+".git"))
		if entry.name == conflictRepo {
			// Divergent history on the campaign branch makes the seed push a
			// non-fast-forward rejection.
			runCampaignGit(t, entry.source, "checkout", "--orphan", "conflict")
			writeCampaignFile(t, filepath.Join(entry.source, "conflict.txt"), "conflict\n")
			runCampaignGit(t, entry.source, "add", ".")
			runCampaignGit(t, entry.source, "-c", "user.name=WB Test", "-c", "user.email=wb@example.test", "commit", "-m", "conflict")
			runCampaignGit(t, entry.source, "push", "origin", "conflict:wb/migrate/campaign-cycle")
			runCampaignGit(t, entry.source, "checkout", "main")
		}
	}

	// The local discovery root keeps the unpublished modules resolvable
	// without any network access.
	sourceRoot := filepath.Join(root, "source", "root")
	writeCampaignFile(t, filepath.Join(sourceRoot, "go.mod"), "module github.com/acme/root\n\ngo 1.24\n\nrequire (\n\tgithub.com/acme/t v0.0.0\n\tgithub.com/acme/x v0.0.0\n\tgithub.com/acme/y v0.0.0\n)\n\nreplace github.com/acme/t => "+filepath.ToSlash(filepath.Join(root, "remote-sources", "t"))+"\n\nreplace github.com/acme/x => "+filepath.ToSlash(filepath.Join(root, "remote-sources", "x"))+"\n\nreplace github.com/acme/y => "+filepath.ToSlash(filepath.Join(root, "remote-sources", "y"))+"\n")
	writeCampaignFile(t, filepath.Join(sourceRoot, "root.go"), "package root\n\nconst Package = \"github.com/acme/t/pkg\"\n")

	proxy := migCovModuleProxy(t, "github.com/acme/t", "v1.0.0", map[string]string{
		"go.mod": "module github.com/acme/t\n\ngo 1.24\n",
		"t.go":   "package t\n\nconst Package = \"github.com/acme/t/pkg\"\n",
	})
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(proxy))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GONOSUMDB", "*")

	return campaignIntegrationFixture{
		spec: Spec{
			Format: MigrationFormatV1,
			ID:     "campaign-cycle",
			Steps: []Step{{
				Kind: "text.replace", Language: "go",
				From: "github.com/acme/t/pkg", To: "github.com/acme/t/new",
			}},
			GoModuleReleases: []GoModuleRelease{{Path: "github.com/acme/t", Version: "v1.0.0"}},
		},
		sourceRoot: sourceRoot,
		githubDir:  filepath.Join(root, "github"),
		cloneURL: func(repository string) string {
			return filepath.Join(remotes, strings.TrimPrefix(repository, "github.com/")+".git")
		},
	}
}

func TestMigCovCampaignSeedsCyclicComponentsBeforeRefusingToContinue(t *testing.T) {
	fixture := migCovCycleFixture(t, "")
	migCovInstallFakeGH(t)

	report, err := RunCampaign(fixture.spec, fixture.sourceRoot, CampaignOptions{
		GitHubDir: fixture.githubDir,
		Apply:     true,
		PR:        true,
		Verify:    VerifyNone,
		Parallel:  1,
		CloneURL:  fixture.cloneURL,
	})
	if err == nil {
		t.Fatal("RunCampaign() completed a cyclic component without a published release")
	}
	if !strings.Contains(err.Error(), "seed pseudo-versions") {
		t.Fatalf("RunCampaign() = %v, want the cycle-seed refusal", err)
	}
	if report.Status != "failed" {
		t.Fatalf("campaign status = %q, want failed", report.Status)
	}
	// Both cycle members published their seed commits before the campaign
	// stopped, which is exactly what a reviewer needs to continue from.
	for _, name := range []string{"github.com/acme/x", "github.com/acme/y"} {
		repository := campaignRepositoryByName(t, report, name)
		if !repository.Pushed {
			t.Errorf("%s seed commit was not pushed: %+v", name, repository)
		}
		if repository.PR != "" {
			t.Errorf("%s opened a pull request before its seeds were released: %q", name, repository.PR)
		}
	}
}

func TestMigCovCampaignReportsCycleSeedPublishFailure(t *testing.T) {
	fixture := migCovCycleFixture(t, "x")
	migCovInstallFakeGH(t)
	fixture.spec.ID = "campaign-cycle"

	_, err := RunCampaign(fixture.spec, fixture.sourceRoot, CampaignOptions{
		GitHubDir: fixture.githubDir,
		Apply:     true,
		PR:        true,
		Verify:    VerifyNone,
		Parallel:  1,
		CloneURL:  fixture.cloneURL,
	})
	if err == nil {
		t.Fatal("RunCampaign() published a seed commit over divergent remote history")
	}
}
