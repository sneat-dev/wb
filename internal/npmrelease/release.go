// Package npmrelease coordinates an approved npm publication workflow without
// ever handling npm credentials. The repository owns the GitHub Actions
// workflow; WB dispatches it, records the exact run/head receipt, verifies the
// requested package version in the npm registry, and returns release events
// for the shared dependency-wave engine.
package npmrelease

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/encode"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/progress"
	"golang.org/x/mod/semver"
	"gopkg.in/yaml.v3"
)

const (
	SchemaVersion          = 3
	StatusPlanned          = "planned"
	StatusRunning          = "running"
	StatusPublished        = "published"
	StatusDispatchUnknown  = "dispatch_unknown"
	StatusDispatchFailed   = "dispatch_failed"
	StatusAwaitingRun      = "awaiting_run"
	StatusAwaitingRegistry = "awaiting_registry"
	StatusFailed           = "failed"
)

const (
	workflowDispatchClockSkew = 5 * time.Minute
	// GitHub CLI paginates up to this requested bound. Seeing the full bound is
	// ambiguous rather than "good enough": an older exact-head dispatch may be
	// on the next page, so correlation fails closed instead of accepting it.
	exactWorkflowRunListLimit = 1000
)

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Release is one explicit provider/workflow/package/version tuple. Keeping
// the tuple intact prevents a multi-package provider campaign from silently
// pairing the wrong workflow with a package.
type Release struct {
	Repository       string `json:"repository" yaml:"repository"`
	Workflow         string `json:"workflow" yaml:"workflow"`
	Package          string `json:"package" yaml:"package"`
	Version          string `json:"version" yaml:"version"`
	Ref              string `json:"ref" yaml:"ref"`
	InputFingerprint string `json:"input_fingerprint,omitempty" yaml:"input_fingerprint,omitempty"`
	// Inputs are deliberately process-local. Even safe-looking workflow field
	// values can carry a credential by mistake, so reports and stdout retain
	// only InputFingerprint for resume identity and never serialize raw values.
	Inputs map[string]string `json:"-" yaml:"-"`
}

// Receipt is the durable evidence for one workflow publication. A receipt is
// retained when a later registry check or dependency wave fails so --resume
// never dispatches an already accepted workflow again.
type Receipt struct {
	Release
	Status     string    `json:"status" yaml:"status"`
	Reason     string    `json:"reason,omitempty" yaml:"reason,omitempty"`
	DispatchAt time.Time `json:"dispatch_at,omitempty" yaml:"dispatch_at,omitempty"`
	HeadSHA    string    `json:"head_sha,omitempty" yaml:"head_sha,omitempty"`
	// DispatchBaselineAt and DispatchBaselineRunIDs are captured before the
	// workflow_dispatch request. They are the primary identity fence for a
	// resume: only an exact-head run absent from this set can be the run WB
	// dispatched. A timestamp is retained solely as a bounded secondary check
	// against a delayed, pre-existing run that was absent from GitHub's list.
	DispatchBaselineAt     time.Time `json:"dispatch_baseline_at,omitempty" yaml:"dispatch_baseline_at,omitempty"`
	DispatchBaselineRunIDs []string  `json:"dispatch_baseline_run_ids,omitempty" yaml:"dispatch_baseline_run_ids,omitempty"`
	RunID                  string    `json:"run_id,omitempty" yaml:"run_id,omitempty"`
	RunURL                 string    `json:"run_url,omitempty" yaml:"run_url,omitempty"`
	RunHeadSHA             string    `json:"run_head_sha,omitempty" yaml:"run_head_sha,omitempty"`
	RunStatus              string    `json:"run_status,omitempty" yaml:"run_status,omitempty"`
	RunConclusion          string    `json:"run_conclusion,omitempty" yaml:"run_conclusion,omitempty"`
	RunCreatedAt           time.Time `json:"run_created_at,omitempty" yaml:"run_created_at,omitempty"`
	RunCompletedAt         time.Time `json:"run_completed_at,omitempty" yaml:"run_completed_at,omitempty"`
	RegistryVersion        string    `json:"registry_version,omitempty" yaml:"registry_version,omitempty"`
	RegistryURL            string    `json:"registry_url,omitempty" yaml:"registry_url,omitempty"`
	RegistryCheckedAt      time.Time `json:"registry_checked_at,omitempty" yaml:"registry_checked_at,omitempty"`
}

// Report is persisted before any external action and after every state
// transition. It is intentionally independent of deps.BumpReport: release
// evidence can be resumed even if the downstream wave has not started.
type Report struct {
	SchemaVersion        int                 `json:"schema_version" yaml:"schema_version"`
	Operation            string              `json:"operation" yaml:"operation"`
	Generation           string              `json:"generation,omitempty" yaml:"generation,omitempty"`
	Status               string              `json:"status" yaml:"status"`
	Ref                  string              `json:"ref" yaml:"ref"`
	Releases             []Receipt           `json:"releases" yaml:"releases"`
	Events               []deps.ReleaseEvent `json:"events,omitempty" yaml:"events,omitempty"`
	PropagationOperation string              `json:"propagation_operation,omitempty" yaml:"propagation_operation,omitempty"`
	// Propagation is the same persisted BumpReport the regular `wb deps bump
	// npm` engine produces. Keeping it here makes the publication receipt
	// independently resumable and machine-readable without a parallel wave
	// orchestration format.
	Propagation *deps.BumpReport `json:"propagation,omitempty" yaml:"propagation,omitempty"`
}

// CommandResult is the small subprocess seam used by tests and by the real
// GitHub/npm command runner. Code and Output are retained separately so a
// non-zero gh status can still carry a useful JSON receipt.
type CommandResult struct {
	Output string
	Code   int
	Err    error
}

