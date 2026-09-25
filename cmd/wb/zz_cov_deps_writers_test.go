package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/spf13/cobra"
)

// cwDepsFailingWriter fails every write, so the writers' error branches are
// reachable without a real broken pipe.
type cwDepsFailingWriter struct{}

func (cwDepsFailingWriter) Write([]byte) (int, error) { return 0, errors.New("cwDeps: write refused") }

// cwDepsNewOutCommand returns a command whose stdout is the given writer.
func cwDepsNewOutCommand(out interface{ Write([]byte) (int, error) }) *cobra.Command {
	command := &cobra.Command{Use: "cw-deps-fixture"}
	command.SetOut(out)
	command.SetErr(out)
	return command
}

func cwDepsDepsReportFixture() deps.Report {
	return deps.Report{
		SchemaVersion: 1,
		Target:        deps.Target{Dependency: "github.com/acme/lib", Version: "v1.2.3", Ecosystem: deps.EcosystemGo, Resolved: "v1.2.3"},
		Status:        "completed",
		BaseRef:       "main",
		Parallel:      1,
		Repositories: []deps.RepositoryReport{{
			Repository: "acme/app", Status: "updated", Reason: "dependency set",
			Ref: "main", ChangedFiles: []string{"go.mod"},
		}},
	}
}

func cwDepsDriftReportFixture() deps.DriftReport {
	return deps.DriftReport{
		SchemaVersion: 1, Ecosystem: deps.EcosystemGo, Mode: "offline", BaseRef: "main",
		Summary: deps.DriftSummary{Repositories: 1, Dependencies: 1, Converged: 1},
		Groups: []deps.DriftVersionGroup{{
			Dependency: "github.com/acme/lib", Classification: "converged",
			Versions: []deps.DriftVersionUse{{Version: "v1.2.3", Kind: "declared", Repositories: []string{"acme/app"}}},
		}},
	}
}

func cwDepsBumpReportFixture() deps.BumpReport {
	return deps.BumpReport{
		SchemaVersion: 1, Operation: "deps-bump-cwfixture", Status: "completed",
		Ecosystem: deps.EcosystemGo, BaseRef: "main", Parallel: 1,
		SeedEvents: []deps.ReleaseEvent{{Dependency: "github.com/acme/lib", Version: "v1.2.3", Source: "explicit"}},
	}
}

func cwDepsPeerReportFixture() deps.PeerReport {
	return deps.PeerReport{
		SchemaVersion: 1, Package: "@acme/lib", Version: "1.0.0", Against: "/tmp/app",
		Peers:   []deps.PeerRow{{Peer: "react", Required: "^18.0.0", Installed: "18.2.0", Verdict: deps.PeerSatisfied}},
		Summary: deps.PeerSummary{Total: 1, Satisfied: 1},
	}
}

// TestCwDepsWritersRenderEveryFormatAndRefuseUnknown drives the four report
// writers directly: markdown, yaml, json, and the unknown-format refusal.
func TestCwDepsWritersRenderEveryFormatAndRefuseUnknown(t *testing.T) {
	type writer struct {
		name  string
		write func(command *cobra.Command, format string) error
	}
	writers := []writer{
		{"deps set", func(command *cobra.Command, format string) error {
			return writeDepsSetReport(command, cwDepsDepsReportFixture(), format)
		}},
		{"deps drift", func(command *cobra.Command, format string) error {
			return writeDepsDriftReport(command, cwDepsDriftReportFixture(), format)
		}},
		{"deps bump", func(command *cobra.Command, format string) error {
			return writeDepsBumpReport(command, cwDepsBumpReportFixture(), format)
		}},
		{"deps peers", func(command *cobra.Command, format string) error {
			return writeDepsPeersReport(command, cwDepsPeerReportFixture(), format)
		}},
	}
	for _, w := range writers {
		t.Run(w.name, func(t *testing.T) {
			for _, format := range []string{"markdown", "yaml", "json"} {
				var out bytes.Buffer
				if err := w.write(cwDepsNewOutCommand(&out), format); err != nil {
					t.Fatalf("%s/%s: %v", w.name, format, err)
				}
				if strings.TrimSpace(out.String()) == "" {
					t.Errorf("%s/%s wrote nothing", w.name, format)
				}
				if format == "json" && !json.Valid(out.Bytes()) {
					t.Errorf("%s/%s is not JSON: %s", w.name, format, out.String())
				}
			}
			err := w.write(cwDepsNewOutCommand(&bytes.Buffer{}), "toml")
			if err == nil || !strings.Contains(err.Error(), `unknown --format "toml"`) {
				t.Fatalf("%s unknown format error = %v", w.name, err)
			}
			// A refused stdout write must surface, never be swallowed.
			if err := w.write(cwDepsNewOutCommand(cwDepsFailingWriter{}), "markdown"); err == nil ||
				!strings.Contains(err.Error(), "write refused") {
				t.Fatalf("%s did not surface a failed stdout write: %v", w.name, err)
			}
		})
	}
}

