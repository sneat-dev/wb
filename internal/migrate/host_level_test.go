package migrate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// TestCampaignCanonicalPathPrefersAnExistingClone pins the resolution the
// migration depends on: a repository that is already cloned is used where it
// is, host level first and legacy placement second, so a campaign never creates
// a second copy of it. Only when no clone exists is the literal host level
// predicted, which is where a new clone must land.
func TestCampaignCanonicalPathPrefersAnExistingClone(t *testing.T) {
	t.Parallel()
	newClone := func(t *testing.T, path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(path, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("host level wins", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		hosted := filepath.Join(root, "github.com", "acme", "app")
		newClone(t, hosted)
		newClone(t, filepath.Join(root, "acme", "app"))
		got, err := campaignCanonicalPath(root, "acme", "app", "github.com/acme/app")
		if err != nil {
			t.Fatal(err)
		}
		if got != hosted {
			t.Fatalf("campaignCanonicalPath = %q, want the host-level clone %q", got, hosted)
		}
	})

	t.Run("legacy clone is used in place", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		legacy := filepath.Join(root, "acme", "app")
		newClone(t, legacy)
		got, err := campaignCanonicalPath(root, "acme", "app", "github.com/acme/app")
		if err != nil {
			t.Fatal(err)
		}
		if got != legacy {
			t.Fatalf("campaignCanonicalPath = %q, want the existing legacy clone %q", got, legacy)
		}
	})

	t.Run("a missing clone is predicted at the host level", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		got, err := campaignCanonicalPath(root, "acme", "app", "github.com/acme/app")
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(root, "github.com", "acme", "app"); got != want {
			t.Fatalf("campaignCanonicalPath = %q, want the host-level destination %q", got, want)
		}
	})
}

// TestCampaignDoesNotCloneASecondCopyOfAHostLevelClone is the regression: on a
// root whose clones live at <root>/github.com/{owner}/{repo}, `wb migrate` used
// to treat the existing clone as uncloned, create a second host-less clone
// beside it, and plan a host-less store suffix. It must instead prepare the
// clone that is already there.
func TestCampaignDoesNotCloneASecondCopyOfAHostLevelClone(t *testing.T) {
	test := newCampaignIntegrationFixture(t)
	hosted := filepath.Join(test.githubDir, "github.com", "acme", "consumer")
	if err := os.MkdirAll(filepath.Dir(hosted), 0o755); err != nil {
		t.Fatal(err)
	}
	runCampaignGit(t, test.githubDir, "clone", "--quiet", test.cloneURL("github.com/acme/consumer"), hosted)

	report, err := RunCampaign(test.spec, test.sourceRoot, CampaignOptions{
		GitHubDir: test.githubDir,
		Apply:     true,
		Verify:    VerifyNone,
		Parallel:  1,
		CloneURL:  test.cloneURL,
	})
	if err != nil {
		t.Fatal(err)
	}

	consumer := campaignRepositoryByName(t, report, "github.com/acme/consumer")
	if filepath.Clean(consumer.CanonicalDir) != filepath.Clean(hosted) {
		t.Fatalf("campaign used canonical %q, want the existing host-level clone %q", consumer.CanonicalDir, hosted)
	}
	if _, err := os.Stat(filepath.Join(test.githubDir, "acme", "consumer")); !os.IsNotExist(err) {
		t.Fatalf("campaign created a host-less second clone at %s", filepath.Join(test.githubDir, "acme", "consumer"))
	}
	// A repository the campaign really did have to clone lands at the host
	// level too, so a migration does not keep producing the legacy shape.
	provider, err := worktrees.CanonicalRepositoryPath(test.githubDir, "acme/provider")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(test.githubDir, "github.com", "acme", "provider"); provider != want {
		t.Fatalf("newly cloned repository is at %q, want %q", provider, want)
	}
	if _, err := os.Stat(filepath.Join(test.githubDir, "acme", "provider")); !os.IsNotExist(err) {
		t.Fatalf("campaign created the new clone flat at %s", filepath.Join(test.githubDir, "acme", "provider"))
	}
}
