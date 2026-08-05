package deps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
