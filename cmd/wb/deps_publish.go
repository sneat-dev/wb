package main

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/encode"
	"github.com/sneat-dev/wb/internal/npmrelease"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// npmPublishOptions deliberately embeds the existing deps-bump lifecycle
// options. The publication command therefore passes one typed lifecycle into
// the same recalculated wave engine instead of growing a second orchestration
// path.
type npmPublishOptions struct {
	depsSetOptions
	repositories   []string
	workflows      []string
	packages       []string
	versions       []string
	workflowInputs []string
	registry       string
	workflowPoll   time.Duration
	apply          bool
}

type npmPublishOutput struct {
	Publication npmrelease.Report `json:"publication" yaml:"publication"`
	Propagation *deps.BumpReport  `json:"propagation,omitempty" yaml:"propagation,omitempty"`
}

type npmPublishPrepared struct {
	releases     []npmrelease.Release
	checks       []quality.Check
	repositories []deps.Repository
	reportDir    string
	operation    string
	previous     *npmrelease.Report
	bumpPrevious *deps.BumpReport
}

func newDepsPublishCmd() *cobra.Command {
	command := &cobra.Command{
		Use:     "publish",
		Aliases: []string{"release"},
		Short:   "Publish approved npm packages through repository workflows, verify the registry, and propagate dependency waves",
	}
	command.AddCommand(newNpmPublishCmd())
	return command
}

func newNpmPublishCmd() *cobra.Command {
	return newNpmPublishCmdWithRun(runNpmPublish)
}