// CommandRunner executes an external command. It must use the caller's
// existing gh/npm credential helpers; WB never accepts or constructs tokens.
type CommandRunner interface {
	Run(context.Context, string, ...string) CommandResult
}

type OSCommandRunner struct{}

func (OSCommandRunner) Run(ctx context.Context, dir string, args ...string) CommandResult {
	if len(args) == 0 {
		return CommandResult{Code: 2, Err: errors.New("empty command")}
	}
	command := exec.CommandContext(ctx, args[0], args[1:]...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	result := CommandResult{Output: string(output), Code: 0, Err: err}
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			result.Code = exitError.ExitCode()
		} else {
			result.Code = 1
		}
	}
	return result
}

// Options controls publication safety and polling. Apply is the only option
// that permits workflow dispatch; dry-run never contacts GitHub or npm.
type Options struct {
	Apply        bool
	DryRun       bool
	Resume       bool
	Ref          string
	Timeout      time.Duration
	PollInterval time.Duration
	Registry     string
	ReportDir    string
	Runner       CommandRunner
	Now          func() time.Time
	Persist      func(Report) error
	Previous     *Report
	Progress     progress.Reporter
	operation    string
}

// ValidateOptions performs every no-I/O publication option check. Command
// handlers use it before fleet discovery or a workflow dispatch, and Run uses
// it again so package callers get the same boundary.
func ValidateOptions(options Options) error {
	if options.Apply && options.DryRun {
		return errors.New("--apply and --dry-run cannot be used together")
	}
	if options.Resume && !options.Apply {
		return errors.New("--resume requires --apply")
	}
	if options.Timeout < 0 || options.PollInterval < 0 {
		return errors.New("timeout and poll interval must not be negative")
	}
	registry := strings.TrimSpace(options.Registry)
	if registry == "" {
		registry = "https://registry.npmjs.org"
	}
	// url.ParseRequestURI intentionally discards a URI fragment because a
	// fragment is not sent in an HTTP request. Reject it before parsing so a
	// pasted credential can never disappear from validation yet remain in a
	// persisted RegistryURL or subprocess argument.
	if strings.Contains(registry, "#") {
		return errors.New("npm registry URL must not contain credentials, a query, or a fragment; use the repository or npm credential helper")
	}
	parsed, err := url.ParseRequestURI(registry)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		// Do not echo a registry value here: a pasted query or userinfo value can
		// itself be a credential. The caller already knows which flag was invalid.
		return errors.New("invalid npm registry URL (want an http or https URL with a host)")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		// Registry query strings and fragments have no useful npm-registry
		// semantics for this command, but can retain credentials in both a
		// subprocess argument and a durable receipt. Reject all of them.
		return errors.New("npm registry URL must not contain credentials, a query, or a fragment; use the repository or npm credential helper")
	}
	return nil
}

// ValidateRelease checks all explicit user-controlled identifiers before any
// command is run. Workflow names are limited to repository-owned workflow
// files, rather than arbitrary shell fragments or a hidden remote action.
func ValidateRelease(release Release) error {
	release.Repository = strings.TrimSpace(release.Repository)
	if !repositoryPattern.MatchString(release.Repository) {
		return fmt.Errorf("invalid GitHub repository %q (want owner/repository)", release.Repository)
	}
	release.Workflow = strings.TrimSpace(release.Workflow)
	if release.Workflow == "" || strings.Contains(release.Workflow, "..") || strings.HasPrefix(release.Workflow, "/") || strings.ContainsAny(release.Workflow, "\\\x00") {
		return fmt.Errorf("invalid release workflow %q (want a repository-owned workflow file)", release.Workflow)
	}
	ext := strings.ToLower(path.Ext(release.Workflow))
	if ext != ".yml" && ext != ".yaml" {
		return fmt.Errorf("release workflow %q must end in .yml or .yaml", release.Workflow)
	}
	if err := deps.ValidateNpmPackageName(release.Package); err != nil {
		return fmt.Errorf("invalid npm package: %w", err)
	}
	target, err := deps.ParseTarget(string(deps.EcosystemNPM), release.Package+"@"+release.Version)
	if err != nil {
		return err
	}
	if !npmVersionValid(target.Version) || strings.HasPrefix(target.Version, "v") {
		return fmt.Errorf("invalid npm release version %q (want an exact npm semver without a v prefix)", release.Version)
	}
	if release.Ref == "" || strings.ContainsAny(release.Ref, " \t\r\n") {
		return fmt.Errorf("release ref must be a non-empty ref without whitespace")
	}
	for key := range release.Inputs {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key, ":=\r\n") {
			return fmt.Errorf("invalid workflow input name %q", key)
		}
		if IsSecretLikeWorkflowInputKey(key) {
			// Do not include the supplied key or value: this path is deliberately
			// safe even when an operator pasted a credential by mistake.
			return errors.New("workflow input name is secret-like; credentials must remain in repository-owned GitHub Actions secrets")
		}
	}
	return nil
}

