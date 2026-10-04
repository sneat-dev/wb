package depsrun

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cliprogress "github.com/sneat-dev/wb/internal/cli/progress"
	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/npmrelease"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/spf13/cobra"
)

type depsSetOptions struct {
	fleet, dryRun, resume, allowDowngrade, noVerify          bool
	commit, push, pr, merge                                  bool
	match, regex, ref, checks, validation, format, reportDir string
	parallel, retry, maxWaves                                int
	parallelExplicit                                         bool
	timeout, releasePoll, refreshAfter                       time.Duration
}

type invocation struct{ projectsRoot string }
type npmPublishOptions struct {
	depsSetOptions
	repositories, workflows, packages, versions, workflowInputs []string
	registry                                                    string
	workflowPoll                                                time.Duration
	apply                                                       bool
}

func testPublicationRequest(options npmPublishOptions, inv *invocation) PublicationRequest {
	root := ""
	if inv != nil {
		root = inv.projectsRoot
	}
	if root == "" {
		root = os.Getenv(wbhome.EnvOverride)
	}
	mode := EffectiveValidationMode(options.validation, options.noVerify)
	return PublicationRequest{Repositories: options.repositories, Workflows: options.workflows, Packages: options.packages, Versions: options.versions, WorkflowInputs: options.workflowInputs, Registry: options.registry, Checks: options.checks, NoVerify: options.noVerify, Apply: options.apply, WorkflowPoll: options.workflowPoll, ReleasePoll: options.releasePoll, RefreshAfter: options.refreshAfter, MaxWaves: options.maxWaves, Selection: Selection{ProjectsRoot: root, Fleet: options.fleet, Match: options.match, Regex: options.regex, Parallel: options.parallel, Retry: options.retry, Timeout: options.timeout}, Lifecycle: deps.Options{GitHubDir: root, Ref: options.ref, Parallel: options.parallel, ParallelExplicit: options.parallelExplicit, DryRun: options.dryRun, Resume: options.resume, AllowDowngrade: options.allowDowngrade, ValidationMode: mode, Verify: mode == deps.ValidationModeFull, Timeout: options.timeout, Retry: options.retry, Commit: options.commit, Push: options.push, PR: options.pr, Merge: options.merge, ReportDir: options.reportDir}}
}
func testPublicationService() *PublicationService {
	return NewPublication(DefaultPublicationDependencies(io.Discard), func(_ int, message string) error { return errors.New(message) })
}
func testPublicationCallbacks() PublicationCallbacks {
	return PublicationCallbacks{ValidateOutput: func() error { return nil }, Emit: func(PublicationOutput) error { return nil }, StartProgress: func(label string) PublicationProgress {
		c := cliprogress.NewCampaign(io.Discard, false, label)
		return PublicationProgress{Reporter: c.Reporter(), Finish: c.Finish}
	}}
}
func testPreflightWithDiscovery(inv *invocation, options npmPublishOptions, discover func(context.Context, Selection) ([]deps.Repository, error)) (npmPublishPrepared, error) {
	service := testPublicationService()
	service.deps.Select = discover
	return service.preflight(context.Background(), testPublicationRequest(options, inv), testPublicationCallbacks())
}
func testNpmPublicationReportDir(inv *invocation, releases []npmrelease.Release, requested string) (string, error) {
	return testPublicationService().npmPublicationReportDir(inv.projectsRoot, releases, requested)
}
func testNpmPublicationResumeReport(dir string, resume bool) (*npmrelease.Report, error) {
	return testPublicationService().npmPublicationResumeReport(dir, resume)
}
func testNpmPublicationBumpPrevious(dir string, resume bool) (*deps.BumpReport, error) {
	return testPublicationService().npmPublicationBumpPrevious(dir, resume)
}
func testAcquireNpmPublicationLocks(inv *invocation, operation string, releases []npmrelease.Release, resume bool) (npmPublicationLocks, error) {
	return testPublicationService().acquireNpmPublicationLocks(inv.projectsRoot, operation, releases, resume)
}
func testInvocation(_ *testing.T, root string) *invocation { return &invocation{projectsRoot: root} }
func cwDepsNewOutCommand(out io.Writer) *cobra.Command {
	c := &cobra.Command{}
	c.SetOut(out)
	return c
}
func testRunWithPreflight(c *cobra.Command, options npmPublishOptions, preflight func(*invocation, npmPublishOptions) (npmPublishPrepared, error), inv *invocation) error {
	service := testPublicationService()
	return service.runWithPreflight(context.Background(), testPublicationRequest(options, inv), func(context.Context, PublicationRequest, PublicationCallbacks) (npmPublishPrepared, error) {
		return preflight(inv, options)
	}, testPublicationCallbacks())
}
func cwDepsReleaseFixture() npmrelease.Release {
	return npmrelease.Release{Repository: "acme/app", Workflow: "release.yml", Package: "@acme/lib", Version: "1.2.3", Ref: "main"}
}
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