// TestCwDepsBumpSeedEventsParsesExplicitAndDerivesFromRegistry covers both
// halves of the seed derivation and their refusal paths.
func TestCwDepsBumpSeedEventsParsesExplicitAndDerivesFromRegistry(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	command := cwDepsNewOutCommand(&bytes.Buffer{})

	// No --latest: the explicit list is parsed, and an empty list is refused
	// rather than silently running a campaign that propagates nothing.
	events, err := depsBumpSeedEvents(command, deps.EcosystemGo,
		[]string{"github.com/acme/lib@v1.2.3"}, nil, &depsSetOptions{}, deps.Options{})
	if err != nil || len(events) != 1 || events[0].Dependency != "github.com/acme/lib" || events[0].Version != "v1.2.3" {
		t.Fatalf("explicit seed events = %+v, %v", events, err)
	}
	if events[0].Source != "explicit" {
		t.Errorf("explicit event source = %q", events[0].Source)
	}
	if _, err := depsBumpSeedEvents(command, deps.EcosystemGo, nil, nil, &depsSetOptions{}, deps.Options{}); err == nil ||
		!strings.Contains(err.Error(), "at least one --changed") {
		t.Fatalf("empty seed events error = %v", err)
	}
	if _, err := depsBumpSeedEvents(command, deps.EcosystemGo, []string{"not-a-target"}, nil, &depsSetOptions{}, deps.Options{}); err == nil {
		t.Fatal("a malformed --changed event must be refused")
	}

	// --latest with no matching module derives nothing and refuses with the
	// reason rather than seeding an empty campaign. The registry lookup that
	// would otherwise follow is never reached, so this stays offline.
	options := depsSetOptions{latest: true, scopes: []string{"github.com/acme/*"}}
	campaign := newCampaignProgress(&bytes.Buffer{}, false, "deps bump")
	options.campaign = campaign
	if _, err := depsBumpSeedEvents(command, deps.EcosystemGo, []string{"github.com/acme/lib@v2.0.0"}, nil, &options, deps.Options{GitHubDir: t.TempDir(), DryRun: true}); err == nil ||
		!strings.Contains(err.Error(), "nothing to derive") {
		t.Fatalf("--latest with no matching module error = %v", err)
	}
	// bumpDerivedScopes only names scopes a --latest run actually used.
	if got := bumpDerivedScopes(options); len(got) != 1 || got[0] != "github.com/acme/*" {
		t.Errorf("bumpDerivedScopes = %v", got)
	}
	if got := bumpDerivedScopes(depsSetOptions{scopes: []string{"github.com/acme/*"}}); got != nil {
		t.Errorf("bumpDerivedScopes without --latest = %v, want nil", got)
	}
}

// TestCwDepsRunBumpWritesReportAndFinishesCampaign drives the shared wave seam
// with an empty fleet: the report is persisted and the campaign is finished.
func TestCwDepsRunBumpWritesReportAndFinishesCampaign(t *testing.T) {
	home := t.TempDir()
	t.Setenv(wbhome.EnvOverride, home)
	reportDir := filepath.Join(home, "reports", "cw-deps-bump")
	var errOut bytes.Buffer
	campaign := newCampaignProgress(&errOut, false, "deps bump")
	command := cwDepsNewOutCommand(&bytes.Buffer{})
	command.SetErr(&errOut)
	events := []deps.ReleaseEvent{{Dependency: "github.com/acme/lib", Version: "v1.2.3", Source: "explicit"}}

	options := depsSetOptions{campaign: campaign, reportDir: reportDir, maxWaves: 1, format: "markdown"}
	lifecycle := deps.Options{GitHubDir: t.TempDir(), DryRun: true, Parallel: 1}
	err := runDepsBump(&invocation{}, command, deps.EcosystemGo, events, nil, options, lifecycle)
	if err != nil {
		t.Fatalf("runDepsBump over an empty fleet: %v", err)
	}
	// The report directory is where the wave engine persists its state; an
	// empty fleet still produces an auditable run.
	if _, statErr := os.Stat(reportDir); statErr != nil {
		t.Fatalf("report dir %s was not written: %v", reportDir, statErr)
	}
}

