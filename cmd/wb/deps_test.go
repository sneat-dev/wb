package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/npmrelease"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/spf13/cobra"
)

func TestGitHubSlugSupportsSSHAndHTTPS(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"git@github.com:strongo/cicd.git":       "strongo/cicd",
		"https://github.com/sneat-dev/wb.git":   "sneat-dev/wb",
		"ssh://git@github.com/dal-go/dalgo.git": "dal-go/dalgo",
	}
	for remote, want := range tests {
		if got := githubSlug(remote); got != want {
			t.Errorf("githubSlug(%q) = %q, want %q", remote, got, want)
		}
	}
}

func TestDepsCommandExposesCumulativeLifecycleFlags(t *testing.T) {
	t.Parallel()
	command := newDepsSetCmd()
	for _, name := range []string{"commit", "push", "pr", "merge", "parallel", "resume", "retry", "timeout", "propagate", "max-waves", "release-poll", "refresh-after", "dependency-order", "layer"} {
		if command.Flags().Lookup(name) == nil {
			t.Errorf("deps set is missing --%s", name)
		}
	}
}

func TestDepsCommandIncludesBumpWithWaveLifecycleFlags(t *testing.T) {
	t.Parallel()
	command := newDepsCmd()
	bump, _, err := command.Find([]string{"bump"})
	if err != nil || bump == command {
		t.Fatalf("find bump: command=%q, error=%v", bump.Name(), err)
	}
	for _, name := range []string{"changed", "fleet", "parallel", "max-waves", "release-poll", "refresh-after", "resume", "commit", "push", "pr", "merge"} {
		if bump.Flags().Lookup(name) == nil {
			t.Errorf("deps bump is missing --%s", name)
		}
	}
}

