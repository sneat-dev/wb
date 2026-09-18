package deps

// This file augments statement coverage for the dependency-runner runtime
// surfaces: the exact-set runner projections, the command environment and
// retry/timeout seams, the Go directive assessor/applyer, the peers evidence
// reader, the Nx version-plan writer, and the latest-scope derivation.
//
// Every helper here is prefixed with depsCovRuntime so concurrent coverage
// work in this package cannot collide with it.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/quality"
)

// depsCovRuntimeAdapter is a scripted deps adapter used to drive
// exactSetHandler without touching a real ecosystem.
type depsCovRuntimeAdapter struct {
	depsCovRuntimeInspectDecisions []Decision
	depsCovRuntimeInspectErr       error
	depsCovRuntimeApplyDecisions   []Decision
	depsCovRuntimeApplyErr         error
}

func (adapter depsCovRuntimeAdapter) inspect(context.Context, string, string, Target, Options) ([]Decision, error) {
	return adapter.depsCovRuntimeInspectDecisions, adapter.depsCovRuntimeInspectErr
}

// inspectWorkingTree replays the same scripted inspection as inspect. These
// runtime tests drive exactSetHandler, which chooses between the two by route
// and does not vary the decisions it expects back, so a second script would add
// a knob no test here sets and could drift out of step with the first.
func (adapter depsCovRuntimeAdapter) inspectWorkingTree(context.Context, string, Target, Options) ([]Decision, error) {
	return adapter.depsCovRuntimeInspectDecisions, adapter.depsCovRuntimeInspectErr
}

func (adapter depsCovRuntimeAdapter) apply(context.Context, string, Target, Options) ([]Decision, error) {
	return adapter.depsCovRuntimeApplyDecisions, adapter.depsCovRuntimeApplyErr
}

// depsCovRuntimeRequireShell skips the runtime tests that need an executable
// POSIX shell shim on Windows, where such a script cannot be launched. The
// production code under test is platform-neutral; only the fake executable is
// not. These skips are reported in the handoff.
func depsCovRuntimeRequireShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell executable shim is unavailable on windows")
	}
}

// depsCovRuntimeWriteExecutable writes a /bin/sh script and returns its path.
func depsCovRuntimeWriteExecutable(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// depsCovRuntimePrependPath puts dir at the front of PATH for one test.
func depsCovRuntimePrependPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// depsCovRuntimeFakeGo installs a fake `go` that answers one scripted way.
func depsCovRuntimeFakeGo(t *testing.T, body string) {
	t.Helper()
	depsCovRuntimeRequireShell(t)
	dir := t.TempDir()
	depsCovRuntimeWriteExecutable(t, dir, "go", body)
	depsCovRuntimePrependPath(t, dir)
}

// depsCovRuntimeFakePnpm installs a fake `pnpm` that answers one scripted way.
func depsCovRuntimeFakePnpm(t *testing.T, body string) {
	t.Helper()
	depsCovRuntimeRequireShell(t)
	dir := t.TempDir()
	depsCovRuntimeWriteExecutable(t, dir, "pnpm", body)
	depsCovRuntimePrependPath(t, dir)
}

// depsCovRuntimeDirectiveModule writes a minimal go.mod the fake `go` can be
// asked about: current directive 1.27.0, no require, no replace.
func depsCovRuntimeDirectiveModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.27.0\n")
	return dir
}

// depsCovRuntimeFakeGoList returns a fake `go` whose `list` prints entries and
// whose `mod` subcommands exit zero without touching the working tree.
func depsCovRuntimeFakeGoList(entries string) string {
	return `if [ "$1" = "list" ]; then printf '%s' '` + entries + `'; exit 0; fi
if [ "$1" = "mod" ]; then exit 0; fi
exit 0
`
}

// ---------------------------------------------------------------------------
// runner.go
// ---------------------------------------------------------------------------

func TestDepsCovRuntimeAdapterForCoversEveryEcosystem(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ecosystem Ecosystem
		want      string
	}{
		{EcosystemGitHubActions, "deps.githubActionsAdapter"},
		{EcosystemGo, "deps.goAdapter"},
		{EcosystemNPM, "deps.npmAdapter"},
		{Ecosystem("cargo"), "<nil>"},
	}
	for _, testCase := range cases {
		if got := fmt.Sprintf("%T", adapterFor(testCase.ecosystem)); got != testCase.want {
			t.Fatalf("adapterFor(%q) = %s, want %s", testCase.ecosystem, got, testCase.want)
		}
	}
}

func TestDepsCovRuntimeMutatesModuleGraph(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{name: "no arguments", args: nil, want: false},
		{name: "empty first argument", args: []string{""}, want: false},
		{name: "go get mutates", args: []string{"get", "example.com/mod@v1.2.3"}, want: true},
		{name: "go mod tidy mutates", args: []string{"mod", "tidy"}, want: true},
		{name: "go mod edit mutates", args: []string{"mod", "edit", "-go=1.26.0"}, want: true},
		{name: "go mod download mutates", args: []string{"mod", "download"}, want: true},
		{name: "go mod why is read-only", args: []string{"mod", "why", "example.com/mod"}, want: false},
		{name: "go mod without a subcommand is read-only", args: []string{"mod"}, want: false},
		{name: "go list is read-only", args: []string{"list", "-m", "all"}, want: false},
	}
	for _, testCase := range cases {
		if got := mutatesModuleGraph(testCase.args); got != testCase.want {
			t.Fatalf("%s: mutatesModuleGraph(%q) = %v, want %v", testCase.name, testCase.args, got, testCase.want)
		}
	}
}

func TestDepsCovRuntimeContainsString(t *testing.T) {
	t.Parallel()
	if !containsString([]string{"alpha", "beta"}, "beta") {
		t.Fatal("containsString must find a present value")
	}
	if containsString([]string{"alpha", "beta"}, "gamma") {
		t.Fatal("containsString must not find an absent value")
	}
	if containsString(nil, "alpha") {
		t.Fatal("containsString must not find anything in an empty slice")
	}
}

func TestDepsCovRuntimeGoCommandEnvironmentSkipsMalformedAndAddsMissingNames(t *testing.T) {
	t.Parallel()
	environment := goCommandEnvironment(
		[]string{"PATH=/bin", "MALFORMED", "PATH=/other"},
		[]string{"example.org/private"},
	)
	values := environmentValues(environment)
	if got := values["PATH"]; got != "/other" {
		t.Fatalf("PATH = %q, want the last occurrence /other", got)
	}
	for _, name := range []string{"GOPRIVATE", "GONOPROXY", "GONOSUMDB"} {
		if got := values[name]; got != "example.org/private" {
			t.Fatalf("%s = %q, want example.org/private", name, got)
		}
	}
	if len(environment) != 4 {
		t.Fatalf("environment = %q, want one PATH plus the three privacy names", environment)
	}
	if !strings.HasPrefix(environment[0], "PATH=") {
		t.Fatalf("environment = %q, want PATH first", environment)
	}
}

func TestDepsCovRuntimeRunCommandRetriesTransientFailure(t *testing.T) {
	depsCovRuntimeRequireShell(t)
	dir := t.TempDir()
	counter := filepath.Join(dir, "attempts")
	script := depsCovRuntimeWriteExecutable(t, dir, "depsCovRuntimeFlaky", `
counter="`+counter+`"
attempt=0
if [ -f "$counter" ]; then attempt=$(cat "$counter"); fi
attempt=$((attempt + 1))
printf '%s' "$attempt" > "$counter"
if [ "$attempt" -lt 2 ]; then
  printf 'transient failure'
  exit 7
fi
printf 'recovered on attempt %s' "$attempt"
`)
	output, attempts, err := runCommandWithEnv(context.Background(), 0, 1, dir, nil, script)
	if err != nil {
		t.Fatalf("runCommandWithEnv retry error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (one retry)", attempts)
	}
	if output != "recovered on attempt 2" {
		t.Fatalf("output = %q, want the second attempt's output", output)
	}
}