// TestCwDepsExecuteDepsBumpWithoutCampaign covers the seam composite commands
// use: no campaign is supplied, so the function owns one, and a --resume with
// no persisted report is refused with the path it looked for.
func TestCwDepsExecuteDepsBumpWithoutCampaign(t *testing.T) {
	home := t.TempDir()
	t.Setenv(wbhome.EnvOverride, home)
	reportDir := filepath.Join(home, "reports", "cw-deps-bump")
	var errOut bytes.Buffer
	command := cwDepsNewOutCommand(&bytes.Buffer{})
	command.SetErr(&errOut)
	events := []deps.ReleaseEvent{{Dependency: "github.com/acme/lib", Version: "v1.2.3", Source: "explicit"}}

	report, resolvedDir, err := executeDepsBump(&invocation{}, command, deps.EcosystemGo, events, nil,
		depsSetOptions{reportDir: reportDir, maxWaves: 1}, deps.Options{GitHubDir: t.TempDir(), DryRun: true, Parallel: 1})
	if err != nil {
		t.Fatalf("executeDepsBump over an empty fleet: %v", err)
	}
	if resolvedDir != reportDir {
		t.Errorf("report directory = %q, want %q", resolvedDir, reportDir)
	}
	if report.Operation == "" {
		t.Error("the wave engine did not stamp an operation id on its report")
	}

	// --resume with nothing persisted names the file it wanted.
	_, resumeDir, err := executeDepsBump(&invocation{}, command, deps.EcosystemGo, events, nil,
		depsSetOptions{reportDir: filepath.Join(home, "reports", "cw-deps-absent"), resume: true, maxWaves: 1}, deps.Options{GitHubDir: t.TempDir(), DryRun: true, Parallel: 1})
	if err == nil || !strings.Contains(err.Error(), "--resume requires") {
		t.Fatalf("resume without a report error = %v (dir %s)", err, resumeDir)
	}
}

// TestCwDepsRepositoryIdentityReadsOriginAndLayout covers the two ways a
// repository's owner/name is established: its origin remote, and its position
// under the projects root when there is no usable remote.
func TestCwDepsRepositoryIdentityReadsOriginAndLayout(t *testing.T) {
	root := t.TempDir()
	withOrigin := initTestRepository(t, filepath.Join(root, "acme", "app"))
	cwDepsGit(t, withOrigin, "remote", "add", "origin", "git@github.com:acme/app.git")
	slug, cloneURL, err := repositoryIdentity(withOrigin, root)
	if err != nil || slug != "acme/app" || cloneURL != "git@github.com:acme/app.git" {
		t.Fatalf("origin identity = (%q, %q, %v)", slug, cloneURL, err)
	}

	// No origin at all: the {org}/{repo} position under the projects root is
	// the only identity available.
	withoutOrigin := initTestRepository(t, filepath.Join(root, "beta", "tool"))
	slug, cloneURL, err = repositoryIdentity(withoutOrigin, root)
	if err != nil || slug != "beta/tool" || cloneURL != "" {
		t.Fatalf("layout identity = (%q, %q, %v)", slug, cloneURL, err)
	}

	// A remote that is not GitHub falls back to the layout.
	nonGitHub := initTestRepository(t, filepath.Join(root, "gamma", "widget"))
	cwDepsGit(t, nonGitHub, "remote", "add", "origin", "https://gitlab.example.test/gamma/widget.git")
	if slug, _, err := repositoryIdentity(nonGitHub, root); err != nil || slug != "gamma/widget" {
		t.Fatalf("non-GitHub remote identity = (%q, %v)", slug, err)
	}

	// Outside the projects root with no origin there is no identity to guess.
	stranger := initTestRepository(t, filepath.Join(t.TempDir(), "somewhere-else"))
	if _, _, err := repositoryIdentity(stranger, root); err == nil ||
		!strings.Contains(err.Error(), "cannot determine GitHub owner/repository identity") {
		t.Fatalf("unidentifiable repository error = %v", err)
	}
	// githubSlug rejects a blank/non-GitHub remote.
	if got := githubSlug("https://example.test/acme/app.git"); got != "" {
		t.Errorf("githubSlug(non-GitHub) = %q, want empty", got)
	}
}

