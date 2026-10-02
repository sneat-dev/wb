//go:build e2e && !windows

package migrate

import (
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/wbhome"
)

const campaignBoundaryChild = "WB_CAMPAIGN_BOUNDARY_CHILD"

//nolint:paralleltest // Existing Git/Go/WB fixture environment mutations execute only in the isolated self-reexec child; the parent is parallel.
func TestE2ECampaignNativeBoundaryFailures(t *testing.T) {
	if os.Getenv(campaignBoundaryChild) == "1" {
		campaignPreparationFailures(t)
		campaignDiscoveryAndCleanupFailures(t)
		campaignSourceAndPublishFailures(t)
		return
	}
	t.Parallel()
	testRun := "^TestE2ECampaignNativeBoundaryFailures$"
	if selected := flag.Lookup("test.run"); selected != nil && selected.Value.String() != "" {
		testRun = campaignBoundaryChildFilter(selected.Value.String())
	}
	command := exec.Command(os.Args[0], "-test.run="+testRun, "-test.timeout=180s")
	command.Env = append(os.Environ(), campaignBoundaryChild+"=1")
	if cover := flag.Lookup("test.gocoverdir"); testing.CoverMode() != "" && cover != nil && cover.Value.String() != "" {
		command.Args = append(command.Args, "-test.gocoverdir="+cover.Value.String())
		command.Env = append(command.Env, "GOCOVERDIR="+cover.Value.String())
	}
	output, err := command.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "PASS") {
		t.Fatalf("isolated native campaign: %v\n%s", err, output)
	}
}

func campaignBoundaryChildFilter(selected string) string {
	const own = "^TestE2ECampaignNativeBoundaryFailures$"
	if !strings.HasPrefix(selected, own+"/") {
		return own
	}
	name := strings.TrimPrefix(selected, own+"/")
	name = strings.TrimPrefix(name, "^")
	name = strings.TrimSuffix(name, "$")
	if name == "" {
		return own
	}
	for _, character := range name {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-') {
			return own
		}
	}
	return own + "/^" + name + "$"
}

func TestE2ECampaignBoundaryChildFilterKeepsExactScope(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ input, want string }{
		{"", "^TestE2ECampaignNativeBoundaryFailures$"},
		{"^TestE2E", "^TestE2ECampaignNativeBoundaryFailures$"},
		{"^Test(E2E|Contract)", "^TestE2ECampaignNativeBoundaryFailures$"},
		{"^TestE2ECampaignNativeBoundaryFailures$", "^TestE2ECampaignNativeBoundaryFailures$"},
		{"^TestE2ECampaignNativeBoundaryFailures$/case|^OtherTest$", "^TestE2ECampaignNativeBoundaryFailures$"},
		{"^TestE2ECampaignNativeBoundaryFailures$/^(one|two)$", "^TestE2ECampaignNativeBoundaryFailures$"},
		{"^TestE2ECampaignNativeBoundaryFailures$/case/nested", "^TestE2ECampaignNativeBoundaryFailures$"},
		{"^TestE2ECampaignNativeBoundaryFailures$/.*", "^TestE2ECampaignNativeBoundaryFailures$"},
		{"^TestE2ECampaignNativeBoundaryFailures$/^$", "^TestE2ECampaignNativeBoundaryFailures$"},
		{"^TestE2ECampaignNativeBoundaryFailures$/^resume_branch_changed$", "^TestE2ECampaignNativeBoundaryFailures$/^resume_branch_changed$"},
		{"^TestE2ECampaignNativeBoundaryFailures$/resume_branch_changed", "^TestE2ECampaignNativeBoundaryFailures$/^resume_branch_changed$"},
		{"^TestE2ECampaignNativeBoundaryFailures$/^resume_branch_changed", "^TestE2ECampaignNativeBoundaryFailures$/^resume_branch_changed$"},
		{"^TestE2ECampaignNativeBoundaryFailures$/resume_branch_changed$", "^TestE2ECampaignNativeBoundaryFailures$/^resume_branch_changed$"},
	} {
		if got := campaignBoundaryChildFilter(tc.input); got != tc.want {
			t.Fatalf("filter %q=%q want %q", tc.input, got, tc.want)
		}
	}
}

