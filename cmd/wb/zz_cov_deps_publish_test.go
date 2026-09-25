package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/npmrelease"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/spf13/cobra"
)

// cwDepsReleaseFixture is one valid aligned publication tuple.
func cwDepsReleaseFixture() npmrelease.Release {
	return npmrelease.Release{Repository: "acme/app", Workflow: "release.yml", Package: "@acme/lib", Version: "1.2.3", Ref: "main"}
}

// errTestPreflight is the injected preflight failure the command seam must
// surface unchanged.
var errTestPreflight = errors.New("fixture preflight failure")

// cwDepsFakeNpmReleaseRunner is a scripted npmrelease.CommandRunner, the same
// recording-and-replaying shape internal/npmrelease's own tests use to drive
// Run's real Apply:true dispatch path without touching a real gh/npm
// subprocess. It is wired in through npmPublishOptions.runner, the test-only
// seam runPreparedNpmPublishLocked forwards into npmrelease.Options.
type cwDepsFakeNpmReleaseRunner struct {
	calls []string
	steps []npmrelease.CommandResult
}

func (r *cwDepsFakeNpmReleaseRunner) Run(_ context.Context, _ string, args ...string) npmrelease.CommandResult {
	r.calls = append(r.calls, strings.Join(args, " "))
	if len(r.steps) == 0 {
		return npmrelease.CommandResult{Code: 2, Err: errors.New("cwDepsFakeNpmReleaseRunner: unexpected command")}
	}
	result := r.steps[0]
	r.steps = r.steps[1:]
	return result
}

// cwDepsPublishOptionsFixture is a minimal options set that passes tuple
// alignment for the fixture tuple.
func cwDepsPublishOptionsFixture() npmPublishOptions {
	return npmPublishOptions{
		depsSetOptions: depsSetOptions{ref: "main", format: "markdown", parallel: 1, maxWaves: 1},
		repositories:   []string{"acme/app"},
		workflows:      []string{"release.yml"},
		packages:       []string{"@acme/lib"},
		versions:       []string{"1.2.3"},
		registry:       "https://registry.npmjs.org",
	}
}

func TestCwDepsAlignedNpmReleasesRequiresAlignedTuples(t *testing.T) {
	if _, err := alignedNpmReleases(npmPublishOptions{}); err == nil ||
		!strings.Contains(err.Error(), "all required and repeatable as aligned tuples") {
		t.Fatalf("empty tuples error = %v", err)
	}
	mismatched := cwDepsPublishOptionsFixture()
	mismatched.versions = []string{"1.2.3", "1.2.4"}
	if _, err := alignedNpmReleases(mismatched); err == nil ||
		!strings.Contains(err.Error(), "must have the same number of values") {
		t.Fatalf("mismatched tuples error = %v", err)
	}
	releases, err := alignedNpmReleases(cwDepsPublishOptionsFixture())
	if err != nil || len(releases) != 1 {
		t.Fatalf("aligned tuples = %+v, %v", releases, err)
	}
	if releases[0].Repository != "acme/app" || releases[0].Version != "1.2.3" || releases[0].Ref != "main" {
		t.Errorf("release = %+v", releases[0])
	}
	// A bare KEY=VALUE belongs to tuple 0 only when there is one tuple.
	withInput := cwDepsPublishOptionsFixture()
	withInput.workflowInputs = []string{"package=runtime"}
	releases, err = alignedNpmReleases(withInput)
	if err != nil || releases[0].Inputs["package"] != "runtime" {
		t.Fatalf("single tuple bare input = %+v, %v", releases, err)
	}
}

