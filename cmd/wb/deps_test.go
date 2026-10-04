package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/npmrelease"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/spf13/cobra"
)

func TestDepsPublishCommandExposesExplicitPublicationAndPropagationFlags(t *testing.T) {
	t.Parallel()
	command := newDepsCmd(&invocation{})
	publish, _, err := command.Find([]string{"publish", "npm"})
	if err != nil || publish == command {
		t.Fatalf("find publish npm: command=%q, error=%v", publish.Name(), err)
	}
	for _, name := range []string{"repo", "workflow", "package", "version", "workflow-input", "registry", "ref", "fleet", "apply", "dry-run", "resume", "workflow-poll", "merge", "report-dir", "format"} {
		if publish.Flags().Lookup(name) == nil {
			t.Errorf("deps publish npm is missing --%s", name)
		}
	}
	if aliases := publish.Parent().Aliases; len(aliases) != 1 || aliases[0] != "release" {
		t.Fatalf("publish parent aliases = %v, want release", aliases)
	}
}

func TestDepsPublishRejectsUnalignedReleaseTuplesBeforeFleetDiscovery(t *testing.T) {
	command := newDepsCmd(&invocation{})
	command.SetArgs([]string{"publish", "npm", "--fleet", "--repo", "acme/provider", "--workflow", "publish.yml", "--package", "@acme/provider", "--version", "1.0.0", "--version", "1.0.1"})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SilenceUsage = true
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "same number of values") {
		t.Fatalf("deps publish npm error = %v, want aligned tuple validation", err)
	}
}

func TestNpmPublishPlanUsesSharedWaveEngineAndPersistsItsReport(t *testing.T) {
	projectsRoot := t.TempDir()
	t.Setenv(wbhome.EnvOverride, projectsRoot)
	reportDir := filepath.Join(t.TempDir(), "report")
	options := validNpmPublishOptions()
	options.reportDir = reportDir
	releases, err := alignedNpmReleases(options)
	if err != nil {
		t.Fatal(err)
	}
	prepared := npmPublishPrepared{releases: releases, reportDir: reportDir, operation: npmrelease.OperationIDFor(releases)}
	var output bytes.Buffer
	command := newRootCmd()
	command.SetOut(&output)
	if err := runPreparedNpmPublish(command, options, prepared, &invocation{projectsRoot: projectsRoot}); err != nil {
		t.Fatalf("default plan shared bump error = %v", err)
	}
	if persisted, err := deps.LoadBumpReport(npmPublicationPlanReportDir(reportDir)); err != nil || persisted.Status != "completed" || !persisted.RegistryLookupsSkipped {
		t.Fatalf("load isolated durable plan report: report=%+v err=%v", persisted, err)
	}
	if !strings.Contains(output.String(), `"status": "planned"`) || !strings.Contains(output.String(), `"propagation"`) {
		t.Fatalf("default plan output = %s", output.String())
	}
}

func TestNpmPublishPlanRetainsDuplicatePackageFleetFinding(t *testing.T) {
	projectsRoot := t.TempDir()
	t.Setenv(wbhome.EnvOverride, projectsRoot)
	reportDir := filepath.Join(t.TempDir(), "report")
	first := npmPublicationTestRepository(t, projectsRoot, "acme", "one", "@acme/duplicate")
	second := npmPublicationTestRepository(t, projectsRoot, "acme", "two", "@acme/duplicate")
	options := validNpmPublishOptions()
	options.reportDir = reportDir
	releases, err := alignedNpmReleases(options)
	if err != nil {
		t.Fatal(err)
	}
	prepared := npmPublishPrepared{releases: releases, reportDir: reportDir, operation: npmrelease.OperationIDFor(releases), repositories: []deps.Repository{first, second}}
	command := newRootCmd()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	err = runPreparedNpmPublish(command, options, prepared, &invocation{projectsRoot: projectsRoot})
	if err == nil || !strings.Contains(err.Error(), "npm package @acme/duplicate is declared by") {
		t.Fatalf("duplicate fleet plan error = %v", err)
	}
	if persisted, loadErr := deps.LoadBumpReport(npmPublicationPlanReportDir(reportDir)); loadErr != nil || persisted.Status != "failed" {
		t.Fatalf("durable duplicate finding = %+v, err=%v", persisted, loadErr)
	}
}