//nolint:paralleltest // These subtests execute only in the isolated self-reexec child and reuse fixtures that mutate process environment.
func campaignPreparationFailures(t *testing.T) {
	t.Helper()
	for _, stage := range []string{"parent mkdir", "physical canonical", "resume registration", "worktree observation", "placement", "creation"} {
		t.Run(stage, func(t *testing.T) {
			fixture := migCovNewApplyCampaign(t, "github.com/acme/app", "github.com/acme/app", map[string]string{"app.go": "package app\nconst Value = \"old\"\n"}, migCovTextReplaceSpec("prepare"), CampaignOptions{Parallel: 1, Verify: VerifyNone})
			repo := fixture.repo
			stat := os.Stat
			run := runIn
			hit := false
			root := fixture.campaign.options.GitHubDir
			if stage == "parent mkdir" {
				stat = func(path string) (os.FileInfo, error) {
					info, err := os.Stat(path)
					if path == repo.canonical && os.IsNotExist(err) {
						if err := os.MkdirAll(filepath.Dir(filepath.Dir(path)), 0755); err != nil {
							t.Fatal(err)
						}
						if err := os.WriteFile(filepath.Dir(path), []byte("retained"), 0600); err != nil {
							t.Fatal(err)
						}
						hit = true
					}
					return info, err
				}
			}
			if stage == "physical canonical" || stage == "resume registration" {
				repo.resume = stage == "resume registration"
				run = func(dir, name string, args ...string) (string, error) {
					out, err := runIn(dir, name, args...)
					want := "fetch --quiet origin"
					if repo.resume {
						want = "rev-parse --verify origin/main^{commit}"
					}
					if err == nil && strings.Join(args, " ") == want {
						if err := os.Rename(repo.canonical, repo.canonical+"-retained"); err != nil {
							t.Fatal(err)
						}
						hit = true
					}
					return out, err
				}
			}
			if stage == "worktree observation" {
				blocked := filepath.Join(t.TempDir(), "block")
				if err := os.WriteFile(blocked, []byte("retained"), 0600); err != nil {
					t.Fatal(err)
				}
				repo.worktree = filepath.Join(blocked, "child")
				hit = true
			}
			if stage == "placement" {
				root = t.TempDir()
				hit = true
			}
			if stage == "creation" {
				repo.branch = "wb/migrate/"
				hit = true
			}
			err := prepareCampaignRepositoryWithIO(repo, root, stat, run, filepath.EvalSymlinks)
			if !hit || err == nil {
				t.Fatalf("hit=%v error=%v", hit, err)
			}
			if stage == "physical canonical" || stage == "resume registration" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("native missing canonical error=%v", err)
				}
				if _, err := os.Stat(filepath.Join(repo.canonical+"-retained", ".git")); err != nil {
					t.Fatalf("canonical evidence lost: %v", err)
				}
			}
		})
	}
}