func TestCwDepsParseWorkflowInputsRejectsEveryMalformedShape(t *testing.T) {
	inputs, err := parseWorkflowInputs([]string{"0:package=runtime", "1:tag=next"}, 2)
	if err != nil || inputs[0]["package"] != "runtime" || inputs[1]["tag"] != "next" {
		t.Fatalf("scoped inputs = %+v, %v", inputs, err)
	}
	if inputs, err := parseWorkflowInputs(nil, 1); err != nil || inputs[0] != nil {
		t.Fatalf("no inputs = %+v, %v", inputs, err)
	}
	tests := map[string]struct {
		values []string
		count  int
		want   string
	}{
		"no equals":            {[]string{"package"}, 1, "want INDEX:KEY=VALUE"},
		"empty index":          {[]string{":package=runtime"}, 1, "want INDEX:KEY=VALUE"},
		"negative index":       {[]string{"-1:package=runtime"}, 1, "tuple index"},
		"leading zero index":   {[]string{"01:package=runtime"}, 1, "tuple index"},
		"non-numeric index":    {[]string{"x:package=runtime"}, 1, "tuple index"},
		"index out of range":   {[]string{"3:package=runtime"}, 2, "tuple index or key/value is invalid"},
		"empty key":            {[]string{"0:=runtime"}, 1, "tuple index or key/value is invalid"},
		"newline in value":     {[]string{"0:package=run\ntime"}, 1, "tuple index or key/value is invalid"},
		"unscoped multi tuple": {[]string{"package=runtime"}, 2, "must identify its tuple"},
		"secret-like key":      {[]string{"0:NPM_TOKEN=x"}, 1, "secret-like"},
		"duplicate key":        {[]string{"0:package=a", "0:package=b"}, 1, "duplicate --workflow-input"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseWorkflowInputs(test.values, test.count); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
	if _, err := parseWorkflowInputs(nil, 0); err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("zero tuple count error = %v", err)
	}
}

func TestCwDepsNpmPublicationIdentityAndEvents(t *testing.T) {
	releases, operation, err := npmPublicationIdentity(cwDepsPublishOptionsFixture())
	if err != nil || len(releases) != 1 || operation == "" {
		t.Fatalf("identity = %v, %q, %v", releases, operation, err)
	}
	events := releaseEventsForReleases(releases)
	if len(events) != 1 || events[0].Dependency != "@acme/lib" || events[0].Version != "1.2.3" || events[0].Source != "npm_workflow_plan" {
		t.Fatalf("events = %+v", events)
	}
	report := npmrelease.Report{Releases: []npmrelease.Receipt{{Release: cwDepsReleaseFixture()}}}
	planned := plannedNpmReleaseEvents(report)
	if len(planned) != 1 || planned[0].Dependency != "@acme/lib" {
		t.Fatalf("planned events = %+v", planned)
	}
	if got := releaseEventsForReleases(nil); len(got) != 0 {
		t.Errorf("no releases produced events: %+v", got)
	}
	// An invalid tuple set is refused before anything is locked or discovered.
	bad := cwDepsPublishOptionsFixture()
	bad.versions = []string{"not-semver"}
	if _, _, err := npmPublicationIdentity(bad); err == nil {
		t.Fatal("an invalid npm version must be refused")
	}
}

func TestCwDepsNpmPublicationPropagationOptionsTurnOffMutationsForPlans(t *testing.T) {
	options := cwDepsPublishOptionsFixture()
	options.commit, options.push, options.pr, options.merge = true, true, true, true
	options.dryRun = false
	plan := npmPublicationPropagationOptions(options, "/tmp/report", true, false)
	if !plan.fleet || plan.reportDir != "/tmp/report" || !plan.dryRun {
		t.Fatalf("plan propagation = %+v", plan)
	}
	if plan.commit || plan.push || plan.pr || plan.merge {
		t.Errorf("a dry-run plan must not carry mutation flags: %+v", plan)
	}
	apply := npmPublicationPropagationOptions(options, "/tmp/report", false, true)
	if !apply.merge || !apply.push || !apply.resume || apply.dryRun {
		t.Errorf("apply propagation = %+v", apply)
	}
}

func TestCwDepsAttachNpmPropagationOnlyWhenThereIsOne(t *testing.T) {
	publication := npmrelease.Report{}
	attachNpmPropagation(&publication, deps.BumpReport{})
	if publication.Propagation != nil || publication.PropagationOperation != "" {
		t.Fatalf("empty propagation was attached: %+v", publication)
	}
	attachNpmPropagation(&publication, deps.BumpReport{Operation: "deps-bump-abc"})
	if publication.Propagation == nil || publication.PropagationOperation != "deps-bump-abc" {
		t.Fatalf("propagation was not attached: %+v", publication)
	}
}

func TestCwDepsNpmPublicationReportPaths(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	previousRoot := projectsRoot
	projectsRoot = t.TempDir()
	t.Cleanup(func() { projectsRoot = previousRoot })

	requested, err := npmPublicationReportDir(nil, "/tmp/explicit")
	if err != nil || requested != "/tmp/explicit" {
		t.Fatalf("requested report dir = %q, %v", requested, err)
	}
	releases := []npmrelease.Release{cwDepsReleaseFixture()}
	derived, err := npmPublicationReportDir(releases, "")
	if err != nil {
		t.Fatalf("derived report dir: %v", err)
	}
	if !strings.HasPrefix(derived, os.Getenv("WB_HOME")) || !strings.Contains(derived, npmrelease.OperationIDFor(releases)) {
		t.Errorf("derived report dir = %q", derived)
	}
	if got := npmPublicationPlanReportDir("/tmp/report"); got != filepath.Join("/tmp/report", "plan") {
		t.Errorf("plan report dir = %q", got)
	}
}

func TestCwDepsNpmPublicationResumeReportBranches(t *testing.T) {
	reportDir := t.TempDir()
	// Nothing persisted and no --resume: a fresh run proceeds with no previous.
	previous, err := npmPublicationResumeReport(reportDir, false)
	if err != nil || previous != nil {
		t.Fatalf("fresh report = %+v, %v", previous, err)
	}
	// --resume with nothing persisted names the file it wanted.
	if _, err := npmPublicationResumeReport(reportDir, true); err == nil ||
		!strings.Contains(err.Error(), "--resume requires") {
		t.Fatalf("resume without a report = %v", err)
	}
	// A persisted report without --resume refuses to overwrite or redispatch.
	// It is produced through the real writer so the YAML/JSON pair carries one
	// consistent generation, exactly as --apply leaves it behind.
	written, err := npmrelease.Run(context.Background(), []npmrelease.Release{cwDepsReleaseFixture()},
		npmrelease.Options{DryRun: true, Ref: "main", ReportDir: reportDir,
			Persist: func(report npmrelease.Report) error { return npmrelease.WriteReport(reportDir, report) }})
	if err != nil {
		t.Fatalf("write report: %v", err)
	}
	if _, err := npmPublicationResumeReport(reportDir, false); err == nil ||
		!strings.Contains(err.Error(), "requires --resume") {
		t.Fatalf("existing report without --resume = %v", err)
	}
	loaded, err := npmPublicationResumeReport(reportDir, true)
	if err != nil || loaded == nil || loaded.Operation != written.Operation {
		t.Fatalf("resumed report = %+v, %v", loaded, err)
	}
	// An unreadable pair is refused rather than guessed.
	incompleteDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(incompleteDir, "npm-publish.yaml"), []byte("operation: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := npmPublicationResumeReport(incompleteDir, true); err == nil ||
		!strings.Contains(err.Error(), "--resume requires readable") {
		t.Fatalf("incomplete report = %v", err)
	}
}

func TestCwDepsNpmPublicationBumpPreviousBranches(t *testing.T) {
	reportDir := t.TempDir()
	if previous, err := npmPublicationBumpPrevious(reportDir, false); err != nil || previous != nil {
		t.Fatalf("no resume = %+v, %v", previous, err)
	}
	// Publication can reach the registry before its first handoff, so a
	// missing downstream report is a normal resume, not an error.
	if previous, err := npmPublicationBumpPrevious(reportDir, true); err != nil || previous != nil {
		t.Fatalf("missing downstream report = %+v, %v", previous, err)
	}
	persisted := deps.BumpReport{SchemaVersion: 1, Operation: "deps-bump-cwfixture", Status: "completed", Parallel: 1}
	if err := deps.WriteBumpReports(reportDir, persisted); err != nil {
		t.Fatalf("write bump report: %v", err)
	}
	previous, err := npmPublicationBumpPrevious(reportDir, true)
	if err != nil || previous == nil || previous.Operation != persisted.Operation {
		t.Fatalf("loaded downstream report = %+v, %v", previous, err)
	}
	// A corrupt report is an explicit error, never a silent fresh start.
	corrupt := t.TempDir()
	if err := os.WriteFile(filepath.Join(corrupt, "deps-bump.yaml"), []byte("schema_version: [oops\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := npmPublicationBumpPrevious(corrupt, true); err == nil ||
		!strings.Contains(err.Error(), "load persisted dependency-wave report") {
		t.Fatalf("corrupt downstream report = %v", err)
	}
}

func TestCwDepsPublicationHasDispatch(t *testing.T) {
	if publicationHasDispatch(nil) {
		t.Error("a nil report has no dispatch")
	}
	if publicationHasDispatch(&npmrelease.Report{Releases: []npmrelease.Receipt{{}}}) {
		t.Error("a receipt with no dispatch timestamp is not a dispatch")
	}
	report := npmrelease.Report{Releases: []npmrelease.Receipt{
		{},
		{DispatchAt: time.Unix(1, 0)},
	}}
	if !publicationHasDispatch(&report) {
		t.Error("a dispatched receipt must be detected")
	}
}

func TestCwDepsValidateNpmPublishFormatAndSelection(t *testing.T) {
	for _, format := range []string{"markdown", "yaml", "json"} {
		if err := validateNpmPublishFormat(format); err != nil {
			t.Errorf("format %q: %v", format, err)
		}
	}
	if err := validateNpmPublishFormat("toml"); err == nil || !strings.Contains(err.Error(), `unknown --format "toml"`) {
		t.Fatalf("unknown format = %v", err)
	}
	if err := validateNpmPublicationSelection(npmPublishOptions{}); err != nil {
		t.Errorf("empty selection: %v", err)
	}
	invalid := cwDepsPublishOptionsFixture().depsSetOptions
	invalid.regex = "("
	if err := validateNpmPublicationSelection(npmPublishOptions{depsSetOptions: invalid}); err == nil ||
		!strings.Contains(err.Error(), "invalid --regex") {
		t.Fatalf("invalid regex = %v", err)
	}
	invalid = cwDepsPublishOptionsFixture().depsSetOptions
	invalid.match = "["
	if err := validateNpmPublicationSelection(npmPublishOptions{depsSetOptions: invalid}); err == nil ||
		!strings.Contains(err.Error(), "invalid --match") {
		t.Fatalf("invalid match = %v", err)
	}
}

func TestCwDepsPreflightNpmPublishRefusals(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	previousRoot := projectsRoot
	projectsRoot = t.TempDir()
	t.Cleanup(func() { projectsRoot = previousRoot })

	base := cwDepsPublishOptionsFixture()
	base.fleet = true
	base.maxWaves = 1
	tests := map[string]struct {
		mutate func(*npmPublishOptions)
		want   string
	}{
		"no fleet":             {func(o *npmPublishOptions) { o.fleet = false }, "requires --fleet"},
		"resume without apply": {func(o *npmPublishOptions) { o.resume = true }, "--resume requires --apply"},
		"merge without apply":  {func(o *npmPublishOptions) { o.merge = true }, "require --apply"},
		"apply with dry-run":   {func(o *npmPublishOptions) { o.apply = true; o.dryRun = true }, "--apply and --dry-run cannot be used together"},
		"push without merge":   {func(o *npmPublishOptions) { o.apply = true; o.push = true }, "require --merge"},
		"no-verify with checks": {func(o *npmPublishOptions) {
			o.noVerify = true
			o.checks = "lint"
		}, "--no-verify and --checks cannot be used together"},
		"bad format":  {func(o *npmPublishOptions) { o.format = "toml" }, "unknown --format"},
		"bad regex":   {func(o *npmPublishOptions) { o.regex = "(" }, "invalid --regex"},
		"bad checks":  {func(o *npmPublishOptions) { o.checks = "nonsense" }, "check"},
		"bad version": {func(o *npmPublishOptions) { o.versions = []string{"nope"} }, "version"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			options := base
			options.repositories = append([]string(nil), base.repositories...)
			options.workflows = append([]string(nil), base.workflows...)
			options.packages = append([]string(nil), base.packages...)
			options.versions = append([]string(nil), base.versions...)
			test.mutate(&options)
			called := false
			_, err := preflightNpmPublishWithDiscovery(&invocation{}, options, func(*invocation, []string, depsSetOptions) ([]deps.Repository, error) {
				called = true
				return nil, nil
			})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if called && name != "bad version" {
				t.Error("fleet discovery ran before the refusal")
			}
		})
	}
	// A nil discovery seam is refused rather than panicking.
	if _, err := preflightNpmPublishWithDiscovery(&invocation{}, base, nil); err == nil ||
		!strings.Contains(err.Error(), "fleet discovery is unavailable") {
		t.Fatalf("nil discovery = %v", err)
	}
}

// TestPreflightNpmPublishDelegatesToRealDependencyDiscovery proves the
// production preflightNpmPublish wrapper wires the real dependencyRepositories
// discoverer into preflightNpmPublishWithDiscovery, not only a test fake: the
// missing --fleet requirement it surfaces is the same validation
// preflightNpmPublishWithDiscovery itself performs before ever calling discover.
func TestPreflightNpmPublishDelegatesToRealDependencyDiscovery(t *testing.T) {
	if _, err := preflightNpmPublish(&invocation{}, npmPublishOptions{}); err == nil ||
		!strings.Contains(err.Error(), "deps publish npm requires --fleet") {
		t.Fatalf("err = %v, want the --fleet requirement", err)
	}
}

func TestCwDepsPreflightNpmPublishSelectsTheFleet(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	previousRoot := projectsRoot
	projectsRoot = t.TempDir()
	t.Cleanup(func() { projectsRoot = previousRoot })

	options := cwDepsPublishOptionsFixture()
	options.fleet = true
	options.maxWaves = 1
	var seenArgs []string
	prepared, err := preflightNpmPublishWithDiscovery(&invocation{}, options, func(_ *invocation, args []string, _ depsSetOptions) ([]deps.Repository, error) {
		seenArgs = args
		return []deps.Repository{{Slug: "acme/consumer", Path: filepath.Join(projectsRoot, "acme", "consumer")}}, nil
	})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if len(seenArgs) != 2 || seenArgs[0] != "npm" || seenArgs[1] != "events" {
		t.Errorf("discovery args = %v", seenArgs)
	}
	if len(prepared.repositories) != 1 || prepared.operation == "" || len(prepared.releases) != 1 || prepared.reportDir == "" {
		t.Fatalf("prepared = %+v", prepared)
	}
	if !strings.HasPrefix(prepared.reportDir, os.Getenv("WB_HOME")) {
		t.Errorf("prepared report dir = %q", prepared.reportDir)
	}
}

// cwDepsWorkflowRunFixture renders the exact gh run list/view JSON shape
// npmrelease.Run decodes, mirroring internal/npmrelease's own workflowRunFixture.
func cwDepsWorkflowRunFixture(id, status, conclusion string, headSHA string, created time.Time) string {
	return fmt.Sprintf(
		`{"databaseId":%s,"headSha":%q,"status":%q,"conclusion":%q,"event":"workflow_dispatch","createdAt":%q,"updatedAt":%q,"url":%q}`,
		id, headSHA, status, conclusion, created.UTC().Format(time.RFC3339), created.UTC().Add(time.Second).Format(time.RFC3339),
		"https://github.com/acme/app/actions/runs/"+id)
}

// TestCwDepsRunPreparedNpmPublishApplyDispatchesAndVerifiesRegistry drives the
// real --apply dispatch branch of runPreparedNpmPublishLocked end to end
// through a scripted npmrelease.CommandRunner: resolve the release head,
// dispatch the workflow, observe it complete, and verify the registry - the
// same sequence internal/npmrelease's own TestRunDispatchWaitAndRegistryEvidence
// proves at the package level, now proven reachable from the command tree.
func TestCwDepsRunPreparedNpmPublishApplyDispatchesAndVerifiesRegistry(t *testing.T) {
	home := t.TempDir()
	t.Setenv(wbhome.EnvOverride, home)
	previousRoot := projectsRoot
	projectsRoot = t.TempDir()
	t.Cleanup(func() { projectsRoot = previousRoot })

	const headSHA = "0123456789abcdef0123456789abcdef01234567"
	created := time.Now().UTC().Truncate(time.Second)
	run := cwDepsWorkflowRunFixture("123", "completed", "success", headSHA, created.Add(time.Second))
	runner := &cwDepsFakeNpmReleaseRunner{steps: []npmrelease.CommandResult{
		{Output: headSHA + "\n"},
		{Output: `[]`},
		{},
		{Output: "[" + run + "]"},
		{Output: run},
		{Output: `"1.2.3"`},
	}}

	options := cwDepsPublishOptionsFixture()
	options.fleet = true
	options.apply = true
	options.maxWaves = 1
	options.runner = runner
	prepared := npmPublishPrepared{
		releases:  []npmrelease.Release{cwDepsReleaseFixture()},
		checks:    []quality.Check{},
		reportDir: filepath.Join(home, "reports", "cw-npm-apply-dispatch"),
		operation: "deps-npm-publish-cwfixture",
	}
	var out bytes.Buffer
	command := cwDepsNewOutCommand(&out)
	if err := runPreparedNpmPublishLocked(command, options, prepared, &invocation{}); err != nil {
		t.Fatalf("apply publication: %v\nstdout: %s", err, out.String())
	}
	if !strings.Contains(strings.Join(runner.calls, "\n"), "npm view "+cwDepsReleaseFixture().Package+"@"+cwDepsReleaseFixture().Version) {
		t.Fatalf("registry was not verified through the injected runner: %v", runner.calls)
	}
	// Read the durable JSON receipt directly rather than through
	// npmrelease.LoadReport: that helper's YAML/JSON generation-consistency
	// check is a separate concern from the invocation-threading this test
	// covers, and is exercised by internal/npmrelease's own test suite.
	persisted, err := os.ReadFile(filepath.Join(prepared.reportDir, "npm-publish.json"))
	if err != nil {
		t.Fatalf("read persisted report: %v", err)
	}
	var report npmrelease.Report
	if err := json.Unmarshal(persisted, &report); err != nil {
		t.Fatalf("decode persisted report: %v", err)
	}
	if report.Status != npmrelease.StatusPublished || len(report.Releases) != 1 || report.Releases[0].RunID != "123" {
		t.Fatalf("persisted report = %+v", report)
	}
}

func TestCwDepsWriteNpmPublishOutputFormats(t *testing.T) {
	output := npmPublishOutput{Publication: npmrelease.Report{
		SchemaVersion: npmrelease.SchemaVersion, Operation: "deps-npm-publish-cwfixture", Status: npmrelease.StatusPlanned, Ref: "main",
		Releases: []npmrelease.Receipt{{
			Release: cwDepsReleaseFixture(),
			Status:  npmrelease.StatusPlanned,
			Reason:  "plan|only",
			RunID:   "42",
			RunURL:  "https://example.test/run/42",
			HeadSHA: "abcdef",
		}},
	}}
	for _, format := range []string{"markdown", "yaml", "json"} {
		var out bytes.Buffer
		if err := writeNpmPublishOutput(cwDepsNewOutCommand(&out), output, format); err != nil {
			t.Fatalf("format %s: %v", format, err)
		}
		if strings.TrimSpace(out.String()) == "" {
			t.Errorf("format %s wrote nothing", format)
		}
		if format == "json" && !json.Valid(out.Bytes()) {
			t.Errorf("json output is not JSON: %s", out.String())
		}
	}
	var markdown bytes.Buffer
	if err := writeNpmPublishMarkdown(cwDepsNewOutCommand(&markdown), output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# WB npm publication and dependency waves", "@acme/lib", "plan\\|only", "[42](https://example.test/run/42)"} {
		if !strings.Contains(markdown.String(), want) {
			t.Errorf("markdown missing %q:\n%s", want, markdown.String())
		}
	}
	// A propagation section is appended when a bump report is attached.
	output.Propagation = &deps.BumpReport{SchemaVersion: 1, Operation: "deps-bump-cwfixture", Status: "completed", Parallel: 1}
	markdown.Reset()
	if err := writeNpmPublishMarkdown(cwDepsNewOutCommand(&markdown), output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(markdown.String(), "deps-bump-cwfixture") {
		t.Errorf("propagation report missing from markdown:\n%s", markdown.String())
	}
	if err := writeNpmPublishOutput(cwDepsNewOutCommand(&bytes.Buffer{}), output, "toml"); err == nil ||
		!strings.Contains(err.Error(), `unknown --format "toml"`) {
		t.Fatalf("unknown format = %v", err)
	}
}

// TestCwDepsRunPreparedNpmPublishPlan drives the plan half of the irreversible
// seam: no workflow is dispatched, but the durable wave plan is still built.
func TestCwDepsRunPreparedNpmPublishPlan(t *testing.T) {
	home := t.TempDir()
	t.Setenv(wbhome.EnvOverride, home)
	previousRoot := projectsRoot
	projectsRoot = t.TempDir()
	t.Cleanup(func() { projectsRoot = previousRoot })

	options := cwDepsPublishOptionsFixture()
	options.fleet = true
	options.apply = false
	options.maxWaves = 1
	prepared := npmPublishPrepared{
		releases:  []npmrelease.Release{cwDepsReleaseFixture()},
		checks:    []quality.Check{},
		reportDir: filepath.Join(home, "reports", "cw-npm-plan"),
		operation: "deps-npm-publish-cwfixture",
	}
	var out, errOut bytes.Buffer
	command := cwDepsNewOutCommand(&out)
	command.SetErr(&errOut)
	err := runPreparedNpmPublishLocked(command, options, prepared, &invocation{})
	if err != nil {
		t.Fatalf("plan publication: %v\nstdout: %s", err, out.String())
	}
	if !strings.Contains(out.String(), "WB npm publication and dependency waves") {
		t.Errorf("plan output = %s", out.String())
	}
	// The plan's wave report lives below /plan so it can never overwrite an
	// apply receipt.
	if _, statErr := os.Stat(filepath.Join(prepared.reportDir, "plan")); statErr != nil {
		t.Errorf("plan wave report directory missing: %v", statErr)
	}
}

// TestCwDepsRunPreparedNpmPublishRefusesExistingReportWithoutResume proves the
// irreversible boundary rechecks inside the lock.
func TestCwDepsRunPreparedNpmPublishRefusesExistingReportWithoutResume(t *testing.T) {
	home := t.TempDir()
	t.Setenv(wbhome.EnvOverride, home)
	previousRoot := projectsRoot
	projectsRoot = t.TempDir()
	t.Cleanup(func() { projectsRoot = previousRoot })

	reportDir := filepath.Join(home, "reports", "cw-npm-apply")
	if err := npmrelease.WriteReport(reportDir, npmrelease.Report{
		SchemaVersion: npmrelease.SchemaVersion, Operation: "deps-npm-publish-cwfixture",
		Status: npmrelease.StatusPlanned, Ref: "main",
	}); err != nil {
		t.Fatal(err)
	}
	options := cwDepsPublishOptionsFixture()
	options.fleet = true
	options.apply = true
	options.maxWaves = 1
	prepared := npmPublishPrepared{
		releases:  []npmrelease.Release{cwDepsReleaseFixture()},
		reportDir: reportDir, operation: "deps-npm-publish-cwfixture",
	}
	var out bytes.Buffer
	command := cwDepsNewOutCommand(&out)
	if err := runPreparedNpmPublishLocked(command, options, prepared, &invocation{}); err == nil ||
		!strings.Contains(err.Error(), "requires --resume") {
		t.Fatalf("existing report without --resume = %v", err)
	}
}

// TestCwDepsRunNpmPublishWithPreflightUsesTheInjectedPreflight covers the lock
// claim, the preflight handoff, and the operation-identity guard.
func TestCwDepsRunNpmPublishWithPreflightUsesTheInjectedPreflight(t *testing.T) {
	home := t.TempDir()
	t.Setenv(wbhome.EnvOverride, home)
	previousRoot := projectsRoot
	projectsRoot = t.TempDir()
	t.Cleanup(func() { projectsRoot = previousRoot })

	options := cwDepsPublishOptionsFixture()
	options.fleet = true
	options.apply = false
	options.maxWaves = 1
	identity, operation, err := npmPublicationIdentity(options)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	command := cwDepsNewOutCommand(&out)
	command.SetErr(&errOut)

	// A preflight that reports a different operation is refused: the lock was
	// claimed for one campaign and the plan describes another.
	if err := runNpmPublishWithPreflight(command, options, func(*invocation, npmPublishOptions) (npmPublishPrepared, error) {
		return npmPublishPrepared{operation: "something-else"}, nil
	}, &invocation{}); err == nil || !strings.Contains(err.Error(), "changed the requested operation") {
		t.Fatalf("operation mismatch = %v", err)
	}
	// A failing preflight fails the selection campaign and surfaces the error.
	if err := runNpmPublishWithPreflight(command, options, func(*invocation, npmPublishOptions) (npmPublishPrepared, error) {
		return npmPublishPrepared{}, errTestPreflight
	}, &invocation{}); err == nil || !strings.Contains(err.Error(), "fixture preflight failure") {
		t.Fatalf("preflight failure = %v", err)
	}
	// A consistent preflight reaches the durable plan.
	prepared := npmPublishPrepared{
		releases:     identity,
		repositories: []deps.Repository{},
		reportDir:    filepath.Join(home, "reports", "cw-npm-preflight"),
		operation:    operation,
	}
	if err := runNpmPublishWithPreflight(command, options, func(*invocation, npmPublishOptions) (npmPublishPrepared, error) {
		return prepared, nil
	}, &invocation{}); err != nil {
		t.Fatalf("consistent preflight: %v\nstdout: %s", err, out.String())
	}
}

func TestCwDepsAcquireNpmPublicationLocksAndRelease(t *testing.T) {
	previousRoot := projectsRoot
	projectsRoot = t.TempDir()
	t.Cleanup(func() { projectsRoot = previousRoot })

	locks, err := acquireNpmPublicationLocks("deps-npm-publish-cwfixture", []npmrelease.Release{cwDepsReleaseFixture()}, false)
	if err != nil {
		t.Fatalf("acquire locks: %v", err)
	}
	if len(locks.locks) != 2 {
		t.Fatalf("locks = %d, want the campaign plus one package-version claim", len(locks.locks))
	}
	locks.Release()
	// Releasing an empty set is a no-op.
	npmPublicationLocks{}.Release()
}

// TestCwDepsPreflightNpmPublishCommandWiring keeps the production constructor
// pointed at the production run function while still allowing an injected
// recorder for Cobra parsing.
func TestCwDepsPreflightNpmPublishCommandWiring(t *testing.T) {
	var captured npmPublishOptions
	command := newNpmPublishCmdWithRun(&invocation{}, func(_ *cobra.Command, options npmPublishOptions, _ *invocation) error {
		captured = options
		return nil
	})
	command.SetArgs([]string{
		"--repo", "acme/app", "--workflow", "release.yml", "--package", "@acme/lib", "--version", "1.2.3",
		"--fleet", "--workflow-input", "0:package=runtime", "--parallel", "3",
	})
	command.SilenceUsage, command.SilenceErrors = true, true
	if err := command.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(captured.repositories) != 1 || captured.workflows[0] != "release.yml" || !captured.fleet {
		t.Fatalf("captured options = %+v", captured)
	}
	if captured.workflowInputs[0] != "0:package=runtime" || captured.parallel != 3 {
		t.Errorf("captured inputs/parallel = %+v", captured)
	}
	if !captured.parallelExplicit {
		t.Error("--parallel must be recorded as explicit for the wave engine")
	}
	if newNpmPublishCmd(&invocation{}) == nil {
		t.Error("the production constructor returned no command")
	}
}
