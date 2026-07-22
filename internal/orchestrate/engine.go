package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/quality"
)

// Run executes a typed mutation over independent repositories. It completes
// every safe local/PR stage before entering the CI wait-and-merge phase.
func Run[T any](ctx context.Context, repositories []Repository, handler Handler[T], options Options) ([]Result[T], error) {
	options, err := Normalize(options)
	if err != nil {
		return nil, err
	}
	repositories = append([]Repository(nil), repositories...)
	sort.Slice(repositories, func(i, j int) bool { return repositories[i].Slug < repositories[j].Slug })
	results := make([]Result[T], len(repositories))
	for index, repository := range repositories {
		results[index] = Result[T]{Repository: repository.Slug, Ref: options.Ref, Status: "selected"}
	}
	if !options.DryRun {
		lock, err := AcquireOperationLock(options.GitHubDir, options.Operation)
		if err != nil {
			return results, err
		}
		defer lock.Release()
	}
	errorsByRepository := make([]error, len(repositories))
	runParallel(len(repositories), options.Parallel, func(index int) {
		errorsByRepository[index] = processRepository(ctx, repositories[index], handler, options, &results[index])
		sort.Strings(results[index].ChangedFiles)
	})
	if options.Merge {
		runParallel(len(repositories), options.Parallel, func(index int) {
			if errorsByRepository[index] != nil || results[index].PR == "" {
				return
			}
			if err := waitAndMerge(ctx, options, &results[index]); err != nil {
				results[index].Status = "failed"
				results[index].Reason = err.Error()
				errorsByRepository[index] = fmt.Errorf("%s: %w", repositories[index].Slug, err)
			}
		})
	}
	var runErrors []error
	for _, err := range errorsByRepository {
		if err != nil {
			runErrors = append(runErrors, err)
		}
	}
	return results, errors.Join(runErrors...)
}

// Normalize validates lifecycle settings and applies cumulative publication
// implications shared by every orchestrated command.
func Normalize(options Options) (Options, error) {
	if strings.TrimSpace(options.GitHubDir) == "" {
		return Options{}, fmt.Errorf("GitHub directory is required")
	}
	absolute, err := filepath.Abs(options.GitHubDir)
	if err != nil {
		return Options{}, err
	}
	options.GitHubDir = absolute
	if strings.TrimSpace(options.Operation) == "" {
		return Options{}, fmt.Errorf("operation identity is required")
	}
	if options.Branch == "" {
		options.Branch = "wb/" + options.Operation
	}
	if options.Ref == "" {
		options.Ref = "main"
	}
	if options.Parallel == 0 {
		options.Parallel = 1
	}
	if options.Parallel < 1 {
		return Options{}, fmt.Errorf("parallelism must be at least 1")
	}
	if options.Retry < 0 {
		return Options{}, fmt.Errorf("retry count must not be negative")
	}
	if options.Timeout < 0 {
		return Options{}, fmt.Errorf("timeout must not be negative")
	}
	if options.Push {
		options.Commit = true
	}
	if options.PR {
		options.Push = true
		options.Commit = true
	}
	if options.Merge {
		options.PR = true
		options.Push = true
		options.Commit = true
	}
	if options.DryRun && (options.Commit || options.Push || options.PR || options.Merge || options.Resume) {
		return Options{}, fmt.Errorf("--dry-run cannot be combined with --commit, --push, --pr, --merge, or --resume")
	}
	if options.Verify && len(options.Checks) == 0 {
		options.Checks = []quality.Check{quality.CheckLint, quality.CheckTest, quality.CheckBuild}
	}
	return options, nil
}