var errTestPreflight = errors.New("fixture preflight failure")

func validNpmPublishOptions() npmPublishOptions {
	return npmPublishOptions{
		depsSetOptions: depsSetOptions{
			fleet: true, ref: "main", parallel: 1, maxWaves: 20,
			timeout: time.Minute, releasePoll: time.Second, refreshAfter: time.Minute, format: "json",
		},
		repositories: []string{"acme/provider"},
		workflows:    []string{"release.yml"},
		packages:     []string{"@acme/provider"},
		versions:     []string{"1.0.0"},
		registry:     "https://registry.npmjs.org",
		workflowPoll: time.Second,
	}
}
func TestNpmPublishPreflightRejectsInvalidOptionsBeforeFleetDiscovery(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		change  func(*npmPublishOptions)
		message string
	}{
		{name: "secret input", change: func(options *npmPublishOptions) { options.workflowInputs = []string{"npm_token=should-never-leak"} }, message: "secret-like"},
		{name: "fleet", change: func(options *npmPublishOptions) { options.fleet = false }, message: "requires --fleet"},
		{name: "repository", change: func(options *npmPublishOptions) { options.repositories = []string{"invalid repository"} }, message: "invalid GitHub repository"},
		{name: "workflow", change: func(options *npmPublishOptions) { options.workflows = []string{"../release.yml"} }, message: "invalid release workflow"},
		{name: "malformed package", change: func(options *npmPublishOptions) { options.packages = []string{"@Acme/Upper"} }, message: "invalid npm package"},
		{name: "version", change: func(options *npmPublishOptions) { options.versions = []string{"v1.0.0"} }, message: "invalid npm release version"},
		{name: "ref", change: func(options *npmPublishOptions) { options.ref = "not a ref" }, message: "release ref"},
		{name: "registry", change: func(options *npmPublishOptions) { options.registry = "ftp://registry.example" }, message: "invalid npm registry URL"},
		{name: "checks", change: func(options *npmPublishOptions) { options.checks = "lint,unknown" }, message: "unknown check"},
		{name: "no verify with checks", change: func(options *npmPublishOptions) { options.noVerify, options.checks = true, "lint" }, message: "--no-verify and --checks"},
		{name: "regex", change: func(options *npmPublishOptions) { options.regex = "[" }, message: "invalid --regex"},
		{name: "match", change: func(options *npmPublishOptions) { options.match = "[" }, message: "invalid --match"},
		{name: "parallel", change: func(options *npmPublishOptions) { options.parallel = -1 }, message: "parallelism"},
		{name: "retry", change: func(options *npmPublishOptions) { options.retry = -1 }, message: "retry count"},
		{name: "timeout", change: func(options *npmPublishOptions) { options.timeout = -time.Second }, message: "timeout"},
		{name: "workflow poll", change: func(options *npmPublishOptions) { options.workflowPoll = -time.Second }, message: "poll interval"},
		{name: "release poll", change: func(options *npmPublishOptions) { options.releasePoll = -time.Second }, message: "release poll interval"},
		{name: "refresh", change: func(options *npmPublishOptions) { options.refreshAfter = -time.Second }, message: "release refresh interval"},
		{name: "max waves", change: func(options *npmPublishOptions) { options.maxWaves = -1 }, message: "max waves"},
		{name: "apply dry run", change: func(options *npmPublishOptions) { options.apply, options.dryRun = true, true }, message: "--apply and --dry-run"},
		{name: "resume without apply", change: func(options *npmPublishOptions) { options.resume = true }, message: "--resume requires --apply"},
		{name: "merge without apply", change: func(options *npmPublishOptions) { options.merge = true }, message: "require --apply"},
		{name: "commit without merge", change: func(options *npmPublishOptions) { options.apply, options.commit = true, true }, message: "require --merge"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := validNpmPublishOptions()
			test.change(&options)
			discovered := false
			_, err := testPreflightWithDiscovery(&invocation{projectsRoot: t.TempDir()}, options, func(_ context.Context, selection Selection) ([]deps.Repository, error) {
				discovered = true
				return nil, nil
			})
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("preflight error = %v, want %q", err, test.message)
			}
			if discovered {
				t.Fatal("invalid publication option reached fleet discovery")
			}
		})
	}
}
func TestNpmPublishFreshReportRequiresResumeBeforeFleetDiscovery(t *testing.T) {
	t.Parallel()
	options := validNpmPublishOptions()
	options.apply = true
	options.reportDir = t.TempDir()
	releases, err := alignedNpmReleases(testPublicationRequest(options, nil))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := npmrelease.Run(t.Context(), releases, npmrelease.Options{DryRun: true, Ref: options.ref, Registry: options.registry})
	if err != nil {
		t.Fatal(err)
	}
	if err := npmrelease.WriteReport(options.reportDir, plan); err != nil {
		t.Fatal(err)
	}
	discovered := false
	_, err = testPreflightWithDiscovery(&invocation{projectsRoot: t.TempDir()}, options, func(_ context.Context, selection Selection) ([]deps.Repository, error) {
		discovered = true
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "requires --resume") {
		t.Fatalf("fresh apply collision error = %v", err)
	}
	if discovered {
		t.Fatal("fresh apply collision reached fleet discovery")
	}
}
func TestNpmPublishJSONOnlyReportBlocksFreshApplyBeforeFleetDiscovery(t *testing.T) {
	t.Parallel()
	options := validNpmPublishOptions()
	options.apply = true
	options.reportDir = t.TempDir()
	releases, err := alignedNpmReleases(testPublicationRequest(options, nil))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := npmrelease.Run(t.Context(), releases, npmrelease.Options{DryRun: true, Ref: options.ref, Registry: options.registry})
	if err != nil {
		t.Fatal(err)
	}
	jsonReport, err := plan.JSON()
	if err != nil {
		t.Fatal(err)
	}
	jsonPath := filepath.Join(options.reportDir, "npm-publish.json")
	if err := os.WriteFile(jsonPath, jsonReport, 0o644); err != nil {
		t.Fatal(err)
	}
	discovered := false
	_, err = testPreflightWithDiscovery(&invocation{projectsRoot: t.TempDir()}, options, func(_ context.Context, selection Selection) ([]deps.Repository, error) {
		discovered = true
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "requires --resume") {
		t.Fatalf("JSON-only fresh apply collision error = %v", err)
	}
	if discovered {
		t.Fatal("JSON-only fresh apply collision reached fleet discovery")
	}
	if _, statErr := os.Stat(filepath.Join(options.reportDir, "npm-publish.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("fresh apply created YAML alongside JSON-only remnant (stat err=%v)", statErr)
	}
	contents, readErr := os.ReadFile(jsonPath)
	if readErr != nil || !bytes.Equal(contents, jsonReport) {
		t.Fatalf("fresh apply modified JSON-only remnant: contents=%q err=%v", contents, readErr)
	}
}
func TestParseWorkflowInputsScopesValuesToAlignedTuples(t *testing.T) {
	t.Parallel()
	inputs, err := parseWorkflowInputs([]string{"approved=true", "channel=next"}, 1)
	if err != nil || inputs[0]["approved"] != "true" || inputs[0]["channel"] != "next" {
		t.Fatalf("inputs = %#v, err=%v", inputs, err)
	}
	inputs, err = parseWorkflowInputs([]string{"0:package=runtime", "1:package=ui"}, 2)
	if err != nil || inputs[0]["package"] != "runtime" || inputs[1]["package"] != "ui" {
		t.Fatalf("scoped inputs = %#v, err=%v", inputs, err)
	}
	if _, err := parseWorkflowInputs([]string{"approved=true"}, 2); err == nil || !strings.Contains(err.Error(), "must identify its tuple") {
		t.Fatalf("unscoped multi-tuple input error = %v", err)
	}
	if _, err := parseWorkflowInputs([]string{"0:approved=true", "0:approved=false"}, 1); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate input error = %v", err)
	}
}
func TestNpmPublishPlannedEventsUseSharedBumpEventShape(t *testing.T) {
	t.Parallel()
	report := npmrelease.Report{Status: npmrelease.StatusPlanned, Releases: []npmrelease.Receipt{{Release: npmrelease.Release{Package: "@acme/provider", Version: "1.0.0"}}}}
	events := plannedNpmReleaseEvents(report)
	if len(events) != 1 || events[0].Dependency != "@acme/provider" || events[0].Version != "1.0.0" || events[0].Source != "npm_workflow_plan" || events[0].CheckedAt.IsZero() {
		t.Fatalf("planned events = %+v", events)
	}
}
func TestCwDepsAlignedNpmReleasesRequiresAlignedTuples(t *testing.T) {
	t.Parallel()
	if _, err := alignedNpmReleases(PublicationRequest{}); err == nil ||
		!strings.Contains(err.Error(), "all required and repeatable as aligned tuples") {
		t.Fatalf("empty tuples error = %v", err)
	}
	mismatched := cwDepsPublishOptionsFixture()
	mismatched.versions = []string{"1.2.3", "1.2.4"}
	if _, err := alignedNpmReleases(testPublicationRequest(mismatched, nil)); err == nil ||
		!strings.Contains(err.Error(), "must have the same number of values") {
		t.Fatalf("mismatched tuples error = %v", err)
	}
	releases, err := alignedNpmReleases(testPublicationRequest(cwDepsPublishOptionsFixture(), nil))
	if err != nil || len(releases) != 1 {
		t.Fatalf("aligned tuples = %+v, %v", releases, err)
	}
	if releases[0].Repository != "acme/app" || releases[0].Version != "1.2.3" || releases[0].Ref != "main" {
		t.Errorf("release = %+v", releases[0])
	}
	// A bare KEY=VALUE belongs to tuple 0 only when there is one tuple.
	withInput := cwDepsPublishOptionsFixture()
	withInput.workflowInputs = []string{"package=runtime"}
	releases, err = alignedNpmReleases(testPublicationRequest(withInput, nil))
	if err != nil || releases[0].Inputs["package"] != "runtime" {
		t.Fatalf("single tuple bare input = %+v, %v", releases, err)
	}
}
func TestCwDepsParseWorkflowInputsRejectsEveryMalformedShape(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	releases, operation, err := npmPublicationIdentity(testPublicationRequest(cwDepsPublishOptionsFixture(), nil))
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
	if _, _, err := npmPublicationIdentity(testPublicationRequest(bad, nil)); err == nil {
		t.Fatal("an invalid npm version must be refused")
	}
}
func TestCwDepsNpmPublicationPropagationOptionsTurnOffMutationsForPlans(t *testing.T) {
	t.Parallel()
	options := cwDepsPublishOptionsFixture()
	options.commit, options.push, options.pr, options.merge = true, true, true, true
	options.dryRun = false
	plan := npmPublicationPropagationOptions(testPublicationRequest(options, nil).Lifecycle, "/tmp/report", true, false)
	if plan.ReportDir != "/tmp/report" || !plan.DryRun {
		t.Fatalf("plan propagation = %+v", plan)
	}
	if plan.Commit || plan.Push || plan.PR || plan.Merge {
		t.Errorf("a dry-run plan must not carry mutation flags: %+v", plan)
	}
	apply := npmPublicationPropagationOptions(testPublicationRequest(options, nil).Lifecycle, "/tmp/report", false, true)
	if !apply.Merge || !apply.Push || !apply.Resume || apply.DryRun {
		t.Errorf("apply propagation = %+v", apply)
	}
}
func TestCwDepsAttachNpmPropagationOnlyWhenThereIsOne(t *testing.T) {
	t.Parallel()
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
func TestCwDepsNpmPublicationReportPaths(t *testing.T) { //nolint:paralleltest // This original mutates process environment; parallel siblings wait until it returns.
	t.Setenv(wbhome.EnvOverride, t.TempDir())

	requested, err := testNpmPublicationReportDir(&invocation{}, nil, "/tmp/explicit")
	if err != nil || requested != "/tmp/explicit" {
		t.Fatalf("requested report dir = %q, %v", requested, err)
	}
	releases := []npmrelease.Release{cwDepsReleaseFixture()}
	derived, err := testNpmPublicationReportDir(&invocation{}, releases, "")
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
	t.Parallel()
	reportDir := t.TempDir()
	// Nothing persisted and no --resume: a fresh run proceeds with no previous.
	previous, err := testNpmPublicationResumeReport(reportDir, false)
	if err != nil || previous != nil {
		t.Fatalf("fresh report = %+v, %v", previous, err)
	}
	// --resume with nothing persisted names the file it wanted.
	if _, err := testNpmPublicationResumeReport(reportDir, true); err == nil ||
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
	if _, err := testNpmPublicationResumeReport(reportDir, false); err == nil ||
		!strings.Contains(err.Error(), "requires --resume") {
		t.Fatalf("existing report without --resume = %v", err)
	}
	loaded, err := testNpmPublicationResumeReport(reportDir, true)
	if err != nil || loaded == nil || loaded.Operation != written.Operation {
		t.Fatalf("resumed report = %+v, %v", loaded, err)
	}
	// An unreadable pair is refused rather than guessed.
	incompleteDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(incompleteDir, "npm-publish.yaml"), []byte("operation: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := testNpmPublicationResumeReport(incompleteDir, true); err == nil ||
		!strings.Contains(err.Error(), "--resume requires readable") {
		t.Fatalf("incomplete report = %v", err)
	}
}
func TestCwDepsNpmPublicationBumpPreviousBranches(t *testing.T) {
	t.Parallel()
	reportDir := t.TempDir()
	if previous, err := testNpmPublicationBumpPrevious(reportDir, false); err != nil || previous != nil {
		t.Fatalf("no resume = %+v, %v", previous, err)
	}
	// Publication can reach the registry before its first handoff, so a
	// missing downstream report is a normal resume, not an error.
	if previous, err := testNpmPublicationBumpPrevious(reportDir, true); err != nil || previous != nil {
		t.Fatalf("missing downstream report = %+v, %v", previous, err)
	}
	persisted := deps.BumpReport{SchemaVersion: 1, Operation: "deps-bump-cwfixture", Status: "completed", Parallel: 1}
	if err := deps.WriteBumpReports(reportDir, persisted); err != nil {
		t.Fatalf("write bump report: %v", err)
	}
	previous, err := testNpmPublicationBumpPrevious(reportDir, true)
	if err != nil || previous == nil || previous.Operation != persisted.Operation {
		t.Fatalf("loaded downstream report = %+v, %v", previous, err)
	}
	// A corrupt report is an explicit error, never a silent fresh start.
	corrupt := t.TempDir()
	if err := os.WriteFile(filepath.Join(corrupt, "deps-bump.yaml"), []byte("schema_version: [oops\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := testNpmPublicationBumpPrevious(corrupt, true); err == nil ||
		!strings.Contains(err.Error(), "load persisted dependency-wave report") {
		t.Fatalf("corrupt downstream report = %v", err)
	}
}
func TestCwDepsPublicationHasDispatch(t *testing.T) {
	t.Parallel()
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
func TestNpmPublicationSelectionContract(t *testing.T) {
	t.Parallel()
	if err := validateNpmPublicationSelection(PublicationRequest{}); err != nil {
		t.Errorf("empty selection: %v", err)
	}
	invalid := cwDepsPublishOptionsFixture().depsSetOptions
	invalid.regex = "("
	if err := validateNpmPublicationSelection(testPublicationRequest(npmPublishOptions{depsSetOptions: invalid}, nil)); err == nil ||
		!strings.Contains(err.Error(), "invalid --regex") {
		t.Fatalf("invalid regex = %v", err)
	}
	invalid = cwDepsPublishOptionsFixture().depsSetOptions
	invalid.match = "["
	if err := validateNpmPublicationSelection(testPublicationRequest(npmPublishOptions{depsSetOptions: invalid}, nil)); err == nil ||
		!strings.Contains(err.Error(), "invalid --match") {
		t.Fatalf("invalid match = %v", err)
	}
}
func TestCwDepsPreflightNpmPublishRefusals(t *testing.T) {
	t.Parallel()

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
			_, err := testPreflightWithDiscovery(&invocation{projectsRoot: t.TempDir()}, options, func(_ context.Context, selection Selection) ([]deps.Repository, error) {
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
	if _, err := testPreflightWithDiscovery(&invocation{projectsRoot: t.TempDir()}, base, nil); err == nil ||
		!strings.Contains(err.Error(), "fleet discovery is unavailable") {
		t.Fatalf("nil discovery = %v", err)
	}
}
func TestCwDepsPreflightNpmPublishSelectsTheFleet(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()

	options := cwDepsPublishOptionsFixture()
	options.fleet = true
	options.maxWaves = 1
	var seenSelection Selection
	prepared, err := testPreflightWithDiscovery(&invocation{projectsRoot: projectsRoot}, options, func(_ context.Context, selection Selection) ([]deps.Repository, error) {
		seenSelection = selection
		return []deps.Repository{{Slug: "acme/consumer", Path: filepath.Join(projectsRoot, "acme", "consumer")}}, nil
	})
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if seenSelection.ProjectsRoot != projectsRoot || !seenSelection.Fleet || seenSelection.RepositoryPath != "" {
		t.Errorf("discovery selection = %+v", seenSelection)
	}
	if len(prepared.repositories) != 1 || prepared.operation == "" || len(prepared.releases) != 1 || prepared.reportDir == "" {
		t.Fatalf("prepared = %+v", prepared)
	}
	home, homeErr := wbhome.Root(projectsRoot)
	if homeErr != nil {
		t.Fatal(homeErr)
	}
	if !strings.HasPrefix(prepared.reportDir, home) {
		t.Errorf("prepared report dir = %q", prepared.reportDir)
	}
}
func TestCwDepsPreflightNpmPublishApplyRevalidatesTheBumpAfterResumeLookup(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()

	options := cwDepsPublishOptionsFixture()
	options.fleet = true
	options.maxWaves = 1
	options.apply = true
	prepared, err := testPreflightWithDiscovery(&invocation{projectsRoot: projectsRoot}, options, func(_ context.Context, selection Selection) ([]deps.Repository, error) {
		return []deps.Repository{{Slug: "acme/consumer", Path: filepath.Join(projectsRoot, "acme", "consumer")}}, nil
	})
	if err != nil {
		t.Fatalf("apply preflight: %v", err)
	}
	if prepared.bumpPrevious != nil {
		t.Errorf("prepared.bumpPrevious = %+v, want nil without --resume", prepared.bumpPrevious)
	}
	if prepared.reportDir == "" {
		t.Fatalf("prepared = %+v", prepared)
	}
}
func TestCwDepsRunNpmPublishWithPreflightUsesTheInjectedPreflight(t *testing.T) {
	t.Parallel()
	home := t.TempDir()

	options := cwDepsPublishOptionsFixture()
	options.fleet = true
	options.apply = false
	options.maxWaves = 1
	identity, operation, err := npmPublicationIdentity(testPublicationRequest(options, nil))
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	command := cwDepsNewOutCommand(&out)
	command.SetErr(&errOut)

	// A preflight that reports a different operation is refused: the lock was
	// claimed for one campaign and the plan describes another.
	if err := testRunWithPreflight(command, options, func(*invocation, npmPublishOptions) (npmPublishPrepared, error) {
		return npmPublishPrepared{operation: "something-else"}, nil
	}, &invocation{projectsRoot: home}); err == nil || !strings.Contains(err.Error(), "changed the requested operation") {
		t.Fatalf("operation mismatch = %v", err)
	}
	// A failing preflight fails the selection campaign and surfaces the error.
	if err := testRunWithPreflight(command, options, func(*invocation, npmPublishOptions) (npmPublishPrepared, error) {
		return npmPublishPrepared{}, errTestPreflight
	}, &invocation{projectsRoot: home}); err == nil || !strings.Contains(err.Error(), "fixture preflight failure") {
		t.Fatalf("preflight failure = %v", err)
	}
	// A consistent preflight reaches the durable plan.
	prepared := npmPublishPrepared{
		releases:     identity,
		repositories: []deps.Repository{},
		reportDir:    filepath.Join(home, "reports", "cw-npm-preflight"),
		operation:    operation,
	}
	if err := testRunWithPreflight(command, options, func(*invocation, npmPublishOptions) (npmPublishPrepared, error) {
		return prepared, nil
	}, &invocation{projectsRoot: home}); err != nil {
		t.Fatalf("consistent preflight: %v\nstdout: %s", err, out.String())
	}
}
func TestCwDepsAcquireNpmPublicationLocksAndRelease(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	locks, err := testAcquireNpmPublicationLocks(testInvocation(t, root), "deps-npm-publish-cwfixture", []npmrelease.Release{cwDepsReleaseFixture()}, false)
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
