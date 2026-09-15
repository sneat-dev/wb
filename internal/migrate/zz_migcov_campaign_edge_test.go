package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// migCovInstallFakeGit puts a scripted git on PATH so a single Git failure can
// be isolated without corrupting a real repository.
func migCovInstallFakeGit(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	writeCampaignFile(t, filepath.Join(binDir, "git"), `#!/bin/sh
case "$1" in
  status)
    if [ "$GIT_FAKE_DIRTY" = "1" ]; then
      printf ' M changed.txt\n'
    fi
    exit 0
    ;;
  diff)
    exit 0
    ;;
  ls-files)
    if [ "$GIT_FAKE_LS_FILES_FAIL" = "1" ]; then
      exit 1
    fi
    exit 0
    ;;
  rev-list)
    printf 'deadbeefdeadbeef\n'
    exit 0
    ;;
  rev-parse)
    if [ "$GIT_FAKE_REV_PARSE_FAIL" = "1" ]; then
      exit 1
    fi
    printf 'deadbeefdeadbeef\n'
    exit 0
    ;;
esac
exit 0
`)
	if err := os.Chmod(filepath.Join(binDir, "git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestMigCovRefreshRepositoryChangeIndexReportsEachGitFailure(t *testing.T) {
	migCovInstallFakeGit(t)
	repo := &campaignRepository{
		repository: "example.com/app", worktree: t.TempDir(), ref: "main",
		report: &CampaignRepositoryReport{},
	}
	t.Setenv("GIT_FAKE_LS_FILES_FAIL", "1")
	err := refreshRepositoryChangeIndex(repo)
	if err == nil || !strings.Contains(err.Error(), "index untracked files for example.com/app") {
		t.Fatalf("refreshRepositoryChangeIndex() = %v, want an untracked-file error", err)
	}
}

func TestMigCovCommitAndPublishRepositoryReportsRevParseFailures(t *testing.T) {
	cleanRepo := &campaignRepository{
		repository: "example.com/app", worktree: t.TempDir(), ref: "main", branch: "main",
		modules: []*campaignModule{{path: "example.com/app", migrate: true}},
		report:  &CampaignRepositoryReport{},
	}
	c := &campaign{spec: Spec{ID: "rev-parse"}, options: CampaignOptions{Resume: true}}

	// A clean --resume that is ahead of its base ref cannot record the head.
	migCovInstallFakeGit(t)
	t.Setenv("GIT_FAKE_REV_PARSE_FAIL", "1")
	if err := c.commitAndPublishRepository(cleanRepo); err == nil {
		t.Fatal("commitAndPublishRepository() recorded a head without rev-parse")
	}

	// A dirty worktree that cannot report its new head is refused too.
	dirtyRepo := &campaignRepository{
		repository: "example.com/app", worktree: t.TempDir(), ref: "main", branch: "main",
		modules: []*campaignModule{{path: "example.com/app", migrate: true}},
		report:  &CampaignRepositoryReport{},
	}
	t.Setenv("GIT_FAKE_DIRTY", "1")
	if err := c.commitAndPublishRepository(dirtyRepo); err == nil {
		t.Fatal("commitAndPublishRepository() committed without a readable head")
	}
}

func TestMigCovSeedCycleComponentReportsRevParseFailure(t *testing.T) {
	migCovInstallFakeGit(t)
	t.Setenv("GIT_FAKE_DIRTY", "1")
	t.Setenv("GIT_FAKE_REV_PARSE_FAIL", "1")
	module := &campaignModule{path: "example.com/app", repository: "github.com/acme/app", migrate: true, root: t.TempDir()}
	repo := &campaignRepository{repository: "github.com/acme/app", worktree: t.TempDir(), branch: "main", modules: []*campaignModule{module}, report: &CampaignRepositoryReport{}}
	c := &campaign{
		spec:    Spec{ID: "seed-rev-parse"},
		modules: map[string]*campaignModule{"example.com/app": module},
		repos:   []*campaignRepository{repo},
	}
	if _, err := c.seedCycleComponent(cycleBootstrap{
		repositories: []*campaignRepository{repo},
		modulePaths:  map[string]bool{"example.com/app": true},
	}); err == nil {
		t.Fatal("seedCycleComponent() recorded a seed without a readable head")
	}
}

func TestMigCovSeedCycleComponentReportsUnknownRepositoryForKnownHead(t *testing.T) {
	worktree := migCovClone(t, "seed-ghost", "module example.com/app\n\ngo 1.24\n")
	module := &campaignModule{path: "example.com/app", repository: "github.com/acme/ghost", migrate: true, root: worktree}
	repo := &campaignRepository{repository: "github.com/acme/ghost", worktree: worktree, branch: "main", modules: []*campaignModule{module}, report: &CampaignRepositoryReport{}}
	// The bootstrap names a repository the campaign itself does not track.
	c := &campaign{
		spec:    Spec{ID: "seed-ghost"},
		modules: map[string]*campaignModule{"example.com/app": module},
		repos:   []*campaignRepository{},
	}
	_, err := c.seedCycleComponent(cycleBootstrap{
		repositories: []*campaignRepository{repo},
		modulePaths:  map[string]bool{"example.com/app": true},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot find repository github.com/acme/ghost") {
		t.Fatalf("seedCycleComponent() = %v, want an unknown-repository error", err)
	}
}

func TestMigCovPseudoVersionRejectsNonsensicalPseudoBase(t *testing.T) {
	repository := migCovClone(t, "pseudo-incompatible", "module github.com/acme/module\n\ngo 1.24\n")
	revision := strings.TrimSpace(runCampaignGit(t, repository, "rev-parse", "HEAD"))
	_, err := pseudoVersionForCommit(
		repository,
		"github.com/acme/module",
		"v0.0.0-20200101000000-abcdef123456+incompatible",
		revision,
	)
	if err == nil || !strings.Contains(err.Error(), "find pseudo-version base") {
		t.Fatalf("pseudoVersionForCommit(nonsensical pseudo base) = %v", err)
	}
}