// IsSecretLikeWorkflowInputKey recognizes names that might carry a credential
// in a workflow_dispatch field. Callers must reject these before a release is
// normalized, persisted, or rendered into subprocess arguments.
func IsSecretLikeWorkflowInputKey(key string) bool {
	var normalized strings.Builder
	for _, character := range strings.ToLower(strings.TrimSpace(key)) {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			normalized.WriteRune(character)
		}
	}
	value := normalized.String()
	for _, marker := range []string{"token", "secret", "password", "credential", "auth"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func npmVersionValid(version string) bool {
	if version == "" {
		return false
	}
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	return semver.IsValid(version)
}

// Normalize validates, fills defaults, copies inputs, and rejects duplicate
// package/version tuples. Duplicate inputs are unsafe because they could
// dispatch the same release workflow twice.
func Normalize(releases []Release, ref string) ([]Release, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		ref = "main"
	}
	if len(releases) == 0 {
		return nil, errors.New("at least one npm release tuple is required")
	}
	seen := make(map[string]bool, len(releases))
	normalized := make([]Release, len(releases))
	for index, release := range releases {
		release.Repository = strings.TrimSpace(release.Repository)
		release.Workflow = strings.TrimSpace(release.Workflow)
		release.Package = strings.TrimSpace(release.Package)
		release.Version = strings.TrimSpace(release.Version)
		if release.Ref == "" {
			release.Ref = ref
		}
		release.Inputs = cloneInputs(release.Inputs)
		release.InputFingerprint = workflowInputFingerprint(release.Inputs)
		if err := ValidateRelease(release); err != nil {
			return nil, fmt.Errorf("release %d: %w", index+1, err)
		}
		key := release.Repository + "\x00" + release.Workflow + "\x00" + release.Package + "\x00" + release.Version + "\x00" + release.Ref
		if seen[key] {
			return nil, fmt.Errorf("duplicate npm release tuple %s@%s for %s/%s", release.Package, release.Version, release.Repository, release.Workflow)
		}
		seen[key] = true
		normalized[index] = release
	}
	return normalized, nil
}

// OperationIDFor returns a deterministic operation name for the publication
// identity: repository/workflow/package/version/ref only. Workflow inputs are
// deliberately excluded so every request to publish the same version shares
// one operation lock and default report directory. Resume performs the
// stricter input-fingerprinted releaseIdentity comparison separately.
func OperationIDFor(releases []Release) string {
	type operationRelease struct {
		Repository string `json:"repository"`
		Workflow   string `json:"workflow"`
		Package    string `json:"package"`
		Version    string `json:"version"`
		Ref        string `json:"ref"`
	}
	copyReleases := make([]operationRelease, len(releases))
	for index, release := range releases {
		copyReleases[index] = operationRelease{
			Repository: release.Repository, Workflow: release.Workflow, Package: release.Package,
			Version: release.Version, Ref: release.Ref,
		}
	}
	sort.Slice(copyReleases, func(i, j int) bool {
		left := copyReleases[i].Repository + "\x00" + copyReleases[i].Workflow + "\x00" + copyReleases[i].Package + "\x00" + copyReleases[i].Version + "\x00" + copyReleases[i].Ref
		right := copyReleases[j].Repository + "\x00" + copyReleases[j].Workflow + "\x00" + copyReleases[j].Package + "\x00" + copyReleases[j].Version + "\x00" + copyReleases[j].Ref
		return left < right
	})
	raw, _ := json.Marshal(copyReleases)
	return "npm-publish-" + shortHash(raw)
}

// PublicationClaimOperationIDs returns deterministic, package-version claim
// lock names for already-normalized releases. Unlike a campaign operation, a
// claim deliberately ignores workflow inputs, repository, workflow, ref, and
// neighboring tuples: npm can publish one exact package version only once, so
// overlapping subset/superset campaigns must serialize that publication.
func PublicationClaimOperationIDs(releases []Release) []string {
	seen := make(map[string]bool, len(releases))
	for _, release := range releases {
		identity := release.Package + "\x00" + release.Version
		seen["npm-publish-claim-"+shortHash([]byte(identity))] = true
	}
	claims := make([]string, 0, len(seen))
	for claim := range seen {
		claims = append(claims, claim)
	}
	sort.Strings(claims)
	return claims
}

func shortHash(raw []byte) string {
	value := sha256.Sum256(raw)
	return fmt.Sprintf("%x", value[:])[:16]
}

