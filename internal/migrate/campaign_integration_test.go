package migrate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCampaignUsesIsolatedWorktreesAndCanResumeAndClean(t *testing.T) {
	test := newCampaignIntegrationFixture(t)
	report, err := RunCampaign(test.spec, test.sourceRoot, CampaignOptions{
		GitHubDir: test.githubDir,
		Apply:     true,
		Commit:    true,
		Verify:    VerifyNone,
		Parallel:  2,
		CloneURL:  test.cloneURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	consumer := campaignRepositoryByName(t, report, "github.com/acme/consumer")
	if consumer.Commit == "" {
		t.Fatal("consumer campaign worktree was not committed")
	}
	changed, err := os.ReadFile(filepath.Join(consumer.WorktreeDir, "consumer.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(changed), "github.com/acme/provider/pkg") {
		t.Fatalf("consumer worktree still has old path:\n%s", changed)
	}
	goMod, err := os.ReadFile(filepath.Join(consumer.WorktreeDir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	provider := campaignRepositoryByName(t, report, "github.com/acme/provider")
	relativeProvider, err := filepath.Rel(consumer.WorktreeDir, provider.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(goMod), "replace github.com/acme/provider => "+filepath.ToSlash(relativeProvider)) {
		t.Fatalf("consumer go.mod does not use an isolated campaign replacement:\n%s", goMod)
	}
	assertGitClean(t, filepath.Join(test.githubDir, "acme", "consumer"))
	if source, err := os.ReadFile(filepath.Join(test.sourceRoot, "consumer.go")); err != nil || strings.Contains(string(source), "new-provider") {
		t.Fatalf("source root was changed: %v\n%s", err, source)
	}

	// A clean existing worktree is safe to resume. It is not recreated or
	// checked out from the canonical clone.
	if _, err := RunCampaign(test.spec, test.sourceRoot, CampaignOptions{
		GitHubDir: test.githubDir,
		Apply:     true,
		Resume:    true,
		Commit:    true,
		Verify:    VerifyNone,
		CloneURL:  test.cloneURL,
	}); err != nil {
		t.Fatalf("resume campaign: %v", err)
	}
	removed, err := CleanupCampaignWorktrees(test.githubDir, test.spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed worktrees = %v, want provider and consumer", removed)
	}
	if _, err := os.Stat(consumer.WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("consumer worktree still exists after cleanup: %v", err)
	}
}

func TestOpenCampaignPRDoesNotReuseMergedPullRequest(t *testing.T) {
	worktree := t.TempDir()
	binDir := filepath.Join(t.TempDir(), "bin")
	logPath := filepath.Join(t.TempDir(), "gh.log")
	writeCampaignFile(t, filepath.Join(binDir, "gh"), `#!/bin/sh
printf '%s\n' "$*" >> "$GH_LOG"
if [ "$1 $2" = "pr list" ]; then
	exit 0
fi
if [ "$1 $2" = "pr create" ]; then
	printf '%s\n' 'https://github.com/acme/example/pull/2'
	exit 0
fi
exit 1
`)
	if err := os.Chmod(filepath.Join(binDir, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GH_LOG", logPath)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	url, err := openCampaignPR(&campaignRepository{
		repository: "github.com/acme/example",
		worktree:   worktree,
		branch:     "wb/migrate/example",
		ref:        "main",
	}, Spec{ID: "example", Title: "Example migration"})
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://github.com/acme/example/pull/2" {
		t.Fatalf("pull request URL = %q", url)
	}
	log := mustReadCampaignFile(t, logPath)
	if !strings.Contains(log, "pr list --head wb/migrate/example --base main --state open") {
		t.Fatalf("open pull request lookup missing from gh calls:\n%s", log)
	}
	if !strings.Contains(log, "pr create --base main --head wb/migrate/example") {
		t.Fatalf("new pull request was not created after open lookup returned none:\n%s", log)
	}
}

func TestCampaignResumesPartialDirtyWorktrees(t *testing.T) {
	test := newCampaignIntegrationFixture(t)
	firstReport, err := RunCampaign(test.spec, test.sourceRoot, CampaignOptions{
		GitHubDir: test.githubDir,
		Apply:     true,
		Verify:    VerifyNone,
		CloneURL:  test.cloneURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstConsumer := campaignRepositoryByName(t, firstReport, "github.com/acme/consumer")
	if firstConsumer.ChangedFiles == nil || !containsString(*firstConsumer.ChangedFiles, "consumer.go") {
		t.Fatalf("initial consumer change index = %v, want consumer.go", firstConsumer.ChangedFiles)
	}
	report, err := RunCampaign(test.spec, test.sourceRoot, CampaignOptions{
		GitHubDir: test.githubDir,
		Apply:     true,
		Resume:    true,
		Verify:    VerifyCompile,
		CloneURL:  test.cloneURL,
	})
	if err != nil {
		t.Fatalf("resume dirty campaign: %v", err)
	}
	consumer := campaignRepositoryByName(t, report, "github.com/acme/consumer")
	if consumer.ChangedFiles == nil || !containsString(*consumer.ChangedFiles, "consumer.go") {
		t.Fatalf("resumed consumer change index = %v, want cumulative consumer.go", consumer.ChangedFiles)
	}
	if consumer.Modules[0].ChangedFiles == nil || *consumer.Modules[0].ChangedFiles != 0 {
		t.Fatalf("resumed pass changed files = %v, want idempotent pass count 0", consumer.Modules[0].ChangedFiles)
	}
	if len(consumer.Modules) != 1 || len(consumer.Modules[0].Verifications) != 1 || !consumer.Modules[0].Verifications[0].Passed {
		t.Fatalf("resumed consumer verification = %+v", consumer.Modules)
	}
	changed, err := os.ReadFile(filepath.Join(consumer.WorktreeDir, "consumer.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(changed), "github.com/acme/provider/pkg") {
		t.Fatalf("resumed worktree lost migration changes:\n%s", changed)
	}
}

func TestCampaignDoesNotCommitDirtyProviderWorktree(t *testing.T) {
	test := newCampaignIntegrationFixture(t)
	report, err := RunCampaign(test.spec, test.sourceRoot, CampaignOptions{
		GitHubDir: test.githubDir,
		Apply:     true,
		Verify:    VerifyNone,
		CloneURL:  test.cloneURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := campaignRepositoryByName(t, report, "github.com/acme/provider")
	providerHead := strings.TrimSpace(runCampaignGit(t, provider.WorktreeDir, "rev-parse", "HEAD"))
	writeCampaignFile(t, filepath.Join(provider.WorktreeDir, "provider-local.txt"), "must remain uncommitted\n")

	resumed, err := RunCampaign(test.spec, test.sourceRoot, CampaignOptions{
		GitHubDir: test.githubDir,
		Apply:     true,
		Resume:    true,
		Commit:    true,
		Verify:    VerifyNone,
		CloneURL:  test.cloneURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	resumedProvider := campaignRepositoryByName(t, resumed, "github.com/acme/provider")
	if resumedProvider.Commit != "" {
		t.Fatalf("provider commit = %q, want none", resumedProvider.Commit)
	}
	if head := strings.TrimSpace(runCampaignGit(t, provider.WorktreeDir, "rev-parse", "HEAD")); head != providerHead {
		t.Fatalf("provider HEAD = %s, want unchanged %s", head, providerHead)
	}
	if status := runCampaignGit(t, provider.WorktreeDir, "status", "--porcelain"); !strings.Contains(status, "provider-local.txt") {
		t.Fatalf("provider worktree change was not preserved: %q", status)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestCampaignResumeDiscoversDependencyAddedInRootWorktree(t *testing.T) {
	test := newCampaignIntegrationFixture(t)
	if _, err := RunCampaign(test.spec, test.sourceRoot, CampaignOptions{
		GitHubDir: test.githubDir,
		Apply:     true,
		Verify:    VerifyNone,
		CloneURL:  test.cloneURL,
	}); err != nil {
		t.Fatal(err)
	}

	lateSource := filepath.Join(t.TempDir(), "late")
	providerSource := filepath.Join(filepath.Dir(test.sourceRoot), "provider")
	writeCampaignFile(t, filepath.Join(lateSource, "go.mod"), "module github.com/acme/late\n\ngo 1.24\n\nrequire github.com/acme/provider v0.0.0\n\nreplace github.com/acme/provider => "+providerSource+"\n")
	writeCampaignFile(t, filepath.Join(lateSource, "late.go"), "package late\n\nimport \"github.com/acme/provider\"\n\nconst Package = \"github.com/acme/provider/pkg\"\nvar _ = provider.Package\n")
	commitCampaignRepository(t, lateSource, test.cloneURL("github.com/acme/late"))

	consumerWorktree := filepath.Join(test.githubDir, ".wb", "worktrees", test.spec.ID, "acme", "consumer")
	consumerGoMod := mustReadCampaignFile(t, filepath.Join(consumerWorktree, "go.mod"))
	consumerGoMod += "\nrequire github.com/acme/late v0.0.0\n\nreplace github.com/acme/late => " + lateSource + "\n"
	writeCampaignFile(t, filepath.Join(consumerWorktree, "go.mod"), consumerGoMod)
	writeCampaignFile(t, filepath.Join(consumerWorktree, "late.go"), "package consumer\n\nimport _ \"github.com/acme/late\"\n")

	report, err := RunCampaign(test.spec, test.sourceRoot, CampaignOptions{
		GitHubDir: test.githubDir,
		Apply:     true,
		Resume:    true,
		Verify:    VerifyNone,
		CloneURL:  test.cloneURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	late := campaignRepositoryByName(t, report, "github.com/acme/late")
	changed := mustReadCampaignFile(t, filepath.Join(late.WorktreeDir, "late.go"))
	if strings.Contains(changed, "github.com/acme/provider/pkg") || !strings.Contains(changed, "github.com/acme/new-provider/pkg") {
		t.Fatalf("newly discovered dependency was not migrated:\n%s", changed)
	}
}

func TestLocalCampaignContinuesVerificationAfterProviderFailure(t *testing.T) {
	test := newCampaignIntegrationFixture(t)
	if _, err := RunCampaign(test.spec, test.sourceRoot, CampaignOptions{
		GitHubDir: test.githubDir,
		Apply:     true,
		Verify:    VerifyNone,
		CloneURL:  test.cloneURL,
	}); err != nil {
		t.Fatal(err)
	}

	adapterSource := filepath.Join(t.TempDir(), "adapter")
	providerSource := filepath.Join(filepath.Dir(test.sourceRoot), "provider")
	writeCampaignFile(t, filepath.Join(adapterSource, "go.mod"), "module github.com/acme/adapter\n\ngo 1.24\n\nrequire github.com/acme/provider v0.0.0\n\nreplace github.com/acme/provider => "+providerSource+"\n")
	writeCampaignFile(t, filepath.Join(adapterSource, "adapter.go"), "package adapter\n\nimport \"github.com/acme/provider\"\n\nconst Package = \"github.com/acme/provider/pkg\"\nvar _ = provider.Package\n")
	writeCampaignFile(t, filepath.Join(adapterSource, "failure_test.go"), "package adapter\n\nimport \"testing\"\n\nfunc TestFailure(t *testing.T) { t.Fatal(\"intentional adapter failure\") }\n")
	commitCampaignRepository(t, adapterSource, test.cloneURL("github.com/acme/adapter"))

	consumerWorktree := filepath.Join(test.githubDir, ".wb", "worktrees", test.spec.ID, "acme", "consumer")
	consumerGoMod := mustReadCampaignFile(t, filepath.Join(consumerWorktree, "go.mod"))
	consumerGoMod += "\nrequire github.com/acme/adapter v0.0.0\n\nreplace github.com/acme/adapter => " + adapterSource + "\n"
	writeCampaignFile(t, filepath.Join(consumerWorktree, "go.mod"), consumerGoMod)
	writeCampaignFile(t, filepath.Join(consumerWorktree, "adapter.go"), "package consumer\n\nimport _ \"github.com/acme/adapter\"\n")

	report, err := RunCampaign(test.spec, test.sourceRoot, CampaignOptions{
		GitHubDir: test.githubDir,
		Apply:     true,
		Resume:    true,
		Verify:    VerifyFull,
		Parallel:  2,
		CloneURL:  test.cloneURL,
	})
	if err == nil {
		t.Fatal("campaign unexpectedly passed")
	}
	provider := campaignRepositoryByName(t, report, "github.com/acme/adapter")
	consumer := campaignRepositoryByName(t, report, "github.com/acme/consumer")
	if len(provider.Modules[0].Verifications) != 2 || provider.Modules[0].Verifications[1].Passed {
		t.Fatalf("provider verifications = %+v", provider.Modules[0].Verifications)
	}
	if len(consumer.Modules[0].Verifications) != 2 || !consumer.Modules[0].Verifications[0].Passed || !consumer.Modules[0].Verifications[1].Passed {
		t.Fatalf("consumer was not fully verified after provider failure: %+v", consumer.Modules[0].Verifications)
	}
}

func TestUpdateGoModuleTidiesUnusedMigrationRequirement(t *testing.T) {
	moduleRoot := t.TempDir()
	recordRoot := t.TempDir()
	writeCampaignFile(t, filepath.Join(moduleRoot, "go.mod"), "module github.com/acme/unused\n\ngo 1.24\n")
	writeCampaignFile(t, filepath.Join(moduleRoot, "unused.go"), "package unused\n")
	writeCampaignFile(t, filepath.Join(recordRoot, "go.mod"), "module github.com/dal-go/record\n\ngo 1.24\n")
	update, err := updateGoModule(moduleRoot, Spec{
		GoModuleRequires: []GoModuleRequire{{Path: "github.com/dal-go/record", Version: "v0.1.0"}},
	}, "github.com/acme/unused", map[string]string{"github.com/dal-go/record": recordRoot})
	if err != nil {
		t.Fatal(err)
	}
	if update.Changed {
		t.Fatalf("unused migration requirement left a go.mod change:\n%s", mustReadCampaignFile(t, filepath.Join(moduleRoot, "go.mod")))
	}
	if len(update.DependencyDecisions) != 1 {
		t.Fatalf("dependency decisions = %+v", update.DependencyDecisions)
	}
	decision := update.DependencyDecisions[0]
	if decision.Path != "github.com/dal-go/record" || decision.RequiredAtCheck || decision.RequiredAfter ||
		decision.TargetVersion != "v0.1.0" || decision.VersionAction != "not_required" ||
		!strings.Contains(decision.Reason, "no source use") {
		t.Fatalf("dependency decision = %+v", decision)
	}
}

func TestCampaignPRRequiresPublishedVersionsBeforePush(t *testing.T) {
	test := newCampaignIntegrationFixture(t)
	_, err := RunCampaign(test.spec, test.sourceRoot, CampaignOptions{
		GitHubDir: test.githubDir,
		Apply:     true,
		PR:        true,
		Verify:    VerifyNone,
		CloneURL:  test.cloneURL,
	})
	if err == nil || !strings.Contains(err.Error(), "go_module_release") {
		t.Fatalf("campaign error = %v, want missing go_module_release", err)
	}
	consumerWorktree := filepath.Join(test.githubDir, ".wb", "worktrees", test.spec.ID, "acme", "consumer")
	if output := runCampaignGit(t, consumerWorktree, "log", "--format=%s", "origin/main..HEAD"); strings.TrimSpace(output) != "" {
		t.Fatalf("consumer was committed before the publishability gate: %s", output)
	}
	assertGitClean(t, consumerWorktree)
}

func TestPreflightPublishedReleasesRejectsUnrelatedLocalReplacement(t *testing.T) {
	moduleRoot := t.TempDir()
	writeCampaignFile(t, filepath.Join(moduleRoot, "go.mod"), "module github.com/acme/consumer\n\ngo 1.24\n\nrequire example.com/unrelated v0.0.0\n\nreplace example.com/unrelated => ../unrelated\n")

	_, err := preflightPublishedReleases(
		moduleRoot,
		Spec{},
		"github.com/acme/consumer",
		map[string]string{"github.com/acme/provider": t.TempDir()},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "has local replacement") {
		t.Fatalf("preflight error = %v, want unrelated local replacement rejection", err)
	}
}

type campaignIntegrationFixture struct {
	spec       Spec
	sourceRoot string
	githubDir  string
	cloneURL   func(string) string
}

func newCampaignIntegrationFixture(t *testing.T) campaignIntegrationFixture {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "WB Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "wb@example.test")
	t.Setenv("GIT_COMMITTER_NAME", "WB Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "wb@example.test")
	root := t.TempDir()
	remotes := filepath.Join(root, "remotes")
	providerSource := filepath.Join(root, "source", "provider")
	consumerSource := filepath.Join(root, "source", "consumer")
	writeCampaignFile(t, filepath.Join(providerSource, "go.mod"), "module github.com/acme/provider\n\ngo 1.24\n")
	writeCampaignFile(t, filepath.Join(providerSource, "provider.go"), "package provider\n\nconst Package = \"github.com/acme/provider/pkg\"\n")
	commitCampaignRepository(t, providerSource, filepath.Join(remotes, "acme", "provider.git"))

	writeCampaignFile(t, filepath.Join(consumerSource, "go.mod"), "module github.com/acme/consumer\n\ngo 1.24\n\nrequire github.com/acme/provider v0.0.0\n\nreplace github.com/acme/provider => ../provider\n")
	writeCampaignFile(t, filepath.Join(consumerSource, "consumer.go"), "package consumer\n\nimport \"github.com/acme/provider\"\n\nconst Package = \"github.com/acme/provider/pkg\"\nvar _ = provider.Package\n")
	remoteConsumer := filepath.Join(root, "remote-consumer")
	writeCampaignFile(t, filepath.Join(remoteConsumer, "go.mod"), "module github.com/acme/consumer\n\ngo 1.24\n\nrequire github.com/acme/provider v0.0.0\n")
	writeCampaignFile(t, filepath.Join(remoteConsumer, "consumer.go"), "package consumer\n\nimport \"github.com/acme/provider\"\n\nconst Package = \"github.com/acme/provider/pkg\"\nvar _ = provider.Package\n")
	commitCampaignRepository(t, remoteConsumer, filepath.Join(remotes, "acme", "consumer.git"))

	return campaignIntegrationFixture{
		spec:       Spec{Format: MigrationFormatV1, ID: "campaign-test", Steps: []Step{{Kind: "text.replace", Language: "go", From: "github.com/acme/provider/pkg", To: "github.com/acme/new-provider/pkg"}}},
		sourceRoot: consumerSource,
		githubDir:  filepath.Join(root, "github"),
		cloneURL: func(repository string) string {
			return filepath.Join(remotes, strings.TrimPrefix(repository, "github.com/")+".git")
		},
	}
}

func writeCampaignFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustReadCampaignFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func commitCampaignRepository(t *testing.T, source, remote string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		t.Fatal(err)
	}
	runCampaignGit(t, filepath.Dir(source), "init", "--bare", remote)
	runCampaignGit(t, source, "init", "--initial-branch=main")
	runCampaignGit(t, source, "add", ".")
	runCampaignGit(t, source, "-c", "user.name=WB Test", "-c", "user.email=wb@example.test", "commit", "-m", "initial")
	runCampaignGit(t, source, "remote", "add", "origin", remote)
	runCampaignGit(t, source, "push", "-u", "origin", "main")
}

func runCampaignGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, output)
	}
	return string(output)
}

func campaignRepositoryByName(t *testing.T, report CampaignReport, name string) CampaignRepositoryReport {
	t.Helper()
	for _, repository := range report.Repositories {
		if repository.Repository == name {
			return repository
		}
	}
	t.Fatalf("report does not contain %s", name)
	return CampaignRepositoryReport{}
}

func assertGitClean(t *testing.T, dir string) {
	t.Helper()
	if output := runCampaignGit(t, dir, "status", "--porcelain"); strings.TrimSpace(output) != "" {
		t.Fatalf("git worktree is dirty at %s:\n%s", dir, output)
	}
}