// newNpmPublishCmdWithRun keeps Cobra parsing testable without allowing tests
// to replace the production publication path. The production constructor above
// always supplies runNpmPublish; tests can supply a recorder and then exercise
// the same no-I/O preflight/plan seams explicitly.
func newNpmPublishCmdWithRun(run func(*cobra.Command, npmPublishOptions) error) *cobra.Command {
	options := npmPublishOptions{}
	command := &cobra.Command{
		Use:   "npm",
		Short: "Publish approved npm packages through repository workflows, verify the registry, and propagate dependency waves",
		Long: `Publishes explicitly named npm package releases through the owning
repository's GitHub Actions workflow. The workflow remains the only publisher:
WB never accepts an npm token or runs npm publish. The default is a dry-run
plan; --apply dispatches each exact repository/workflow/package/version tuple,
waits for its exact workflow run and head, verifies the requested version in
the npm registry, then hands the confirmed events to the same recalculated
deps bump engine used by ` + "`wb deps bump npm`" + `.

A plan validates the explicit publication tuples and invokes the existing
dependency-wave engine in dry-run mode. This preserves real fleet findings
(such as duplicate declarations) and a durable wave report below
` + "`<report-dir>/plan`" + `, but it never dispatches a GitHub Actions workflow,
queries the npm registry, or changes downstream dependency files.

Use --resume with the same tuples and --report-dir after a workflow or registry
failure. A receipt that already has a dispatch timestamp is never dispatched
again. --merge is an independent explicit opt-in for downstream dependency PR
publication; without it the confirmed events are passed to deps bump in
dry-run mode.

Each repeatable --repo, --workflow, --package, and --version flag contributes
one aligned tuple. Workflow inputs are tuple-scoped: use
--workflow-input INDEX:KEY=VALUE (zero-based INDEX), for example
--workflow-input 0:package=runtime --workflow-input 1:package=ui. A bare
KEY=VALUE is accepted only for a one-tuple command and belongs to tuple 0.
Inputs are passed in deterministic key order to their repository-owned
workflow dispatch only.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return run(command, options)
		},
	}
	command.Flags().StringArrayVar(&options.repositories, "repo", nil, "provider GitHub repository owner/name (repeatable, aligned with --workflow/--package/--version)")
	command.Flags().StringArrayVar(&options.workflows, "workflow", nil, "repository-owned release workflow file ending .yml or .yaml (repeatable, aligned)")
	command.Flags().StringArrayVar(&options.packages, "package", nil, "exact npm package name (repeatable, aligned)")
	command.Flags().StringArrayVar(&options.versions, "version", nil, "exact npm semver without a v prefix (repeatable, aligned)")
	command.Flags().StringArrayVar(&options.workflowInputs, "workflow-input", nil, "tuple-scoped workflow_dispatch input INDEX:KEY=VALUE (repeatable; bare KEY=VALUE only for one tuple)")
	command.Flags().StringVar(&options.registry, "registry", "https://registry.npmjs.org", "npm registry URL used only for read-only release evidence")
	command.Flags().StringVar(&options.ref, "ref", "main", "provider branch whose exact head is dispatched")
	command.Flags().BoolVar(&options.fleet, "fleet", false, "select downstream dependency consumers from the local and owned repository fleet")
	command.Flags().StringVar(&options.match, "match", "", "glob matched against downstream org/repo, e.g. sneat-co/*")
	command.Flags().StringVar(&options.regex, "regex", "", "regular expression matched against downstream org/repo")
	command.Flags().IntVar(&options.parallel, "parallel", 1, "maximum downstream repositories or wave observations concurrently")
	command.Flags().IntVar(&options.maxWaves, "max-waves", 20, "maximum recalculated downstream dependency waves")
	command.Flags().DurationVar(&options.workflowPoll, "workflow-poll", 30*time.Second, "interval between exact GitHub workflow run observations")
	command.Flags().DurationVar(&options.releasePoll, "release-poll", 30*time.Second, "interval between downstream published-release observations")
	command.Flags().DurationVar(&options.refreshAfter, "refresh-after", 5*time.Minute, "recheck stale downstream release events before CI (0 disables)")
	command.Flags().BoolVar(&options.apply, "apply", false, "dispatch repository-owned workflows and perform registry verification")
	command.Flags().BoolVar(&options.dryRun, "dry-run", false, "plan publication and downstream waves without workflow dispatch, npm registry queries, or dependency-file changes")
	command.Flags().BoolVar(&options.resume, "resume", false, "resume the persisted publication and dependency-wave reports without redispatching receipted workflows")
	command.Flags().BoolVar(&options.allowDowngrade, "allow-downgrade", false, "permit downstream dependency references lower than an observed version")
	command.Flags().StringVar(&options.checks, "checks", "", "downstream checks: lint,test,build (default all)")
	command.Flags().BoolVar(&options.noVerify, "no-verify", false, "explicitly skip downstream local verification")
	command.Flags().DurationVar(&options.timeout, "timeout", 30*time.Minute, "maximum duration per workflow, registry, downstream check, or CI wait")
	command.Flags().IntVar(&options.retry, "retry", 0, "additional attempts for eligible downstream external commands")
	command.Flags().BoolVar(&options.commit, "commit", false, "commit downstream dependency changes (requires --merge for this command)")
	command.Flags().BoolVar(&options.push, "push", false, "push downstream dependency branches (requires --merge for this command)")
	command.Flags().BoolVar(&options.pr, "pr", false, "open downstream dependency pull requests (requires --merge for this command)")
	command.Flags().BoolVar(&options.merge, "merge", false, "publish and merge passing downstream dependency waves after exact CI evidence")
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "stable directory for npm-publish.yaml/json and apply deps-bump reports (plans use plan/deps-bump)")
	return command
}

func runNpmPublish(command *cobra.Command, options npmPublishOptions) error {
	return runNpmPublishWithPreflight(command, options, preflightNpmPublish)
}

type npmPublishPreflight func(npmPublishOptions) (npmPublishPrepared, error)

func runNpmPublishWithPreflight(command *cobra.Command, options npmPublishOptions, preflight npmPublishPreflight) error {
	releases, operation, err := npmPublicationIdentity(options)
	if err != nil {
		return err
	}
	// Claim the report campaign and every package-version publication before
	// preflight selects the fleet. A concurrent invocation must fail without
	// even walking downstream repositories, including an overlapping subset or
	// superset campaign that would otherwise dispatch the same npm version.
	locks, err := acquireNpmPublicationLocks(operation, releases, options.resume)
	if err != nil {
		return err
	}
	defer locks.Release()
	prepared, err := preflight(options)
	if err != nil {
		return err
	}
	if prepared.operation != operation {
		return fmt.Errorf("npm publication preflight changed the requested operation; refusing to dispatch")
	}
	return runPreparedNpmPublishLocked(command, options, prepared)
}

// npmPublicationIdentity validates the explicit tuple identity needed to
// acquire campaign and package-version locks. The complete option and fleet
// preflight still runs below those locks and always before workflow dispatch.
func npmPublicationIdentity(options npmPublishOptions) ([]npmrelease.Release, string, error) {
	releases, err := alignedNpmReleases(options)
	if err != nil {
		return nil, "", err
	}
	normalized, err := npmrelease.Normalize(releases, options.ref)
	if err != nil {
		return nil, "", err
	}
	return normalized, npmrelease.OperationIDFor(normalized), nil
}

type npmPublicationLocks struct {
	locks []orchestrate.OperationLock
}

// acquireNpmPublicationLocks holds the campaign/report lock plus one claim
// for every npm package-version in sorted order. Claims stay independent of
// workflow inputs and the surrounding campaign so overlap is fail-closed even
// when two callers use different report directories or tuple sets.
func acquireNpmPublicationLocks(operation string, releases []npmrelease.Release, resume bool) (npmPublicationLocks, error) {
	campaign, err := orchestrate.AcquireOperationLock(projectsRoot, operation, resume)
	if err != nil {
		return npmPublicationLocks{}, err
	}
	locks := npmPublicationLocks{locks: []orchestrate.OperationLock{campaign}}
	for _, claim := range npmrelease.PublicationClaimOperationIDs(releases) {
		lock, err := orchestrate.AcquireOperationLock(projectsRoot, claim, resume)
		if err != nil {
			locks.Release()
			return npmPublicationLocks{}, fmt.Errorf("acquire npm publication claim %q: %w", claim, err)
		}
		locks.locks = append(locks.locks, lock)
	}
	return locks, nil
}

func (locks npmPublicationLocks) Release() {
	for index := len(locks.locks) - 1; index >= 0; index-- {
		_ = locks.locks[index].Release()
	}
}

// runPreparedNpmPublish owns the irreversible boundary after the complete
// preflight has selected a stable fleet. Keeping it separate makes the
// campaign-lock contract directly testable without a live GitHub or npm call.
func runPreparedNpmPublish(command *cobra.Command, options npmPublishOptions, prepared npmPublishPrepared) error {
	// Every path below writes a durable report. Take both campaign and
	// package-version locks before either a dry-run plan or --apply can touch
	// it, so a plan cannot overwrite an in-progress apply/resume handoff and an
	// overlapping campaign cannot publish the same npm version concurrently.
	locks, err := acquireNpmPublicationLocks(prepared.operation, prepared.releases, options.resume)
	if err != nil {
		return err
	}
	defer locks.Release()
	return runPreparedNpmPublishLocked(command, options, prepared)
}

func runPreparedNpmPublishLocked(command *cobra.Command, options npmPublishOptions, prepared npmPublishPrepared) error {
	publication, err := plannedNpmPublication(commandExecutionContext(command), prepared, options)
	if err != nil {
		return err
	}
	if !options.apply {
		// A plan deliberately reaches the existing recalculated wave engine in
		// dry-run mode. It never calls GitHub's workflow dispatch or npm, but it
		// does retain true fleet findings (including duplicate declarations)
		// rather than making a provider-only plan look deceptively clean.
		// Its report lives below /plan and can never overwrite an apply/resume
		// deps-bump receipt in the publication report directory.
		planPrepared := prepared
		planPrepared.reportDir = npmPublicationPlanReportDir(prepared.reportDir)
		bumpReport, bumpErr := runNpmPublicationBump(command, planPrepared, options, plannedNpmReleaseEvents(publication), true, false, true)
		attachNpmPropagation(&publication, bumpReport)
		if err := writeNpmPublishOutput(command, npmPublishOutput{Publication: publication, Propagation: publication.Propagation}, options.format); err != nil {
			return err
		}
		return bumpErr
	}

	// Recheck inside the operation lock: a concurrent invocation cannot create
	// a fresh report between preflight and the irreversible dispatch boundary.
	previous, err := npmPublicationResumeReport(prepared.reportDir, options.resume)
	if err != nil {
		return err
	}
	prepared.previous = previous
	prepared.bumpPrevious, err = npmPublicationBumpPrevious(prepared.reportDir, options.resume)
	if err != nil {
		return err
	}
	if err := validateNpmPublicationBump(options, prepared.checks, prepared.reportDir, releaseEventsForReleases(prepared.releases), !options.merge, prepared.bumpPrevious != nil, prepared.bumpPrevious); err != nil {
		return err
	}

	var preDispatchBump deps.BumpReport
	if !publicationHasDispatch(previous) {
		preDispatchBump, err = runNpmPublicationBump(command, prepared, options, plannedNpmReleaseEvents(publication), true, false, true)
		attachNpmPropagation(&publication, preDispatchBump)
		if err != nil {
			if outputErr := writeNpmPublishOutput(command, npmPublishOutput{Publication: publication, Propagation: publication.Propagation}, options.format); outputErr != nil {
				return outputErr
			}
			return err
		}
	}

	publication, publicationErr := npmrelease.Run(commandExecutionContext(command), prepared.releases, npmrelease.Options{
		Apply: true, Resume: options.resume, Ref: options.ref,
		Timeout: options.timeout, PollInterval: options.workflowPoll, Registry: options.registry,
		ReportDir: prepared.reportDir, Previous: prepared.previous,
		Persist: func(report npmrelease.Report) error { return npmrelease.WriteReport(prepared.reportDir, report) },
	})
	if preDispatchBump.Operation != "" {
		attachNpmPropagation(&publication, preDispatchBump)
	}
	if publicationErr != nil {
		if err := npmrelease.WriteReport(prepared.reportDir, publication); err != nil {
			return err
		}
		if outputErr := writeNpmPublishOutput(command, npmPublishOutput{Publication: publication, Propagation: publication.Propagation}, options.format); outputErr != nil {
			return outputErr
		}
		return &exitError{code: exitFindings, message: "npm publication did not reach registry evidence: " + publicationErr.Error()}
	}
	events, err := npmrelease.EventsFor(publication)
	if err != nil {
		return err
	}
	resumeBump := options.resume && prepared.bumpPrevious != nil
	bumpReport, bumpErr := runNpmPublicationBump(command, prepared, options, events, !options.merge, resumeBump, false)
	attachNpmPropagation(&publication, bumpReport)
	if err := npmrelease.WriteReport(prepared.reportDir, publication); err != nil {
		return err
	}
	if err := writeNpmPublishOutput(command, npmPublishOutput{Publication: publication, Propagation: publication.Propagation}, options.format); err != nil {
		return err
	}
	return bumpErr
}

// npmRepositoryDiscovery keeps the option-validation boundary testable: all
// command and propagation flags must be rejected before fleet discovery (and
// therefore before any provider workflow can possibly be dispatched).
type npmRepositoryDiscovery func([]string, depsSetOptions) ([]deps.Repository, error)

func preflightNpmPublish(options npmPublishOptions) (npmPublishPrepared, error) {
	return preflightNpmPublishWithDiscovery(options, dependencyRepositories)
}

func preflightNpmPublishWithDiscovery(options npmPublishOptions, discover npmRepositoryDiscovery) (npmPublishPrepared, error) {
	if !options.fleet {
		return npmPublishPrepared{}, fmt.Errorf("deps publish npm requires --fleet for downstream propagation")
	}
	if !options.apply && options.resume {
		return npmPublishPrepared{}, fmt.Errorf("--resume requires --apply")
	}
	if (options.commit || options.push || options.pr || options.merge) && !options.apply {
		return npmPublishPrepared{}, fmt.Errorf("--commit, --push, --pr, and --merge require --apply")
	}
	if options.apply && options.dryRun {
		return npmPublishPrepared{}, fmt.Errorf("--apply and --dry-run cannot be used together")
	}
	if (options.commit || options.push || options.pr) && !options.merge {
		return npmPublishPrepared{}, fmt.Errorf("--commit, --push, and --pr require --merge for deps publish npm")
	}
	if options.noVerify && options.checks != "" {
		return npmPublishPrepared{}, fmt.Errorf("--no-verify and --checks cannot be used together")
	}
	if err := validateNpmPublishFormat(options.format); err != nil {
		return npmPublishPrepared{}, err
	}
	if err := validateNpmPublicationSelection(options); err != nil {
		return npmPublishPrepared{}, err
	}
	releases, err := alignedNpmReleases(options)
	if err != nil {
		return npmPublishPrepared{}, err
	}
	normalized, err := npmrelease.Normalize(releases, options.ref)
	if err != nil {
		return npmPublishPrepared{}, err
	}
	if err := npmrelease.ValidateOptions(npmrelease.Options{
		Apply: options.apply, DryRun: options.dryRun, Resume: options.resume, Timeout: options.timeout,
		PollInterval: options.workflowPoll, Registry: options.registry,
	}); err != nil {
		return npmPublishPrepared{}, err
	}
	checks, err := quality.ParseChecks(options.checks)
	if err != nil {
		return npmPublishPrepared{}, err
	}
	// Validate both downstream modes before creating a report directory or
	// discovering the fleet. The report directory does not participate in
	// normalization, so the user-supplied value is sufficient for this pure
	// preflight pass.
	events := releaseEventsForReleases(normalized)
	if err := validateNpmPublicationBump(options, checks, options.reportDir, events, true, false, nil); err != nil {
		return npmPublishPrepared{}, err
	}
	if options.apply {
		if err := validateNpmPublicationBump(options, checks, options.reportDir, events, !options.merge, false, nil); err != nil {
			return npmPublishPrepared{}, err
		}
	}
	reportDir, err := npmPublicationReportDir(normalized, options.reportDir)
	if err != nil {
		return npmPublishPrepared{}, err
	}
	prepared := npmPublishPrepared{
		releases: normalized, checks: checks, reportDir: reportDir,
		operation: npmrelease.OperationIDFor(normalized),
	}
	if options.apply {
		prepared.previous, err = npmPublicationResumeReport(reportDir, options.resume)
		if err != nil {
			return npmPublishPrepared{}, err
		}
	}
	if options.resume {
		prepared.bumpPrevious, err = npmPublicationBumpPrevious(reportDir, true)
		if err != nil {
			return npmPublishPrepared{}, err
		}
	}
	if options.apply {
		if err := validateNpmPublicationBump(options, checks, reportDir, events, !options.merge, prepared.bumpPrevious != nil, prepared.bumpPrevious); err != nil {
			return npmPublishPrepared{}, err
		}
	}
	// Fleet selection itself is part of preflight: invalid filters, an empty
	// fleet, or local discovery constraints fail before a provider is allowed to
	// dispatch anything.
	if discover == nil {
		return npmPublishPrepared{}, fmt.Errorf("npm publication fleet discovery is unavailable")
	}
	prepared.repositories, err = discover([]string{"npm", "events"}, options.depsSetOptions)
	if err != nil {
		return npmPublishPrepared{}, err
	}
	return prepared, nil
}

func validateNpmPublishFormat(format string) error {
	switch format {
	case "markdown", "yaml", "json":
		return nil
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, or json)", format)
	}
}

func validateNpmPublicationSelection(options npmPublishOptions) error {
	if _, err := compileDependencyRegex(options.regex); err != nil {
		return err
	}
	if options.match != "" {
		if _, err := path.Match(options.match, ""); err != nil {
			return fmt.Errorf("invalid --match: %w", err)
		}
	}
	return nil
}

func npmPublicationReportDir(releases []npmrelease.Release, requested string) (string, error) {
	if requested != "" {
		return requested, nil
	}
	home, err := wbhome.EnsureRoot(projectsRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "reports", npmrelease.OperationIDFor(releases)), nil
}

func npmPublicationPlanReportDir(reportDir string) string {
	return filepath.Join(reportDir, "plan")
}

func npmPublicationResumeReport(reportDir string, resume bool) (*npmrelease.Report, error) {
	exists, err := npmrelease.ReportExists(reportDir)
	if err != nil {
		return nil, fmt.Errorf("inspect npm publication report: %w", err)
	}
	if !resume && exists {
		return nil, fmt.Errorf("existing npm publication report in %s requires --resume; refusing to overwrite or redispatch", reportDir)
	}
	if !resume {
		return nil, nil
	}
	if !exists {
		return nil, fmt.Errorf("--resume requires %s", filepath.Join(reportDir, "npm-publish.yaml"))
	}
	loaded, err := npmrelease.LoadReport(reportDir)
	if err != nil {
		return nil, fmt.Errorf("--resume requires readable %s: %w", filepath.Join(reportDir, "npm-publish.yaml"), err)
	}
	return &loaded, nil
}

func npmPublicationBumpPrevious(reportDir string, resume bool) (*deps.BumpReport, error) {
	if !resume {
		return nil, nil
	}
	previous, err := deps.LoadBumpReport(reportDir)
	if os.IsNotExist(err) {
		// Publication can have reached the registry before its first handoff.
		// In that case --resume starts the shared bump engine exactly once rather
		// than treating a missing downstream report as permission to redispatch.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load persisted dependency-wave report: %w", err)
	}
	return &previous, nil
}

func publicationHasDispatch(report *npmrelease.Report) bool {
	if report == nil {
		return false
	}
	for _, receipt := range report.Releases {
		if !receipt.DispatchAt.IsZero() {
			return true
		}
	}
	return false
}

func plannedNpmPublication(ctx context.Context, prepared npmPublishPrepared, options npmPublishOptions) (npmrelease.Report, error) {
	return npmrelease.Run(ctx, prepared.releases, npmrelease.Options{
		DryRun: true, Ref: options.ref, Registry: options.registry, Timeout: options.timeout, PollInterval: options.workflowPoll,
	})
}

func releaseEventsForReleases(releases []npmrelease.Release) []deps.ReleaseEvent {
	events := make([]deps.ReleaseEvent, len(releases))
	checkedAt := time.Now().UTC()
	for index, release := range releases {
		events[index] = deps.ReleaseEvent{Dependency: release.Package, Version: release.Version, Source: "npm_workflow_plan", CheckedAt: checkedAt}
	}
	return events
}

func validateNpmPublicationBump(options npmPublishOptions, checks []quality.Check, reportDir string, events []deps.ReleaseEvent, dryRun bool, resume bool, previous *deps.BumpReport) error {
	propagation := npmPublicationPropagationOptions(options, reportDir, dryRun, resume)
	return deps.ValidateBumpOptions(deps.BumpOptions{
		Options: dependencyOptions(propagation, checks), Ecosystem: deps.EcosystemNPM,
		MaxWaves: options.maxWaves, PollInterval: options.releasePoll, RefreshAfter: options.refreshAfter,
		Previous: previous,
	}, events)
}

func npmPublicationPropagationOptions(options npmPublishOptions, reportDir string, dryRun, resume bool) depsSetOptions {
	propagation := options.depsSetOptions
	propagation.fleet = true
	propagation.reportDir = reportDir
	propagation.dryRun = dryRun
	propagation.resume = resume
	if dryRun {
		propagation.commit = false
		propagation.push = false
		propagation.pr = false
		propagation.merge = false
	}
	return propagation
}

func runNpmPublicationBump(command *cobra.Command, prepared npmPublishPrepared, options npmPublishOptions, events []deps.ReleaseEvent, dryRun, resume, noRegistry bool) (deps.BumpReport, error) {
	propagation := npmPublicationPropagationOptions(options, prepared.reportDir, dryRun, resume)
	report, _, err := executeDepsBumpWithRegistryPolicy(command, deps.EcosystemNPM, events, prepared.repositories, propagation, dependencyOptions(propagation, prepared.checks), noRegistry)
	return report, err
}

func attachNpmPropagation(publication *npmrelease.Report, propagation deps.BumpReport) {
	if propagation.Operation == "" {
		return
	}
	publication.PropagationOperation = propagation.Operation
	publication.Propagation = &propagation
}

func alignedNpmReleases(options npmPublishOptions) ([]npmrelease.Release, error) {
	lengths := []int{len(options.repositories), len(options.workflows), len(options.packages), len(options.versions)}
	for _, length := range lengths {
		if length == 0 {
			return nil, fmt.Errorf("--repo, --workflow, --package, and --version are all required and repeatable as aligned tuples")
		}
	}
	for _, length := range lengths[1:] {
		if length != lengths[0] {
			return nil, fmt.Errorf("--repo, --workflow, --package, and --version must have the same number of values; got %d, %d, %d, %d", lengths[0], lengths[1], lengths[2], lengths[3])
		}
	}
	inputs, err := parseWorkflowInputs(options.workflowInputs, lengths[0])
	if err != nil {
		return nil, err
	}
	releases := make([]npmrelease.Release, lengths[0])
	for index := range releases {
		releases[index] = npmrelease.Release{
			Repository: options.repositories[index], Workflow: options.workflows[index],
			Package: options.packages[index], Version: options.versions[index], Ref: options.ref,
			Inputs: inputs[index],
		}
	}
	return releases, nil
}

func parseWorkflowInputs(values []string, tupleCount int) ([]map[string]string, error) {
	if tupleCount < 1 {
		return nil, fmt.Errorf("workflow input tuple count must be positive")
	}
	inputs := make([]map[string]string, tupleCount)
	for _, value := range values {
		scope, raw, found := strings.Cut(value, "=")
		if !found {
			return nil, fmt.Errorf("invalid --workflow-input (want INDEX:KEY=VALUE)")
		}
		index := 0
		key := strings.TrimSpace(scope)
		if prefix, scopedKey, scoped := strings.Cut(key, ":"); scoped {
			if prefix == "" {
				return nil, fmt.Errorf("invalid --workflow-input (want INDEX:KEY=VALUE)")
			}
			parsed, err := strconv.Atoi(prefix)
			if err != nil || parsed < 0 || (len(prefix) > 1 && prefix[0] == '0') {
				return nil, fmt.Errorf("invalid --workflow-input tuple index %q (want a zero-based integer)", prefix)
			}
			index = parsed
			key = strings.TrimSpace(scopedKey)
		} else if tupleCount > 1 {
			return nil, fmt.Errorf("--workflow-input must identify its tuple as INDEX:KEY=VALUE when multiple releases are requested")
		}
		if npmrelease.IsSecretLikeWorkflowInputKey(key) {
			return nil, fmt.Errorf("workflow input name is secret-like; credentials must remain in repository-owned GitHub Actions secrets")
		}
		if index >= tupleCount || key == "" || strings.ContainsAny(key, ":=\r\n") || strings.ContainsAny(raw, "\r\n") {
			return nil, fmt.Errorf("invalid --workflow-input (tuple index or key/value is invalid)")
		}
		if inputs[index] == nil {
			inputs[index] = make(map[string]string)
		}
		if _, exists := inputs[index][key]; exists {
			return nil, fmt.Errorf("duplicate --workflow-input key %q for tuple %d", key, index)
		}
		inputs[index][key] = raw
	}
	return inputs, nil
}

func plannedNpmReleaseEvents(report npmrelease.Report) []deps.ReleaseEvent {
	releases := make([]npmrelease.Release, len(report.Releases))
	for index, receipt := range report.Releases {
		releases[index] = receipt.Release
	}
	return releaseEventsForReleases(releases)
}

func writeNpmPublishOutput(command *cobra.Command, output npmPublishOutput, format string) error {
	// Keep the machine-readable composite output on the same report generation
	// contract as the durable YAML/JSON receipt.
	output.Publication = output.Publication.WithGeneration()
	switch format {
	case "json":
		raw, err := encode.JSON(output)
		if err != nil {
			return err
		}
		_, err = command.OutOrStdout().Write(raw)
		return err
	case "yaml":
		raw, err := yaml.Marshal(output)
		if err != nil {
			return err
		}
		_, err = command.OutOrStdout().Write(raw)
		return err
	case "markdown":
		return writeNpmPublishMarkdown(command, output)
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, or json)", format)
	}
}

func writeNpmPublishMarkdown(command *cobra.Command, output npmPublishOutput) error {
	var builder strings.Builder
	builder.WriteString("# WB npm publication and dependency waves\n\n")
	fmt.Fprintf(&builder, "- Operation: `%s`\n", output.Publication.Operation)
	fmt.Fprintf(&builder, "- Publication status: `%s`\n", output.Publication.Status)
	fmt.Fprintf(&builder, "- Ref: `%s`\n\n", output.Publication.Ref)
	builder.WriteString("## Workflow and registry receipts\n\n")
	builder.WriteString("| Repository | Workflow | Package | Version | Status | Head | Run | Registry | Reason |\n")
	builder.WriteString("|---|---|---|---|---|---|---|---|---|\n")
	for _, receipt := range output.Publication.Releases {
		run := receipt.RunID
		if receipt.RunURL != "" {
			run = "[" + receipt.RunID + "](" + receipt.RunURL + ")"
		}
		fmt.Fprintf(&builder, "| `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | %s | `%s` | %s |\n", receipt.Repository, receipt.Workflow, receipt.Package, receipt.Version, receipt.Status, receipt.HeadSHA, run, receipt.RegistryVersion, strings.ReplaceAll(receipt.Reason, "|", "\\|"))
	}
	if output.Propagation != nil {
		builder.WriteString("\n")
		builder.WriteString(output.Propagation.Markdown())
	}
	_, err := fmt.Fprint(command.OutOrStdout(), builder.String())
	return err
}