func cloneInputs(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func workflowInputFingerprint(input map[string]string) string {
	if len(input) == 0 {
		return ""
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var source strings.Builder
	for _, key := range keys {
		source.WriteString(key)
		source.WriteByte(0)
		source.WriteString(input[key])
		source.WriteByte(0)
	}
	digest := sha256.Sum256([]byte(source.String()))
	return fmt.Sprintf("%x", digest[:])
}

func now(options Options) time.Time {
	if options.Now != nil {
		return options.Now().UTC()
	}
	return time.Now().UTC()
}

// runExternal gives each gh/npm subprocess its own timeout budget. The
// publication state machine can legitimately poll more than once, so a single
// operation-wide context would incorrectly make later observations inherit an
// earlier command's elapsed time. The caller context still carries Cobra/user
// cancellation through every command.
func runExternal(ctx context.Context, options Options, args ...string) CommandResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if options.Timeout <= 0 {
		return options.Runner.Run(ctx, "", args...)
	}
	commandContext, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	return options.Runner.Run(commandContext, "", args...)
}

// Run executes the publication phase. If Apply is false it returns a
// deterministic planned report without invoking gh or npm. If Apply is true,
// every receipt is persisted before dispatch and after each external state
// transition. Resume reuses all receipts and never dispatches a tuple already
// carrying a dispatch timestamp or run identity.
func Run(ctx context.Context, releases []Release, options Options) (Report, error) {
	normalized, err := Normalize(releases, options.Ref)
	if err != nil {
		return Report{}, err
	}
	if err := ValidateOptions(options); err != nil {
		return Report{}, err
	}
	if options.Registry == "" {
		options.Registry = "https://registry.npmjs.org"
	}
	if options.Runner == nil {
		options.Runner = OSCommandRunner{}
	}
	report := plannedReport(normalized)
	options.operation = report.Operation
	if options.Resume {
		if options.Previous == nil {
			return Report{}, errors.New("--resume requires a persisted npm publication report")
		}
		orderedReceipts, err := validatePrevious(*options.Previous, normalized)
		if err != nil {
			return Report{}, err
		}
		report = *options.Previous
		report.Releases = orderedReceipts
		report.Status = StatusRunning
		// YAML/JSON deliberately omit raw workflow-input values. Hydrate them
		// only from this invocation after validating the saved fingerprint, so a
		// resume can dispatch an unreceipted tuple without reading a value back
		// from disk.
		for index := range report.Releases {
			report.Releases[index].Inputs = cloneInputs(normalized[index].Inputs)
			report.Releases[index].InputFingerprint = normalized[index].InputFingerprint
		}
	}
	if err := persist(options, report); err != nil {
		return report, err
	}
	if !options.Apply {
		report.Status = StatusPlanned
		return report, persist(options, report)
	}
	for index := range report.Releases {
		receipt := &report.Releases[index]
		if receipt.Status == StatusPublished {
			continue
		}
		progress.Report(options.Progress, progress.Event{Operation: report.Operation, Phase: "release", Repository: receipt.Repository, Detail: receipt.Package + "@" + receipt.Version, State: progress.Started, Completed: index, Total: len(report.Releases)})
		if err := processReceipt(ctx, receipt, options, func() error { return persist(options, report) }); err != nil {
			if receipt.Status == "" || receipt.Status == StatusPlanned || receipt.Status == StatusRunning {
				receipt.Status = StatusFailed
			}
			report.Status = receipt.Status
			receipt.Reason = err.Error()
			_ = persist(options, report)
			progress.Report(options.Progress, progress.Event{Operation: report.Operation, Phase: "release", Repository: receipt.Repository, State: progress.Failed, Completed: index, Total: len(report.Releases)})
			return report, err
		}
		progress.Report(options.Progress, progress.Event{Operation: report.Operation, Phase: "release", Repository: receipt.Repository, Detail: receipt.Package + "@" + receipt.Version, State: progress.Completed, Completed: index + 1, Total: len(report.Releases)})
		if err := persist(options, report); err != nil {
			return report, err
		}
	}
	report.Events = make([]deps.ReleaseEvent, len(report.Releases))
	for index, receipt := range report.Releases {
		report.Events[index] = deps.ReleaseEvent{Dependency: receipt.Package, Version: receipt.Version, Source: "npm_workflow", CheckedAt: receipt.RegistryCheckedAt}
	}
	report.Status = StatusPublished
	return report, persist(options, report)
}

func plannedReport(releases []Release) Report {
	receipts := make([]Receipt, len(releases))
	for index, release := range releases {
		receipts[index] = Receipt{Release: release, Status: StatusPlanned, Reason: "publication is disabled until --apply"}
	}
	return Report{SchemaVersion: SchemaVersion, Operation: OperationIDFor(releases), Status: StatusPlanned, Ref: releases[0].Ref, Releases: receipts}
}

// validatePrevious verifies that a durable receipt belongs to exactly the
// requested tuple set and returns receipts in the invocation's tuple order.
// OperationIDFor is intentionally order-independent, so a safe --resume can
// reorder aligned tuples without losing a receipt or dispatching it twice.
func validatePrevious(previous Report, releases []Release) ([]Receipt, error) {
	if previous.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("resume report schema version %d is unsupported; want %d", previous.SchemaVersion, SchemaVersion)
	}
	if previous.Operation != OperationIDFor(releases) {
		return nil, errors.New("resume report does not match the requested repository/workflow/package/version tuples")
	}
	if len(previous.Releases) != len(releases) {
		return nil, errors.New("resume report release count does not match the requested tuples")
	}
	byTuple := make(map[string]Receipt, len(previous.Releases))
	for index, receipt := range previous.Releases {
		key := releaseIdentity(receipt.Release)
		if _, exists := byTuple[key]; exists {
			return nil, fmt.Errorf("resume report contains duplicate release tuple %d", index+1)
		}
		byTuple[key] = receipt
	}
	ordered := make([]Receipt, len(releases))
	for index := range releases {
		release := releases[index]
		receipt, exists := byTuple[releaseIdentity(release)]
		if !exists {
			return nil, fmt.Errorf("resume report release %d does not match the requested tuple", index+1)
		}
		if !receipt.DispatchAt.IsZero() && (receipt.HeadSHA == "" || receipt.DispatchBaselineAt.IsZero()) {
			return nil, fmt.Errorf("resume report release %d lacks its durable pre-dispatch baseline; refusing to guess or redispatch", index+1)
		}
		ordered[index] = receipt
	}
	return ordered, nil
}

func releaseIdentity(release Release) string {
	return release.Repository + "\x00" + release.Workflow + "\x00" + release.Package + "\x00" + release.Version + "\x00" + release.Ref + "\x00" + release.InputFingerprint
}

func persist(options Options, report Report) error {
	if options.Persist == nil {
		return nil
	}
	// Do not rely on one particular writer to honor Release's serialization
	// tags. Persist callbacks are a public seam, so remove process-local input
	// values before handing the report to any caller-owned persistence path.
	persisted := report
	persisted.Releases = append([]Receipt(nil), report.Releases...)
	for index := range persisted.Releases {
		persisted.Releases[index].Inputs = nil
	}
	return options.Persist(persisted)
}