func TestDepsCovRuntimeRunCommandReportsTimeout(t *testing.T) {
	depsCovRuntimeRequireShell(t)
	dir := t.TempDir()
	// `exec` replaces the shell with sleep, so the context kill reaches the
	// process actually holding the output pipe and the call returns promptly.
	script := depsCovRuntimeWriteExecutable(t, dir, "depsCovRuntimeSlow", "exec sleep 30\n")
	started := time.Now()
	_, attempts, err := runCommandWithEnv(context.Background(), 200*time.Millisecond, 0, dir, nil, script)
	if err == nil || !strings.Contains(err.Error(), "timed out after") {
		t.Fatalf("error = %v, want a timeout report", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Fatalf("runCommandWithEnv waited %s on a timed-out command", elapsed)
	}
}

func TestDepsCovRuntimeNormalizeValidationModeBranches(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		options    Options
		wantMode   ValidationMode
		wantVerify bool
		wantErr    string
	}{
		{name: "default legacy none", options: Options{}, wantMode: ValidationModeNone},
		{name: "verify implies full", options: Options{Verify: true}, wantMode: ValidationModeFull, wantVerify: true},
		{name: "full implies verify", options: Options{ValidationMode: ValidationModeFull}, wantMode: ValidationModeFull, wantVerify: true},
		{name: "none clears verify", options: Options{ValidationMode: ValidationModeNone, Verify: true}, wantMode: ValidationModeNone},
		{name: "fast with pr", options: Options{ValidationMode: ValidationModeFast, PR: true}, wantMode: ValidationModeFast},
		{name: "fast with merge", options: Options{ValidationMode: ValidationModeFast, Merge: true}, wantMode: ValidationModeFast},
		{name: "fast with dry run", options: Options{ValidationMode: ValidationModeFast, DryRun: true}, wantMode: ValidationModeFast},
		{
			name: "fast without publication", options: Options{ValidationMode: ValidationModeFast},
			wantErr: "requires --pr or --merge",
		},
		{
			name:    "fast with local checks",
			options: Options{ValidationMode: ValidationModeFast, PR: true, Checks: []quality.Check{quality.CheckLint}},
			wantErr: "cannot be combined with local --checks",
		},
		{
			name:    "unknown mode",
			options: Options{ValidationMode: ValidationMode("reckless")},
			wantErr: `unknown validation mode "reckless" (want full, fast, or the legacy no-verify policy)`,
		},
	}
	for _, testCase := range cases {
		options, err := normalizeValidationMode(testCase.options)
		if testCase.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("%s: error = %v, want %q", testCase.name, err, testCase.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: unexpected error %v", testCase.name, err)
		}
		if options.ValidationMode != testCase.wantMode {
			t.Fatalf("%s: mode = %q, want %q", testCase.name, options.ValidationMode, testCase.wantMode)
		}
		if options.Verify != testCase.wantVerify {
			t.Fatalf("%s: verify = %v, want %v", testCase.name, options.Verify, testCase.wantVerify)
		}
	}
}

func TestDepsCovRuntimeNormalizeOptionsSurfacesErrors(t *testing.T) {
	t.Parallel()
	if _, _, err := normalizeOptions(Options{}, "deps-set-x"); err == nil || !strings.Contains(err.Error(), "GitHub directory is required") {
		t.Fatalf("error = %v, want the orchestration requirement", err)
	}
	if _, _, err := normalizeOptions(Options{GitHubDir: t.TempDir(), ValidationMode: ValidationMode("reckless")}, "deps-set-x"); err == nil || !strings.Contains(err.Error(), "unknown validation mode") {
		t.Fatalf("error = %v, want the validation-mode refusal", err)
	}
}

func TestDepsCovRuntimeSortDecisionsOrdersByFileThenDependencyThenBeforeRef(t *testing.T) {
	t.Parallel()
	decisions := []Decision{
		{File: "b/package.json", Dependency: "a", BeforeRef: "1.0.0"},
		{File: "a/package.json", Dependency: "z", BeforeRef: "1.0.0"},
		{File: "a/package.json", Dependency: "a", BeforeRef: "2.0.0"},
		{File: "a/package.json", Dependency: "a", BeforeRef: "1.0.0"},
	}
	sortDecisions(decisions)
	want := []Decision{
		{File: "a/package.json", Dependency: "a", BeforeRef: "1.0.0"},
		{File: "a/package.json", Dependency: "a", BeforeRef: "2.0.0"},
		{File: "a/package.json", Dependency: "z", BeforeRef: "1.0.0"},
		{File: "b/package.json", Dependency: "a", BeforeRef: "1.0.0"},
	}
	if !reflect.DeepEqual(decisions, want) {
		t.Fatalf("sortDecisions = %+v, want %+v", decisions, want)
	}
}

func TestDepsCovRuntimeDependencyLockfileSelector(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		ecosystem  Ecosystem
		lockfile   string
		dependency string
		version    string
		want       string
	}{
		{name: "go names the module itself", ecosystem: EcosystemGo, lockfile: "go.sum", dependency: "example.com/mod", version: "v1.2.3", want: "example.com/mod"},
		{name: "package-lock json pointer", ecosystem: EcosystemNPM, lockfile: "package-lock.json", dependency: "nx", version: "22.7.7", want: "packages|node_modules/nx|version"},
		{name: "pnpm snapshot pointer", ecosystem: EcosystemNPM, lockfile: "pnpm-lock.yaml", dependency: "nx", version: "22.7.7", want: "snapshots|/nx@22.7.7|version"},
		{name: "nested pnpm lockfile uses its base name", ecosystem: EcosystemNPM, lockfile: "sub/pnpm-lock.yaml", dependency: "nx", version: "22.7.7", want: "snapshots|/nx@22.7.7|version"},
		{name: "unsupported yarn grammar is not guessed", ecosystem: EcosystemNPM, lockfile: "yarn.lock", dependency: "nx", version: "22.7.7", want: ""},
	}
	for _, testCase := range cases {
		if got := dependencyLockfileSelector(testCase.ecosystem, testCase.lockfile, testCase.dependency, testCase.version); got != testCase.want {
			t.Fatalf("%s: selector = %q, want %q", testCase.name, got, testCase.want)
		}
	}
}

func TestDepsCovRuntimeLockfileForManifest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		manifest  string
		lockfiles []string
		want      string
	}{
		{name: "root manifest and root lockfile", manifest: "package.json", lockfiles: []string{"package-lock.json"}, want: "package-lock.json"},
		{name: "nested manifest prefers its own directory", manifest: "packages/a/package.json", lockfiles: []string{"package-lock.json", "packages/a/pnpm-lock.yaml", "packages/b/yarn.lock"}, want: "packages/a/pnpm-lock.yaml"},
		{name: "ancestor lockfile governs a deeper manifest", manifest: "packages/a/sub/package.json", lockfiles: []string{"packages/a/pnpm-lock.yaml"}, want: "packages/a/pnpm-lock.yaml"},
		{name: "unrelated directory is skipped", manifest: "packages/a/package.json", lockfiles: []string{"packages/b/pnpm-lock.yaml"}, want: ""},
		{name: "equal directory depth breaks the tie by name", manifest: "packages/a/package.json", lockfiles: []string{"packages/a/pnpm-lock.yaml", "packages/a/package-lock.json"}, want: "packages/a/package-lock.json"},
		{name: "no lockfiles", manifest: "package.json", lockfiles: nil, want: ""},
	}
	for _, testCase := range cases {
		if got := lockfileForManifest(testCase.manifest, testCase.lockfiles); got != testCase.want {
			t.Fatalf("%s: lockfileForManifest(%q, %q) = %q, want %q", testCase.name, testCase.manifest, testCase.lockfiles, got, testCase.want)
		}
	}
}

