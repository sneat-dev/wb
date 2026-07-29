package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// ListOptions selects WB-managed task worktrees and optional GitHub PR state.
type ListOptions struct {
	ProjectsRoot string
	Task         string
	Base         string
	GitHub       bool
}

// PullRequest is the GitHub evidence used to decide whether a branch is safe
// to clean up. HeadSHA must match the current branch tip.
type PullRequest struct {
	Number  int        `json:"number"`
	URL     string     `json:"url"`
	State   string     `json:"state"`
	Base    string     `json:"base"`
	HeadSHA string     `json:"head_sha"`
	Merged  *time.Time `json:"merged_at,omitempty"`
}

// ListResult describes one linked checkout below the WB task hierarchy.
type ListResult struct {
	Task              string       `json:"task"`
	Repository        string       `json:"repository"`
	CanonicalDir      string       `json:"canonical_dir"`
	WorktreeDir       string       `json:"worktree_dir"`
	WorktreesRoot     string       `json:"worktrees_root"`
	Branch            string       `json:"branch"`
	Base              string       `json:"base"`
	HeadSHA           string       `json:"head_sha"`
	RemoteHeadSHA     string       `json:"remote_head_sha,omitempty"`
	Clean             bool         `json:"clean"`
	LocallyMerged     bool         `json:"locally_merged"`
	Locked            bool         `json:"locked"`
	LastCommit        time.Time    `json:"last_commit"`
	OpenPullRequest   *PullRequest `json:"open_pull_request,omitempty"`
	MergedPullRequest *PullRequest `json:"merged_pull_request,omitempty"`
}