//nolint:paralleltest // These subtests execute only in the isolated self-reexec child and reuse fixtures that mutate process environment.
func campaignSourceAndPublishFailures(t *testing.T) {
	t.Helper()
	t.Run("changed after planning", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "app.py")
		writeCampaignFile(t, path, "old\n")
		repo := &campaignRepository{repository: "github.com/acme/app", report: &CampaignRepositoryReport{}}
		module := &campaignModule{path: repo.repository, repository: repo.repository, migrate: true, root: root, report: &CampaignModuleReport{}}
		repo.modules = []*campaignModule{module}
		c := &campaign{spec: migCovTextReplaceSpec("planned"), modules: map[string]*campaignModule{module.path: module}, order: []string{module.path}}
		err := c.applyRepositorySourcesWithApply(repo, func(plan Plan) error { writeCampaignFile(t, path, "concurrent owner\n"); return Apply(plan) })
		if err == nil || !strings.Contains(err.Error(), "file changed after planning") || module.report.PlanState == "complete" {
			t.Fatalf("report=%+v error=%v", module.report, err)
		}
		if got := mustReadCampaignFile(t, path); got != "concurrent owner\n" {
			t.Fatalf("concurrent bytes replaced: %q", got)
		}
	})
	for _, stage := range []string{"finalize manifest", "verify publishable", "checks", "merge"} {
		t.Run(stage, func(t *testing.T) {
			options := CampaignOptions{Parallel: 1, Verify: VerifyNone, PR: true}
			if stage == "verify publishable" {
				options.Verify = VerifyTest
			}
			if stage == "checks" || stage == "merge" {
				options.PR = false
				options.Merge = true
			}
			fixture := migCovNewApplyCampaign(t, "github.com/acme/app", "github.com/acme/app", map[string]string{"app.go": "package app\nconst Value = \"old\"\n"}, migCovTextReplaceSpec("publish"), options)
			hit := false
			fixture.campaign.options.Progress = func(event progress.Event) {
				if event.State != progress.Running && event.State != progress.Waiting {
					return
				}
				switch {
				case stage == "finalize manifest" && event.Phase == "finalize_manifests":
					writeCampaignFile(t, filepath.Join(fixture.module.root, "go.mod"), "invalid go.mod")
					hit = true
				case stage == "verify publishable" && event.Phase == "verify_publishable":
					writeCampaignFile(t, filepath.Join(fixture.module.root, "failing_test.go"), "package app\nimport \"testing\"\nfunc TestFailure(t *testing.T){t.Fatal(\"intentional publication failure\")}\n")
					fixture.module.report.PublishableManifest = true
					hit = true
				case stage == "checks" && event.Phase == "required_checks":
					hit = true
				case stage == "merge" && event.Phase == "merge":
					hit = true
				}
			}
			if stage == "checks" {
				fixture.repo.report.PR = "invalid-pull-request-url"
			}
			if stage == "merge" {
				migCovInstallFakeGH(t)
				t.Setenv("GH_FAKE_MODE", "fail-merge")
				fixture.repo.report.PR = "https://github.com/acme/app/pull/1"
			}
			err := fixture.campaign.apply()
			if !hit || err == nil || fixture.repo.report.Merged {
				t.Fatalf("hit=%v report=%+v error=%v", hit, fixture.repo.report, err)
			}
			want := map[string]string{"finalize manifest": "make go.mod publishable", "verify publishable": "verification failed", "checks": "pull request", "merge": "merge conflict"}[stage]
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error=%v want=%q", err, want)
			}
		})
	}
	t.Run("ready component before blocked peer", func(t *testing.T) {
		fixture := newCampaignIntegrationFixture(t)
		options, err := normalizeCampaignOptions(CampaignOptions{GitHubDir: fixture.githubDir, Apply: true, PR: true, Verify: VerifyNone, Parallel: 1, CloneURL: fixture.cloneURL})
		if err != nil {
			t.Fatal(err)
		}
		c, err := planCampaign(fixture.spec, fixture.sourceRoot, options)
		if err != nil {
			t.Fatal(err)
		}
		// Two independent components share a layer; the consumer's manifest
		// still requires an unpublished provider and must stay unchanged.
		c.children = nil
		c.options.Commit = false
		err = c.apply()
		if err == nil || !strings.Contains(err.Error(), "go_module_release") {
			t.Fatalf("preflight error=%v", err)
		}
		provider := c.repositoryByName("github.com/acme/provider")
		consumer := c.repositoryByName("github.com/acme/consumer")
		if provider.modules[0].report.Status != "provided" || provider.modules[0].report.PlanState != "not_applicable" || consumer.modules[0].report.PlanState != "deferred" {
			t.Fatalf("ready=%+v blocked=%+v", provider.modules[0].report, consumer.modules[0].report)
		}
		assertGitClean(t, consumer.worktree)
	})
	t.Run("cycle seed push", func(t *testing.T) {
		fixture := migCovCycleFixture(t, "")
		migCovInstallFakeGH(t)
		options, err := normalizeCampaignOptions(CampaignOptions{GitHubDir: fixture.githubDir, Apply: true, PR: true, Verify: VerifyNone, Parallel: 1, CloneURL: fixture.cloneURL})
		if err != nil {
			t.Fatal(err)
		}
		c, err := planCampaign(fixture.spec, fixture.sourceRoot, options)
		if err != nil {
			t.Fatal(err)
		}
		hit := false
		c.options.Progress = func(event progress.Event) {
			if event.Phase == "verify" && event.State == progress.Completed && event.Repository == "github.com/acme/x" {
				repo := c.repositoryByName(event.Repository)
				runCampaignGit(t, repo.worktree, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "absent.git"))
				hit = true
			}
		}
		err = c.apply()
		if !hit || err == nil || !strings.Contains(err.Error(), "git push") {
			t.Fatalf("hit=%v error=%v", hit, err)
		}
		repo := c.repositoryByName("github.com/acme/x")
		if repo.report.Pushed || repo.report.PR != "" {
			t.Fatalf("failed seed reported published: %+v", repo.report)
		}
	})
}

