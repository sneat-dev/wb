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

type campaignIntegrationFixture struct {
	spec       Spec
	sourceRoot string
	githubDir  string
	cloneURL   func(string) string
}

func newCampaignIntegrationFixture(t *testing.T) campaignIntegrationFixture {
	t.Helper()
	root := t.TempDir()
	remotes := filepath.Join(root, "remotes")
	providerSource := filepath.Join(root, "source", "provider")
	consumerSource := filepath.Join(root, "source", "consumer")
	writeCampaignFile(t, filepath.Join(providerSource, "go.mod"), "module github.com/acme/provider\n\ngo 1.24\n")
	writeCampaignFile(t, filepath.Join(providerSource, "provider.go"), "package provider\n\nconst Package = \"github.com/acme/provider/pkg\"\n")
	commitCampaignRepository(t, providerSource, filepath.Join(remotes, "acme", "provider.git"))

	writeCampaignFile(t, filepath.Join(consumerSource, "go.mod"), "module github.com/acme/consumer\n\ngo 1.24\n\nrequire github.com/acme/provider v0.0.0\n\nreplace github.com/acme/provider => ../provider\n")
	writeCampaignFile(t, filepath.Join(consumerSource, "consumer.go"), "package consumer\n\nconst Package = \"github.com/acme/provider/pkg\"\n")
	remoteConsumer := filepath.Join(root, "remote-consumer")
	writeCampaignFile(t, filepath.Join(remoteConsumer, "go.mod"), "module github.com/acme/consumer\n\ngo 1.24\n\nrequire github.com/acme/provider v0.0.0\n")
	writeCampaignFile(t, filepath.Join(remoteConsumer, "consumer.go"), "package consumer\n\nconst Package = \"github.com/acme/provider/pkg\"\n")
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