func TestNpmPublishPreflightRejectsInvalidOptionsBeforeFleetDiscovery(t *testing.T) {
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
		{name: "format", change: func(options *npmPublishOptions) { options.format = "toml" }, message: "unknown --format"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := validNpmPublishOptions()
			test.change(&options)
			discovered := false
			_, err := preflightNpmPublishWithDiscovery(&invocation{projectsRoot: t.TempDir()}, options, func(*invocation, []string, depsSetOptions) ([]deps.Repository, error) {
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
	options := validNpmPublishOptions()
	options.apply = true
	options.reportDir = t.TempDir()
	releases, err := alignedNpmReleases(options)
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
	_, err = preflightNpmPublishWithDiscovery(&invocation{projectsRoot: t.TempDir()}, options, func(*invocation, []string, depsSetOptions) ([]deps.Repository, error) {
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
	options := validNpmPublishOptions()
	options.apply = true
	options.reportDir = t.TempDir()
	releases, err := alignedNpmReleases(options)
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
	_, err = preflightNpmPublishWithDiscovery(&invocation{projectsRoot: t.TempDir()}, options, func(*invocation, []string, depsSetOptions) ([]deps.Repository, error) {
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

func TestNpmPublishRealAcceptanceCampaignPlansAsOneOperation(t *testing.T) {
	projectsRoot := t.TempDir()
	t.Setenv(wbhome.EnvOverride, projectsRoot)
	reportDir := filepath.Join(t.TempDir(), "report")
	var prepared npmPublishPrepared
	runCalls := 0
	var output bytes.Buffer
	command := newNpmPublishCmdWithRun(&invocation{projectsRoot: projectsRoot}, func(command *cobra.Command, options npmPublishOptions, inv *invocation) error {
		runCalls++
		return runNpmPublishWithPreflight(command, options, func(_ *invocation, options npmPublishOptions) (npmPublishPrepared, error) {
			var err error
			prepared, err = preflightNpmPublishWithDiscovery(&invocation{projectsRoot: projectsRoot}, options, func(*invocation, []string, depsSetOptions) ([]deps.Repository, error) {
				return nil, nil
			})
			return prepared, err
		}, inv)
	})
	command.SetOut(&output)
	command.SetErr(io.Discard)
	command.SilenceUsage = true
	command.SetArgs(realNpmPublishAcceptanceArgs(reportDir))
	if err := command.Execute(); err != nil {
		t.Fatalf("single campaign plan error = %v", err)
	}
	if runCalls != 1 {
		t.Fatalf("campaign command runs = %d, want one", runCalls)
	}
	if len(prepared.releases) != 3 ||
		prepared.releases[0].Repository != "sneat-co/assetus" || prepared.releases[0].Workflow != "release-frontend.yml" || prepared.releases[0].Package != "@sneat/extension-assetus" || prepared.releases[0].Version != "0.1.0" || len(prepared.releases[0].Inputs) != 0 ||
		prepared.releases[1].Repository != "sneat-co/eventius" || prepared.releases[1].Workflow != "release-frontend.yml" || prepared.releases[1].Package != "@sneat/extension-eventius" || prepared.releases[1].Version != "0.0.1" || prepared.releases[1].Inputs["package"] != "runtime" ||
		prepared.releases[2].Repository != "sneat-co/eventius" || prepared.releases[2].Workflow != "release-frontend.yml" || prepared.releases[2].Package != "@sneat/extension-eventius-ui" || prepared.releases[2].Version != "0.0.1" || prepared.releases[2].Inputs["package"] != "ui" {
		t.Fatalf("acceptance tuples = %+v", prepared.releases)
	}
	if !strings.Contains(output.String(), `"status": "planned"`) || !strings.Contains(output.String(), "@sneat/extension-eventius-ui") || !strings.Contains(output.String(), `"propagation"`) {
		t.Fatalf("single campaign plan output = %s", output.String())
	}
}

func TestNpmPublishCommandRejectsSeparatedDuplicateTuplesBeforeFleetDiscoveryOrWorkflowDispatch(t *testing.T) {
	reportDir := filepath.Join(t.TempDir(), "report")
	fleetDiscoveryCalls := 0
	commandRuns := 0
	command := newNpmPublishCmdWithRun(&invocation{}, func(command *cobra.Command, options npmPublishOptions, inv *invocation) error {
		commandRuns++
		return runNpmPublishWithPreflight(command, options, func(*invocation, npmPublishOptions) (npmPublishPrepared, error) {
			fleetDiscoveryCalls++
			return npmPublishPrepared{}, nil
		}, inv)
	})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SilenceUsage = true
	command.SetArgs([]string{
		"--fleet", "--report-dir", reportDir,
		"--repo", "sneat-co/assetus", "--workflow", "release-frontend.yml", "--package", "@sneat/extension-assetus", "--version", "0.1.0",
		"--repo", "sneat-co/eventius", "--workflow", "release-frontend.yml", "--package", "@sneat/extension-eventius", "--version", "0.0.1",
		"--repo", "sneat-co/assetus", "--workflow", "release-frontend.yml", "--package", "@sneat/extension-assetus", "--version", "0.1.0",
		"--workflow-input", "1:package=runtime",
	})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "duplicate npm release tuple") {
		t.Fatalf("separated duplicate command error = %v", err)
	}
	if commandRuns != 1 || fleetDiscoveryCalls != 0 {
		t.Fatalf("duplicate command runs=%d fleet discoveries=%d, want one command and zero discovery/dispatch paths", commandRuns, fleetDiscoveryCalls)
	}
	if _, statErr := os.Stat(filepath.Join(reportDir, "npm-publish.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("duplicate command created a publication receipt before refusal (stat err=%v)", statErr)
	}
}

func TestRunNpmPublishPlanRefusesActiveOperationLock(t *testing.T) {
	projectsRoot := t.TempDir()
	t.Setenv(wbhome.EnvOverride, projectsRoot)
	options := validNpmPublishOptions()
	options.reportDir = t.TempDir()
	releases, err := alignedNpmReleases(options)
	if err != nil {
		t.Fatal(err)
	}
	prepared := npmPublishPrepared{releases: releases, reportDir: options.reportDir, operation: npmrelease.OperationIDFor(releases)}
	owner, err := orchestrate.AcquireOperationLock(projectsRoot, prepared.operation, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Release() }()
	command := newRootCmd()
	command.SetOut(io.Discard)
	err = runPreparedNpmPublish(command, options, prepared, &invocation{})
	if err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("active plan lock error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(npmPublicationPlanReportDir(options.reportDir), "deps-bump.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("active lock advanced plan report before refusal (stat err=%v)", statErr)
	}
}

func TestRunNpmPublishApplyRefusesActiveOperationLockBeforeFleetDiscovery(t *testing.T) {
	projectsRoot := t.TempDir()
	t.Setenv(wbhome.EnvOverride, projectsRoot)
	options := validNpmPublishOptions()
	options.apply = true
	options.reportDir = t.TempDir()
	releases, err := alignedNpmReleases(options)
	if err != nil {
		t.Fatal(err)
	}
	prepared := npmPublishPrepared{
		releases: releases, reportDir: options.reportDir,
		operation: npmrelease.OperationIDFor(releases),
	}
	owner, err := orchestrate.AcquireOperationLock(projectsRoot, prepared.operation, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Release() }()
	command := newRootCmd()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	preflightCalls := 0
	err = runNpmPublishWithPreflight(command, options, func(_ *invocation, got npmPublishOptions) (npmPublishPrepared, error) {
		preflightCalls++
		if got.apply != options.apply {
			t.Fatalf("apply option changed before lock acquisition")
		}
		return prepared, nil
	}, &invocation{})
	if err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("active publication lock error = %v", err)
	}
	if preflightCalls != 0 {
		t.Fatalf("fleet preflight calls = %d, want 0 after active-lock refusal", preflightCalls)
	}
	if _, statErr := os.Stat(filepath.Join(options.reportDir, "npm-publish.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("active lock advanced publication report before refusal (stat err=%v)", statErr)
	}
}

func TestNpmPublicationClaimLocksRejectOverlappingSubsetAndSupersetCampaigns(t *testing.T) {
	projectsRoot := t.TempDir()
	t.Setenv(wbhome.EnvOverride, projectsRoot)

	assetus := validNpmPublishOptions()
	assetus.repositories = []string{"sneat-co/assetus"}
	assetus.workflows = []string{"release-frontend.yml"}
	assetus.packages = []string{"@sneat/extension-assetus"}
	assetus.versions = []string{"0.1.0"}
	assetusReleases, err := alignedNpmReleases(assetus)
	if err != nil {
		t.Fatal(err)
	}
	assetusOperation := npmrelease.OperationIDFor(assetusReleases)
	assetusLocks, err := acquireNpmPublicationLocks(&invocation{}, assetusOperation, assetusReleases, false)
	if err != nil {
		t.Fatal(err)
	}
	defer assetusLocks.Release()

	superset := realNpmPublishAcceptanceOptions()
	supersetReleases, err := alignedNpmReleases(superset)
	if err != nil {
		t.Fatal(err)
	}
	supersetOperation := npmrelease.OperationIDFor(supersetReleases)
	if supersetOperation == assetusOperation {
		t.Fatalf("subset and superset unexpectedly share campaign operation %q", assetusOperation)
	}
	if _, err := acquireNpmPublicationLocks(&invocation{}, supersetOperation, supersetReleases, false); err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("overlapping publication claim acquisition error = %v, want active Assetus claim refusal", err)
	}
}

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

func realNpmPublishAcceptanceOptions() npmPublishOptions {
	options := validNpmPublishOptions()
	options.repositories = []string{"sneat-co/assetus", "sneat-co/eventius", "sneat-co/eventius"}
	options.workflows = []string{"release-frontend.yml", "release-frontend.yml", "release-frontend.yml"}
	options.packages = []string{"@sneat/extension-assetus", "@sneat/extension-eventius", "@sneat/extension-eventius-ui"}
	options.versions = []string{"0.1.0", "0.0.1", "0.0.1"}
	options.workflowInputs = []string{"1:package=runtime", "2:package=ui"}
	return options
}

func realNpmPublishAcceptanceArgs(reportDir string) []string {
	return []string{
		"--fleet", "--match", "sneat-co/*", "--format", "json", "--report-dir", reportDir,
		"--repo", "sneat-co/assetus", "--workflow", "release-frontend.yml", "--package", "@sneat/extension-assetus", "--version", "0.1.0",
		"--repo", "sneat-co/eventius", "--workflow", "release-frontend.yml", "--package", "@sneat/extension-eventius", "--version", "0.0.1",
		"--repo", "sneat-co/eventius", "--workflow", "release-frontend.yml", "--package", "@sneat/extension-eventius-ui", "--version", "0.0.1",
		"--workflow-input", "1:package=runtime", "--workflow-input", "2:package=ui",
	}
}

func npmPublicationTestRepository(t *testing.T, root, owner, name, packageName string) deps.Repository {
	t.Helper()
	directory := filepath.Join(root, owner, name)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	scratchGit(t, directory, "init", "-q", "-b", "main")
	manifest := `{"name":"` + packageName + `","version":"1.0.0"}` + "\n"
	if err := os.WriteFile(filepath.Join(directory, "package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	scratchGit(t, directory, "add", "package.json")
	scratchGit(t, directory, "commit", "-q", "-m", "seed")
	origin := bareOrigin(t)
	scratchGit(t, directory, "remote", "add", "origin", origin)
	scratchGit(t, directory, "push", "-q", "-u", "origin", "main")
	return deps.Repository{Slug: owner + "/" + name, Path: directory, CloneURL: origin}
}

func TestParseWorkflowInputsScopesValuesToAlignedTuples(t *testing.T) {
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
	report := npmrelease.Report{Status: npmrelease.StatusPlanned, Releases: []npmrelease.Receipt{{Release: npmrelease.Release{Package: "@acme/provider", Version: "1.0.0"}}}}
	events := plannedNpmReleaseEvents(report)
	if len(events) != 1 || events[0].Dependency != "@acme/provider" || events[0].Version != "1.0.0" || events[0].Source != "npm_workflow_plan" || events[0].CheckedAt.IsZero() {
		t.Fatalf("planned events = %+v", events)
	}
}

func TestNpmPublishOutputOmitsTokenLookingValueUnderSafeInputKey(t *testing.T) {
	const value = "token-looking-value-must-not-be-printed"
	release := npmrelease.Release{
		Repository: "sneat-co/eventius", Workflow: "release-frontend.yml",
		Package: "@sneat/extension-eventius", Version: "0.0.1", Ref: "main",
		Inputs: map[string]string{"package": value},
	}
	releases, err := npmrelease.Normalize([]npmrelease.Release{release}, "main")
	if err != nil {
		t.Fatalf("safe workflow-input key was heuristically rejected: %v", err)
	}
	publication, err := npmrelease.Run(t.Context(), releases, npmrelease.Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"json", "yaml", "markdown"} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer
			command := newRootCmd()
			command.SetOut(&output)
			if err := writeNpmPublishOutput(command, npmPublishOutput{Publication: publication}, format); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output.String(), value) {
				t.Fatalf("%s stdout leaked a workflow input value: %s", format, output.String())
			}
		})
	}
}

// `--scope` only means something to `--latest`. Accepting it silently would
// let an operator believe a campaign was narrowed to one scope when it was
// seeded from whatever `--changed` happened to say, so the flag combination is
// refused with the two ways out named.

// The complement: --latest with no scope is refused too, before any repository
// is discovered. A registry sweep with no selection is not a default anyone
// wants applied to a whole fleet.