// TestCwDepsDependencyRepositoriesSelectsLocallyAndOverFleet covers the
// selection guardrails and both selection modes without any network.
func TestCwDepsDependencyRepositoriesSelectsLocallyAndOverFleet(t *testing.T) {
	root := t.TempDir()
	app := initTestRepository(t, filepath.Join(root, "acme", "app"))
	cwDepsGit(t, app, "remote", "add", "origin", "git@github.com:acme/app.git")
	initTestRepository(t, filepath.Join(root, "acme", "other"))
	cwCovFakeGH(t, "cwcov-user", []string{"acme"}, `[]`)
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	previousProjectsRoot := projectsRoot
	projectsRoot = root
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })

	// Guardrails are checked before any discovery happens.
	if _, err := dependencyRepositories(&invocation{}, []string{"go", "set"}, depsSetOptions{parallel: 0}); err == nil ||
		!strings.Contains(err.Error(), "parallelism must be at least 1") {
		t.Fatalf("parallel guard = %v", err)
	}
	if _, err := dependencyRepositories(&invocation{}, []string{"go", "set"}, depsSetOptions{parallel: 1, retry: -1}); err == nil ||
		!strings.Contains(err.Error(), "retry count must not be negative") {
		t.Fatalf("retry guard = %v", err)
	}
	if _, err := dependencyRepositories(&invocation{}, []string{"go", "set"}, depsSetOptions{parallel: 1, timeout: -1}); err == nil ||
		!strings.Contains(err.Error(), "timeout must not be negative") {
		t.Fatalf("timeout guard = %v", err)
	}
	if _, err := dependencyRepositories(&invocation{}, []string{"go", "set"}, depsSetOptions{parallel: 1, regex: "("}); err == nil ||
		!strings.Contains(err.Error(), "invalid --regex") {
		t.Fatalf("regex guard = %v", err)
	}
	if _, err := dependencyRepositories(&invocation{}, []string{"go", "set"}, depsSetOptions{parallel: 1, match: "["}); err == nil ||
		!strings.Contains(err.Error(), "invalid --match") {
		t.Fatalf("match guard = %v", err)
	}

	// Single-repository mode: the path is the third argument.
	repositories, err := dependencyRepositories(&invocation{}, []string{"go", "set", app}, depsSetOptions{parallel: 1})
	if err != nil || len(repositories) != 1 || repositories[0].Slug != "acme/app" || repositories[0].Path != app {
		t.Fatalf("single repository selection = %+v, %v", repositories, err)
	}
	// A --match that excludes the only candidate is refused, not reported as
	// an empty successful run.
	if _, err := dependencyRepositories(&invocation{}, []string{"go", "set", app}, depsSetOptions{parallel: 1, match: "beta/*"}); err == nil ||
		!strings.Contains(err.Error(), "does not match selected filters") {
		t.Fatalf("unmatched single repository = %v", err)
	}
	// --filter is applied to the identity too.
	previousFilter := filterFlag
	filterFlag = "nothing-matches"
	_, err = dependencyRepositories(&invocation{}, []string{"go", "set", app}, depsSetOptions{parallel: 1})
	filterFlag = previousFilter
	if err == nil || !strings.Contains(err.Error(), "does not match --filter") {
		t.Fatalf("filtered single repository = %v", err)
	}

	// Fleet mode reconciles local clones with the owned repositories.
	repositories, err = dependencyRepositories(&invocation{}, []string{"go", "set"}, depsSetOptions{parallel: 1, fleet: true, timeout: 0})
	if err != nil {
		t.Fatalf("fleet selection: %v", err)
	}
	slugs := map[string]bool{}
	for _, repository := range repositories {
		slugs[repository.Slug] = true
	}
	if !slugs["acme/app"] {
		t.Errorf("fleet selection lost the local clone: %+v", slugs)
	}
	for i := 1; i < len(repositories); i++ {
		if repositories[i-1].Slug > repositories[i].Slug {
			t.Fatalf("fleet selection is not sorted: %+v", repositories)
		}
	}
	if _, err := dependencyRepositories(&invocation{}, []string{"go", "set"}, depsSetOptions{parallel: 1, fleet: true, match: "nothing/*"}); err == nil ||
		!strings.Contains(err.Error(), "no repositories match the selected fleet filters") {
		t.Fatalf("empty fleet selection = %v", err)
	}
}

// TestCwDepsMatchesDependencyRepository covers the glob+regex conjunction.
func TestCwDepsMatchesDependencyRepository(t *testing.T) {
	expression, err := compileDependencyRegex("^acme/")
	if err != nil {
		t.Fatal(err)
	}
	if !matchesDependencyRepository("acme/app", "acme/*", expression) {
		t.Error("glob and regex that both match must select")
	}
	if matchesDependencyRepository("acme/app", "beta/*", expression) {
		t.Error("a failing glob must not select")
	}
	if matchesDependencyRepository("beta/app", "beta/*", expression) {
		t.Error("a failing regex must not select")
	}
	if !matchesDependencyRepository("beta/app", "", nil) {
		t.Error("no filter at all must select")
	}
	if matchesDependencyRepository("acme/app", "[", nil) {
		t.Error("an invalid glob must not select")
	}
	if expression, err := compileDependencyRegex(""); err != nil || expression != nil {
		t.Fatalf("empty regex = (%v, %v), want nil, nil", expression, err)
	}
}

// cwDepsGit runs one git command in a scratch repository.
func cwDepsGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	if output, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