func TestDepsCovRuntimeDependencyDeltasUseLockfilesAndFallBackToAfterVersion(t *testing.T) {
	t.Parallel()
	result := orchestrate.Result[[]Decision]{
		Repository: "acme/app", Commit: "deadbeef", PR: "https://example.test/pr/1",
		Metadata: []Decision{
			{Dependency: "nx", Ecosystem: EcosystemNPM, File: "package.json", Selector: "dependencies.nx", BeforeRef: "22.6.4", TargetVersion: "22.7.7", AfterRef: "22.7.7", Action: "updated"},
			{Dependency: "nx", Ecosystem: EcosystemNPM, File: "package-lock.json", TargetVersion: "22.7.7", Action: "lockfile_regenerated"},
			{Dependency: "left-pad", Ecosystem: EcosystemNPM, File: "package.json", Selector: "dependencies.left-pad", BeforeRef: "1.0.0", TargetVersion: "1.1.0", AfterVersion: "1.1.0", Action: "updated"},
			{Dependency: "ignored", Ecosystem: EcosystemNPM, File: "package.json", Action: "unchanged"},
		},
	}
	deltas := dependencyDeltasFromResult(result)
	if len(deltas) != 2 {
		t.Fatalf("deltas = %+v, want one per referenced decision", deltas)
	}
	// Sorted by manifest then selector: dependencies.left-pad precedes dependencies.nx.
	if deltas[0].Package != "left-pad" || deltas[0].CandidateAfter != "1.1.0" {
		t.Fatalf("deltas[0] = %+v, want the AfterVersion fallback", deltas[0])
	}
	if deltas[0].Lockfile != "package-lock.json" || deltas[0].LockfileSelector != "packages|node_modules/left-pad|version" || deltas[0].LockfileVersion != "1.1.0" {
		t.Fatalf("deltas[0] lockfile evidence = %+v", deltas[0])
	}
	if deltas[0].SourcePR != "https://example.test/pr/1" || deltas[0].SourceHead != "deadbeef" || deltas[0].Consumer != "acme/app" {
		t.Fatalf("deltas[0] provenance = %+v", deltas[0])
	}
	if deltas[1].Package != "nx" || deltas[1].CandidateAfter != "22.7.7" || deltas[1].LockfileSelector != "packages|node_modules/nx|version" {
		t.Fatalf("deltas[1] = %+v", deltas[1])
	}
}

func TestDepsCovRuntimeDependencyDeltasSortByEveryTiebreakKey(t *testing.T) {
	t.Parallel()
	makeDecision := func(file, selector, dependency, requested string) Decision {
		return Decision{
			Dependency: dependency, Ecosystem: EcosystemGo, File: file, Selector: selector,
			BeforeRef: "v1.0.0", TargetVersion: requested, AfterRef: requested, Action: "updated",
		}
	}
	result := orchestrate.Result[[]Decision]{
		Repository: "acme/app",
		Metadata: []Decision{
			makeDecision("a/pkg.json", "a", "a", "v1.0.0"),
			makeDecision("a/pkg.json", "a", "b", "v1.0.0"),
			makeDecision("a/pkg.json", "b", "a", "v1.0.0"),
			makeDecision("b/pkg.json", "a", "a", "v1.0.0"),
			makeDecision("a/pkg.json", "a", "a", "v2.0.0"),
		},
	}
	deltas := dependencyDeltasFromResult(result)
	got := make([]string, 0, len(deltas))
	for _, delta := range deltas {
		got = append(got, delta.Manifest+"/"+delta.Selector+"/"+delta.Package+"/"+delta.RequestedAfter)
	}
	want := []string{
		"a/pkg.json/a/a/v1.0.0",
		"a/pkg.json/a/a/v2.0.0",
		"a/pkg.json/a/b/v1.0.0",
		"a/pkg.json/b/a/v1.0.0",
		"b/pkg.json/a/a/v1.0.0",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("delta order = %q, want %q", got, want)
	}
}

func TestDepsCovRuntimeRepositoryReportFromResultProjectsEveryField(t *testing.T) {
	t.Parallel()
	result := orchestrate.Result[[]Decision]{
		Repository: "acme/app", CanonicalDir: "/canonical", WorktreeDir: "/worktree",
		Branch: "wb/deps-set-x", Ref: "main", Status: "changed", Reason: "needs exact target",
		Metadata: []Decision{
			{Dependency: "b", Ecosystem: EcosystemNPM, File: "b/package.json", Selector: "dependencies.b", BeforeRef: "1.0.0", TargetVersion: "2.0.0", AfterRef: "2.0.0", Action: "updated"},
			{Dependency: "a", Ecosystem: EcosystemNPM, File: "a/package.json", Selector: "dependencies.a", BeforeRef: "1.0.0", TargetVersion: "2.0.0", AfterRef: "2.0.0", Action: "updated"},
		},
		ChangedFiles:  []string{"z.txt", "a.txt"},
		Verifications: []quality.VerificationEntry{{Language: "go", Check: quality.CheckTest, Status: quality.StatusPassed, Detail: "ok"}},
		Commit:        "abc123", Pushed: true, PR: "https://example.test/pr/7",
		Checks: []orchestrate.RemoteCheck{{Name: "ci/test", Bucket: "pass", Link: "https://example.test/check/1"}},
		Merged: true, Held: true,
	}
	report := RepositoryReportFromResult(result)
	if report.Repository != "acme/app" || report.CanonicalDir != "/canonical" || report.WorktreeDir != "/worktree" {
		t.Fatalf("identity projection = %+v", report)
	}
	if report.Branch != "wb/deps-set-x" || report.Ref != "main" || report.Status != "changed" || report.Reason != "needs exact target" {
		t.Fatalf("lifecycle projection = %+v", report)
	}
	if report.Commit != "abc123" || !report.Pushed || report.PR != "https://example.test/pr/7" || !report.Merged || !report.Held {
		t.Fatalf("publication projection = %+v", report)
	}
	if len(report.Verifications) != 1 || report.Verifications[0].Language != "go" {
		t.Fatalf("verification projection = %+v", report.Verifications)
	}
	if !reflect.DeepEqual(report.ChangedFiles, []string{"a.txt", "z.txt"}) {
		t.Fatalf("changed files = %q, want sorted", report.ChangedFiles)
	}
	if len(report.Decisions) != 2 || report.Decisions[0].File != "a/package.json" {
		t.Fatalf("decisions = %+v, want sorted by file", report.Decisions)
	}
	if len(report.DependencyDeltas) != 2 || report.DependencyDeltas[0].Package != "a" {
		t.Fatalf("dependency deltas = %+v", report.DependencyDeltas)
	}
	if len(report.Checks) != 1 || report.Checks[0].Name != "ci/test" || report.Checks[0].Bucket != "pass" || report.Checks[0].Link != "https://example.test/check/1" {
		t.Fatalf("checks = %+v", report.Checks)
	}
	if !reflect.DeepEqual(report, repositoryReportFromResult(result)) {
		t.Fatalf("RepositoryReportFromResult diverged from repositoryReportFromResult")
	}
}

