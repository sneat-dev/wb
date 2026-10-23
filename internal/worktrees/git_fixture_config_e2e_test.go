//go:build e2e

package worktrees

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/testenv"
)

func configureFixtureGitWithSubprocesses(t *testing.T, directory string, bare bool) {
	t.Helper()
	if !bare {
		configureGitUser(t, directory)
	}
	testenv.ConfigureGitAutoMaintenanceOff(t, directory)
}

func newFixtureConfigExperiment(t *testing.T, configure func(*testing.T, string, bool)) *gitFixture {
	t.Helper()
	root := t.TempDir()
	projects := filepath.Join(root, "projects")
	t.Setenv("HOME", filepath.Join(root, "home"))
	return newGitFixtureAtRepositoryWithConfig(t, root, filepath.Join(projects, ".wb"), "app", configure)
}

func fixtureConfigGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	environment := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, "GIT_CONFIG") {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	result, err := runner.New().RunOpts(t.Context(), directory, runner.RunOptions{Env: environment}, "git", args...)
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, result.Stderr)
	}
	return strings.TrimSpace(result.Stdout)
}

//nolint:paralleltest // Fixture constructors set process-wide HOME and XDG configuration.
func TestE2EGitFixtureConfigSeedingPreservesGitState(t *testing.T) {
	for _, backend := range []struct {
		name      string
		configure func(*testing.T, string, bool)
	}{{"subprocess", configureFixtureGitWithSubprocesses}, {"seeded", seedFreshFixtureGitConfig}} {
		//nolint:paralleltest // Each isolated fixture changes the process environment.
		t.Run(backend.name, func(t *testing.T) {
			fixture := newFixtureConfigExperiment(t, backend.configure)
			for _, repository := range []struct{ directory, config string }{
				{fixture.remote, filepath.Join(fixture.remote, "config")},
				{fixture.canonical, filepath.Join(fixture.canonical, ".git", "config")},
			} {
				for key, want := range map[string]string{"gc.auto": "0", "maintenance.auto": "false", "receive.autogc": "false"} {
					if got := fixtureConfigGit(t, repository.directory, "config", "--file", repository.config, "--get", key); got != want {
						t.Fatalf("%s %s = %q, want %q", repository.directory, key, got, want)
					}
				}
			}
			config := filepath.Join(fixture.canonical, ".git", "config")
			for key, want := range map[string]string{"user.name": "WB Test", "user.email": "wb@example.test", "remote.origin.url": fixture.remote, "core.bare": "false"} {
				if got := fixtureConfigGit(t, fixture.canonical, "config", "--file", config, "--get", key); got != want {
					t.Fatalf("%s = %q, want %q", key, got, want)
				}
			}
			if got := fixtureConfigGit(t, fixture.remote, "config", "--file", filepath.Join(fixture.remote, "config"), "--get", "core.bare"); got != "true" {
				t.Fatalf("bare remote core.bare = %q", got)
			}
			canonical := fixtureConfigGit(t, fixture.canonical, "rev-parse", "refs/heads/main")
			if remote := fixtureConfigGit(t, fixture.remote, "rev-parse", "refs/heads/main"); canonical != remote {
				t.Fatalf("initial commit was not pushed: canonical=%s remote=%s", canonical, remote)
			}
		})
	}
}

//nolint:paralleltest // Paired serial measurements include each fixture's environment restoration and cleanup.
func TestE2EGitFixtureConfigSetupTiming(t *testing.T) {
	if os.Getenv("WB_FIXTURE_TIMING") != "1" {
		t.Skip("opt-in fixture timing experiment")
	}
	var samples [2][]time.Duration
	backends := []func(*testing.T, string, bool){configureFixtureGitWithSubprocesses, seedFreshFixtureGitConfig}
	for pair := range 8 {
		for step := range 2 {
			index := (pair + step) % 2
			started := time.Now()
			//nolint:paralleltest // Alternating complete fixture runs are deliberately measured serially.
			t.Run(fmt.Sprintf("pair-%02d/backend-%d", pair, index), func(t *testing.T) {
				newFixtureConfigExperiment(t, backends[index])
			})
			samples[index] = append(samples[index], time.Since(started))
		}
	}
	for index, values := range samples {
		slices.Sort(values)
		t.Logf("backend=%d median=%s min=%s max=%s samples=%v", index, (values[3]+values[4])/2, values[0], values[7], values)
	}
}