//nolint:paralleltest // These subtests execute only in the isolated self-reexec child and reuse fixtures that mutate process environment.
func campaignDiscoveryAndCleanupFailures(t *testing.T) {
	t.Helper()
	t.Run("invalid task segment", func(t *testing.T) {
		root := t.TempDir()
		migCovWriteGoMod(t, root, "module github.com/acme/app\n\ngo 1.27\n")
		spec := migCovTextReplaceSpec("!!!")
		spec.Steps[0].From = "github.com/acme/app/pkg"
		plan, err := planCampaign(spec, root, CampaignOptions{GitHubDir: t.TempDir(), Ref: "main"})
		if plan != nil || err == nil || !strings.Contains(err.Error(), "invalid worktree task") {
			t.Fatalf("plan=%v error=%v", plan, err)
		}
	})
	for _, stage := range []string{"resume branch", "resume branch changed", "resume module", "locked cleanup"} {
		t.Run(stage, func(t *testing.T) {
			fixture := migCovNewApplyCampaign(t, "github.com/acme/app", "github.com/acme/app", nil, migCovTextReplaceSpec("cleanup"), CampaignOptions{Parallel: 1, Verify: VerifyNone})
			if err := prepareCampaignRepository(fixture.repo, fixture.campaign.options.GitHubDir); err != nil {
				t.Fatal(err)
			}
			hit := false
			run := func(dir, name string, args ...string) (string, error) {
				if strings.Join(args, " ") == "branch --show-current" {
					hit = true
					if stage == "resume branch" {
						if err := os.Rename(filepath.Join(dir, ".git"), filepath.Join(dir, ".git-retained")); err != nil {
							t.Fatal(err)
						}
					}
					if stage == "resume branch changed" {
						runCampaignGit(t, dir, "checkout", "-b", "manual-repair")
					}
					output, err := runIn(dir, name, args...)
					if stage == "resume module" {
						if err := os.Rename(filepath.Join(dir, "go.mod"), filepath.Join(dir, "go.mod-retained")); err != nil {
							t.Fatal(err)
						}
					}
					return output, err
				}
				if stage == "locked cleanup" && len(args) > 1 && args[0] == "worktree" && args[1] == "remove" {
					runCampaignGit(t, fixture.repo.canonical, "worktree", "lock", "--reason", "retain fixture", fixture.repo.worktree)
					hit = true
				}
				return runIn(dir, name, args...)
			}
			var err error
			if stage == "locked cleanup" {
				_, err = cleanupCampaignWorktreesWithRootAndRun(fixture.campaign.options.GitHubDir, "cleanup", wbhome.Root, run)
			} else {
				_, err = campaignDiscoveryRootWithRun(fixture.campaign.spec, fixture.repo.canonical, CampaignOptions{GitHubDir: fixture.campaign.options.GitHubDir, Resume: true}, run)
			}
			if !hit || err == nil {
				t.Fatalf("hit=%v error=%v", hit, err)
			}
			if stage == "resume branch changed" && !strings.Contains(err.Error(), "cannot discover resumed") {
				t.Fatalf("native branch mismatch error=%v", err)
			}
			if stage == "locked cleanup" && !strings.Contains(err.Error(), "locked") {
				t.Fatalf("native removal error=%v", err)
			}
			if _, err := os.Stat(fixture.repo.worktree); err != nil {
				t.Fatalf("worktree evidence lost: %v", err)
			}
		})
	}
}
