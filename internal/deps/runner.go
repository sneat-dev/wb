package deps

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/sneat-dev/wb/internal/orchestrate"
)

// Run resolves one exact target and delegates repository lifecycle to the
// shared typed orchestration engine. The adapter owns dependency decisions.
func Run(ctx context.Context, target Target, repositories []Repository, options Options) (Report, error) {
	options, lifecycle, err := normalizeOptions(options, operationID(target))
	if err != nil {
		return Report{}, err
	}
	adapter := adapterFor(target.Ecosystem)
	if adapter == nil {
		return Report{}, fmt.Errorf("unsupported dependency ecosystem %q", target.Ecosystem)
	}
	if target.Ecosystem == EcosystemGitHubActions {
		target.Resolved, err = resolveGitHubRef(ctx, target.Dependency, target.Version, options)
		if err != nil {
			return Report{}, err
		}
	} else {
		target.Resolved = target.Version
	}
	handler := exactSetHandler{adapter: adapter, target: target, options: options}
	results, runErr := orchestrate.Run(ctx, repositories, handler, lifecycle)
	report := Report{
		SchemaVersion: 1, Operation: lifecycle.Operation, Status: "completed", Target: target,
		GitHubDir: lifecycle.GitHubDir, BaseRef: lifecycle.Ref, Parallel: lifecycle.Parallel,
	}
	if lifecycle.Verify {
		report.Verification = append(report.Verification, lifecycle.Checks...)
	}
	for _, result := range results {
		report.Repositories = append(report.Repositories, repositoryReportFromResult(result))
	}
	if runErr != nil {
		report.Status = "failed"
	}
	return report, runErr
}

func repositoryReportFromResult(result orchestrate.Result[[]Decision]) RepositoryReport {
	repository := RepositoryReport{
		Repository: result.Repository, CanonicalDir: result.CanonicalDir,
		WorktreeDir: result.WorktreeDir, Branch: result.Branch, Ref: result.Ref,
		Status: result.Status, Reason: result.Reason, Decisions: result.Metadata,
		ChangedFiles: result.ChangedFiles, Verifications: result.Verifications,
		Commit: result.Commit, Pushed: result.Pushed, PR: result.PR,
		Merged: result.Merged,
	}
	for _, check := range result.Checks {
		repository.Checks = append(repository.Checks, RemoteCheck(check))
	}
	sortRepositoryReport(&repository)
	return repository
}

type exactSetHandler struct {
	adapter adapter
	target  Target
	options Options
}

func (handler exactSetHandler) Inspect(ctx context.Context, canonical, base string, _ orchestrate.Repository) (orchestrate.Assessment[[]Decision], error) {
	decisions, err := handler.adapter.inspect(ctx, canonical, base, handler.target, handler.options)
	assessment := orchestrate.Assessment[[]Decision]{Metadata: decisions}
	if err != nil {
		return assessment, err
	}
	if len(decisions) == 0 {
		assessment.Reason = fmt.Sprintf("dependency absent on %s", base)
		return assessment, nil
	}
	assessment.Applicable = true
	for _, decision := range decisions {
		if decision.Action != "unchanged" {
			assessment.NeedsChange = true
			break
		}
	}
	if assessment.NeedsChange {
		assessment.Reason = "existing references require the exact target"
	} else {
		assessment.Reason = "all existing references are already at the exact target"
	}
	return assessment, nil
}

func (handler exactSetHandler) Apply(ctx context.Context, worktree string, _ orchestrate.Repository) ([]Decision, error) {
	return handler.adapter.apply(ctx, worktree, handler.target, handler.options)
}

func (handler exactSetHandler) ValidatePublishable(_ context.Context, worktree string, _ orchestrate.Repository) error {
	if handler.target.Ecosystem != EcosystemGo {
		return nil
	}
	return validatePublishableGoManifests(worktree)
}

func (handler exactSetHandler) CommitMessage(orchestrate.Repository) string {
	return fmt.Sprintf("chore(deps): set %s to %s", handler.target.Dependency, handler.target.Version)
}

func (handler exactSetHandler) PullRequest(orchestrate.Repository) (string, string) {
	title := handler.CommitMessage(orchestrate.Repository{})
	body := fmt.Sprintf("Automated by `wb deps set %s %s@%s`. Applicable local lint, test, and build verification completed before this pull request was opened.", handler.target.Ecosystem, handler.target.Dependency, handler.target.Version)
	return title, body
}

func normalizeOptions(options Options, operation string) (Options, orchestrate.Options, error) {
	lifecycle, err := orchestrate.Normalize(orchestrate.Options{
		GitHubDir: options.GitHubDir, Operation: operation,
		Branch: "wb/deps/" + strings.TrimPrefix(operation, "deps-"), Ref: options.Ref,
		Parallel: options.Parallel, DryRun: options.DryRun, Resume: options.Resume,
		Verify: options.Verify, Checks: options.Checks, Timeout: options.Timeout, Retry: options.Retry,
		Commit: options.Commit, Push: options.Push, PR: options.PR, Merge: options.Merge,
	})
	if err != nil {
		return Options{}, orchestrate.Options{}, err
	}
	options.GitHubDir = lifecycle.GitHubDir
	options.Ref = lifecycle.Ref
	options.Parallel = lifecycle.Parallel
	options.Checks = append(options.Checks[:0], lifecycle.Checks...)
	options.GoPrivate = normalizeGoPrivatePatterns(options.GoPrivate)
	options.Commit = lifecycle.Commit
	options.Push = lifecycle.Push
	options.PR = lifecycle.PR
	options.Merge = lifecycle.Merge
	return options, lifecycle, nil
}

func operationID(target Target) string {
	return "deps-set-" + safeSlug(string(target.Ecosystem)+"-"+target.Dependency+"-"+target.Version)
}

func safeSlug(value string) string {
	var output strings.Builder
	separator := false
	for _, character := range strings.ToLower(value) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			if separator && output.Len() > 0 {
				output.WriteByte('-')
			}
			separator = false
			output.WriteRune(character)
		} else {
			separator = true
		}
	}
	return strings.Trim(output.String(), "-")
}

func sortDecisions(decisions []Decision) {
	sort.Slice(decisions, func(i, j int) bool {
		if decisions[i].File == decisions[j].File {
			if decisions[i].Dependency != decisions[j].Dependency {
				return decisions[i].Dependency < decisions[j].Dependency
			}
			return decisions[i].BeforeRef < decisions[j].BeforeRef
		}
		return decisions[i].File < decisions[j].File
	})
}