func processRepository[T any](ctx context.Context, repository Repository, handler Handler[T], options Options, result *Result[T]) error {
	if repository.Archived {
		result.Status = "skipped"
		result.Reason = "repository is archived"
		return nil
	}
	owner, name, err := splitRepository(repository.Slug)
	if err != nil {
		return failResult(result, err)
	}
	canonical := repository.Path
	if canonical == "" {
		canonical = filepath.Join(options.GitHubDir, owner, name)
	}
	result.CanonicalDir = canonical
	if err := EnsureCanonical(ctx, repository, canonical, options); err != nil {
		return failResult(result, err)
	}
	base := "origin/" + options.Ref
	assessment, err := handler.Inspect(ctx, canonical, base, repository)
	result.Metadata = assessment.Metadata
	if err != nil {
		return failResult(result, err)
	}
	if !assessment.Applicable {
		result.Status = "skipped"
		result.Reason = assessment.Reason
		return nil
	}
	if !assessment.NeedsChange {
		result.Status = "skipped"
		result.Reason = assessment.Reason
		return nil
	}
	if options.DryRun {
		result.Status = "planned"
		result.Reason = assessment.Reason
		return nil
	}
	worktree := filepath.Join(options.GitHubDir, ".wb", "worktrees", options.Operation, owner, name)
	result.WorktreeDir = worktree
	result.Branch = options.Branch
	if err := prepareWorktree(ctx, canonical, worktree, options.Branch, base, options); err != nil {
		return failResult(result, err)
	}
	metadata, err := handler.Apply(ctx, worktree, repository)
	result.Metadata = metadata
	if err != nil {
		return failResult(result, err)
	}
	if options.Commit {
		if err := handler.ValidatePublishable(ctx, worktree, repository); err != nil {
			return failResult(result, fmt.Errorf("publishability validation failed: %w", err))
		}
	}
	result.ChangedFiles, err = changedFiles(ctx, worktree, options)
	if err != nil {
		return failResult(result, err)
	}
	ahead, err := branchAhead(ctx, worktree, base, options)
	if err != nil {
		return failResult(result, err)
	}
	if len(result.ChangedFiles) == 0 && !ahead {
		result.Status = "skipped"
		result.Reason = "mutation produced no file change"
		return nil
	}
	if options.Verify {
		verification := quality.VerifyWithOptions(ctx, repository.Slug, worktree, options.Checks, quality.RunOptions{Timeout: options.Timeout, Retry: options.Retry})
		result.Verifications = verification.Results
		if verification.Status == quality.StatusFailed {
			return failResult(result, fmt.Errorf("local verification failed"))
		}
	}
	if !options.Commit {
		result.Status = "changed"
		result.Reason = "verified changes remain in the local operation worktree"
		return nil
	}
	if len(result.ChangedFiles) > 0 {
		if _, _, err := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "add", "-A"); err != nil {
			return failResult(result, err)
		}
		if _, _, err := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "commit", "-m", handler.CommitMessage(repository)); err != nil {
			return failResult(result, err)
		}
	}
	head, _, err := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "rev-parse", "HEAD")
	if err != nil {
		return failResult(result, err)
	}
	result.Commit = strings.TrimSpace(head)
	result.Status = "committed"
	result.Reason = "verified operation committed locally"
	if options.Push {
		if _, _, err := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "push", "-u", "origin", options.Branch); err != nil {
			return failResult(result, err)
		}
		result.Pushed = true
		result.Status = "pushed"
		result.Reason = "verified commit pushed to the operation branch"
	}
	if options.PR {
		title, body := handler.PullRequest(repository)
		prURL, err := openPullRequest(ctx, worktree, options.Branch, options.Ref, title, body, options)
		if err != nil {
			return failResult(result, err)
		}
		result.PR = prURL
		result.Status = "pr_open"
		result.Reason = "pull request opened; local verification passed"
	}
	return nil
}

// EnsureCanonical clones a missing repository, fetches origin, and verifies
// the configured base ref without checking out or modifying the canonical tree.
func EnsureCanonical(ctx context.Context, repository Repository, canonical string, options Options) error {
	if _, err := os.Stat(canonical); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
			return err
		}
		cloneURL := repository.CloneURL
		if cloneURL == "" {
			cloneURL = "https://github.com/" + repository.Slug + ".git"
		}
		if _, _, err := runCommand(ctx, options.Timeout, 0, filepath.Dir(canonical), "git", "clone", "--quiet", cloneURL, canonical); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if _, _, err := runCommand(ctx, options.Timeout, options.Retry, canonical, "git", "fetch", "--quiet", "origin"); err != nil {
		return err
	}
	if _, _, err := runCommand(ctx, options.Timeout, options.Retry, canonical, "git", "rev-parse", "--verify", "origin/"+options.Ref+"^{commit}"); err != nil {
		return fmt.Errorf("%s does not contain origin/%s: %w", repository.Slug, options.Ref, err)
	}
	return nil
}

func prepareWorktree(ctx context.Context, canonical, worktree, branch, base string, options Options) error {
	if _, err := os.Stat(worktree); err == nil {
		if !options.Resume {
			return fmt.Errorf("operation worktree already exists: %s (use --resume or choose a different operation)", worktree)
		}
		current, _, err := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "branch", "--show-current")
		if err != nil {
			return err
		}
		if strings.TrimSpace(current) != branch {
			return fmt.Errorf("cannot resume worktree branch %q; want %q", strings.TrimSpace(current), branch)
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(worktree), 0o755); err != nil {
		return err
	}
	if _, _, err := runCommand(ctx, options.Timeout, options.Retry, canonical, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		if !options.Resume {
			return fmt.Errorf("operation branch already exists: %s (use --resume)", branch)
		}
		_, _, err = runCommand(ctx, options.Timeout, options.Retry, canonical, "git", "worktree", "add", "--quiet", worktree, branch)
		return err
	}
	_, _, err := runCommand(ctx, options.Timeout, options.Retry, canonical, "git", "worktree", "add", "--quiet", "-b", branch, worktree, base)
	return err
}

func changedFiles(ctx context.Context, worktree string, options Options) ([]string, error) {
	output, _, err := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "status", "--porcelain=v1", "-z")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, entry := range strings.Split(strings.TrimSuffix(output, "\x00"), "\x00") {
		if len(entry) < 4 {
			continue
		}
		path := entry[3:]
		if arrow := strings.LastIndex(path, " -> "); arrow >= 0 {
			path = path[arrow+4:]
		}
		files = append(files, filepath.ToSlash(path))
	}
	return files, nil
}

