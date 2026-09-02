package deps

import (
	"context"
	"strings"
	"testing"
	"time"
)

func driftObservedAt() time.Time { return time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC) }

// newNpmDriftRepository writes a checkout with arbitrary npm manifests. Drift
// inspects the working tree, so unlike the bump-wave fixtures no git remote is
// needed — but the files must live where a real repository would put them.
func newNpmDriftRepository(t *testing.T, root, name string, files map[string]string) Repository {
	t.Helper()
	checkout := root + "/" + name
	for relative, contents := range files {
		writeTestFile(t, checkout+"/"+relative, contents)
	}
	return Repository{Slug: "acme/" + name, Path: checkout}
}

const calendariusPackageJSON = `{
  "name": "@sneat/calendarius-ui",
  "version": "0.24.3",
  "dependencies": {
    "@sneat/core": "^0.30.0"
  },
  "peerDependencies": {
    "@sneat/extension-contactus-ui": "0.14.0"
  }
}
`

func TestAnalyzeNpmDriftPrefersTheLockedVersionOverTheDeclaredRange(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	app := newNpmDriftRepository(t, root, "app", map[string]string{
		"package.json": `{"name":"app","dependencies":{"@sneat/core":"^0.30.0"}}` + "\n",
		"pnpm-lock.yaml": "lockfileVersion: '9.0'\n" +
			"importers:\n" +
			"  .:\n" +
			"    dependencies:\n" +
			"      '@sneat/core':\n" +
			"        specifier: ^0.30.0\n" +
			"        version: 0.30.1(@angular/core@20.0.0)\n",
	})

	report, err := AnalyzeDrift(context.Background(), []Repository{app}, DriftOptions{
		Ecosystem: EcosystemNPM, GitHubDir: root, Parallel: 1, Now: driftObservedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Ecosystem != EcosystemNPM {
		t.Fatalf("ecosystem = %s", report.Ecosystem)
	}
	dependency := report.Repositories[0].Dependencies[0]
	if dependency.Declared.Value != "^0.30.0" {
		t.Fatalf("declared = %+v", dependency.Declared)
	}
	if dependency.Selected.Value != "0.30.1" {
		t.Fatalf("selected = %+v, want the peer-suffix stripped lockfile version", dependency.Selected)
	}
	if dependency.Field != "dependencies" {
		t.Fatalf("field = %q", dependency.Field)
	}
}

func TestAnalyzeNpmDriftReportsDivergentLockedVersions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	lock := func(version string) string {
		return "lockfileVersion: '9.0'\nimporters:\n  .:\n    dependencies:\n      '@sneat/core':\n        specifier: ^0.30.0\n        version: " + version + "\n"
	}
	ahead := newNpmDriftRepository(t, root, "ahead", map[string]string{
		"package.json":   `{"name":"ahead","dependencies":{"@sneat/core":"^0.30.0"}}` + "\n",
		"pnpm-lock.yaml": lock("0.30.4"),
	})
	behind := newNpmDriftRepository(t, root, "behind", map[string]string{
		"package.json":   `{"name":"behind","dependencies":{"@sneat/core":"^0.30.0"}}` + "\n",
		"pnpm-lock.yaml": lock("0.30.1"),
	})

	report, err := AnalyzeDrift(context.Background(), []Repository{ahead, behind}, DriftOptions{
		Ecosystem: EcosystemNPM, GitHubDir: root, Parallel: 2, Now: driftObservedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	group := driftGroup(t, report, "@sneat/core")
	if group.Classification != DriftDivergent {
		t.Fatalf("classification = %s, want divergent: %+v", group.Classification, group)
	}
	if group.Family != "" {
		t.Fatalf("family = %q, want npm names never grouped into major-path families", group.Family)
	}
	if !DriftFailed(report, true) {
		t.Fatal("--fail-on-drift must fail on divergent npm selections")
	}
}

func TestAnalyzeNpmDriftReportsRepositoriesBehindTheRegistryLatest(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	calendarius := newNpmDriftRepository(t, root, "calendarius", map[string]string{
		"package.json": calendariusPackageJSON,
		"pnpm-lock.yaml": "lockfileVersion: '9.0'\nimporters:\n  .:\n    dependencies:\n      '@sneat/core':\n" +
			"        specifier: ^0.30.0\n        version: 0.30.1\n",
	})

	report, err := AnalyzeDrift(context.Background(), []Repository{calendarius}, DriftOptions{
		Ecosystem: EcosystemNPM, GitHubDir: root, Parallel: 1, Online: true, Now: driftObservedAt,
		Scopes: []string{"@sneat/*"},
		LatestNpmVersion: func(_ context.Context, module string) (string, error) {
			switch module {
			case "@sneat/core":
				return "0.30.7", nil
			case "@sneat/extension-contactus-ui":
				return "0.14.3", nil
			}
			return "", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	core := driftGroup(t, report, "@sneat/core")
	if !core.Behind || core.Classification != DriftBehindLatest {
		t.Fatalf("core = %+v, want a behind_latest classification", core)
	}
	if len(core.BehindRepositories) != 1 || core.BehindRepositories[0] != "acme/calendarius" {
		t.Fatalf("behind repositories = %+v", core.BehindRepositories)
	}

	// The exact peer pin "0.14.0" has no lockfile entry, so the range itself
	// must be judged: it provably cannot admit the published 0.14.3.
	peer := driftGroup(t, report, "@sneat/extension-contactus-ui")
	if !peer.Behind {
		t.Fatalf("peer = %+v, want an exact pin below latest reported as behind", peer)
	}

	if report.Summary.Behind != 2 {
		t.Fatalf("summary.behind = %d, want 2", report.Summary.Behind)
	}
	if DriftFailedWith(report, false, false) {
		t.Fatal("behind groups must not fail without --fail-on-behind")
	}
	if !DriftFailedWith(report, false, true) {
		t.Fatal("--fail-on-behind must fail when a repository lags latest")
	}
}

func TestAnalyzeNpmDriftNeverGuessesAnUnevaluableRange(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	app := newNpmDriftRepository(t, root, "workspace-app", map[string]string{
		"package.json": `{"name":"workspace-app","dependencies":{"@sneat/core":"workspace:*"}}` + "\n",
	})

	report, err := AnalyzeDrift(context.Background(), []Repository{app}, DriftOptions{
		Ecosystem: EcosystemNPM, GitHubDir: root, Parallel: 1, Online: true, Now: driftObservedAt,
		LatestNpmVersion: func(context.Context, string) (string, error) { return "0.30.7", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	group := driftGroup(t, report, "@sneat/core")
	if group.Behind {
		t.Fatalf("group = %+v, want a workspace: protocol never reported as behind", group)
	}
	dependency := report.Repositories[0].Dependencies[0]
	if !strings.Contains(dependency.Selected.Reason, "no lockfile governs") {
		t.Fatalf("selected = %+v, want an explicit no-lockfile reason", dependency.Selected)
	}
}

func TestAnalyzeNpmDriftReadsPnpmWorkspaceOverridesAndPackageLock(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	app := newNpmDriftRepository(t, root, "overrides-app", map[string]string{
		"package.json":        `{"name":"overrides-app","devDependencies":{"@sneat/core":"^0.30.0"}}` + "\n",
		"pnpm-workspace.yaml": "packages:\n  - 'packages/*'\noverrides:\n  '@sneat/core': 0.30.2\n",
		"package-lock.json": `{"lockfileVersion":3,"packages":{"":{"name":"overrides-app"},` +
			`"node_modules/@sneat/core":{"version":"0.30.2"}}}` + "\n",
	})

	report, err := AnalyzeDrift(context.Background(), []Repository{app}, DriftOptions{
		Ecosystem: EcosystemNPM, GitHubDir: root, Parallel: 1, Now: driftObservedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	fields := map[string]string{}
	for _, dependency := range report.Repositories[0].Dependencies {
		fields[dependency.Field] = dependency.Selected.Value
	}
	if fields["devDependencies"] != "0.30.2" {
		t.Fatalf("devDependencies selection = %q, want the package-lock resolution: %+v", fields["devDependencies"], fields)
	}
	if fields["overrides.@sneat/core"] != "0.30.2" {
		t.Fatalf("pnpm workspace override = %q, want it inspected as its own reference: %+v", fields["overrides.@sneat/core"], fields)
	}
}

func TestAnalyzeDriftExcludesRepositoriesByGlobAndReportsThem(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	kept := newNpmDriftRepository(t, root, "kept", map[string]string{
		"package.json": `{"name":"kept","dependencies":{"@sneat/core":"0.30.1"}}` + "\n",
	})
	gated := newNpmDriftRepository(t, root, "sneat-go", map[string]string{
		"package.json": `{"name":"sneat-go","dependencies":{"@sneat/core":"0.1.0"}}` + "\n",
	})

	report, err := AnalyzeDrift(context.Background(), []Repository{kept, gated}, DriftOptions{
		Ecosystem: EcosystemNPM, GitHubDir: root, Parallel: 1, Now: driftObservedAt,
		ExcludeRepositories: []string{"acme/sneat-*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Repositories) != 1 || report.Repositories[0].Repository != "acme/kept" {
		t.Fatalf("repositories = %+v, want only the retained repository", report.Repositories)
	}
	if len(report.Excluded) != 1 || report.Excluded[0] != "acme/sneat-go" {
		t.Fatalf("excluded = %+v", report.Excluded)
	}
	group := driftGroup(t, report, "@sneat/core")
	if group.Classification != DriftConverged {
		t.Fatalf("classification = %s; the excluded repository must not create drift", group.Classification)
	}
	if !strings.Contains(report.Markdown(), "acme/sneat-go") {
		t.Fatalf("markdown must name excluded repositories:\n%s", report.Markdown())
	}
}

func TestAnalyzeNpmDriftScopeRestrictsRetainedDependencies(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	app := newNpmDriftRepository(t, root, "scoped", map[string]string{
		"package.json": `{"name":"scoped","dependencies":{"@sneat/core":"0.30.1","rxjs":"7.8.0","@angular/core":"20.0.0"}}` + "\n",
	})

	report, err := AnalyzeDrift(context.Background(), []Repository{app}, DriftOptions{
		Ecosystem: EcosystemNPM, GitHubDir: root, Parallel: 1, Now: driftObservedAt,
		Scopes: []string{"@sneat/*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Groups) != 1 || report.Groups[0].Dependency != "@sneat/core" {
		t.Fatalf("groups = %+v, want only the @sneat scope retained", report.Groups)
	}
	if len(report.Repositories[0].Dependencies) != 1 {
		t.Fatalf("dependencies = %+v", report.Repositories[0].Dependencies)
	}
}

func TestAnalyzeDriftRejectsAnUnknownEcosystem(t *testing.T) {
	t.Parallel()
	if _, err := AnalyzeDrift(context.Background(), nil, DriftOptions{Ecosystem: Ecosystem("cargo")}); err == nil {
		t.Fatal("an unknown ecosystem must be refused rather than silently inspected as go")
	}
}