func TestDepsCovRuntimeExactSetHandlerInspectBranches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := orchestrate.Repository{}
	handler := func(adapter depsCovRuntimeAdapter) exactSetHandler {
		return exactSetHandler{adapter: adapter, target: Target{Ecosystem: EcosystemNPM, Dependency: "@acme/core", Version: "1.0.0"}}
	}

	boom := errors.New("inspect boom")
	assessment, err := handler(depsCovRuntimeAdapter{depsCovRuntimeInspectErr: boom}).Inspect(ctx, "/canonical", "main", repository)
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the adapter failure", err)
	}
	if len(assessment.Metadata) != 0 {
		t.Fatalf("assessment = %+v, want no decisions on failure", assessment)
	}

	assessment, err = handler(depsCovRuntimeAdapter{}).Inspect(ctx, "/canonical", "main", repository)
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Applicable || assessment.NeedsChange || assessment.Reason != "dependency absent on main" {
		t.Fatalf("absent assessment = %+v", assessment)
	}

	assessment, err = handler(depsCovRuntimeAdapter{depsCovRuntimeInspectDecisions: []Decision{{Action: "unchanged"}}}).Inspect(ctx, "/canonical", "main", repository)
	if err != nil {
		t.Fatal(err)
	}
	if !assessment.Applicable || assessment.NeedsChange || assessment.Reason != "all existing references are already at the exact target" {
		t.Fatalf("unchanged assessment = %+v", assessment)
	}

	decisions := []Decision{{Action: "unchanged"}, {Action: "updated"}}
	assessment, err = handler(depsCovRuntimeAdapter{depsCovRuntimeInspectDecisions: decisions}).Inspect(ctx, "/canonical", "main", repository)
	if err != nil {
		t.Fatal(err)
	}
	if !assessment.Applicable || !assessment.NeedsChange || assessment.Reason != "existing references require the exact target" {
		t.Fatalf("changed assessment = %+v", assessment)
	}
	if !reflect.DeepEqual(assessment.Metadata, decisions) {
		t.Fatalf("metadata = %+v, want the adapter decisions", assessment.Metadata)
	}
}

func TestDepsCovRuntimeExactSetHandlerValidatePublishable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	npm := exactSetHandler{target: Target{Ecosystem: EcosystemNPM}}
	if err := npm.ValidatePublishable(ctx, t.TempDir(), orchestrate.Repository{}); err != nil {
		t.Fatalf("a non-Go ecosystem must not be inspected as Go: %v", err)
	}
	goDir := t.TempDir()
	writeTestFile(t, filepath.Join(goDir, "go.mod"), "module example.com/app\n\ngo 1.24\n\nrequire example.com/model v0.2.0\n\nreplace example.com/model => ../model\n")
	goHandler := exactSetHandler{target: Target{Ecosystem: EcosystemGo}}
	if err := goHandler.ValidatePublishable(ctx, goDir, orchestrate.Repository{}); err == nil || !strings.Contains(err.Error(), "example.com/model => ../model") {
		t.Fatalf("error = %v, want the unpublishable local replace", err)
	}
}

func TestDepsCovRuntimeExactSetHandlerPullRequestBodyPerValidationMode(t *testing.T) {
	t.Parallel()
	target := Target{Ecosystem: EcosystemNPM, Dependency: "@acme/core", Version: "2.0.0"}
	cases := []struct {
		name    string
		options Options
		want    string
	}{
		{name: "default full validation", options: Options{}, want: "Full local lint, test, and build verification completed"},
		{name: "explicit full validation", options: Options{ValidationMode: ValidationModeFull}, want: "Full local lint, test, and build verification completed"},
		{name: "fast with merge", options: Options{ValidationMode: ValidationModeFast, Merge: true}, want: "before merge"},
		{name: "fast without merge", options: Options{ValidationMode: ValidationModeFast}, want: "merge remains an explicit follow-up"},
		{name: "legacy none", options: Options{ValidationMode: ValidationModeNone}, want: "legacy no-verify policy"},
	}
	for _, testCase := range cases {
		title, body := exactSetHandler{target: target, options: testCase.options}.PullRequest(orchestrate.Repository{})
		if title != "chore(deps): set @acme/core to 2.0.0" {
			t.Fatalf("%s: title = %q", testCase.name, title)
		}
		if !strings.Contains(body, testCase.want) {
			t.Fatalf("%s: body = %q, want %q", testCase.name, body, testCase.want)
		}
	}
}

func TestDepsCovRuntimeRunRejectsUnsupportedEcosystem(t *testing.T) {
	t.Parallel()
	_, err := Run(context.Background(),
		Target{Ecosystem: Ecosystem("cargo"), Dependency: "serde", Version: "1.0.0"},
		nil, Options{GitHubDir: t.TempDir(), DryRun: true})
	if err == nil || !strings.Contains(err.Error(), `unsupported dependency ecosystem "cargo"`) {
		t.Fatalf("error = %v, want an unsupported-ecosystem refusal", err)
	}
}

func TestDepsCovRuntimeRunSurfacesOptionAndRefErrors(t *testing.T) {
	t.Parallel()
	if _, err := Run(context.Background(), Target{Ecosystem: EcosystemGo, Dependency: "example.com/mod", Version: "v1.0.0"}, nil, Options{}); err == nil || !strings.Contains(err.Error(), "GitHub directory is required") {
		t.Fatalf("error = %v, want the normalized-options refusal", err)
	}
	want := errors.New("ref lookup failed")
	_, err := Run(context.Background(),
		Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd", Version: "v1.1.0"}, nil,
		Options{
			GitHubDir: t.TempDir(), DryRun: true,
			ResolveGitHubRef: func(context.Context, string, string) (string, error) { return "", want },
		})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want the ref resolution failure", err)
	}
}

func TestDepsCovRuntimeRunRecordsVerificationAndFailure(t *testing.T) {
	t.Parallel()
	report, err := Run(context.Background(),
		Target{Ecosystem: EcosystemGitHubActions, Dependency: "acme/cicd", Version: "v1.1.0"},
		[]Repository{{Slug: "not-a-repository-slug", Path: filepath.Join(t.TempDir(), "app")}},
		Options{
			GitHubDir: t.TempDir(), DryRun: true, Verify: true, Timeout: time.Minute,
			ResolveGitHubRef: func(context.Context, string, string) (string, error) { return strings.Repeat("a", 40), nil },
		})
	if err == nil {
		t.Fatal("an invalid repository slug must fail the run")
	}
	if report.Status != "failed" {
		t.Fatalf("status = %q, want failed", report.Status)
	}
	if len(report.Verification) == 0 {
		t.Fatal("--verify must be recorded on the report even when the run fails")
	}
	if len(report.Repositories) != 1 || report.Repositories[0].Status != "failed" {
		t.Fatalf("repository report = %+v", report.Repositories)
	}
}

// ---------------------------------------------------------------------------
// go_directive.go
// ---------------------------------------------------------------------------