func branchAhead(ctx context.Context, worktree, base string, options Options) (bool, error) {
	output, _, err := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "rev-list", base+"..HEAD")
	return strings.TrimSpace(output) != "", err
}

func openPullRequest(ctx context.Context, worktree, branch, base, title, body string, options Options) (string, error) {
	output, _, err := runCommand(ctx, options.Timeout, options.Retry, worktree, "gh", "pr", "list", "--head", branch, "--base", base,
		"--state", "open", "--json", "url", "--jq", ".[0].url")
	if err == nil {
		if existing := strings.TrimSpace(output); existing != "" {
			return existing, nil
		}
	}
	output, _, err = runCommand(ctx, options.Timeout, options.Retry, worktree, "gh", "pr", "create", "--base", base, "--head", branch, "--title", title, "--body", body)
	if err != nil {
		return "", err
	}
	if url := lastNonEmptyLine(output); url != "" {
		return url, nil
	}
	return "", fmt.Errorf("gh pr create returned no pull request URL")
}

func waitAndMerge[T any](ctx context.Context, options Options, result *Result[T]) error {
	deadline := time.Time{}
	if options.Timeout > 0 {
		deadline = time.Now().Add(options.Timeout)
	}
	for {
		output, _, err := runCommand(ctx, options.Timeout, options.Retry, result.WorktreeDir, "gh", "pr", "checks", result.PR, "--json", "name,bucket,link")
		checks, pending, checkErr := decodePullRequestChecks(result.PR, output, err)
		if checkErr != nil {
			return checkErr
		}
		result.Checks = checks
		failed := false
		for _, check := range checks {
			switch check.Bucket {
			case "pass", "skipping":
			case "fail", "cancel":
				failed = true
			default:
				pending = true
			}
		}
		if failed {
			return fmt.Errorf("GitHub checks failed for %s", result.PR)
		}
		if len(checks) > 0 && !pending {
			break
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			if len(checks) == 0 {
				return fmt.Errorf("no GitHub checks appeared before timeout for %s", result.PR)
			}
			return fmt.Errorf("GitHub checks remained pending past timeout for %s", result.PR)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(githubChecksPollInterval(options)):
		}
	}
	if _, _, err := runCommand(ctx, options.Timeout, options.Retry, result.WorktreeDir, "gh", "pr", "merge", result.PR, "--merge"); err != nil {
		return err
	}
	result.Merged = true
	result.Status = "merged"
	result.Reason = "all observed GitHub checks passed or skipped; pull request merged normally"
	return nil
}

func decodePullRequestChecks(pr, output string, commandErr error) ([]RemoteCheck, bool, error) {
	if commandErr != nil {
		if strings.Contains(strings.ToLower(output), "no checks reported") {
			return nil, true, nil
		}
		return nil, false, commandErr
	}
	var checks []RemoteCheck
	if err := json.Unmarshal([]byte(output), &checks); err != nil {
		return nil, false, fmt.Errorf("decode checks for %s: %w", pr, err)
	}
	return checks, len(checks) == 0, nil
}

func githubChecksPollInterval(options Options) time.Duration {
	if options.CheckPollInterval > 0 {
		return options.CheckPollInterval
	}
	return 10 * time.Second
}

func failResult[T any](result *Result[T], err error) error {
	result.Status = "failed"
	result.Reason = err.Error()
	return fmt.Errorf("%s: %w", result.Repository, err)
}

func splitRepository(slug string) (string, string, error) {
	owner, name, ok := strings.Cut(slug, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("invalid repository slug %q (want owner/repository)", slug)
	}
	return owner, name, nil
}

func runParallel(count, parallel int, run func(int)) {
	if count == 0 {
		return
	}
	if parallel > count {
		parallel = count
	}
	jobs := make(chan int)
	var group sync.WaitGroup
	for range parallel {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range jobs {
				run(index)
			}
		}()
	}
	for index := range count {
		jobs <- index
	}
	close(jobs)
	group.Wait()
}

// OperationLock prevents two processes from mutating the same operation
// worktrees. Higher-level planners may hold a campaign lock while individual
// lifecycle runs also protect their wave directories.
type OperationLock struct{ path string }

// AcquireOperationLock creates an exclusive lock below the operation root.
func AcquireOperationLock(githubDir, operation string) (OperationLock, error) {
	directory := filepath.Join(githubDir, ".wb", "worktrees", operation)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return OperationLock{}, err
	}
	path := filepath.Join(directory, ".lock")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return OperationLock{}, fmt.Errorf("operation is already active or was interrupted: %s", path)
		}
		return OperationLock{}, err
	}
	if _, err := fmt.Fprintf(file, "operation=%s\npid=%d\n", operation, os.Getpid()); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return OperationLock{}, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return OperationLock{}, err
	}
	return OperationLock{path: path}, nil
}

// Release removes the operation lock. It is safe to call from defer.
func (lock OperationLock) Release() { _ = os.Remove(lock.path) }