func processReceipt(ctx context.Context, receipt *Receipt, options Options, persistReceipt func() error) error {
	if receipt.DispatchAt.IsZero() {
		reportReceiptProgress(options, *receipt, "resolve_head", progress.Running, "")
		headhash, err := resolveHead(ctx, *receipt, options)
		if err != nil {
			return fmt.Errorf("resolve exact %s head before dispatch: %w", receipt.Repository, err)
		}
		receipt.HeadSHA = headhash
		reportReceiptProgress(options, *receipt, "capture_baseline", progress.Running, "")
		baseline, err := listExactWorkflowRuns(ctx, *receipt, options)
		if err != nil {
			return fmt.Errorf("capture exact workflow-run baseline before dispatch: %w", err)
		}
		receipt.DispatchBaselineRunIDs = workflowRunIDs(baseline)
		receipt.DispatchBaselineAt = now(options)
		receipt.DispatchAt = now(options)
		receipt.Status = StatusDispatchUnknown
		receipt.Reason = "pre-dispatch baseline is durable; resume will only locate a new exact-head run and never redispatch automatically"
		// Persist the exact head, baseline IDs, and dispatch timestamp before
		// the external request. A crash on either side of the request is then
		// explicitly unknown rather than an excuse to duplicate publication.
		if err := persistReceipt(); err != nil {
			return err
		}
		reportReceiptProgress(options, *receipt, "dispatch_workflow", progress.Running, "")
		if err := dispatchWorkflow(ctx, *receipt, options); err != nil {
			receipt.Status = StatusDispatchFailed
			receipt.Reason = "workflow dispatch command failed; use --resume only to locate a possibly accepted run, never to redispatch"
			if persistErr := persistReceipt(); persistErr != nil {
				return errors.Join(err, fmt.Errorf("persist dispatch failure state: %w", persistErr))
			}
			return err
		}
		receipt.Status = StatusAwaitingRun
		receipt.Reason = "workflow dispatch accepted; locating one exact workflow run absent from the pre-dispatch baseline"
		if err := persistReceipt(); err != nil {
			return err
		}
	} else if receipt.HeadSHA == "" || receipt.DispatchBaselineAt.IsZero() {
		receipt.Status = StatusDispatchUnknown
		receipt.Reason = "persisted dispatch state is incomplete; WB will not redispatch without a durable pre-dispatch baseline"
		return errors.New("persisted dispatch state lacks an exact head or pre-dispatch baseline; inspect the provider workflow and start a new operation only after resolving the ambiguity")
	}
	if receipt.RunID == "" {
		reportReceiptProgress(options, *receipt, "locate_workflow_run", progress.Waiting, "")
		run, err := locateRun(ctx, *receipt, options)
		if err != nil {
			if receipt.Status != StatusDispatchUnknown && receipt.Status != StatusDispatchFailed {
				receipt.Status = StatusAwaitingRun
			}
			return err
		}
		receipt.RunID = workflowRunID(run)
		receipt.RunURL = run.URL
		receipt.RunHeadSHA = run.HeadSHA
		receipt.RunStatus = run.Status
		receipt.RunConclusion = run.Conclusion
		receipt.RunCreatedAt = run.CreatedAt
		receipt.Reason = "exact post-dispatch workflow run located"
		if err := persistReceipt(); err != nil {
			return err
		}
	}
	if receipt.RunHeadSHA != "" && !strings.EqualFold(receipt.RunHeadSHA, receipt.HeadSHA) {
		receipt.Status = StatusFailed
		return fmt.Errorf("workflow run %s head %s does not match dispatched head %s", receipt.RunID, receipt.RunHeadSHA, receipt.HeadSHA)
	}
	reportReceiptProgress(options, *receipt, "wait_workflow", progress.Waiting, receipt.RunStatus)
	if err := waitRun(ctx, receipt, options, persistReceipt); err != nil {
		return err
	}
	reportReceiptProgress(options, *receipt, "verify_registry", progress.Waiting, "")
	version, checkedAt, err := verifyRegistry(ctx, *receipt, options)
	receipt.RegistryCheckedAt = checkedAt
	if err != nil {
		receipt.Status = StatusAwaitingRegistry
		return err
	}
	receipt.RegistryVersion = version
	receipt.RegistryURL = registryURL(options.Registry, receipt.Package, receipt.Version)
	receipt.Status = StatusPublished
	receipt.Reason = "exact workflow run passed and npm registry returned the requested version"
	return nil
}

func reportReceiptProgress(options Options, receipt Receipt, phase string, state progress.State, detail string) {
	progress.Report(options.Progress, progress.Event{
		Operation: options.operation, Phase: phase,
		Repository: receipt.Repository, Detail: detail, State: state,
	})
}

type workflowRun struct {
	// gh run list/view emits databaseId as a JSON number, not a quoted string.
	// Keep it numeric at the decode boundary and convert only when recording the
	// durable receipt, whose IDs are strings for report compatibility.
	ID         int64     `json:"databaseId"`
	HeadSHA    string    `json:"headSha"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	URL        string    `json:"url"`
	Workflow   string    `json:"workflowName"`
	Event      string    `json:"event"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

func workflowRunID(run workflowRun) string {
	return strconv.FormatInt(run.ID, 10)
}

func resolveHead(ctx context.Context, receipt Receipt, options Options) (string, error) {
	if useSharedGitHubObserver(options.Runner) {
		response, err := githubobserver.Get(ctx, githubobserver.GetRequest{
			Repository:  receipt.Repository,
			Target:      receipt.Ref,
			Endpoint:    "repos/" + receipt.Repository + "/git/ref/heads/" + url.PathEscape(receipt.Ref),
			FreshWindow: 0,
		})
		if err != nil {
			return "", fmt.Errorf("resolve workflow head: %w", err)
		}
		var reference struct {
			Object struct {
				SHA string `json:"sha"`
			} `json:"object"`
		}
		if err := json.Unmarshal(response.Body, &reference); err != nil {
			return "", fmt.Errorf("decode workflow head: %w", err)
		}
		sha := strings.TrimSpace(reference.Object.SHA)
		if !isSHA(sha) {
			return "", fmt.Errorf("GitHub returned invalid head SHA %q", sha)
		}
		return strings.ToLower(sha), nil
	}
	result := runExternal(ctx, options, "gh", "api", "repos/"+receipt.Repository+"/git/ref/heads/"+url.PathEscape(receipt.Ref), "--jq", ".object.sha")
	if result.Err != nil || result.Code != 0 {
		return "", commandError("resolve workflow head", result)
	}
	sha := strings.TrimSpace(result.Output)
	if !isSHA(sha) {
		return "", fmt.Errorf("GitHub returned invalid head SHA %q", sha)
	}
	return strings.ToLower(sha), nil
}

func dispatchWorkflow(ctx context.Context, receipt Receipt, options Options) error {
	args := []string{"gh", "workflow", "run", receipt.Workflow, "--repo", receipt.Repository, "--ref", receipt.Ref}
	keys := make([]string, 0, len(receipt.Inputs))
	for key := range receipt.Inputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--field", key+"="+receipt.Inputs[key])
	}
	result := runExternal(ctx, options, args...)
	if result.Err != nil || result.Code != 0 {
		return commandError("dispatch release workflow", result)
	}
	return nil
}