func TestExecuteDepsBumpResumeHonorsExplicitParallelAndRetainsPersistedParallel(t *testing.T) {
	reportDir := t.TempDir()
	events := []deps.ReleaseEvent{{Dependency: "@acme/provider", Version: "0.2.0", Source: "explicit", CheckedAt: time.Unix(1, 0)}}
	persisted := deps.BumpReport{
		SchemaVersion: 1, Operation: deps.BumpOperationIDFor(deps.EcosystemNPM, events), Status: "planned", Ecosystem: deps.EcosystemNPM,
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

	explicitCommand := newDepsBumpCmd()
	if err := explicitCommand.Flags().Set("parallel", "2"); err != nil {
		t.Fatal(err)
	}
	explicitParallel, err := explicitCommand.Flags().GetInt("parallel")
	if err != nil {
		t.Fatal(err)
	}
	explicitExecution, explicit, err := resolveDepsBumpResumeParallel(deps.Options{Parallel: explicitParallel}, loaded, depsBumpParallelExplicit(explicitCommand))
	if err != nil {
		t.Fatalf("resolve explicit resume parallelism: %v", err)
	}
	if explicitExecution.Parallel != 2 || explicit.Parallel != 2 {
		t.Fatalf("explicit runtime/report parallel = %d/%d, want 2/2", explicitExecution.Parallel, explicit.Parallel)
	}
	if !reflect.DeepEqual(loaded.Waves, explicit.Waves) {
		t.Fatalf("explicit parallelism changed consumer waves: before=%+v after=%+v", loaded.Waves, explicit.Waves)
	}
	if err := deps.WriteBumpReports(reportDir, explicit); err != nil {
		t.Fatalf("write explicit resume report: %v", err)
	}
	if persisted, err := deps.LoadBumpReport(reportDir); err != nil || persisted.Parallel != 2 {
		t.Fatalf("load explicit resume report: report=%+v err=%v", persisted, err)
	}

	omittedCommand := newDepsBumpCmd()
	omittedParallel, err := omittedCommand.Flags().GetInt("parallel")
	if err != nil {
		t.Fatal(err)
	}
	omittedExecution, omitted, err := resolveDepsBumpResumeParallel(deps.Options{Parallel: omittedParallel}, explicit, depsBumpParallelExplicit(omittedCommand))
	if err != nil {
		t.Fatalf("resolve omitted resume parallelism: %v", err)
	}
	if omittedExecution.Parallel != 2 || omitted.Parallel != 2 {
		t.Fatalf("omitted runtime/report parallel = %d/%d, want persisted 2/2", omittedExecution.Parallel, omitted.Parallel)
	}
	if !reflect.DeepEqual(explicit.Waves, omitted.Waves) {
		t.Fatalf("omitted parallelism changed consumer waves: before=%+v after=%+v", explicit.Waves, omitted.Waves)
	}
	if persisted, err := deps.LoadBumpReport(reportDir); err != nil || persisted.Parallel != 2 {
		t.Fatalf("load omitted resume report: report=%+v err=%v", persisted, err)
	}
}

func TestDepsPublishCommandExposesExplicitPublicationAndPropagationFlags(t *testing.T) {
	t.Parallel()
	command := newDepsCmd()
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
	command := newDepsCmd()
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
	previousProjectsRoot := projectsRoot
	projectsRoot = t.TempDir()
	defer func() { projectsRoot = previousProjectsRoot }()
	t.Setenv(wbhome.EnvOverride, t.TempDir())
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
	if err := runPreparedNpmPublish(command, options, prepared); err != nil {
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
	previousProjectsRoot := projectsRoot
	projectsRoot = t.TempDir()
	defer func() { projectsRoot = previousProjectsRoot }()
	t.Setenv(wbhome.EnvOverride, t.TempDir())
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
	err = runPreparedNpmPublish(command, options, prepared)
	if err == nil || !strings.Contains(err.Error(), "npm package @acme/duplicate is declared by") {
		t.Fatalf("duplicate fleet plan error = %v", err)
	}
	if persisted, loadErr := deps.LoadBumpReport(npmPublicationPlanReportDir(reportDir)); loadErr != nil || persisted.Status != "failed" {
		t.Fatalf("durable duplicate finding = %+v, err=%v", persisted, loadErr)
	}
}

func TestNpmPublishPreflightRejectsInvalidOptionsBeforeFleetDiscovery(t *testing.T) {
	previousProjectsRoot := projectsRoot
	projectsRoot = t.TempDir()
	defer func() { projectsRoot = previousProjectsRoot }()
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
			_, err := preflightNpmPublishWithDiscovery(options, func([]string, depsSetOptions) ([]deps.Repository, error) {
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
	previousProjectsRoot := projectsRoot
	projectsRoot = t.TempDir()
	defer func() { projectsRoot = previousProjectsRoot }()
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
	_, err = preflightNpmPublishWithDiscovery(options, func([]string, depsSetOptions) ([]deps.Repository, error) {
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
	previousProjectsRoot := projectsRoot
	projectsRoot = t.TempDir()
	defer func() { projectsRoot = previousProjectsRoot }()
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
	_, err = preflightNpmPublishWithDiscovery(options, func([]string, depsSetOptions) ([]deps.Repository, error) {
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
	previousProjectsRoot := projectsRoot
	projectsRoot = t.TempDir()
	defer func() { projectsRoot = previousProjectsRoot }()
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	reportDir := filepath.Join(t.TempDir(), "report")
	var prepared npmPublishPrepared
	runCalls := 0
	var output bytes.Buffer
	command := newNpmPublishCmdWithRun(func(command *cobra.Command, options npmPublishOptions) error {
		runCalls++
		return runNpmPublishWithPreflight(command, options, func(options npmPublishOptions) (npmPublishPrepared, error) {
			var err error
			prepared, err = preflightNpmPublishWithDiscovery(options, func([]string, depsSetOptions) ([]deps.Repository, error) {
				return nil, nil
			})
			return prepared, err
		})
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
	previousProjectsRoot := projectsRoot
	projectsRoot = t.TempDir()
	defer func() { projectsRoot = previousProjectsRoot }()
	reportDir := filepath.Join(t.TempDir(), "report")
	fleetDiscoveryCalls := 0
	commandRuns := 0
	command := newNpmPublishCmdWithRun(func(command *cobra.Command, options npmPublishOptions) error {
		commandRuns++
		return runNpmPublishWithPreflight(command, options, func(npmPublishOptions) (npmPublishPrepared, error) {
			fleetDiscoveryCalls++
			return npmPublishPrepared{}, nil
		})
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
	previousProjectsRoot := projectsRoot
	projectsRoot = t.TempDir()
	defer func() { projectsRoot = previousProjectsRoot }()
	t.Setenv(wbhome.EnvOverride, t.TempDir())
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
	err = runPreparedNpmPublish(command, options, prepared)
	if err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("active plan lock error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(npmPublicationPlanReportDir(options.reportDir), "deps-bump.yaml")); !os.IsNotExist(statErr) {
		t.Fatalf("active lock advanced plan report before refusal (stat err=%v)", statErr)
	}
}

func TestRunNpmPublishApplyRefusesActiveOperationLockBeforeFleetDiscovery(t *testing.T) {
	previousProjectsRoot := projectsRoot
	projectsRoot = t.TempDir()
	defer func() { projectsRoot = previousProjectsRoot }()
	t.Setenv(wbhome.EnvOverride, t.TempDir())
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
	err = runNpmPublishWithPreflight(command, options, func(got npmPublishOptions) (npmPublishPrepared, error) {
		preflightCalls++
		if got.apply != options.apply {
			t.Fatalf("apply option changed before lock acquisition")
		}
		return prepared, nil
	})
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
	previousProjectsRoot := projectsRoot
	projectsRoot = t.TempDir()
	defer func() { projectsRoot = previousProjectsRoot }()
	t.Setenv(wbhome.EnvOverride, t.TempDir())

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
	assetusLocks, err := acquireNpmPublicationLocks(assetusOperation, assetusReleases, false)
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
	if _, err := acquireNpmPublicationLocks(supersetOperation, supersetReleases, false); err == nil || !strings.Contains(err.Error(), "already active") {
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

func TestDepsCommandIncludesGraphViewsAndBrowserReportFlags(t *testing.T) {
	t.Parallel()
	command := newDepsCmd()
	graph, _, err := command.Find([]string{"graph"})
	if err != nil || graph == command {
		t.Fatalf("find graph: command=%q, error=%v", graph.Name(), err)
	}
	for _, name := range []string{"fleet", "match", "regex", "ref", "parallel", "dependency", "view", "format", "report-dir", "open"} {
		if graph.Flags().Lookup(name) == nil {
			t.Errorf("deps graph is missing --%s", name)
		}
	}
}

func TestDepsSetRejectsUnusableDependencyOrderCombinations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args    []string
		message string
	}{
		{args: []string{"go", "example.com/a@v1.0.0", "--layer", "0"}, message: "--layer requires --dependency-order"},
		{args: []string{"github-actions", "acme/cicd@v1.0.0", "--dependency-order"}, message: "only for the go ecosystem"},
		{args: []string{"go", "example.com/a@v1.0.0", "--dependency-order", "--propagate", "--fleet"}, message: "cannot be used together"},
		{args: []string{"go", "example.com/a@v1.0.0", "--dependency-order", "--layer", "two"}, message: "invalid layer selection"},
	}
	for _, test := range tests {
		command := newDepsSetCmd()
		command.SetArgs(test.args)
		command.SetOut(io.Discard)
		command.SetErr(io.Discard)
		command.SilenceUsage = true
		err := command.Execute()
		if err == nil || !strings.Contains(err.Error(), test.message) {
			t.Errorf("deps set %v error = %v, want %q", test.args, err, test.message)
		}
	}
}

func TestParseReleaseEventsPreservesMultipleExactSeeds(t *testing.T) {
	t.Parallel()
	events, err := parseReleaseEvents(deps.EcosystemGo, []string{"example.com/a@v0.2.0", "example.com/b@v1.3.0"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0] != (deps.ReleaseEvent{Dependency: "example.com/a", Version: "v0.2.0", Source: "explicit"}) {
		t.Fatalf("events = %+v", events)
	}
}

func TestParseReleaseEventsSupportsScopedNpmPackages(t *testing.T) {
	t.Parallel()
	events, err := parseReleaseEvents(deps.EcosystemNPM, []string{"@sneat/core@2.3.1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0] != (deps.ReleaseEvent{Dependency: "@sneat/core", Version: "2.3.1", Source: "explicit"}) {
		t.Fatalf("events = %+v", events)
	}
}

func TestDepsGraphAndBumpAcceptNpmEcosystem(t *testing.T) {
	t.Parallel()
	graph := newDepsGraphCmd()
	graph.SetArgs([]string{"--ecosystem", "cobol", "--fleet"})
	graph.SetOut(io.Discard)
	graph.SetErr(io.Discard)
	graph.SilenceUsage = true
	if err := graph.Execute(); err == nil || !strings.Contains(err.Error(), "go and npm ecosystems") {
		t.Fatalf("deps graph --ecosystem cobol error = %v, want a go/npm ecosystem rejection", err)
	}

	bump := newDepsBumpCmd()
	bump.SetArgs([]string{"cobol", "--fleet", "--changed", "example.com/a@v1.0.0"})
	bump.SetOut(io.Discard)
	bump.SetErr(io.Discard)
	bump.SilenceUsage = true
	if err := bump.Execute(); err == nil || !strings.Contains(err.Error(), "go and npm ecosystems") {
		t.Fatalf("deps bump cobol error = %v, want a go/npm ecosystem rejection", err)
	}
}
