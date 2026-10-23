package depsrun

import (
	"context"
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/npmrelease"
	"github.com/sneat-dev/wb/internal/quality"
)

func (service *PublicationService) preflight(ctx context.Context, request PublicationRequest, callbacks PublicationCallbacks) (npmPublishPrepared, error) {
	if !request.Selection.Fleet {
		return npmPublishPrepared{}, fmt.Errorf("deps publish npm requires --fleet for downstream propagation")
	}
	if !request.Apply && request.Lifecycle.Resume {
		return npmPublishPrepared{}, fmt.Errorf("--resume requires --apply")
	}
	if (request.Lifecycle.Commit || request.Lifecycle.Push || request.Lifecycle.PR || request.Lifecycle.Merge) && !request.Apply {
		return npmPublishPrepared{}, fmt.Errorf("--commit, --push, --pr, and --merge require --apply")
	}
	if request.Apply && request.Lifecycle.DryRun {
		return npmPublishPrepared{}, fmt.Errorf("--apply and --dry-run cannot be used together")
	}
	if (request.Lifecycle.Commit || request.Lifecycle.Push || request.Lifecycle.PR) && !request.Lifecycle.Merge {
		return npmPublishPrepared{}, fmt.Errorf("--commit, --push, and --pr require --merge for deps publish npm")
	}
	if request.NoVerify && request.Checks != "" {
		return npmPublishPrepared{}, fmt.Errorf("--no-verify and --checks cannot be used together")
	}
	if err := callbacks.ValidateOutput(); err != nil {
		return npmPublishPrepared{}, err
	}
	if err := validateNpmPublicationSelection(request); err != nil {
		return npmPublishPrepared{}, err
	}
	releases, err := alignedNpmReleases(request)
	if err != nil {
		return npmPublishPrepared{}, err
	}
	normalized, err := npmrelease.Normalize(releases, request.Lifecycle.Ref)
	if err != nil {
		return npmPublishPrepared{}, err
	}
	if err := npmrelease.ValidateOptions(npmrelease.Options{
		Apply: request.Apply, DryRun: request.Lifecycle.DryRun, Resume: request.Lifecycle.Resume, Timeout: request.Lifecycle.Timeout,
		PollInterval: request.WorkflowPoll, Registry: request.Registry,
	}); err != nil {
		return npmPublishPrepared{}, err
	}
	checks, err := quality.ParseChecks(request.Checks)
	if err != nil {
		return npmPublishPrepared{}, err
	}
	// Validate downstream options before creating a report directory or
	// discovering the fleet. Dry-run clears mutation flags; Merge only implies
	// PR/push/commit and introduces no additional refusal after this pass.
	// The loaded wave is validated separately below with its actual resume mode.
	events := releaseEventsForReleases(normalized)
	if err := validateNpmPublicationBump(request, checks, request.Lifecycle.ReportDir, events, true, false, nil); err != nil {
		return npmPublishPrepared{}, err
	}
	reportDir, err := service.npmPublicationReportDir(request.Selection.ProjectsRoot, normalized, request.Lifecycle.ReportDir)
	if err != nil {
		return npmPublishPrepared{}, err
	}
	prepared := npmPublishPrepared{
		releases: normalized, checks: checks, reportDir: reportDir,
		operation: npmrelease.OperationIDFor(normalized),
	}
	if request.Apply {
		prepared.previous, err = service.npmPublicationResumeReport(reportDir, request.Lifecycle.Resume)
		if err != nil {
			return npmPublishPrepared{}, err
		}
	}
	if request.Lifecycle.Resume {
		prepared.bumpPrevious, err = service.npmPublicationBumpPrevious(reportDir, true)
		if err != nil {
			return npmPublishPrepared{}, err
		}
	}
	if request.Apply {
		if err := validateNpmPublicationBump(request, checks, reportDir, events, !request.Lifecycle.Merge, prepared.bumpPrevious != nil, prepared.bumpPrevious); err != nil {
			return npmPublishPrepared{}, err
		}
	}
	// Fleet selection itself is part of preflight: invalid filters, an empty
	// fleet, or local discovery constraints fail before a provider is allowed to
	// dispatch anything.
	if service.deps.Select == nil {
		return npmPublishPrepared{}, fmt.Errorf("npm publication fleet discovery is unavailable")
	}
	prepared.repositories, err = service.deps.Select(context.Background(), request.Selection)
	if err != nil {
		return npmPublishPrepared{}, err
	}
	return prepared, nil
}
func npmPublicationIdentity(request PublicationRequest) ([]npmrelease.Release, string, error) {
	releases, err := alignedNpmReleases(request)
	if err != nil {
		return nil, "", err
	}
	normalized, err := npmrelease.Normalize(releases, request.Lifecycle.Ref)
	if err != nil {
		return nil, "", err
	}
	return normalized, npmrelease.OperationIDFor(normalized), nil
}
func alignedNpmReleases(request PublicationRequest) ([]npmrelease.Release, error) {
	lengths := []int{len(request.Repositories), len(request.Workflows), len(request.Packages), len(request.Versions)}
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
	inputs, err := parseWorkflowInputs(request.WorkflowInputs, lengths[0])
	if err != nil {
		return nil, err
	}
	releases := make([]npmrelease.Release, lengths[0])
	for index := range releases {
		releases[index] = npmrelease.Release{
			Repository: request.Repositories[index], Workflow: request.Workflows[index],
			Package: request.Packages[index], Version: request.Versions[index], Ref: request.Lifecycle.Ref,
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
func validateNpmPublicationSelection(request PublicationRequest) error {
	if _, err := CompileRegex(request.Selection.Regex); err != nil {
		return err
	}
	if request.Selection.Match != "" {
		if _, err := path.Match(request.Selection.Match, ""); err != nil {
			return fmt.Errorf("invalid --match: %w", err)
		}
	}
	return nil
}
func validateNpmPublicationBump(request PublicationRequest, checks []quality.Check, reportDir string, events []deps.ReleaseEvent, dryRun bool, resume bool, previous *deps.BumpReport) error {
	propagation := npmPublicationPropagationOptions(request.Lifecycle, reportDir, dryRun, resume)
	propagation.Checks = checks
	return deps.ValidateBumpOptions(deps.BumpOptions{
		Options: propagation, Ecosystem: deps.EcosystemNPM,
		MaxWaves: request.MaxWaves, PollInterval: request.ReleasePoll, RefreshAfter: request.RefreshAfter,
		Previous: previous,
	}, events)
}
func npmPublicationPropagationOptions(lifecycle deps.Options, reportDir string, dryRun, resume bool) deps.Options {
	propagation := lifecycle
	propagation.ReportDir = reportDir
	propagation.DryRun = dryRun
	propagation.Resume = resume
	if dryRun {
		propagation.Commit = false
		propagation.Push = false
		propagation.PR = false
		propagation.Merge = false
	}
	return propagation
}