// listExactWorkflowRuns lists only workflow_dispatch runs that GitHub itself
// reports at the exact resolved head. The caller may safely use the returned
// ID set as a pre-dispatch baseline; missing identity metadata is rejected by
// omission rather than being treated as a candidate.
func listExactWorkflowRuns(ctx context.Context, receipt Receipt, options Options) ([]workflowRun, error) {
	output := ""
	if useSharedGitHubObserver(options.Runner) {
		read, err := githubobserver.Read(ctx, "", "run", "list", "--repo", receipt.Repository, "--workflow", receipt.Workflow, "--commit", receipt.HeadSHA, "--event", "workflow_dispatch", "--limit", fmt.Sprintf("%d", exactWorkflowRunListLimit), "--json", "databaseId,headSha,status,conclusion,url,workflowName,event,createdAt,updatedAt")
		if err != nil {
			return nil, fmt.Errorf("list exact workflow runs: %w", err)
		}
		output = string(read)
	} else {
		result := runExternal(ctx, options, "gh", "run", "list", "--repo", receipt.Repository, "--workflow", receipt.Workflow, "--commit", receipt.HeadSHA, "--event", "workflow_dispatch", "--limit", fmt.Sprintf("%d", exactWorkflowRunListLimit), "--json", "databaseId,headSha,status,conclusion,url,workflowName,event,createdAt,updatedAt")
		if result.Err != nil || result.Code != 0 {
			return nil, commandError("list exact workflow runs", result)
		}
		output = result.Output
	}
	var listed []workflowRun
	if err := json.Unmarshal([]byte(output), &listed); err != nil {
		return nil, fmt.Errorf("decode GitHub workflow run list: %w", err)
	}
	if len(listed) >= exactWorkflowRunListLimit {
		return nil, fmt.Errorf("GitHub returned %d exact workflow runs, reaching the %d-run correlation limit; refusing a potentially truncated baseline", len(listed), exactWorkflowRunListLimit)
	}
	byID := make(map[string]workflowRun, len(listed))
	for _, run := range listed {
		id := workflowRunID(run)
		if run.ID <= 0 || !strings.EqualFold(run.Event, "workflow_dispatch") || !strings.EqualFold(run.HeadSHA, receipt.HeadSHA) {
			continue
		}
		if _, exists := byID[id]; exists {
			return nil, fmt.Errorf("GitHub returned duplicate workflow run ID %s while listing %s/%s", id, receipt.Repository, receipt.Workflow)
		}
		byID[id] = run
	}
	runs := make([]workflowRun, 0, len(byID))
	for _, run := range byID {
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	return runs, nil
}

func workflowRunIDs(runs []workflowRun) []string {
	ids := make([]string, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, workflowRunID(run))
	}
	sort.Strings(ids)
	return ids
}

