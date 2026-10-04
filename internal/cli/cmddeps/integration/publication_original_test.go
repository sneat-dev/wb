package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cli/cmddeps"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/depsrun"
	"github.com/sneat-dev/wb/internal/npmrelease"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/spf13/cobra"
)

type depsSetOptions struct {
	fleet, dryRun, resume, noVerify              bool
	commit, push, pr, merge                      bool
	match, regex, ref, checks, format, reportDir string
	parallel, maxWaves                           int
	timeout, releasePoll, refreshAfter           time.Duration
}

type invocation struct{ projectsRoot string }
type npmPublishOptions struct {
	depsSetOptions
	repositories, workflows, packages, versions, workflowInputs []string
	registry                                                    string
	workflowPoll                                                time.Duration
	apply                                                       bool
	runner                                                      npmrelease.CommandRunner
}
type npmPublishPrepared struct {
	releases             []npmrelease.Release
	checks               []quality.Check
	repositories         []deps.Repository
	reportDir, operation string
}
type exitError struct {
	code    int
	message string
}

func (e *exitError) Error() string { return e.message }

func newRootCmd() *cobra.Command { return &cobra.Command{} }
func cwDepsNewOutCommand(out io.Writer) *cobra.Command {
	command := &cobra.Command{}
	command.SetOut(out)
	return command
}
func publicationRoot(inv *invocation) string {
	if inv != nil && inv.projectsRoot != "" {
		return inv.projectsRoot
	}
	return os.Getenv(wbhome.EnvOverride)
}
func publicationArgs(options npmPublishOptions) []string {
	args := []string{"npm", "--ref", options.ref, "--registry", options.registry, "--format", options.format, "--parallel", strconv.Itoa(options.parallel), "--max-waves", strconv.Itoa(options.maxWaves), "--timeout", options.timeout.String(), "--workflow-poll", options.workflowPoll.String(), "--release-poll", options.releasePoll.String(), "--refresh-after", options.refreshAfter.String()}
	for _, pair := range []struct {
		flag   string
		values []string
	}{{"repo", options.repositories}, {"workflow", options.workflows}, {"package", options.packages}, {"version", options.versions}, {"workflow-input", options.workflowInputs}} {
		for _, v := range pair.values {
			args = append(args, "--"+pair.flag, v)
		}
	}
	for _, pair := range []struct {
		flag    string
		enabled bool
	}{{"fleet", options.fleet}, {"apply", options.apply}, {"dry-run", options.dryRun}, {"resume", options.resume}, {"merge", options.merge}, {"commit", options.commit}, {"push", options.push}, {"pr", options.pr}, {"no-verify", options.noVerify}} {
		if pair.enabled {
			args = append(args, "--"+pair.flag)
		}
	}
	if options.reportDir != "" {
		args = append(args, "--report-dir", options.reportDir)
	}
	if options.match != "" {
		args = append(args, "--match", options.match)
	}
	if options.regex != "" {
		args = append(args, "--regex", options.regex)
	}
	if options.checks != "" {
		args = append(args, "--checks", options.checks)
	}
	return args
}
func publicationFamily(inv *invocation, options npmPublishOptions, repositories []deps.Repository, defaultSelect bool, selectSpy func()) *cobra.Command {
	effects := depsrun.DefaultPublicationDependencies(io.Discard)
	if !defaultSelect {
		effects.Select = func(context.Context, depsrun.Selection) ([]deps.Repository, error) {
			if selectSpy != nil {
				selectSpy()
			}
			return repositories, nil
		}
	}
	if options.runner != nil {
		effects.Run = func(ctx context.Context, releases []npmrelease.Release, runOptions npmrelease.Options) (npmrelease.Report, error) {
			runOptions.Runner = options.runner
			return npmrelease.Run(ctx, releases, runOptions)
		}
	}
	runtime := shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: publicationRoot(inv), NonInteractive: true} }, ExitError: func(code int, message string) error { return &exitError{code: code, message: message} }}
	service := depsrun.NewPublication(effects, runtime.ExitError)
	command := cmddeps.NewPublish(runtime, cmddeps.PublicationOperations(service))
	command.SilenceUsage = true
	command.SilenceErrors = true
	return command
}
func runPreparedNpmPublish(command *cobra.Command, options npmPublishOptions, prepared npmPublishPrepared, inv *invocation) error {
	options.reportDir = prepared.reportDir
	family := publicationFamily(inv, options, prepared.repositories, false, nil)
	family.SetOut(command.OutOrStdout())
	family.SetErr(command.ErrOrStderr())
	family.SetContext(command.Context())
	family.SetArgs(publicationArgs(options))
	return family.Execute()
}
func runPreparedNpmPublishLocked(command *cobra.Command, options npmPublishOptions, prepared npmPublishPrepared, inv *invocation) error {
	return runPreparedNpmPublish(command, options, prepared, inv)
}
func alignedNpmReleases(options npmPublishOptions) ([]npmrelease.Release, error) {
	out := make([]npmrelease.Release, len(options.repositories))
	for i := range out {
		out[i] = npmrelease.Release{Repository: options.repositories[i], Workflow: options.workflows[i], Package: options.packages[i], Version: options.versions[i], Ref: options.ref}
	}
	return npmrelease.Normalize(out, options.ref)
}
func npmPublicationPlanReportDir(dir string) string { return filepath.Join(dir, "plan") }
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
func cwDepsWorkflowRunFixture(id, status, conclusion string, headSHA string, created time.Time) string {
	return fmt.Sprintf(
		`{"databaseId":%s,"headSha":%q,"status":%q,"conclusion":%q,"event":"workflow_dispatch","createdAt":%q,"updatedAt":%q,"url":%q}`,
		id, headSHA, status, conclusion, created.UTC().Format(time.RFC3339), created.UTC().Add(time.Second).Format(time.RFC3339),
		"https://github.com/acme/app/actions/runs/"+id)
}

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
func scratchGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.com", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.com")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s %v", args, out, err)
	}
}
func bareOrigin(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	scratchGit(t, root, "init", "--bare", "-b", "main")
	testenv.ConfigureGitAutoMaintenanceOff(t, root)
	return root
}
func TestNpmPublishPlanUsesSharedWaveEngineAndPersistsItsReport(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
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
	t.Parallel()
	projectsRoot := t.TempDir()
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
func TestNpmPublishRealAcceptanceCampaignPlansAsOneOperation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	options := realNpmPublishAcceptanceOptions()
	options.reportDir = filepath.Join(root, "report")
	var observed depsrun.PublicationOutput
	effects := depsrun.DefaultPublicationDependencies(io.Discard)
	effects.Select = func(context.Context, depsrun.Selection) ([]deps.Repository, error) { return nil, nil }
	service := depsrun.NewPublication(effects, func(_ int, s string) error { return errors.New(s) })
	runtime := shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: root, NonInteractive: true} }, ExitError: func(_ int, s string) error { return errors.New(s) }}
	ops := cmddeps.PublicationOperations(service)
	calls := 0
	run := ops.Run
	ops.Run = func(ctx context.Context, req depsrun.PublicationRequest, cb depsrun.PublicationCallbacks) error {
		calls++
		emit := cb.Emit
		cb.Emit = func(out depsrun.PublicationOutput) error { observed = out; return emit(out) }
		return run(ctx, req, cb)
	}
	family := cmddeps.NewPublish(runtime, ops)
	var out bytes.Buffer
	family.SetOut(&out)
	family.SetErr(io.Discard)
	family.SetArgs(append([]string{"npm"}, realNpmPublishAcceptanceArgs(options.reportDir)...))
	if err := family.Execute(); err != nil {
		t.Fatal(err)
	}
	releases := observed.Publication.Releases
	if calls != 1 || len(releases) != 3 || releases[0].Repository != "sneat-co/assetus" || releases[0].Workflow != "release-frontend.yml" || releases[0].Package != "@sneat/extension-assetus" || releases[0].Version != "0.1.0" || len(releases[0].Inputs) != 0 || releases[1].Repository != "sneat-co/eventius" || releases[1].Workflow != "release-frontend.yml" || releases[1].Package != "@sneat/extension-eventius" || releases[1].Version != "0.0.1" || releases[1].Inputs["package"] != "runtime" || releases[2].Repository != "sneat-co/eventius" || releases[2].Workflow != "release-frontend.yml" || releases[2].Package != "@sneat/extension-eventius-ui" || releases[2].Version != "0.0.1" || releases[2].Inputs["package"] != "ui" {
		t.Fatalf("actual tuples=%+v calls=%d", releases, calls)
	}
	if !strings.Contains(out.String(), `"status": "planned"`) || !strings.Contains(out.String(), "@sneat/extension-eventius-ui") || !strings.Contains(out.String(), `"propagation"`) {
		t.Fatal(out.String())
	}
}
func TestNpmPublishCommandRejectsSeparatedDuplicateTuplesBeforeFleetDiscoveryOrWorkflowDispatch(t *testing.T) {
	t.Parallel()
	reportDir := filepath.Join(t.TempDir(), "report")
	fleetDiscoveryCalls := 0
	commandRuns := 0
	effects := depsrun.DefaultPublicationDependencies(io.Discard)
	effects.Select = func(context.Context, depsrun.Selection) ([]deps.Repository, error) {
		fleetDiscoveryCalls++
		return nil, nil
	}
	runtime := shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: t.TempDir(), NonInteractive: true} }, ExitError: func(_ int, s string) error { return errors.New(s) }}
	ops := cmddeps.PublicationOperations(depsrun.NewPublication(effects, runtime.ExitError))
	run := ops.Run
	ops.Run = func(ctx context.Context, req depsrun.PublicationRequest, cb depsrun.PublicationCallbacks) error {
		commandRuns++
		return run(ctx, req, cb)
	}
	family := cmddeps.NewPublish(runtime, ops)
	child, _, _ := family.Find([]string{"npm"})
	family.RemoveCommand(child)
	command := child
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
func TestRunNpmPublishPlanRefusesActiveOperationLock(t *testing.T) { //nolint:paralleltest // This original mutates process environment; parallel siblings wait until it returns.
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
	t.Cleanup(func() { _ = owner.Release() })
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
	t.Parallel()
	projectsRoot := t.TempDir()
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
	t.Cleanup(func() { _ = owner.Release() })
	command := newRootCmd()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	preflightCalls := 0
	family := publicationFamily(&invocation{projectsRoot: projectsRoot}, options, nil, false, func() { preflightCalls++ })
	family.SetOut(command.OutOrStdout())
	family.SetErr(command.ErrOrStderr())
	family.SetArgs(publicationArgs(options))
	err = family.Execute()
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
	t.Parallel()
	root := t.TempDir()
	assetus := realNpmPublishAcceptanceOptions()
	assetus.repositories = assetus.repositories[:1]
	assetus.workflows = assetus.workflows[:1]
	assetus.packages = assetus.packages[:1]
	assetus.versions = assetus.versions[:1]
	assetus.workflowInputs = nil
	releases, err := alignedNpmReleases(assetus)
	if err != nil {
		t.Fatal(err)
	}
	claim := npmrelease.PublicationClaimOperationIDs(releases)[0]
	lock, err := orchestrate.AcquireOperationLock(root, claim, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	superset := realNpmPublishAcceptanceOptions()
	superset.reportDir = filepath.Join(root, "report")
	superReleases, err := alignedNpmReleases(superset)
	if err != nil {
		t.Fatal(err)
	}
	if npmrelease.OperationIDFor(superReleases) == npmrelease.OperationIDFor(releases) {
		t.Fatal("campaign identities collapsed")
	}
	selected := 0
	family := publicationFamily(&invocation{projectsRoot: root}, superset, nil, false, func() { selected++ })
	family.SetOut(io.Discard)
	family.SetErr(io.Discard)
	family.SetArgs(publicationArgs(superset))
	if err := family.Execute(); err == nil || !strings.Contains(err.Error(), "already active") || selected != 0 {
		t.Fatalf("claim=%v selection=%d", err, selected)
	}
	campaign, err := orchestrate.AcquireOperationLock(root, npmrelease.OperationIDFor(superReleases), false)
	if err != nil {
		t.Fatalf("partial claim leaked campaign: %v", err)
	}
	_ = campaign.Release()
}
func TestRunPreparedNpmPublishApplyDispatchFailureWritesFailedReport(t *testing.T) {
	t.Parallel()
	home := t.TempDir()

	runner := &cwDepsFakeNpmReleaseRunner{steps: []npmrelease.CommandResult{
		{Code: 1, Err: errors.New("cgxc2 fixture: resolve head failed")},
	}}
	options := cwDepsPublishOptionsFixture()
	options.fleet = true
	options.apply = true
	options.maxWaves = 1
	options.runner = runner
	prepared := npmPublishPrepared{
		releases:  []npmrelease.Release{cwDepsReleaseFixture()},
		checks:    []quality.Check{},
		reportDir: filepath.Join(home, "reports", "cgxc2-npm-apply-fail"),
		operation: "deps-npm-publish-cgxc2-fail",
	}
	var out bytes.Buffer
	command := cwDepsNewOutCommand(&out)
	err := runPreparedNpmPublishLocked(command, options, prepared, &invocation{projectsRoot: home})
	if err == nil {
		t.Fatalf("expected publication failure error, got nil")
	}
	exitErr, ok := err.(*exitError)
	if !ok {
		t.Fatalf("err = %v (%T), want *exitError", err, err)
	}
	if exitErr.code != exitFindings {
		t.Errorf("exitErr.code = %d, want %d", exitErr.code, exitFindings)
	}
	if !strings.Contains(err.Error(), "npm publication did not reach registry evidence") {
		t.Fatalf("err = %v", err)
	}
	persisted, readErr := os.ReadFile(filepath.Join(prepared.reportDir, "npm-publish.json"))
	if readErr != nil {
		t.Fatalf("read persisted report: %v", readErr)
	}
	if !json.Valid(persisted) {
		t.Fatalf("persisted report is not valid JSON: %s", persisted)
	}
	var report npmrelease.Report
	if err := json.Unmarshal(persisted, &report); err != nil {
		t.Fatalf("decode persisted report: %v", err)
	}
	if report.Status == npmrelease.StatusPublished {
		t.Errorf("persisted report status = %v, want a non-published status on failure", report.Status)
	}
}
func TestPreflightNpmPublishDelegatesToRealDependencyDiscovery(t *testing.T) { //nolint:paralleltest // Actual fake gh executable is selected through process PATH.
	root := t.TempDir()
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in *login*) echo 'cwcov-npm-user';; *) echo '[]';; esac\n"
	path := filepath.Join(bin, "gh")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	options := validNpmPublishOptions()
	family := publicationFamily(&invocation{projectsRoot: root}, options, nil, true, nil)
	family.SetArgs(publicationArgs(options))
	family.SetOut(io.Discard)
	family.SetErr(io.Discard)
	if err := family.Execute(); err == nil || !strings.Contains(err.Error(), "no repositories match the selected fleet filters") {
		t.Fatalf("actual discovery refusal=%v", err)
	}
}
func TestCwDepsRunPreparedNpmPublishApplyDispatchesAndVerifiesRegistry(t *testing.T) {
	t.Parallel()
	home := t.TempDir()

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
	if err := runPreparedNpmPublishLocked(command, options, prepared, &invocation{projectsRoot: home}); err != nil {
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
func TestCwDepsRunPreparedNpmPublishPlan(t *testing.T) {
	t.Parallel()
	home := t.TempDir()

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
	err := runPreparedNpmPublishLocked(command, options, prepared, &invocation{projectsRoot: home})
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
func TestCwDepsRunPreparedNpmPublishRefusesExistingReportWithoutResume(t *testing.T) { //nolint:paralleltest // This original mutates process environment; parallel siblings wait until it returns.
	home := t.TempDir()
	t.Setenv(wbhome.EnvOverride, home)

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
func TestDepsRootCompositeAdapterKeepsHomeEngineAndReportCustody(t *testing.T) {
	t.Parallel()
	home, engine := t.TempDir(), t.TempDir()
	events := []deps.ReleaseEvent{{Dependency: "github.com/acme/provider", Version: "v1.2.0", Source: "explicit"}}
	service := depsrun.New(depsrun.DefaultDependencies(io.Discard))
	result, err := service.Bump(context.Background(), depsrun.BumpRequest{ProjectsRoot: home, Events: events, Options: deps.BumpOptions{Options: deps.Options{GitHubDir: engine, Ref: "main", DryRun: true, Parallel: 1}, Ecosystem: deps.EcosystemGo, MaxWaves: 1, NoRegistry: true}})
	report, directory := result.Report, result.ReportDir
	if err != nil || report.Operation == "" || report.GitHubDir != engine || !strings.HasPrefix(directory, filepath.Join(home, ".wb", "reports")) {
		t.Fatalf("report=%+v directory=%q err=%v", report, directory, err)
	}
	persisted, err := deps.LoadBumpReport(directory)
	if err != nil || persisted.Operation != report.Operation || !persisted.RegistryLookupsSkipped {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}
}
