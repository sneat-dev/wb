package depsrun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/testenv"
)

func initTestRepository(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("git", "-C", path, "init", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("init %s: %v\n%s", path, err, output)
	}
	return path
}

func cwCovFakeGH(t *testing.T, user string, orgs []string, remoteReposJSON string) {
	t.Helper()
	binDir := t.TempDir()
	orgsJSON, err := json.Marshal(cwCovOrgLogins(orgs))
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "api" ]; then
  case "$2" in
    user)
      printf 'HTTP/2 200 OK\n\n{"login":"%s"}\n'
      exit 0
      ;;
    user/orgs)
      printf 'HTTP/2 200 OK\n\n%s\n'
      exit 0
      ;;
    *)
      printf '{"total_count":0,"items":[]}\n'
      exit 0
      ;;
  esac
fi
if [ "$1" = "repo" ] && [ "$2" = "list" ]; then
  printf '%%s\n' '%s'
  exit 0
fi
printf '{"total_count":0,"items":[]}\n'
exit 0
`, user, string(orgsJSON), remoteReposJSON)
	if err := testenv.WriteExecutableFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("WB_HOME", t.TempDir())
}

func cwCovOrgLogins(orgs []string) []map[string]string {
	out := make([]map[string]string, 0, len(orgs))
	for _, org := range orgs {
		out = append(out, map[string]string{"login": org})
	}
	return out
}

func cwDepsGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-C", dir}, args...)
	if output, err := exec.Command("git", full...).CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

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

func TestCwDepsDependencyRepositoriesSelectsLocallyAndOverFleet(t *testing.T) {
	root := t.TempDir()
	service := New(DefaultDependencies(io.Discard))
	app := initTestRepository(t, filepath.Join(root, "acme", "app"))
	cwDepsGit(t, app, "remote", "add", "origin", "git@github.com:acme/app.git")
	initTestRepository(t, filepath.Join(root, "acme", "other"))
	cwCovFakeGH(t, "cwcov-user", []string{"acme"}, `[]`)

	// Guardrails are checked before any discovery happens.
	if _, err := service.Select(context.Background(), Selection{ProjectsRoot: root, Parallel: 0}); err == nil ||
		!strings.Contains(err.Error(), "parallelism must be at least 1") {
		t.Fatalf("parallel guard = %v", err)
	}
	if _, err := service.Select(context.Background(), Selection{ProjectsRoot: root, Parallel: 1, Retry: -1}); err == nil ||
		!strings.Contains(err.Error(), "retry count must not be negative") {
		t.Fatalf("retry guard = %v", err)
	}
	if _, err := service.Select(context.Background(), Selection{ProjectsRoot: root, Parallel: 1, Timeout: -1}); err == nil ||
		!strings.Contains(err.Error(), "timeout must not be negative") {
		t.Fatalf("timeout guard = %v", err)
	}
	if _, err := service.Select(context.Background(), Selection{ProjectsRoot: root, Parallel: 1, Regex: "("}); err == nil ||
		!strings.Contains(err.Error(), "invalid --regex") {
		t.Fatalf("regex guard = %v", err)
	}
	if _, err := service.Select(context.Background(), Selection{ProjectsRoot: root, Parallel: 1, Match: "["}); err == nil ||
		!strings.Contains(err.Error(), "invalid --match") {
		t.Fatalf("match guard = %v", err)
	}

	// Single-repository mode: the path is the third argument.
	repositories, err := service.Select(context.Background(), Selection{ProjectsRoot: root, Parallel: 1, RepositoryPath: app})
	if err != nil || len(repositories) != 1 || repositories[0].Slug != "acme/app" || repositories[0].Path != app {
		t.Fatalf("single repository selection = %+v, %v", repositories, err)
	}
	// A --match that excludes the only candidate is refused, not reported as
	// an empty successful run.
	if _, err := service.Select(context.Background(), Selection{ProjectsRoot: root, Parallel: 1, Match: "beta/*", RepositoryPath: app}); err == nil ||
		!strings.Contains(err.Error(), "does not match selected filters") {
		t.Fatalf("unmatched single repository = %v", err)
	}
	// --filter is applied to the identity too.
	_, err = service.Select(context.Background(), Selection{Filter: "nothing-matches", Parallel: 1, RepositoryPath: app})
	if err == nil || !strings.Contains(err.Error(), "does not match --filter") {
		t.Fatalf("filtered single repository = %v", err)
	}

	// Fleet mode reconciles local clones with the owned repositories.
	repositories, err = service.Select(context.Background(), Selection{ProjectsRoot: root, Parallel: 1, Fleet: true, Timeout: 0})
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
	if _, err := service.Select(context.Background(), Selection{ProjectsRoot: root, Parallel: 1, Fleet: true, Match: "nothing/*"}); err == nil ||
		!strings.Contains(err.Error(), "no repositories match the selected fleet filters") {
		t.Fatalf("empty fleet selection = %v", err)
	}
}

func TestExecuteDepsBumpWithRegistryPolicyNoEventsReturnsEmptyReport(t *testing.T) {
	root := t.TempDir()
	result, err := New(DefaultDependencies(io.Discard)).Bump(context.Background(), BumpRequest{ProjectsRoot: root, Options: deps.BumpOptions{Ecosystem: deps.EcosystemGo, Options: deps.Options{GitHubDir: root}}})
	report, reportDirectory := result.Report, result.ReportDir
	if err == nil || !strings.Contains(err.Error(), "at least one --changed module@version event is required") {
		t.Fatalf("executeDepsBumpWithRegistryPolicy with no events: err = %v, want the missing-events refusal", err)
	}
	if report.Operation != "" {
		t.Fatalf("report.Operation = %q, want empty since RunBump never produced a report", report.Operation)
	}
	if !strings.Contains(reportDirectory, "deps-bump") {
		t.Fatalf("reportDirectory = %q, want it under a deps-bump-* home directory", reportDirectory)
	}
}

func TestCwDepsExecuteDepsBumpWithoutCampaign(t *testing.T) {
	home := t.TempDir()
	service := New(DefaultDependencies(io.Discard))
	reportDir := filepath.Join(home, "reports", "cw-deps-bump")

	events := []deps.ReleaseEvent{{Dependency: "github.com/acme/lib", Version: "v1.2.3", Source: "explicit"}}

	result, err := service.Bump(context.Background(), BumpRequest{ReportDir: reportDir, Events: events, Options: deps.BumpOptions{Ecosystem: deps.EcosystemGo, MaxWaves: 1, Options: deps.Options{ReportDir: reportDir, GitHubDir: t.TempDir(), DryRun: true, Parallel: 1}}})
	report, resolvedDir := result.Report, result.ReportDir
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
	result, err = service.Bump(context.Background(), BumpRequest{ReportDir: filepath.Join(home, "reports", "cw-deps-absent"), Resume: true, Events: events, Options: deps.BumpOptions{Ecosystem: deps.EcosystemGo, MaxWaves: 1, Options: deps.Options{ReportDir: filepath.Join(home, "reports", "cw-deps-absent"), Resume: true, GitHubDir: t.TempDir(), DryRun: true, Parallel: 1}}})
	resumeDir := result.ReportDir
	if err == nil || !strings.Contains(err.Error(), "--resume requires") {
		t.Fatalf("resume without a report error = %v (dir %s)", err, resumeDir)
	}
}

func TestExecuteDepsBumpResumeHonorsExplicitParallelAndRetainsPersistedParallel(t *testing.T) {
	root := t.TempDir()
	service := New(DefaultDependencies(io.Discard))
	reportDir := filepath.Join(root, "report")
	githubDir := filepath.Join(root, "projects")
	events := []deps.ReleaseEvent{{Dependency: "@acme/provider", Version: "0.2.0", Source: "explicit", CheckedAt: time.Unix(1, 0)}}
	persisted := deps.BumpReport{
		SchemaVersion: 1, Operation: deps.BumpOperationIDFor(deps.EcosystemNPM, events), Status: "completed", Ecosystem: deps.EcosystemNPM,
		SeedEvents: events, BaseRef: "main", Parallel: 1,
		Waves: []deps.BumpWaveReport{{Index: 1, Status: "planned", Events: events, Repositories: []deps.RepositoryReport{{Repository: "acme/consumer", Status: "planned"}}}},
	}
	if err := deps.WriteBumpReports(reportDir, persisted); err != nil {
		t.Fatalf("write initial report: %v", err)
	}
	loaded, err := deps.LoadBumpReport(reportDir)
	if err != nil || loaded.Parallel != 1 {
		t.Fatalf("load initial report: report=%+v err=%v", persisted, err)
	}

	result, err := service.Bump(context.Background(), BumpRequest{ReportDir: reportDir, Resume: true, Events: events, ResumeParallelExplicit: true, Options: deps.BumpOptions{Ecosystem: deps.EcosystemNPM, Options: deps.Options{GitHubDir: githubDir, Ref: "main", Parallel: 2, Resume: true, ReportDir: reportDir}}})
	explicit, returnedReportDir := result.Report, result.ReportDir
	if err != nil {
		t.Fatalf("execute explicit resume: %v", err)
	}
	if returnedReportDir != reportDir || explicit.Parallel != 2 {
		t.Fatalf("explicit execution report directory/parallel = %q/%d, want %q/2", returnedReportDir, explicit.Parallel, reportDir)
	}
	if !reflect.DeepEqual(loaded.Waves, explicit.Waves) {
		t.Fatalf("explicit parallelism changed consumer waves: before=%+v after=%+v", loaded.Waves, explicit.Waves)
	}
	if persisted, err := deps.LoadBumpReport(reportDir); err != nil || persisted.Parallel != 2 {
		t.Fatalf("load explicit resume report: report=%+v err=%v", persisted, err)
	}

	result, err = service.Bump(context.Background(), BumpRequest{ReportDir: reportDir, Resume: true, Events: events, Options: deps.BumpOptions{Ecosystem: deps.EcosystemNPM, Options: deps.Options{GitHubDir: githubDir, Ref: "main", Parallel: 1, Resume: true, ReportDir: reportDir}}})
	omitted, returnedReportDir := result.Report, result.ReportDir
	if err != nil {
		t.Fatalf("execute omitted resume: %v", err)
	}
	if returnedReportDir != reportDir || omitted.Parallel != 2 {
		t.Fatalf("omitted execution report directory/parallel = %q/%d, want %q/2", returnedReportDir, omitted.Parallel, reportDir)
	}
	if !reflect.DeepEqual(explicit.Waves, omitted.Waves) {
		t.Fatalf("omitted parallelism changed consumer waves: before=%+v after=%+v", explicit.Waves, omitted.Waves)
	}
	if persisted, err := deps.LoadBumpReport(reportDir); err != nil || persisted.Parallel != 2 {
		t.Fatalf("load omitted resume report: report=%+v err=%v", persisted, err)
	}
}
func TestRunDepsBumpEnsureRootFailureFinishesCampaignAsFailedPersistence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(root, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := New(DefaultDependencies(io.Discard)).Bump(context.Background(), BumpRequest{ProjectsRoot: filepath.Join(root, "child"), Options: deps.BumpOptions{Ecosystem: deps.EcosystemGo, Options: deps.Options{GitHubDir: filepath.Join(root, "child")}}})
	if err == nil || result.Report.Operation != "" {
		t.Fatalf("blocked home result=%+v err=%v", result, err)
	}
}

func TestCwDepsBumpSeedEventsParsesExplicitAndDerivesFromRegistry(t *testing.T) {
	service := New(DefaultDependencies(io.Discard))

	// No --latest: the explicit list is parsed, and an empty list is refused
	// rather than silently running a campaign that propagates nothing.
	events, err := seedEvents(service, []string{"github.com/acme/lib@v1.2.3"})
	if err != nil || len(events) != 1 || events[0].Dependency != "github.com/acme/lib" || events[0].Version != "v1.2.3" {
		t.Fatalf("explicit seed events = %+v, %v", events, err)
	}
	if events[0].Source != "explicit" {
		t.Errorf("explicit event source = %q", events[0].Source)
	}
	if _, err := seedEvents(service, nil); err == nil ||
		!strings.Contains(err.Error(), "at least one --changed") {
		t.Fatalf("empty seed events error = %v", err)
	}
	if _, err := seedEvents(service, []string{"not-a-target"}); err == nil {
		t.Fatal("a malformed --changed event must be refused")
	}

	if _, err := service.Seed(context.Background(), SeedRequest{Ecosystem: deps.EcosystemGo, Changed: []string{"github.com/acme/lib@v2.0.0"}, Latest: true, Scopes: []string{"github.com/acme/*"}, Options: deps.BumpOptions{Options: deps.Options{GitHubDir: t.TempDir(), DryRun: true}}}); err == nil || !strings.Contains(err.Error(), "nothing to derive") {
		t.Fatalf("latest empty selection: %v", err)
	}
}

func seedEvents(service *Service, changed []string) ([]deps.ReleaseEvent, error) {
	result, err := service.Seed(context.Background(), SeedRequest{Ecosystem: deps.EcosystemGo, Changed: changed})
	return result.Events, err
}

func TestDefaultFleetDiscoveryKeepsTheActualBlockedLayoutFailure(t *testing.T) {
	// Native OS observation, not a fabricated discovery result. Auth commands are
	// local private executables; no provider request or user configuration is used.
	root := t.TempDir()
	cwCovFakeGH(t, "private-user", nil, `[]`)
	blocking := filepath.Join(root, "file")
	if err := os.WriteFile(blocking, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := DefaultDependencies(io.Discard).Fleet(filepath.Join(blocking, "child"), "", nil); err == nil {
		t.Fatal("non-directory scan accepted")
	}
}