func TestDepsCovRuntimeGoSyntaxPrefixesBareVersions(t *testing.T) {
	t.Parallel()
	cases := map[string]string{"1.26.0": "go1.26.0", "go1.27.0": "go1.27.0", "": "go"}
	for input, want := range cases {
		if got := goSyntax(input); got != want {
			t.Fatalf("goSyntax(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestDepsCovRuntimeAssessDirectiveInputErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	policy := fleetPolicy()

	missing := filepath.Join(t.TempDir(), "missing")
	assessment, err := AssessDirective(ctx, missing, policy, Options{Timeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "read") || assessment.ModuleDir != missing {
		t.Fatalf("error = %v assessment = %+v, want a read failure naming the module dir", err, assessment)
	}

	unparseable := t.TempDir()
	writeTestFile(t, filepath.Join(unparseable, "go.mod"), "this is not a go.mod\n")
	if _, err := AssessDirective(ctx, unparseable, policy, Options{Timeout: time.Minute}); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("error = %v, want a parse failure", err)
	}

	nameless := t.TempDir()
	writeTestFile(t, filepath.Join(nameless, "go.mod"), "go 1.27.0\n")
	if _, err := AssessDirective(ctx, nameless, policy, Options{Timeout: time.Minute}); err == nil || !strings.Contains(err.Error(), "has no module path") {
		t.Fatalf("error = %v, want a missing-module-path failure", err)
	}
}

func TestDepsCovRuntimeResolveModuleGraphReportsGoListFailure(t *testing.T) {
	depsCovRuntimeFakeGo(t, "printf 'go list exploded' >&2\nexit 1\n")
	dir := depsCovRuntimeDirectiveModule(t)
	if _, err := resolveModuleGraph(context.Background(), dir, Options{Timeout: time.Minute}); err == nil || !strings.Contains(err.Error(), "go list") {
		t.Fatalf("error = %v, want the go list failure", err)
	}
}

func TestDepsCovRuntimeAssessDirectiveReportsUnresolvableGraph(t *testing.T) {
	depsCovRuntimeFakeGo(t, "printf 'go list exploded' >&2\nexit 1\n")
	dir := depsCovRuntimeDirectiveModule(t)
	assessment, err := AssessDirective(context.Background(), dir, fleetPolicy(), Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Verdict != DirectiveError || !strings.Contains(assessment.Detail, "resolve module graph") {
		t.Fatalf("assessment = %+v, want a resolve error verdict", assessment)
	}
}

func TestDepsCovRuntimeResolveModuleGraphReportsUndecodableOutput(t *testing.T) {
	depsCovRuntimeFakeGo(t, "printf 'not json at all'\n")
	dir := depsCovRuntimeDirectiveModule(t)
	if _, err := resolveModuleGraph(context.Background(), dir, Options{Timeout: time.Minute}); err == nil || !strings.Contains(err.Error(), "decode go list -m -json output") {
		t.Fatalf("error = %v, want the decode failure", err)
	}
}

func TestDepsCovRuntimeResolveModuleGraphCopiesGoSumAndDecodesEntries(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.27.0\n")
	writeTestFile(t, filepath.Join(dir, "go.sum"), "\n")
	depsCovRuntimeFakeGo(t, `printf '%s' '{"Path":"example.com/a","Version":"v1.0.0","GoVersion":"1.24.0"}
{"Path":"example.com/app","Main":true}'
`)
	entries, err := resolveModuleGraph(context.Background(), dir, Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Path != "example.com/a" || entries[0].GoVersion != "1.24.0" || !entries[1].Main {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestDepsCovRuntimeResolveModuleGraphReportsGoSumReadFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/app\n\ngo 1.27.0\n")
	if err := os.MkdirAll(filepath.Join(dir, "go.sum"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveModuleGraph(context.Background(), dir, Options{Timeout: time.Minute}); err == nil {
		t.Fatal("a go.sum that cannot be read must be an error")
	}
}

func TestDepsCovRuntimeResolveModuleGraphReportsMissingModuleDir(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := resolveModuleGraph(context.Background(), missing, Options{Timeout: time.Minute}); err == nil || !strings.Contains(err.Error(), "go.mod") {
		t.Fatalf("error = %v, want a go.mod read failure", err)
	}
}

func TestDepsCovRuntimeResolveModuleGraphReportsScratchCreationFailure(t *testing.T) {
	// os.TempDir honors TMPDIR on unix; on Windows it does not, and the test
	// would fall through to a real `go list`, so it is gated there.
	if runtime.GOOS == "windows" {
		t.Skip("TMPDIR-based scratch-directory failure is unix-only")
	}
	// Resolve every temp directory before repointing TMPDIR, so neither this
	// test's own fixtures nor the cleanup path depend on the injected value.
	fixture := t.TempDir()
	missing := filepath.Join(fixture, "absent-tmp")
	t.Setenv("TMPDIR", missing)
	if _, err := resolveModuleGraph(context.Background(), fixture, Options{Timeout: time.Minute}); err == nil {
		t.Fatal("a scratch directory that cannot be created must be an error")
	}
}

func TestDepsCovRuntimeAssessDirectiveRaisesTargetWithinLanguageVersion(t *testing.T) {
	depsCovRuntimeFakeGo(t, `printf '%s' '{"Path":"example.com/a","Version":"v1.0.0","GoVersion":"1.25.0"}
{"Path":"example.com/b","Version":"v1.0.0","GoVersion":"1.26.4"}
{"Path":"example.com/c","Version":"v1.0.0","GoVersion":"1.26.4"}'
`)
	dir := depsCovRuntimeDirectiveModule(t)
	assessment, err := AssessDirective(context.Background(), dir, fleetPolicy(), Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Verdict != DirectiveWouldChange {
		t.Fatalf("verdict = %q, want would-change (detail: %s)", assessment.Verdict, assessment.Detail)
	}
	if assessment.Ceiling != "1.26.4" || assessment.TargetGoVersion != "1.26.4" {
		t.Fatalf("ceiling = %q target = %q, want 1.26.4", assessment.Ceiling, assessment.TargetGoVersion)
	}
	if len(assessment.Forcing) != 0 {
		t.Fatalf("forcing = %+v, want nothing reported for an achievable target", assessment.Forcing)
	}
}

func TestDepsCovRuntimeAssessDirectiveSortsTiedForcingDependencies(t *testing.T) {
	depsCovRuntimeFakeGo(t, `printf '%s' '{"Path":"example.com/zeta","Version":"v1.0.0","GoVersion":"1.27.0"}
{"Path":"example.com/alpha","Version":"v1.0.0","GoVersion":"1.27.0"}'
`)
	dir := depsCovRuntimeDirectiveModule(t)
	assessment, err := AssessDirective(context.Background(), dir, fleetPolicy(), Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Verdict != DirectiveCannotComply {
		t.Fatalf("verdict = %q, want cannot-comply (detail: %s)", assessment.Verdict, assessment.Detail)
	}
	if assessment.Ceiling != "1.27.0" {
		t.Fatalf("ceiling = %q, want 1.27.0", assessment.Ceiling)
	}
	if len(assessment.Forcing) != 2 || assessment.Forcing[0].Path != "example.com/alpha" || assessment.Forcing[1].Path != "example.com/zeta" {
		t.Fatalf("forcing = %+v, want the two tied dependencies sorted by path", assessment.Forcing)
	}
	if !strings.Contains(assessment.Detail, "example.com/alpha@v1.0.0 declares go 1.27.0") || !strings.Contains(assessment.Detail, "example.com/zeta@v1.0.0 declares go 1.27.0") {
		t.Fatalf("detail = %q, want both forcing dependencies named", assessment.Detail)
	}
}

func TestDepsCovRuntimeApplyDirectiveSurfacesAssessError(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := ApplyDirective(context.Background(), missing, fleetPolicy(), Options{Timeout: time.Minute}); err == nil {
		t.Fatal("apply must surface an assessment read failure")
	}
}

func TestDepsCovRuntimeApplyDirectiveReportsModEditFailure(t *testing.T) {
	depsCovRuntimeFakeGo(t, `if [ "$1" = "list" ]; then printf '%s' '{"Path":"example.com/a","Version":"v1.0.0","GoVersion":"1.24.0"}'; exit 0; fi
if [ "$1" = "mod" ] && [ "$2" = "edit" ]; then printf 'mod edit refused' >&2; exit 3; fi
exit 0
`)
	dir := depsCovRuntimeDirectiveModule(t)
	_, err := ApplyDirective(context.Background(), dir, fleetPolicy(), Options{Timeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "go mod edit") {
		t.Fatalf("error = %v, want a go mod edit failure", err)
	}
}

func TestDepsCovRuntimeApplyDirectiveReportsTidyFailure(t *testing.T) {
	depsCovRuntimeFakeGo(t, `if [ "$1" = "list" ]; then printf '%s' '{"Path":"example.com/a","Version":"v1.0.0","GoVersion":"1.24.0"}'; exit 0; fi
if [ "$1" = "mod" ] && [ "$2" = "edit" ]; then exit 0; fi
if [ "$1" = "mod" ] && [ "$2" = "tidy" ]; then printf 'tidy refused' >&2; exit 4; fi
exit 0
`)
	dir := depsCovRuntimeDirectiveModule(t)
	_, err := ApplyDirective(context.Background(), dir, fleetPolicy(), Options{Timeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "go mod tidy after edit") {
		t.Fatalf("error = %v, want a go mod tidy failure", err)
	}
}

func TestDepsCovRuntimeApplyDirectiveReportsReassessFailure(t *testing.T) {
	// The fake go mod edit corrupts go.mod, so the re-assessment cannot parse
	// it and ApplyDirective must report the re-assess failure rather than the
	// revert check.
	depsCovRuntimeFakeGo(t, `if [ "$1" = "list" ]; then printf '%s' '{"Path":"example.com/a","Version":"v1.0.0","GoVersion":"1.24.0"}'; exit 0; fi
if [ "$1" = "mod" ] && [ "$2" = "edit" ]; then printf 'this is not a go.mod\n' > go.mod; exit 0; fi
if [ "$1" = "mod" ] && [ "$2" = "tidy" ]; then exit 0; fi
exit 0
`)
	dir := depsCovRuntimeDirectiveModule(t)
	_, err := ApplyDirective(context.Background(), dir, fleetPolicy(), Options{Timeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "re-assess after go mod tidy") {
		t.Fatalf("error = %v, want a re-assess failure", err)
	}
}

func TestDepsCovRuntimeApplyDirectiveDetectsRevertedEdit(t *testing.T) {
	depsCovRuntimeFakeGo(t, depsCovRuntimeFakeGoList(`{"Path":"example.com/a","Version":"v1.0.0","GoVersion":"1.24.0"}`))
	dir := depsCovRuntimeDirectiveModule(t)
	_, err := ApplyDirective(context.Background(), dir, fleetPolicy(), Options{Timeout: time.Minute})
	if err == nil || !strings.Contains(err.Error(), "reverted the edit") {
		t.Fatalf("error = %v, want a reverted-edit failure", err)
	}
}

// ---------------------------------------------------------------------------
// nx_version_plan.go
// ---------------------------------------------------------------------------

func depsCovRuntimeNxWorktree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, body := range files {
		writeTestFile(t, filepath.Join(root, path), body)
	}
	return root
}

func TestDepsCovRuntimeNxVersionPlansEnabledBranches(t *testing.T) {
	t.Parallel()
	missing := t.TempDir()
	if enabled, err := nxVersionPlansEnabled(missing); err != nil || enabled {
		t.Fatalf("missing nx.json: enabled = %v err = %v, want false/nil", enabled, err)
	}

	enabled := depsCovRuntimeNxWorktree(t, map[string]string{"nx.json": `{"release":{"versionPlans":true}}`})
	if got, err := nxVersionPlansEnabled(enabled); err != nil || !got {
		t.Fatalf("enabled nx.json: got = %v err = %v, want true/nil", got, err)
	}

	disabled := depsCovRuntimeNxWorktree(t, map[string]string{"nx.json": `{"release":{"versionPlans":false}}`})
	if got, err := nxVersionPlansEnabled(disabled); err != nil || got {
		t.Fatalf("disabled nx.json: got = %v err = %v, want false/nil", got, err)
	}

	unparseable := depsCovRuntimeNxWorktree(t, map[string]string{"nx.json": "not json"})
	if _, err := nxVersionPlansEnabled(unparseable); err == nil || !strings.Contains(err.Error(), "parse nx.json") {
		t.Fatalf("unparseable nx.json error = %v, want a parse failure", err)
	}

	unreadable := t.TempDir()
	if err := os.MkdirAll(filepath.Join(unreadable, "nx.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := nxVersionPlansEnabled(unreadable); err == nil {
		t.Fatal("an nx.json that cannot be read must be an error")
	}
}

func TestDepsCovRuntimeChangedPublishableNxProjectsBranches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	options := Options{Timeout: time.Minute}
	worktree := depsCovRuntimeNxWorktree(t, map[string]string{
		"packages/a/package.json":           `{"name":"@acme/a"}` + "\n",
		"packages/a/project.json":           `{"name":"a"}` + "\n",
		"packages/private/package.json":     `{"name":"@acme/private","private":true}` + "\n",
		"packages/private/project.json":     `{"name":"private"}` + "\n",
		"packages/nameless/package.json":    `{"private":false}` + "\n",
		"packages/nameless/project.json":    `{"name":"nameless"}` + "\n",
		"packages/projectless/package.json": `{"name":"@acme/projectless"}` + "\n",
	})
	projects, err := changedPublishableNxProjects(ctx, worktree, []Decision{
		{Action: "updated", File: "packages/a/package.json"},
		{Action: "updated", File: "packages/private/package.json"},
		{Action: "updated", File: "packages/nameless/package.json"},
		{Action: "updated", File: "packages/projectless/package.json"},
		{Action: "updated", File: "package.json"},
		{Action: "unchanged", File: "packages/a/package.json"},
	}, options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(projects, []string{"a"}) {
		t.Fatalf("projects = %q, want only the publishable project with a project.json", projects)
	}

	malformedPackage := depsCovRuntimeNxWorktree(t, map[string]string{"packages/bad/package.json": "not json"})
	if _, err := changedPublishableNxProjects(ctx, malformedPackage, []Decision{{Action: "updated", File: "packages/bad/package.json"}}, options); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("error = %v, want a package.json parse failure", err)
	}

	malformedProject := depsCovRuntimeNxWorktree(t, map[string]string{
		"packages/bad/package.json": `{"name":"@acme/bad"}`,
		"packages/bad/project.json": "not json",
	})
	if _, err := changedPublishableNxProjects(ctx, malformedProject, []Decision{{Action: "updated", File: "packages/bad/package.json"}}, options); err == nil || !strings.Contains(err.Error(), "project.json") {
		t.Fatalf("error = %v, want a project.json parse failure", err)
	}

	if _, err := changedPublishableNxProjects(ctx, t.TempDir(), []Decision{{Action: "updated", File: "packages/missing/package.json"}}, options); err == nil {
		t.Fatal("a decision naming a missing manifest must be an error")
	}

	unreadableProject := depsCovRuntimeNxWorktree(t, map[string]string{"packages/x/package.json": `{"name":"@acme/x"}`})
	if err := os.MkdirAll(filepath.Join(unreadableProject, "packages", "x", "project.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := changedPublishableNxProjects(ctx, unreadableProject, []Decision{{Action: "updated", File: "packages/x/package.json"}}, options); err == nil {
		t.Fatal("a project.json that cannot be read must be an error")
	}
}

func TestDepsCovRuntimeChangedPublishableNxProjectsUsesCandidateGitDiff(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "packages/a/package.json"), `{"name":"@acme/a"}`+"\n")
	writeTestFile(t, filepath.Join(dir, "packages/a/project.json"), `{"name":"a"}`+"\n")
	runTestGit(t, dir, "init", "-b", "main")
	runTestGit(t, dir, "config", "user.name", "WB Test")
	runTestGit(t, dir, "config", "user.email", "wb@example.test")
	runTestGit(t, dir, "add", "-A")
	runTestGit(t, dir, "commit", "-m", "initial")
	// An already-applied manifest is durable evidence even when the repeat
	// decision reports it as unchanged.
	writeTestFile(t, filepath.Join(dir, "packages/a/package.json"), `{"name":"@acme/a","version":"0.2.0"}`+"\n")
	projects, err := changedPublishableNxProjects(context.Background(), dir, nil, Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(projects, []string{"a"}) {
		t.Fatalf("projects = %q, want the candidate diff's project", projects)
	}
}

func TestDepsCovRuntimeChangedPublishableNxProjectsSurfacesGitAndLstatErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	options := Options{Timeout: time.Minute}

	notARepository := depsCovRuntimeNxWorktree(t, map[string]string{".git/HEAD": "ref: refs/heads/main\n"})
	if _, err := changedPublishableNxProjects(ctx, notARepository, nil, options); err == nil {
		t.Fatal("a git diff failure must surface")
	}

	file := filepath.Join(t.TempDir(), "not-a-directory")
	writeTestFile(t, file, "x\n")
	if _, err := changedPublishableNxProjects(ctx, file, nil, options); err == nil {
		t.Fatal("an unreadable .git probe must surface")
	}
}

func TestDepsCovRuntimeGenerateNxVersionPlanBranches(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	options := Options{Timeout: time.Minute}
	enabled := depsCovRuntimeNxWorktree(t, map[string]string{
		"nx.json":                 `{"release":{"versionPlans":true}}` + "\n",
		"packages/a/package.json": `{"name":"@acme/a"}` + "\n",
		"packages/a/project.json": `{"name":"a"}` + "\n",
	})
	decisions := []Decision{{Action: "updated", File: "packages/a/package.json"}}

	if err := generateNxVersionPlan(ctx, enabled, "", decisions, options); err != nil {
		t.Fatalf("empty wave id error = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(enabled, ".nx", "version-plans", "wb-.md")); !os.IsNotExist(err) {
		t.Fatal("an empty wave id must not write a plan")
	}

	if err := generateNxVersionPlan(ctx, enabled, "wave-1", []Decision{{Action: "updated", File: "package.json"}}, options); err != nil {
		t.Fatalf("no publishable project error = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(enabled, ".nx", "version-plans", "wb-wave-1.md")); !os.IsNotExist(err) {
		t.Fatal("no publishable project must not write a plan")
	}

	if err := generateNxVersionPlan(ctx, enabled, "wave-2", []Decision{{Action: "updated", File: "packages/missing/package.json"}}, options); err == nil {
		t.Fatal("a manifest read failure must surface")
	}

	disabled := depsCovRuntimeNxWorktree(t, map[string]string{"nx.json": `{"release":{"versionPlans":false}}`})
	if err := generateNxVersionPlan(ctx, disabled, "wave-3", decisions, options); err != nil {
		t.Fatalf("disabled release plans error = %v, want nil", err)
	}

	if err := generateNxVersionPlan(ctx, enabled, "wave-1", decisions, options); err != nil {
		t.Fatal(err)
	}
	planPath := filepath.Join(enabled, ".nx", "version-plans", "wb-wave-1.md")
	contents, err := os.ReadFile(planPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(contents, nxVersionPlanContents([]string{"a"})) {
		t.Fatalf("plan contents = %q", contents)
	}

	if err := generateNxVersionPlan(ctx, enabled, "wave-1", decisions, options); err != nil {
		t.Fatalf("an identical plan must be an idempotent no-op: %v", err)
	}

	writeTestFile(t, planPath, "different\n")
	if err := generateNxVersionPlan(ctx, enabled, "wave-1", decisions, options); err == nil || !strings.Contains(err.Error(), "already exists with different contents") {
		t.Fatalf("error = %v, want a refusal to overwrite a user plan", err)
	}

	blocked := depsCovRuntimeNxWorktree(t, map[string]string{
		"nx.json":                 `{"release":{"versionPlans":true}}`,
		"packages/a/package.json": `{"name":"@acme/a"}`,
		"packages/a/project.json": `{"name":"a"}`,
	})
	if err := os.MkdirAll(filepath.Join(blocked, ".nx", "version-plans", "wb-wave-1.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := generateNxVersionPlan(ctx, blocked, "wave-1", decisions, options); err == nil {
		t.Fatal("a plan path that is a directory must surface the read error")
	}
}

func TestDepsCovRuntimeGenerateNxVersionPlanRefusesAnUncreatablePlanDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("dangling-symlink setup is not portable to windows")
	}
	worktree := depsCovRuntimeNxWorktree(t, map[string]string{
		"nx.json":                 `{"release":{"versionPlans":true}}`,
		"packages/a/package.json": `{"name":"@acme/a"}`,
		"packages/a/project.json": `{"name":"a"}`,
	})
	// A dangling .nx symlink makes the plan path read as not-exist while
	// MkdirAll of its parent still fails, which is the only portable way to
	// reach the directory-creation branch without depending on file modes.
	if err := os.Symlink(filepath.Join(worktree, "absent"), filepath.Join(worktree, ".nx")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	err := generateNxVersionPlan(context.Background(), worktree, "wave-1", []Decision{{Action: "updated", File: "packages/a/package.json"}}, Options{})
	if err == nil {
		t.Fatal("an uncreatable plan directory must be an error")
	}
}

// ---------------------------------------------------------------------------
// latest_scope.go
// ---------------------------------------------------------------------------

func TestDepsCovRuntimeNormalizeScopesDropsBlanks(t *testing.T) {
	t.Parallel()
	if got := NormalizeScopes([]string{"  @acme/*  ", "", "   ", "\tgithub.com/x/*\n"}); !reflect.DeepEqual(got, []string{"@acme/*", "github.com/x/*"}) {
		t.Fatalf("NormalizeScopes = %q, want trimmed non-blank scopes", got)
	}
	if got := NormalizeScopes(nil); got != nil {
		t.Fatalf("NormalizeScopes(nil) = %q, want nil", got)
	}
}

func TestDepsCovRuntimeFirstScopeReasonsBoundsAndFallback(t *testing.T) {
	t.Parallel()
	if got := firstScopeReasons(nil); got != "no registry reason was recorded" {
		t.Fatalf("firstScopeReasons(nil) = %q", got)
	}
	if got := firstScopeReasons([]LatestScopeResolution{{Dependency: "a"}}); got != "no registry reason was recorded" {
		t.Fatalf("firstScopeReasons(no reasons) = %q", got)
	}
	got := firstScopeReasons([]LatestScopeResolution{
		{Dependency: "a", Reason: "boom-a"},
		{Dependency: "b"},
		{Dependency: "c", Reason: "boom-c"},
		{Dependency: "d", Reason: "boom-d"},
		{Dependency: "e", Reason: "boom-e"},
	})
	want := "a: boom-a; c: boom-c; d: boom-d"
	if got != want {
		t.Fatalf("firstScopeReasons = %q, want %q", got, want)
	}
}

func TestDepsCovRuntimeDeriveLatestReleaseEventsDefaultsEcosystemAndSurfacesGraphErrors(t *testing.T) {
	t.Parallel()
	_, _, err := DeriveLatestReleaseEvents(context.Background(), nil, []string{"@acme/*"}, BumpOptions{})
	if err == nil || !strings.Contains(err.Error(), "GitHub directory is required") {
		t.Fatalf("error = %v, want the graph build's own failure", err)
	}
}

// ---------------------------------------------------------------------------
// peers.go
// ---------------------------------------------------------------------------

func TestDepsCovRuntimeReadPublishedPeersParsesFakePnpmOutput(t *testing.T) {
	depsCovRuntimeFakePnpm(t, `printf '%s' '{"version":"2.1.0","peerDependencies":{"react":"^18.0.0","vue":"^3.0.0"},"peerDependenciesMeta":{"vue":{"optional":true}}}'
`)
	set, err := readPublishedPeers(context.Background(), "@acme/widget", PeerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if set.Version != "2.1.0" || set.Peers["react"] != "^18.0.0" || !set.Optional["vue"] || set.Optional["react"] {
		t.Fatalf("set = %+v", set)
	}
	if got, want := set.Source, "pnpm view @acme/widget version peerDependencies peerDependenciesMeta --json"; got != want {
		t.Fatalf("source = %q, want %q", got, want)
	}
}

func TestDepsCovRuntimeReadPublishedPeersSurfacesCommandFailure(t *testing.T) {
	depsCovRuntimeFakePnpm(t, "printf 'registry unavailable' >&2\nexit 9\n")
	_, err := readPublishedPeers(context.Background(), "@acme/widget", PeerOptions{})
	if err == nil || !strings.Contains(err.Error(), "pnpm view") {
		t.Fatalf("error = %v, want the pnpm command failure", err)
	}
}

func TestDepsCovRuntimeReadPublishedPeersSurfacesUndecodableOutput(t *testing.T) {
	depsCovRuntimeFakePnpm(t, "printf '%s' 'not-json'\n")
	_, err := readPublishedPeers(context.Background(), "@acme/widget", PeerOptions{})
	if err == nil || !strings.Contains(err.Error(), "decode published peer requirements") {
		t.Fatalf("error = %v, want the decode failure naming the package", err)
	}
}

func TestDepsCovRuntimeReadPublishedPeersTreatsEmptyOutputAsNoPeers(t *testing.T) {
	depsCovRuntimeFakePnpm(t, "exit 0\n")
	set, err := readPublishedPeers(context.Background(), "@acme/widget", PeerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Peers) != 0 || set.Source == "" {
		t.Fatalf("set = %+v, want an empty peer set with its source recorded", set)
	}
}

func TestDepsCovRuntimeParsePublishedPeersRejectsInvalidJSON(t *testing.T) {
	t.Parallel()
	if _, err := parsePublishedPeers("{not json"); err == nil {
		t.Fatal("invalid JSON must be an error")
	}
}

func TestDepsCovRuntimePeerNowDefaultsToUTC(t *testing.T) {
	t.Parallel()
	now := peerNow(PeerOptions{})
	if now.IsZero() || now.Location() != time.UTC {
		t.Fatalf("peerNow(no clock) = %v, want a UTC instant", now)
	}
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("offset", 3600))
	got := peerNow(PeerOptions{Now: func() time.Time { return fixed }})
	if !got.Equal(fixed) || got.Location() != time.UTC {
		t.Fatalf("peerNow(injected) = %v, want the injected instant in UTC", got)
	}
}

func TestDepsCovRuntimeJudgePeerReportsRecordedEvidenceReason(t *testing.T) {
	t.Parallel()
	reason := "lockfile importers pin conflicting versions: 18.3.1, 19.0.0"
	row := judgePeer("react", "^18.0.0", false, peerEvidence{Source: "pnpm-lock.yaml", Reason: reason})
	if row.Verdict != PeerMissing || row.Reason != reason {
		t.Fatalf("row = %+v, want the recorded evidence reason", row)
	}
	optional := judgePeer("react", "^18.0.0", true, peerEvidence{})
	if optional.Verdict != PeerOptionalMissing || optional.Reason == "" {
		t.Fatalf("optional row = %+v, want optional_missing with a reason", optional)
	}
}

const depsCovRuntimeConflictingPnpmLock = `lockfileVersion: '9.0'

importers:
  .:
    dependencies:
      react:
        specifier: ^18.0.0
        version: 18.3.1
  packages/a:
    dependencies:
      react:
        specifier: ^18.0.0
        version: 19.0.0
`

func TestDepsCovRuntimeInstalledNpmVersionsReportsConflictingLockfileImporters(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "package.json"), `{"name":"@acme/host"}`+"\n")
	writeTestFile(t, filepath.Join(root, "pnpm-lock.yaml"), depsCovRuntimeConflictingPnpmLock)
	installed, name, err := installedNpmVersions(root)
	if err != nil {
		t.Fatal(err)
	}
	if name != "@acme/host" {
		t.Fatalf("target name = %q, want @acme/host", name)
	}
	evidence := installed["react"]
	if evidence.Version != "" || !strings.Contains(evidence.Reason, "conflicting") {
		t.Fatalf("evidence = %+v, want a conflict reason and no invented version", evidence)
	}
}

func TestDepsCovRuntimeInstalledNpmVersionsSurfacesManifestErrors(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "absent")
	if _, _, err := installedNpmVersions(missing); err == nil {
		t.Fatal("a missing root must be an error")
	}

	malformed := t.TempDir()
	writeTestFile(t, filepath.Join(malformed, "package.json"), "not json\n")
	if _, _, err := installedNpmVersions(malformed); err == nil || !strings.Contains(err.Error(), "parse package.json") {
		t.Fatalf("error = %v, want a package.json parse failure", err)
	}
}

func TestDepsCovRuntimeInspectPeersRejectsInvalidPackageName(t *testing.T) {
	t.Parallel()
	root := newPeerTargetCheckout(t, map[string]string{"package.json": peerTargetPackageJSON})
	options := peerOptions(t, root, PublishedPeerSet{Version: "2.1.0"})
	options.Package = "Not A Valid Name"
	if _, err := InspectPeers(context.Background(), options); err == nil || !strings.Contains(err.Error(), "npm package name") {
		t.Fatalf("error = %v, want an invalid package-name refusal", err)
	}
}

func TestDepsCovRuntimeInspectPeersSurfacesTargetEvidenceErrors(t *testing.T) {
	t.Parallel()
	malformed := newPeerTargetCheckout(t, map[string]string{"package.json": "not json\n"})
	options := peerOptions(t, malformed, PublishedPeerSet{Version: "2.1.0"})
	if _, err := InspectPeers(context.Background(), options); err == nil || !strings.Contains(err.Error(), "parse package.json") {
		t.Fatalf("error = %v, want the target evidence failure", err)
	}
}

func TestDepsCovRuntimeInspectPeersReportsConflictingLockfileEvidence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "package.json"), `{"name":"@acme/host"}`+"\n")
	writeTestFile(t, filepath.Join(root, "pnpm-lock.yaml"), depsCovRuntimeConflictingPnpmLock)
	options := peerOptions(t, root, PublishedPeerSet{
		Version: "2.1.0",
		Peers:   map[string]string{"react": "^18.0.0"},
	})
	report, err := InspectPeers(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	row := peerRowByName(t, report, "react")
	if row.Verdict != PeerMissing || !strings.Contains(row.Reason, "conflicting") {
		t.Fatalf("row = %+v, want the conflicting lockfile evidence surfaced", row)
	}
}