// ListDiagnostic describes a malformed task-layout candidate that was skipped
// without hiding valid sibling worktrees. It is intentionally separate from
// ListResult so cleanup can never mistake an unvalidated path for a safe
// linked checkout.
type ListDiagnostic struct {
	Task    string `json:"task,omitempty"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ListOutcome preserves the valid local inventory while exposing every
// deterministic malformed-candidate diagnostic encountered during scanning.
type ListOutcome struct {
	Results     []ListResult     `json:"results"`
	Diagnostics []ListDiagnostic `json:"diagnostics,omitempty"`
}

// CleanupOptions controls planning and removal of merged WB tasks.
type CleanupOptions struct {
	ProjectsRoot string
	Task         string
	Base         string
	AllMerged    bool
	Apply        bool
	DeleteRemote bool
	OlderThan    time.Duration
	ReportDir    string
	Now          func() time.Time
}

// CleanupResult records one repository's cleanup decision and outcome.
type CleanupResult struct {
	ListResult
	Eligible      bool   `json:"eligible"`
	Applied       bool   `json:"applied"`
	RemoteDeleted bool   `json:"remote_deleted"`
	Reason        string `json:"reason,omitempty"`
}

// CleanupOutcome contains the decisions plus the durable audit report written
// before any destructive apply.
type CleanupOutcome struct {
	Results    []CleanupResult `json:"results"`
	ReportPath string          `json:"report_path,omitempty"`
}

type cleanupReport struct {
	GeneratedAt  time.Time       `json:"generated_at"`
	Phase        string          `json:"phase"`
	Task         string          `json:"task,omitempty"`
	AllMerged    bool            `json:"all_merged"`
	Apply        bool            `json:"apply"`
	DeleteRemote bool            `json:"delete_remote"`
	OlderThan    string          `json:"older_than"`
	Results      []CleanupResult `json:"results"`
}

type githubPullRequest struct {
	Number      int        `json:"number"`
	URL         string     `json:"url"`
	State       string     `json:"state"`
	BaseRefName string     `json:"baseRefName"`
	HeadRefOID  string     `json:"headRefOid"`
	MergedAt    *time.Time `json:"mergedAt"`
}

// List inspects real Git worktrees. It stays local unless GitHub is requested.
// Callers that present diagnostics should use ListWithDiagnostics.
func List(ctx context.Context, options ListOptions) ([]ListResult, error) {
	outcome, err := ListWithDiagnostics(ctx, options)
	if err != nil {
		return nil, err
	}
	return outcome.Results, nil
}

// ListWithDiagnostics inventories every resolver-recognized layout. It never
// descends below a Git root, which prevents ordinary repository directories
// such as .claude, .github, source, and generated trees from being re-read as
// task-level repositories.
func ListWithDiagnostics(ctx context.Context, options ListOptions) (ListOutcome, error) {
	projectsRoot, task, base, err := normalizeListOptions(options)
	if err != nil {
		return ListOutcome{}, err
	}
	resolution, err := wbhome.Resolve(projectsRoot)
	if err != nil {
		return ListOutcome{}, err
	}
	outcome := ListOutcome{}
	for _, layout := range resolution.Read {
		results, diagnostics, listErr := listLayout(ctx, projectsRoot, layout, task, base, options.GitHub)
		if listErr != nil {
			return ListOutcome{}, listErr
		}
		outcome.Results = append(outcome.Results, results...)
		outcome.Diagnostics = append(outcome.Diagnostics, diagnostics...)
	}
	sort.Slice(outcome.Results, func(i, j int) bool {
		if outcome.Results[i].Task == outcome.Results[j].Task {
			if outcome.Results[i].Repository == outcome.Results[j].Repository {
				return outcome.Results[i].WorktreeDir < outcome.Results[j].WorktreeDir
			}
			return outcome.Results[i].Repository < outcome.Results[j].Repository
		}
		return outcome.Results[i].Task < outcome.Results[j].Task
	})
	sort.Slice(outcome.Diagnostics, func(i, j int) bool {
		if outcome.Diagnostics[i].Task == outcome.Diagnostics[j].Task {
			return outcome.Diagnostics[i].Path < outcome.Diagnostics[j].Path
		}
		return outcome.Diagnostics[i].Task < outcome.Diagnostics[j].Task
	})
	return outcome, nil
}

func listLayout(
	ctx context.Context,
	projectsRoot string,
	layout wbhome.Layout,
	task, base string,
	withGitHub bool,
) ([]ListResult, []ListDiagnostic, error) {
	taskEntries, err := os.ReadDir(layout.WorktreesRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("read worktree tasks under %s: %w", layout.WorktreesRoot, err)
	}
	results := make([]ListResult, 0)
	diagnostics := make([]ListDiagnostic, 0)
	for _, taskEntry := range taskEntries {
		if !taskEntry.IsDir() || strings.HasPrefix(taskEntry.Name(), ".") || (task != "" && taskEntry.Name() != task) {
			continue
		}
		if !validSafeSegment(taskEntry.Name()) {
			diagnostics = append(diagnostics, listDiagnostic(taskEntry.Name(), filepath.Join(layout.WorktreesRoot, taskEntry.Name()), "invalid task directory name"))
			continue
		}
		taskRoot := filepath.Join(layout.WorktreesRoot, taskEntry.Name())
		_, lockErr := os.Stat(filepath.Join(taskRoot, ".lock"))
		locked := lockErr == nil
		if lockErr != nil && !errors.Is(lockErr, os.ErrNotExist) {
			return nil, nil, fmt.Errorf("inspect task lock %s: %w", taskRoot, lockErr)
		}
		entries, readErr := os.ReadDir(taskRoot)
		if readErr != nil {
			return nil, nil, fmt.Errorf("read task %s: %w", taskEntry.Name(), readErr)
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			candidate := filepath.Join(taskRoot, entry.Name())
			if isGitRoot(ctx, candidate) {
				result, inspectErr := inspectLifecycleWorktree(ctx, projectsRoot, layout, taskEntry.Name(), candidate, base, withGitHub, locked)
				if inspectErr != nil {
					diagnostics = append(diagnostics, listDiagnostic(taskEntry.Name(), candidate, inspectErr.Error()))
					continue
				}
				results = append(results, result)
				// A repository boundary is terminal. Never inspect its source or
				// tool directories as candidate repositories.
				continue
			}
			// Metadata directories are not candidate owners or legacy repository
			// names, but a valid registered worktree itself may intentionally
			// start with a dot. Detect the Git boundary before ignoring ordinary
			// dot directories.
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			if !validSafeSegment(entry.Name()) {
				diagnostics = append(diagnostics, listDiagnostic(taskEntry.Name(), candidate, "invalid owner or legacy repository directory name"))
				continue
			}
			nested, nestedErr := os.ReadDir(candidate)
			if nestedErr != nil {
				diagnostics = append(diagnostics, listDiagnostic(taskEntry.Name(), candidate, fmt.Sprintf("read candidate directory: %v", nestedErr)))
				continue
			}
			for _, repositoryEntry := range nested {
				if !repositoryEntry.IsDir() {
					continue
				}
				repositoryPath := filepath.Join(candidate, repositoryEntry.Name())
				if isGitRoot(ctx, repositoryPath) {
					result, inspectErr := inspectLifecycleWorktree(ctx, projectsRoot, layout, taskEntry.Name(), repositoryPath, base, withGitHub, locked)
					if inspectErr != nil {
						diagnostics = append(diagnostics, listDiagnostic(taskEntry.Name(), repositoryPath, inspectErr.Error()))
						continue
					}
					results = append(results, result)
					continue
				}
				if strings.HasPrefix(repositoryEntry.Name(), ".") {
					continue
				}
				if !validSafeSegment(repositoryEntry.Name()) {
					diagnostics = append(diagnostics, listDiagnostic(taskEntry.Name(), repositoryPath, "invalid repository directory name"))
					continue
				}
				diagnostics = append(diagnostics, listDiagnostic(taskEntry.Name(), repositoryPath, "candidate is not a Git worktree root"))
			}
		}
	}
	return results, diagnostics, nil
}

func listDiagnostic(task, path, message string) ListDiagnostic {
	return ListDiagnostic{Task: task, Path: path, Message: message}
}

func isGitRoot(ctx context.Context, path string) bool {
	root, err := git(ctx, path, "rev-parse", "--show-toplevel")
	return err == nil && filepath.Clean(root) == filepath.Clean(path)
}

// Cleanup plans or applies cleanup for one task or every safely merged task.
// A coordinated task is all-or-nothing: one unsafe repository blocks all of
// its worktrees.
func Cleanup(ctx context.Context, options CleanupOptions) (CleanupOutcome, error) {
	normalized, err := normalizeCleanupOptions(options)
	if err != nil {
		return CleanupOutcome{}, err
	}
	resolution, err := wbhome.Resolve(normalized.ProjectsRoot)
	if err != nil {
		return CleanupOutcome{}, err
	}
	now := normalized.Now()
	if normalized.ReportDir == "" && normalized.Apply {
		normalized.ReportDir = DefaultCleanupReportDir(resolution.Write.Home, now)
	}
	listed, err := ListWithDiagnostics(ctx, ListOptions{
		ProjectsRoot: normalized.ProjectsRoot,
		Task:         normalized.Task,
		Base:         normalized.Base,
		GitHub:       true,
	})
	if err != nil {
		return CleanupOutcome{}, err
	}
	if len(listed.Diagnostics) > 0 {
		first := listed.Diagnostics[0]
		return CleanupOutcome{}, fmt.Errorf("refusing cleanup while managed-worktree inventory has malformed candidate %s: %s", first.Path, first.Message)
	}
	if normalized.Task != "" && len(listed.Results) == 0 {
		return CleanupOutcome{}, fmt.Errorf("WB worktree task %q was not found", normalized.Task)
	}

	results := make([]CleanupResult, len(listed.Results))
	for index, entry := range listed.Results {
		eligible, reason := cleanupEligibility(entry, normalized.OlderThan, now)
		results[index] = CleanupResult{ListResult: entry, Eligible: eligible, Reason: reason}
	}
	blockUnsafeTasks(results)
	outcome := CleanupOutcome{Results: results}
	if normalized.ReportDir != "" {
		outcome.ReportPath, err = writeCleanupReport(normalized, now, "planned", outcome.Results)
		if err != nil {
			return outcome, err
		}
	}
	if !normalized.Apply {
		return outcome, nil
	}

	fail := func(cleanupErr error) (CleanupOutcome, error) {
		if normalized.ReportDir != "" {
			if _, reportErr := writeCleanupReport(normalized, now, "failed", outcome.Results); reportErr != nil {
				return outcome, fmt.Errorf("%w; write failed cleanup report: %v", cleanupErr, reportErr)
			}
		}
		return outcome, cleanupErr
	}
	// Hold the same per-task lock used by worktree creation across the complete
	// recheck-and-remove sequence. Without this lock, a resume or second cleanup
	// could start after the plan observed an unlocked task but before deletion.
	locks, err := acquireCleanupLocks(outcome.Results)
	if err != nil {
		return fail(err)
	}
	defer func() { releaseCleanupLocks(locks) }()

	for index := range outcome.Results {
		if !outcome.Results[index].Eligible {
			continue
		}
		refreshed, err := inspectLifecycleWorktree(
			ctx,
			normalized.ProjectsRoot,
			wbhome.Layout{WorktreesRoot: outcome.Results[index].WorktreesRoot},
			outcome.Results[index].Task,
			outcome.Results[index].WorktreeDir,
			normalized.Base,
			true,
			false, // The task is locked by this cleanup operation.
		)
		if err != nil {
			return fail(err)
		}
		eligible, reason := cleanupEligibility(refreshed, normalized.OlderThan, now)
		if !eligible {
			return fail(fmt.Errorf("cleanup safety changed for %s: %s", refreshed.Repository, reason))
		}
		if refreshed.HeadSHA != outcome.Results[index].HeadSHA {
			return fail(fmt.Errorf("cleanup safety changed for %s: branch head moved", refreshed.Repository))
		}
		outcome.Results[index].ListResult = refreshed
		if normalized.DeleteRemote && refreshed.RemoteHeadSHA != "" {
			if err := deleteRemoteBranch(ctx, refreshed); err != nil {
				return fail(err)
			}
			outcome.Results[index].RemoteDeleted = true
		}
		if _, err := git(ctx, refreshed.CanonicalDir, "worktree", "remove", refreshed.WorktreeDir); err != nil {
			return fail(fmt.Errorf("remove worktree %s: %w", refreshed.WorktreeDir, err))
		}
		if _, err := git(ctx, refreshed.CanonicalDir, "update-ref", "-d", "refs/heads/"+refreshed.Branch, refreshed.HeadSHA); err != nil {
			return fail(fmt.Errorf("delete local branch %s at %s: %w", refreshed.Branch, refreshed.HeadSHA, err))
		}
		outcome.Results[index].Applied = true
	}
	releaseCleanupLocks(locks)
	locks = nil
	removeEmptyTaskDirectories(outcome.Results)
	if normalized.ReportDir != "" {
		outcome.ReportPath, err = writeCleanupReport(normalized, now, "applied", outcome.Results)
		if err != nil {
			return outcome, err
		}
	}
	return outcome, nil
}

func normalizeListOptions(options ListOptions) (projectsRoot, task, base string, err error) {
	projectsRoot, err = absoluteProjectsRoot(options.ProjectsRoot)
	if err != nil {
		return "", "", "", err
	}
	task = strings.TrimSpace(options.Task)
	if task != "" && !validSafeSegment(task) {
		return "", "", "", fmt.Errorf("task %q must be one safe path segment", task)
	}
	base = strings.TrimSpace(options.Base)
	if base == "" {
		base = "main"
	}
	if !validBranch(context.Background(), base) {
		return "", "", "", fmt.Errorf("invalid base branch %q", base)
	}
	return projectsRoot, task, base, nil
}

func normalizeCleanupOptions(options CleanupOptions) (CleanupOptions, error) {
	projectsRoot, task, base, err := normalizeListOptions(ListOptions{
		ProjectsRoot: options.ProjectsRoot,
		Task:         options.Task,
		Base:         options.Base,
	})
	if err != nil {
		return CleanupOptions{}, err
	}
	options.ProjectsRoot = projectsRoot
	options.Task = task
	options.Base = base
	if options.Task == "" && !options.AllMerged {
		return CleanupOptions{}, fmt.Errorf("supply one task or use --all-merged")
	}
	if options.Task != "" && options.AllMerged {
		return CleanupOptions{}, fmt.Errorf("task and --all-merged cannot be combined")
	}
	if options.OlderThan < 0 {
		return CleanupOptions{}, fmt.Errorf("--older-than cannot be negative")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.ReportDir != "" {
		options.ReportDir, err = filepath.Abs(options.ReportDir)
		if err != nil {
			return CleanupOptions{}, fmt.Errorf("resolve cleanup report directory: %w", err)
		}
		options.ReportDir = filepath.Clean(options.ReportDir)
	}
	return options, nil
}

// DefaultCleanupReportDir returns the durable audit directory for one apply,
// below the already-resolved WB home directory (see wbhome.Root).
func DefaultCleanupReportDir(home string, now time.Time) string {
	return filepath.Join(
		home,
		"reports",
		"worktree-cleanup",
		now.UTC().Format("20060102T150405.000000000Z"),
	)
}

func validSafeSegment(value string) bool {
	return safeSegment.MatchString(value) && value != "." && value != ".."
}

func validRepositorySegment(value string) bool {
	return safeRepositorySegment.MatchString(value) && value != "." && value != ".."
}

func inspectLifecycleWorktree(
	ctx context.Context,
	projectsRoot string,
	layout wbhome.Layout,
	task, worktree, base string,
	withGitHub, locked bool,
) (ListResult, error) {
	root, err := git(ctx, worktree, "rev-parse", "--show-toplevel")
	if err != nil {
		return ListResult{}, fmt.Errorf("inspect %s: %w", worktree, err)
	}
	if filepath.Clean(root) != filepath.Clean(worktree) {
		return ListResult{}, fmt.Errorf("WB worktree %s has Git root %s", worktree, root)
	}
	location, err := locateManagedWorktree(ctx, projectsRoot, worktree, []wbhome.Layout{layout})
	if err != nil {
		return ListResult{}, err
	}
	if location.Task != task {
		return ListResult{}, fmt.Errorf("WB worktree %s belongs to task %q, not %q", worktree, location.Task, task)
	}
	slug := location.Owner + "/" + location.Repository
	canonical := filepath.Join(projectsRoot, location.Owner, location.Repository)
	_, commonDir, err := gitDirectories(ctx, worktree)
	if err != nil {
		return ListResult{}, err
	}
	expectedCommonDir := filepath.Join(canonical, ".git")
	if resolved, resolveErr := filepath.EvalSymlinks(expectedCommonDir); resolveErr == nil {
		expectedCommonDir = resolved
	}
	if filepath.Clean(commonDir) != filepath.Clean(expectedCommonDir) {
		return ListResult{}, fmt.Errorf("WB worktree %s belongs to %s, not %s", worktree, commonDir, canonical)
	}
	branch, err := git(ctx, worktree, "branch", "--show-current")
	if err != nil {
		return ListResult{}, err
	}
	if branch == "" || branch == base {
		return ListResult{}, fmt.Errorf("WB worktree %s is not on a feature branch", worktree)
	}
	head, err := git(ctx, worktree, "rev-parse", "HEAD")
	if err != nil {
		return ListResult{}, err
	}
	clean, err := cleanWorktree(ctx, worktree)
	if err != nil {
		return ListResult{}, err
	}
	locallyMerged, err := isAncestor(ctx, canonical, head, "origin/"+base)
	if err != nil {
		return ListResult{}, err
	}
	lastCommitValue, err := git(ctx, worktree, "show", "-s", "--format=%cI", "HEAD")
	if err != nil {
		return ListResult{}, err
	}
	lastCommit, err := time.Parse(time.RFC3339, lastCommitValue)
	if err != nil {
		return ListResult{}, fmt.Errorf("parse last commit time for %s: %w", slug, err)
	}
	result := ListResult{
		Task: task, Repository: slug, CanonicalDir: canonical, WorktreeDir: worktree,
		WorktreesRoot: layout.WorktreesRoot,
		Branch:        branch, Base: base, HeadSHA: head,
		Clean: clean, LocallyMerged: locallyMerged, Locked: locked, LastCommit: lastCommit,
	}
	if withGitHub {
		result.RemoteHeadSHA, err = remoteBranchHead(ctx, canonical, branch)
		if err != nil {
			return ListResult{}, err
		}
		pullRequests, err := githubPullRequests(ctx, worktree, slug, branch)
		if err != nil {
			return ListResult{}, err
		}
		result.OpenPullRequest, result.MergedPullRequest = matchingPullRequests(pullRequests, base, head)
	}
	return result, nil
}

func isAncestor(ctx context.Context, repository, ancestor, descendant string) (bool, error) {
	command := exec.CommandContext(ctx, "git", "-C", repository, "merge-base", "--is-ancestor", ancestor, descendant)
	command.Env = console.Env()
	err := command.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check whether %s is merged into %s: %w", ancestor, descendant, err)
}

func remoteBranchHead(ctx context.Context, repository, branch string) (string, error) {
	output, err := git(ctx, repository, "ls-remote", "--heads", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return "", nil
	}
	if len(fields) != 2 {
		return "", fmt.Errorf("unexpected remote branch response for %s: %q", branch, output)
	}
	return fields[0], nil
}

func githubPullRequests(ctx context.Context, worktree, repository, branch string) ([]githubPullRequest, error) {
	command := exec.CommandContext(
		ctx,
		"gh", "pr", "list",
		"--repo", repository,
		"--head", branch,
		"--state", "all",
		"--limit", "100",
		"--json", "number,url,state,mergedAt,headRefOid,baseRefName",
	)
	command.Dir = worktree
	command.Env = console.Env()
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("query pull requests for %s:%s: %w: %s", repository, branch, err, strings.TrimSpace(string(output)))
	}
	var pullRequests []githubPullRequest
	if err := json.Unmarshal(output, &pullRequests); err != nil {
		return nil, fmt.Errorf("decode pull requests for %s:%s: %w", repository, branch, err)
	}
	return pullRequests, nil
}

func matchingPullRequests(pullRequests []githubPullRequest, base, head string) (open, merged *PullRequest) {
	for _, candidate := range pullRequests {
		pullRequest := &PullRequest{
			Number: candidate.Number, URL: candidate.URL, State: candidate.State,
			Base: candidate.BaseRefName, HeadSHA: candidate.HeadRefOID, Merged: candidate.MergedAt,
		}
		if strings.EqualFold(candidate.State, "OPEN") {
			if open == nil || candidate.Number > open.Number {
				open = pullRequest
			}
			continue
		}
		if !strings.EqualFold(candidate.State, "MERGED") ||
			candidate.BaseRefName != base ||
			candidate.HeadRefOID != head ||
			candidate.MergedAt == nil {
			continue
		}
		if merged == nil || candidate.MergedAt.After(*merged.Merged) {
			merged = pullRequest
		}
	}
	return open, merged
}

func cleanupEligibility(entry ListResult, olderThan time.Duration, now time.Time) (bool, string) {
	switch {
	case entry.Locked:
		return false, "task is locked by an active or interrupted operation"
	case !entry.Clean:
		return false, "worktree has local changes"
	case entry.OpenPullRequest != nil:
		return false, "branch still has an open pull request: " + entry.OpenPullRequest.URL
	case entry.MergedPullRequest == nil:
		return false, "no merged pull request matches the current branch head and base"
	case entry.RemoteHeadSHA != "" && entry.RemoteHeadSHA != entry.HeadSHA:
		return false, "remote branch advanced after the merged pull request"
	case olderThan > 0 && entry.MergedPullRequest.Merged.Add(olderThan).After(now):
		return false, "merged pull request is newer than the cleanup safety window"
	default:
		return true, ""
	}
}

func blockUnsafeTasks(results []CleanupResult) {
	reasonByTask := map[string]string{}
	for _, result := range results {
		if !result.Eligible && reasonByTask[result.Task] == "" {
			reasonByTask[result.Task] = result.Repository + ": " + result.Reason
		}
	}
	for index := range results {
		if results[index].Eligible && reasonByTask[results[index].Task] != "" {
			results[index].Eligible = false
			results[index].Reason = "coordinated task blocked by " + reasonByTask[results[index].Task]
		}
	}
}

func acquireCleanupLocks(results []CleanupResult) ([]operationLock, error) {
	locks := make([]operationLock, 0)
	seen := map[string]bool{}
	for _, result := range results {
		key := result.WorktreesRoot + "\x00" + result.Task
		if !result.Eligible || seen[key] {
			continue
		}
		seen[key] = true
		taskRoot := filepath.Join(result.WorktreesRoot, result.Task)
		lock, err := acquireLock(taskRoot)
		if err != nil {
			releaseCleanupLocks(locks)
			return nil, fmt.Errorf("lock cleanup task %s: %w", result.Task, err)
		}
		locks = append(locks, lock)
	}
	return locks, nil
}

func releaseCleanupLocks(locks []operationLock) {
	for index := len(locks) - 1; index >= 0; index-- {
		locks[index].release()
	}
}

func deleteRemoteBranch(ctx context.Context, entry ListResult) error {
	lease := "--force-with-lease=refs/heads/" + entry.Branch + ":" + entry.HeadSHA
	refspec := ":refs/heads/" + entry.Branch
	if _, err := git(ctx, entry.CanonicalDir, "push", lease, "origin", refspec); err != nil {
		return fmt.Errorf("delete remote branch %s at %s: %w", entry.Branch, entry.HeadSHA, err)
	}
	return nil
}

func removeEmptyTaskDirectories(results []CleanupResult) {
	tasks := map[string]bool{}
	for _, result := range results {
		if !result.Applied {
			continue
		}
		taskRoot := filepath.Join(result.WorktreesRoot, result.Task)
		// Current worktrees leave an empty owner directory; legacy direct
		// worktrees have the task directory as their immediate parent. Both
		// removals are best-effort and refuse non-empty sibling worktrees.
		_ = os.Remove(filepath.Dir(result.WorktreeDir))
		tasks[taskRoot] = true
	}
	for taskRoot := range tasks {
		_ = os.Remove(taskRoot)
	}
}

func writeCleanupReport(
	options CleanupOptions,
	generatedAt time.Time,
	phase string,
	results []CleanupResult,
) (string, error) {
	if err := os.MkdirAll(options.ReportDir, 0o755); err != nil {
		return "", fmt.Errorf("create cleanup report directory: %w", err)
	}
	report := cleanupReport{
		GeneratedAt:  generatedAt,
		Phase:        phase,
		Task:         options.Task,
		AllMerged:    options.AllMerged,
		Apply:        options.Apply,
		DeleteRemote: options.DeleteRemote,
		OlderThan:    options.OlderThan.String(),
		Results:      results,
	}
	content, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode cleanup report: %w", err)
	}
	content = append(content, '\n')
	path := filepath.Join(options.ReportDir, "cleanup.json")
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, content, 0o644); err != nil {
		return "", fmt.Errorf("write cleanup report: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return "", fmt.Errorf("activate cleanup report: %w", err)
	}
	return path, nil
}
