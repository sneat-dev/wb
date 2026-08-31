package deps

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newNpmBumpRepository mirrors newBumpRepository (see bump_test.go) for the
// npm ecosystem: it seeds a bare remote plus a canonical clone containing the
// given files, on "main".
func newNpmBumpRepository(t *testing.T, root, githubDir, name string, files map[string]string) Repository {
	t.Helper()
	seed := filepath.Join(root, name+"-npm-seed")
	remote := filepath.Join(root, name+"-npm.git")
	canonical := filepath.Join(githubDir, "acme", name)
	for path, body := range files {
		writeTestFile(t, filepath.Join(seed, path), body)
	}
	runTestGit(t, seed, "init", "-b", "main")
	runTestGit(t, seed, "config", "user.name", "WB Test")
	runTestGit(t, seed, "config", "user.email", "wb@example.test")
	runTestGit(t, seed, "add", "-A")
	runTestGit(t, seed, "commit", "-m", "initial")
	runTestGit(t, root, "clone", "--bare", seed, remote)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "clone", remote, canonical)
	return Repository{Slug: "acme/" + name, Path: canonical, CloneURL: remote}
}

// TestRunBumpNpmDryRunPlansOnlyDirectConsumers is the npm-ecosystem twin of
// TestRunBumpDryRunPlansOnlyDirectConsumers: the same provider -> adapter ->
// consumer chain, recalculated wave selection, and dry-run reporting, but
// driven entirely through package.json dependency fields instead of go.mod
// requirements, proving `deps bump npm` shares the same wave engine as `deps
// bump go` rather than a parallel implementation that could silently drift.
func TestRunBumpNpmDryRunPlansOnlyDirectConsumers(t *testing.T) {
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	repositories := []Repository{
		newNpmBumpRepository(t, root, githubDir, "provider", map[string]string{
			"package.json": npmPackageJSONWithDependency("@acme/provider", "left-pad", "^1.0.0"),
		}),
		newNpmBumpRepository(t, root, githubDir, "adapter", map[string]string{
			"package.json": npmPackageJSONWithDependency("@acme/adapter", "@acme/provider", "0.1.0"),
		}),
		newNpmBumpRepository(t, root, githubDir, "consumer", map[string]string{
			"package.json": npmPackageJSONWithDependency("@acme/consumer", "@acme/adapter", "0.1.0"),
		}),
	}
	report, err := RunBump(context.Background(), []ReleaseEvent{{Dependency: "@acme/provider", Version: "0.2.0", Source: "explicit"}}, repositories, BumpOptions{
		Ecosystem: EcosystemNPM,
		Options:   Options{GitHubDir: githubDir, Ref: "main", Parallel: 2, DryRun: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Ecosystem != EcosystemNPM {
		t.Fatalf("report ecosystem = %q, want npm", report.Ecosystem)
	}
	if !strings.HasPrefix(report.Operation, "deps-bump-npm-") {
		t.Fatalf("report operation = %q, want an npm-prefixed campaign id", report.Operation)
	}
	if report.Status != "planned" || len(report.Waves) != 1 || len(report.Waves[0].Repositories) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if repository := report.Waves[0].Repositories[0]; repository.Repository != "acme/adapter" || repository.Status != "planned" {
		t.Fatalf("wave repository = %+v", repository)
	}
	if markdown := report.Markdown(); !strings.Contains(markdown, "@acme/provider") || !strings.Contains(markdown, "npm") {
		t.Fatalf("dry-run decisions are missing from Markdown:\n%s", markdown)
	}
}

// TestRunBumpNpmSecondSweepTraversesPublishedPeerDependencyCarrier covers a
// published consumer whose only selected provider is a peer dependency. The
// fleet graph deliberately includes every npm dependency field, so registry
// verification must consume that same field set before it decides a consumer
// release cannot carry the next provider wave.
func TestRunBumpNpmSecondSweepTraversesPublishedPeerDependencyCarrier(t *testing.T) {
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	repositories := []Repository{
		newNpmBumpRepository(t, root, githubDir, "provider", map[string]string{
			"package.json": npmPackageJSONWithDependency("@acme/provider", "left-pad", "^1.0.0"),
		}),
		newNpmBumpRepository(t, root, githubDir, "adapter", map[string]string{
			"package.json": "{\n  \"name\": \"@acme/adapter\",\n  \"version\": \"0.2.1\",\n  \"peerDependencies\": {\n    \"@acme/provider\": \"0.2.0\"\n  }\n}\n",
		}),
		newNpmBumpRepository(t, root, githubDir, "consumer", map[string]string{
			"package.json": npmPackageJSONWithDependency("@acme/consumer", "@acme/adapter", "0.1.0"),
		}),
	}

	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	pnpm := filepath.Join(binDir, "pnpm")
	writeTestFile(t, pnpm, `#!/bin/sh
if [ "$1" = "view" ] && [ "$3" = "version" ]; then
  printf '%s\n' '0.2.1'
  exit 0
fi
if [ "$1" = "view" ] && [ "$2" = "@acme/adapter@0.2.1" ]; then
  case " $* " in
    *" peerDependencies "*)
      printf '%s\n' '{"dependencies":{"tslib":"^2.3.0"},"peerDependencies":{"@acme/provider":"0.2.0"}}'
      exit 0
      ;;
  esac
  printf '%s\n' '{"tslib":"^2.3.0"}'
  exit 0
fi
printf 'unexpected pnpm arguments: %s\n' "$*" >&2
exit 1
`)
	if err := os.Chmod(pnpm, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	report, err := RunBump(context.Background(), []ReleaseEvent{{Dependency: "@acme/provider", Version: "0.2.0", Source: "explicit"}}, repositories, BumpOptions{
		Ecosystem: EcosystemNPM,
		Options:   Options{GitHubDir: githubDir, Ref: "main", Parallel: 2, DryRun: true, Timeout: time.Minute},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "planned" || len(report.Waves) != 1 {
		t.Fatalf("report = %+v", report)
	}
	if release := report.Waves[0].Releases[0]; release.Module != "@acme/adapter" || release.After != "0.2.1" || release.Status != "released" {
		t.Fatalf("existing peer release = %+v", release)
	}
	if repository := report.Waves[0].Repositories[0]; repository.Repository != "acme/consumer" || repository.Status != "planned" {
		t.Fatalf("downstream repository = %+v", repository)
	}
}

func TestParsePublishedNpmRequirementsUsesCanonicalDiscoveryFields(t *testing.T) {
	requirements, err := parsePublishedNpmRequirements(`{
  "dependencies": {"@acme/core": "1.0.0"},
  "devDependencies": {"@acme/data": "1.0.0"},
  "peerDependencies": {"@acme/dto": "1.0.0"},
  "optionalDependencies": {"@acme/space": "1.0.0"}
}`)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"@acme/core": "1.0.0", "@acme/data": "1.0.0", "@acme/dto": "1.0.0", "@acme/space": "1.0.0",
	}
	if len(requirements) != len(want) {
		t.Fatalf("requirements = %#v, want %#v", requirements, want)
	}
	for dependency, version := range want {
		if requirements[dependency] != version {
			t.Fatalf("requirements[%q] = %q, want %q", dependency, requirements[dependency], version)
		}
	}
}

func TestParsePublishedNpmRequirementsRejectsConflictingFields(t *testing.T) {
	_, err := parsePublishedNpmRequirements(`{
  "dependencies": {"@acme/core": "1.0.0"},
  "peerDependencies": {"@acme/core": "2.0.0"}
}`)
	if err == nil || !strings.Contains(err.Error(), "conflicting published npm selections") {
		t.Fatalf("error = %v", err)
	}
}

// TestRunBumpNpmNoRegistrySkipsCurrentCarrierEvidence proves the explicit
// no-registry policy used by `wb deps publish npm` plans. The adapter is
// already current for the provider event and has an external consumer, which
// would normally make discoverExistingReleaseCarriers invoke pnpm view.
func TestRunBumpNpmNoRegistrySkipsCurrentCarrierEvidence(t *testing.T) {
	root := t.TempDir()
	githubDir := filepath.Join(root, "projects")
	repositories := []Repository{
		newNpmBumpRepository(t, root, githubDir, "provider", map[string]string{
			"package.json": npmPackageJSONWithDependency("@acme/provider", "left-pad", "^1.0.0"),
		}),
		newNpmBumpRepository(t, root, githubDir, "adapter", map[string]string{
			"package.json": npmPackageJSONWithDependency("@acme/adapter", "@acme/provider", "0.2.0"),
		}),
		newNpmBumpRepository(t, root, githubDir, "consumer", map[string]string{
			"package.json": npmPackageJSONWithDependency("@acme/consumer", "@acme/adapter", "0.1.0"),
		}),
	}
	calledRegistry := false
	report, err := RunBump(context.Background(), []ReleaseEvent{{
		Dependency: "@acme/provider", Version: "0.2.0", Source: "planned", CheckedAt: time.Unix(1, 0),
	}}, repositories, BumpOptions{
		Ecosystem:  EcosystemNPM,
		Options:    Options{GitHubDir: githubDir, Ref: "main", Parallel: 2, DryRun: true},
		NoRegistry: true, RefreshAfter: time.Nanosecond,
		Now: func() time.Time { return time.Unix(2, 0) },
		LatestNpmVersion: func(context.Context, string) (string, error) {
			calledRegistry = true
			return "", errors.New("registry must not be called")
		},
		LatestNpmRelease: func(context.Context, string) (PublishedGoRelease, error) {
			calledRegistry = true
			return PublishedGoRelease{}, errors.New("registry must not be called")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calledRegistry {
		t.Fatal("no-registry npm plan invoked a registry resolver")
	}
	if !report.RegistryLookupsSkipped || !strings.Contains(report.Markdown(), "Registry carrier and stale-event lookups: `skipped`") {
		t.Fatalf("no-registry report does not disclose its policy: %+v", report)
	}
}

// TestRunBumpNpmRejectsInvalidPackageName pins the npm-ecosystem branch of
// normalizeBumpOptions' event validation: a `--changed` event has to look
// like a real npm package identity before any repository work starts.
func TestRunBumpNpmRejectsInvalidPackageName(t *testing.T) {
	t.Parallel()
	_, err := RunBump(context.Background(), []ReleaseEvent{{Dependency: "Not An npm Name", Version: "1.0.0"}}, nil, BumpOptions{
		Ecosystem: EcosystemNPM,
		Options:   Options{GitHubDir: t.TempDir(), DryRun: true},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid npm package") {
		t.Fatalf("error = %v, want an invalid npm package rejection", err)
	}
}

// TestRunBumpNpmRejectsRangeAsReleaseVersion pins that a `--changed` event
// must be an exact published version, not a range — deps bump only ever
// carries evidence of an actual release forward.
func TestRunBumpNpmRejectsRangeAsReleaseVersion(t *testing.T) {
	t.Parallel()
	_, err := RunBump(context.Background(), []ReleaseEvent{{Dependency: "@acme/provider", Version: "^1.0.0"}}, nil, BumpOptions{
		Ecosystem: EcosystemNPM,
		Options:   Options{GitHubDir: t.TempDir(), DryRun: true},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid npm version") {
		t.Fatalf("error = %v, want an invalid npm version rejection", err)
	}
}