func locateRun(ctx context.Context, receipt Receipt, options Options) (workflowRun, error) {
	if receipt.DispatchAt.IsZero() || receipt.DispatchBaselineAt.IsZero() || receipt.HeadSHA == "" {
		return workflowRun{}, errors.New("cannot locate a dispatched workflow without the persisted head, baseline, and dispatch timestamp")
	}
	baseline := make(map[string]bool, len(receipt.DispatchBaselineRunIDs))
	for _, id := range receipt.DispatchBaselineRunIDs {
		if id != "" {
			baseline[id] = true
		}
	}
	deadline := time.Time{}
	if options.Timeout > 0 {
		deadline = time.Now().Add(options.Timeout)
	}
	for {
		reportReceiptProgress(options, receipt, "locate_workflow_run", progress.Waiting, "polling GitHub")
		runs, err := listExactWorkflowRuns(ctx, receipt, options)
		if err != nil {
			return workflowRun{}, fmt.Errorf("locate dispatched workflow run: %w", err)
		}
		var candidates []workflowRun
		var staleUnbaselined []workflowRun
		for _, run := range runs {
			if baseline[workflowRunID(run)] {
				continue
			}
			// The baseline is the primary correlation mechanism. This bounded
			// timestamp check is only a secondary fence for an old run whose list
			// visibility lagged the baseline observation.
			if !run.CreatedAt.IsZero() && run.CreatedAt.Before(receipt.DispatchAt.Add(-workflowDispatchClockSkew)) {
				staleUnbaselined = append(staleUnbaselined, run)
				continue
			}
			candidates = append(candidates, run)
		}
		if len(staleUnbaselined) > 0 {
			return workflowRun{}, fmt.Errorf("an exact-head workflow run absent from the baseline predates dispatch beyond %s; refusing a delayed-visibility ambiguity", workflowDispatchClockSkew)
		}
		if len(candidates) > 1 {
			return workflowRun{}, fmt.Errorf("multiple workflow_dispatch runs for %s/%s at exact head %s are absent from the persisted baseline; refusing an ambiguous receipt", receipt.Repository, receipt.Workflow, receipt.HeadSHA)
		}
		if len(candidates) == 1 {
			return candidates[0], nil
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return workflowRun{}, fmt.Errorf("no workflow_dispatch run for %s/%s at exact head %s is visible before timeout", receipt.Repository, receipt.Workflow, receipt.HeadSHA)
		}
		interval := options.PollInterval
		if interval <= 0 {
			interval = 100 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return workflowRun{}, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func waitRun(ctx context.Context, receipt *Receipt, options Options, persistReceipt func() error) error {
	deadline := time.Time{}
	if options.Timeout > 0 {
		deadline = time.Now().Add(options.Timeout)
	}
	for {
		output := ""
		if useSharedGitHubObserver(options.Runner) {
			read, err := githubobserver.Read(ctx, "", "run", "view", receipt.RunID, "--repo", receipt.Repository, "--json", "databaseId,headSha,status,conclusion,url,workflowName,event,createdAt,updatedAt")
			if err != nil {
				return fmt.Errorf("observe workflow run %s: %w", receipt.RunID, err)
			}
			output = string(read)
		} else {
			result := runExternal(ctx, options, "gh", "run", "view", receipt.RunID, "--repo", receipt.Repository, "--json", "databaseId,headSha,status,conclusion,url,workflowName,event,createdAt,updatedAt")
			if result.Err != nil || result.Code != 0 {
				return commandError("observe workflow run "+receipt.RunID, result)
			}
			output = result.Output
		}
		var run workflowRun
		if err := json.Unmarshal([]byte(output), &run); err != nil {
			return fmt.Errorf("decode workflow run %s: %w", receipt.RunID, err)
		}
		if run.ID <= 0 || workflowRunID(run) != receipt.RunID || !strings.EqualFold(run.Event, "workflow_dispatch") {
			return fmt.Errorf("workflow run observation no longer identifies exact workflow_dispatch run %s", receipt.RunID)
		}
		receipt.RunStatus = run.Status
		receipt.RunConclusion = run.Conclusion
		if !run.UpdatedAt.IsZero() {
			receipt.RunCompletedAt = run.UpdatedAt
		}
		if run.URL != "" {
			receipt.RunURL = run.URL
		}
		if run.HeadSHA != "" {
			receipt.RunHeadSHA = run.HeadSHA
		}
		if receipt.RunHeadSHA != "" && !strings.EqualFold(receipt.RunHeadSHA, receipt.HeadSHA) {
			receipt.Status = StatusFailed
			return fmt.Errorf("workflow run %s head %s does not match dispatched head %s", receipt.RunID, receipt.RunHeadSHA, receipt.HeadSHA)
		}
		reportReceiptProgress(options, *receipt, "wait_workflow", progress.Waiting, strings.TrimSpace(run.Status+" "+run.Conclusion))
		switch strings.ToLower(run.Status) {
		case "completed":
			switch strings.ToLower(run.Conclusion) {
			case "success":
				receipt.Status = StatusAwaitingRegistry
				if err := persistReceipt(); err != nil {
					return err
				}
				return nil
			case "failure", "cancelled", "timed_out", "action_required", "startup_failure":
				receipt.Status = StatusFailed
				if err := persistReceipt(); err != nil {
					return err
				}
				return fmt.Errorf("workflow run %s completed with conclusion %s", receipt.RunID, run.Conclusion)
			default:
				receipt.Status = StatusFailed
				if err := persistReceipt(); err != nil {
					return err
				}
				return fmt.Errorf("workflow run %s completed without a successful conclusion: %s", receipt.RunID, run.Conclusion)
			}
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			receipt.Status = StatusAwaitingRun
			if err := persistReceipt(); err != nil {
				return err
			}
			return fmt.Errorf("workflow run %s did not complete before timeout", receipt.RunID)
		}
		if err := persistReceipt(); err != nil {
			return err
		}
		interval := options.PollInterval
		if interval <= 0 {
			interval = 100 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// Production GitHub observations go through githubobserver. A custom runner
// bypasses it only for hermetic tests that need exact stubbed stdout/exit
// control from `gh` subcommands such as `gh run list` and `gh run view`; the
// live release transport still uses the shared observer path.
func useSharedGitHubObserver(runner CommandRunner) bool {
	if runner == nil {
		return true
	}
	switch runner.(type) {
	case OSCommandRunner, *OSCommandRunner:
		return true
	default:
		return false
	}
}

func verifyRegistry(ctx context.Context, receipt Receipt, options Options) (string, time.Time, error) {
	checkedAt := now(options)
	result := runExternal(ctx, options, "npm", "view", receipt.Package+"@"+receipt.Version, "version", "--json", "--registry", options.Registry)
	if result.Err != nil || result.Code != 0 {
		return "", checkedAt, commandError("verify npm registry release "+receipt.Package+"@"+receipt.Version, result)
	}
	version := strings.TrimSpace(result.Output)
	var decoded string
	if err := json.Unmarshal([]byte(version), &decoded); err == nil {
		version = strings.TrimSpace(decoded)
	}
	version = strings.Trim(version, "\"\r\n \t")
	if version != receipt.Version {
		return version, checkedAt, fmt.Errorf("npm registry returned %q for %s@%s; exact version evidence is required", version, receipt.Package, receipt.Version)
	}
	return version, checkedAt, nil
}

func registryURL(registry, packageName, version string) string {
	base := strings.TrimRight(registry, "/")
	return base + "/" + url.PathEscape(packageName) + "/" + url.PathEscape(version)
}

func isSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}

func commandError(action string, result CommandResult) error {
	detail := strings.TrimSpace(result.Output)
	if detail == "" && result.Err != nil {
		detail = result.Err.Error()
	}
	if detail == "" {
		detail = fmt.Sprintf("exit code %d", result.Code)
	}
	return fmt.Errorf("%s: %s", action, sanitizeCommandDetail(detail))
}

var (
	credentialAssignment = regexp.MustCompile(`(?i)\b([a-z0-9_.-]*(?:token|secret|password|credential|auth)[a-z0-9_.-]*\s*=\s*)[^\s&]+`)
	bearerCredential     = regexp.MustCompile(`(?i)(authorization\s*:\s*bearer\s+)[^\s]+`)
)

func sanitizeCommandDetail(detail string) string {
	detail = credentialAssignment.ReplaceAllString(detail, "${1}[redacted]")
	return bearerCredential.ReplaceAllString(detail, "${1}[redacted]")
}

// JSON and YAML are intentionally exposed by the package so command handlers
// and integration tests use the same field names and deterministic encoding.
// Both carry one logical generation, allowing --resume to fail closed if a
// crash happens between their individual atomic renames.
func (report Report) WithGeneration() Report {
	report.Generation = reportGeneration(report)
	return report
}

func (report Report) JSON() ([]byte, error) {
	report = report.WithGeneration()
	return encode.JSON(report)
}

func (report Report) YAML() ([]byte, error) {
	report = report.WithGeneration()
	return yaml.Marshal(report)
}

func reportGeneration(report Report) string {
	report.Generation = ""
	raw, _ := json.Marshal(report)
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("%x", digest[:])
}

// EventsFor returns only registry-confirmed events. A caller must not hand
// planned or failed receipts to deps bump.
func EventsFor(report Report) ([]deps.ReleaseEvent, error) {
	if report.Status != StatusPublished {
		return nil, fmt.Errorf("cannot hand off npm releases while publication status is %s", report.Status)
	}
	events := make([]deps.ReleaseEvent, len(report.Releases))
	for index, receipt := range report.Releases {
		if receipt.Status != StatusPublished || receipt.RegistryVersion != receipt.Version || receipt.RegistryCheckedAt.IsZero() {
			return nil, fmt.Errorf("release %s@%s lacks exact registry evidence", receipt.Package, receipt.Version)
		}
		events[index] = deps.ReleaseEvent{Dependency: receipt.Package, Version: receipt.Version, Source: "npm_workflow", CheckedAt: receipt.RegistryCheckedAt}
	}
	return events, nil
}

// WriteReport persists the resumable report. Both canonical YAML and JSON are
// independently atomically replaced so a crash cannot expose a half-written
// document to a resume or machine reader. It is intentionally separate from
// stdout formatting so failure paths keep the same durable receipt.
func WriteReport(directory string, report Report) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	report = report.WithGeneration()
	yamlReport, err := report.YAML()
	if err != nil {
		return err
	}
	jsonReport, err := report.JSON()
	if err != nil {
		return err
	}
	// Write JSON first. YAML is the resume authority; it advances only after
	// both serializations were produced and JSON has reached an atomic rename.
	if err := writeAtomic(filepath.Join(directory, "npm-publish.json"), jsonReport, 0o644); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(directory, "npm-publish.yaml"), yamlReport, 0o644)
}

func writeAtomic(filename string, contents []byte, mode os.FileMode) error {
	temporary, err := os.CreateTemp(filepath.Dir(filename), ".npm-publish-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(temporaryName)
		}
	}()
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode.Perm()); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, filename); err != nil {
		return err
	}
	remove = false
	return nil
}

