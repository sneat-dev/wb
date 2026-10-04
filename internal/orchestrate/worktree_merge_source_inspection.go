package orchestrate

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func canonicalForMergeSource(ctx context.Context, source string) (string, error) {
	return canonicalForMergeSourceWithRunner(ctx, defaultRunner, source)
}

func canonicalForMergeSourceWithRunner(ctx context.Context, run runner.Runner, source string) (string, error) {
	rootOutput, _, err := runCommand(ctx, run, 0, 0, source, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(rootOutput)
	commonOutput, _, err := runCommand(ctx, run, 0, 0, root, "git", "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	common := strings.TrimSpace(commonOutput)
	if !filepath.IsAbs(common) {
		common = filepath.Join(root, common)
	}
	return filepath.Dir(filepath.Clean(common)), nil
}

func inspectWorktreeMergeSources(ctx context.Context, projectsRoot string, paths []string, target string) ([]WorktreeMergeSource, string, string, error) {
	return inspectWorktreeMergeSourcesWithRunner(ctx, defaultRunner, projectsRoot, paths, target)
}

func inspectWorktreeMergeSourcesWithRunner(ctx context.Context, run runner.Runner, projectsRoot string, paths []string, target string) ([]WorktreeMergeSource, string, string, error) {
	sources := make([]WorktreeMergeSource, 0, len(paths))
	seen := map[string]bool{}
	var repository, canonical string
	for _, input := range paths {
		guard, err := worktrees.Guard(ctx, input, worktrees.GuardOptions{ProjectsRoot: projectsRoot, Base: target})
		if err != nil {
			return nil, "", "", fmt.Errorf("guard source %s: %w", input, err)
		}
		if guard.Kind != "linked" || guard.Transient {
			return nil, "", "", fmt.Errorf("source %s must be a non-transient WB linked worktree", input)
		}
		if err := requireCleanMergeWorktreeWithRunner(ctx, run, guard.Path); err != nil {
			return nil, "", "", fmt.Errorf("source %s: %w", input, err)
		}
		view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: projectsRoot, Worktree: guard.Path})
		if err != nil {
			return nil, "", "", fmt.Errorf("load Work Log for source %s: %w", input, err)
		}
		if view.Claim == nil {
			return nil, "", "", fmt.Errorf("source %s has no authoritative active Work Log claim", input)
		}
		if repository == "" {
			repository, canonical = view.Claim.Repository, guard.CanonicalDir
		} else if view.Claim.Repository != repository || filepath.Clean(guard.CanonicalDir) != filepath.Clean(canonical) {
			return nil, "", "", fmt.Errorf("all source worktrees must belong to one repository; got %s and %s", repository, view.Claim.Repository)
		}
		head, err := mergeRevision(ctx, run, guard.Path, "HEAD")
		if err != nil {
			return nil, "", "", err
		}
		if seen[head] {
			return nil, "", "", fmt.Errorf("source head %s was supplied more than once", head)
		}
		seen[head] = true
		sources = append(sources, WorktreeMergeSource{Task: view.Claim.Task, Worktree: guard.Path, Branch: guard.Branch, SHA: head})
	}
	return sources, repository, canonical, nil
}

// PeekWorktreeMergeValidationDeferral cheaply resolves the merge route for
// not-yet-prepared source worktrees and reports whether this call's local
// candidate validation will be deferred to the pull-request route's
// authoritative CI, reusing the exact same resolveWorktreeMergeValidationPlan
// logic RunWorktreeMerge itself applies once a receipt exists. It performs
// only source inspection and remote route/required-check-policy reads --
// never a CPU-heavy local validation run -- so a caller deciding whether to
// gate a not-yet-started merge on host load can learn the answer without
// paying for the validation it is trying to avoid gating on (Minor 10,
// sneat-dev/wb#591 round 3 red-team follow-up: the combined `wb worktree
// land`/`wb land` used to check host load before it could know validation
// would be deferred).
func PeekWorktreeMergeValidationDeferral(ctx context.Context, projectsRoot string, sources []string, target string, requestedRoute WorktreeMergeRoute, validateLocally, allowUnfenced bool, directCIPullRequest ...string) (bool, error) {
	return peekWorktreeMergeValidationDeferral(ctx, defaultRunner, filepath.Abs, projectsRoot, sources, target, requestedRoute, validateLocally, allowUnfenced, directCIPullRequest...)
}

// The native entry binds filepath.Abs. A per-call normalizer allows callers'
// normalization failures to be tested without changing process-wide cwd.
func peekWorktreeMergeValidationDeferral(ctx context.Context, run runner.Runner, abs func(string) (string, error), projectsRoot string, sources []string, target string, requestedRoute WorktreeMergeRoute, validateLocally, allowUnfenced bool, directCIPullRequest ...string) (bool, error) {
	projectsRoot, err := abs(strings.TrimSpace(projectsRoot))
	if err != nil || strings.TrimSpace(projectsRoot) == "" {
		return false, fmt.Errorf("projects root is required")
	}
	if len(sources) == 0 {
		return false, fmt.Errorf("at least one source worktree is required")
	}
	target = strings.TrimSpace(target)
	if target == "" {
		canonicalProbe, probeErr := canonicalForMergeSourceWithRunner(ctx, run, sources[0])
		if probeErr != nil {
			return false, fmt.Errorf("resolve source canonical clone: %w", probeErr)
		}
		target, probeErr = gitops.DefaultBranch(canonicalProbe)
		if probeErr != nil {
			return false, fmt.Errorf("resolve remote default branch: %w", probeErr)
		}
	}
	_, repository, _, err := inspectWorktreeMergeSourcesWithRunner(ctx, run, projectsRoot, sources, target)
	if err != nil {
		return false, err
	}
	plan, err := resolveWorktreeMergeValidationPlan(ctx, repository, target, requestedRoute, validateLocally, allowUnfenced, directCIPullRequest...)
	if err != nil {
		return false, err
	}
	return plan.Defer, nil
}