// LoadReport loads the canonical YAML resume artifact.
func LoadReport(directory string) (Report, error) {
	raw, err := os.ReadFile(filepath.Join(directory, "npm-publish.yaml"))
	if err != nil {
		return Report{}, err
	}
	var report Report
	if err := yaml.Unmarshal(raw, &report); err != nil {
		return Report{}, err
	}
	jsonRaw, err := os.ReadFile(filepath.Join(directory, "npm-publish.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return Report{}, errors.New("npm publication report is incomplete: canonical YAML has no matching JSON generation")
		}
		return Report{}, err
	}
	var jsonReport Report
	// JSON is emitted from YAML tags by encode.JSON, including flattened
	// embedded receipt fields. Decode it with the same YAML tag mapping so the
	// two formats compare as one logical snapshot.
	if err := yaml.Unmarshal(jsonRaw, &jsonReport); err != nil {
		return Report{}, fmt.Errorf("decode npm publication JSON report: %w", err)
	}
	if report.Generation == "" || jsonReport.Generation == "" || report.Generation != jsonReport.Generation ||
		report.Generation != reportGeneration(report) || jsonReport.Generation != reportGeneration(jsonReport) ||
		report.Operation != jsonReport.Operation || report.SchemaVersion != jsonReport.SchemaVersion {
		return Report{}, errors.New("npm publication YAML and JSON report generations are inconsistent; refusing an ambiguous resume")
	}
	return report, nil
}

// ReportExists recognizes either durable representation. A JSON-only remnant
// is intentionally treated as existing (and therefore refuses a fresh apply)
// rather than silently allowing a duplicate workflow dispatch.
func ReportExists(directory string) (bool, error) {
	for _, name := range []string{"npm-publish.yaml", "npm-publish.json"} {
		_, err := os.Stat(filepath.Join(directory, name))
		if err == nil {
			return true, nil
		}
		if !os.IsNotExist(err) {
			return false, err
		}
	}
	return false, nil
}
