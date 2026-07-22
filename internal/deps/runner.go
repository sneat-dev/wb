package deps

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
	"unicode"

	"github.com/sneat-dev/wb/internal/quality"
)

// Run applies one exact target to the selected repositories. Independent
// repository failures are accumulated so every safe result reaches the report.
func Run(ctx context.Context, target Target, repositories []Repository, options Options) (Report, error) {
	options, err := normalizeOptions(options)
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
	operation := operationID(target)
	report := Report{
		SchemaVersion: 1, Operation: operation, Status: "running", Target: target,
		GitHubDir: options.GitHubDir, BaseRef: options.Ref, Parallel: options.Parallel,
	}
	if options.Verify {
		report.Verification = append([]quality.Check(nil), options.Checks...)
	}
	sort.Slice(repositories, func(i, j int) bool { return repositories[i].Slug < repositories[j].Slug })
	report.Repositories = make([]RepositoryReport, len(repositories))
	for index, repository := range repositories {
		report.Repositories[index] = RepositoryReport{Repository: repository.Slug, Ref: options.Ref, Status: "selected"}
	}

	if !options.DryRun {
		lock, err := acquireLock(options.GitHubDir, operation)
		if err != nil {
			return report, err
		}
		defer lock.release()
	}

	errorsByRepository := make([]error, len(repositories))
	runParallel(len(repositories), options.Parallel, func(index int) {
		errorsByRepository[index] = processRepository(ctx, adapter, target, repositories[index], options, operation, &report.Repositories[index])
		sortRepositoryReport(&report.Repositories[index])
	})
	if options.Merge {
		runParallel(len(repositories), options.Parallel, func(index int) {
			if errorsByRepository[index] != nil || report.Repositories[index].PR == "" {
				return
			}
			if err := waitAndMerge(ctx, options, &report.Repositories[index]); err != nil {
				report.Repositories[index].Status = "failed"
				report.Repositories[index].Reason = err.Error()
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
	if len(runErrors) > 0 {
		report.Status = "failed"
		return report, errors.Join(runErrors...)
	}
	report.Status = "completed"
	return report, nil
}

func normalizeOptions(options Options) (Options, error) {
	if strings.TrimSpace(options.GitHubDir) == "" {
		return Options{}, fmt.Errorf("GitHub directory is required")
	}
	absolute, err := filepath.Abs(options.GitHubDir)
	if err != nil {
		return Options{}, err
	}
	options.GitHubDir = absolute
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

func processRepository(
	ctx context.Context,
	adapter adapter,
	target Target,
	repository Repository,
	options Options,
	operation string,
	report *RepositoryReport,
) error {
	if repository.Archived {
		report.Status = "skipped"
		report.Reason = "repository is archived"
		return nil
	}
	owner, name, err := splitRepository(repository.Slug)
	if err != nil {
		return failRepository(report, err)
	}
	canonical := repository.Path
	if canonical == "" {
		canonical = filepath.Join(options.GitHubDir, owner, name)
	}
	report.CanonicalDir = canonical
	if err := ensureCanonical(ctx, repository, canonical, options); err != nil {
		return failRepository(report, err)
	}
	base := "origin/" + options.Ref
	decisions, err := adapter.inspect(ctx, canonical, base, target, options)
	report.Decisions = decisions
	if err != nil {
		return failRepository(report, err)
	}
	if len(decisions) == 0 {
		report.Status = "skipped"
		report.Reason = fmt.Sprintf("dependency absent on %s", base)
		return nil
	}
	needsChange := false
	for _, decision := range decisions {
		if decision.Action != "unchanged" {
			needsChange = true
			break
		}
	}
	if !needsChange {
		report.Status = "skipped"
		report.Reason = "all existing references are already at the exact target"
		return nil
	}
	if options.DryRun {
		report.Status = "planned"
		report.Reason = "existing references require the exact target"
		return nil
	}
	worktree := filepath.Join(options.GitHubDir, ".wb", "worktrees", operation, owner, name)
	branch := "wb/deps/" + strings.TrimPrefix(operation, "deps-")
	report.WorktreeDir = worktree
	report.Branch = branch
	if err := prepareWorktree(ctx, canonical, worktree, branch, base, options); err != nil {
		return failRepository(report, err)
	}
	decisions, err = adapter.apply(ctx, worktree, target, options)
	report.Decisions = decisions
	if err != nil {
		return failRepository(report, err)
	}
	report.ChangedFiles, err = changedFiles(ctx, worktree, options)
	if err != nil {
		return failRepository(report, err)
	}
	ahead, err := branchAhead(ctx, worktree, base, options)
	if err != nil {
		return failRepository(report, err)
	}
	if len(report.ChangedFiles) == 0 && !ahead {
		report.Status = "skipped"
		report.Reason = "adapter found no file change after inspecting existing references"
		return nil
	}
	if options.Verify {
		verification := quality.VerifyWithOptions(ctx, repository.Slug, worktree, options.Checks, quality.RunOptions{Timeout: options.Timeout, Retry: options.Retry})
		report.Verifications = verification.Results
		if verification.Status == quality.StatusFailed {
			return failRepository(report, fmt.Errorf("local verification failed"))
		}
	}
	if !options.Commit {
		report.Status = "changed"
		report.Reason = "verified changes remain in the local operation worktree"
		return nil
	}
	if len(report.ChangedFiles) > 0 {
		if _, _, err := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "add", "-A"); err != nil {
			return failRepository(report, err)
		}
		message := fmt.Sprintf("chore(deps): set %s to %s", target.Dependency, target.Version)
		if _, _, err := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "commit", "-m", message); err != nil {
			return failRepository(report, err)
		}
	}
	head, _, err := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "rev-parse", "HEAD")
	if err != nil {
		return failRepository(report, err)
	}
	report.Commit = strings.TrimSpace(head)
	report.Status = "committed"
	report.Reason = "verified exact dependency update committed locally"
	if options.Push {
		if _, _, err := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "push", "-u", "origin", branch); err != nil {
			return failRepository(report, err)
		}
		report.Pushed = true
		report.Status = "pushed"
		report.Reason = "verified commit pushed to the operation branch"
	}
	if options.PR {
		prURL, err := openPullRequest(ctx, worktree, branch, options.Ref, target, options)
		if err != nil {
			return failRepository(report, err)
		}
		report.PR = prURL
		report.Status = "pr_open"
		report.Reason = "pull request opened; local verification passed"
	}
	return nil
}

func ensureCanonical(ctx context.Context, repository Repository, canonical string, options Options) error {
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
			return fmt.Errorf("operation worktree already exists: %s (use --resume or choose a different target)", worktree)
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
	sort.Strings(files)
	return files, nil
}

func branchAhead(ctx context.Context, worktree, base string, options Options) (bool, error) {
	output, _, err := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "rev-list", base+"..HEAD")
	return strings.TrimSpace(output) != "", err
}

func openPullRequest(ctx context.Context, worktree, branch, base string, target Target, options Options) (string, error) {
	output, _, err := runCommand(ctx, options.Timeout, options.Retry, worktree, "gh", "pr", "list", "--head", branch, "--base", base,
		"--state", "open", "--json", "url", "--jq", ".[0].url")
	if err == nil {
		if existing := strings.TrimSpace(output); existing != "" {
			return existing, nil
		}
	}
	title := fmt.Sprintf("chore(deps): set %s to %s", target.Dependency, target.Version)
	body := fmt.Sprintf("Automated by `wb deps set %s %s@%s`. Applicable local lint, test, and build verification completed before this pull request was opened.", target.Ecosystem, target.Dependency, target.Version)
	output, _, err = runCommand(ctx, options.Timeout, options.Retry, worktree, "gh", "pr", "create", "--base", base, "--head", branch, "--title", title, "--body", body)
	if err != nil {
		return "", err
	}
	if url := lastNonEmptyLine(output); url != "" {
		return url, nil
	}
	return "", fmt.Errorf("gh pr create returned no pull request URL")
}

func waitAndMerge(ctx context.Context, options Options, report *RepositoryReport) error {
	deadline := time.Time{}
	if options.Timeout > 0 {
		deadline = time.Now().Add(options.Timeout)
	}
	for {
		output, _, err := runCommand(ctx, options.Timeout, options.Retry, report.WorktreeDir, "gh", "pr", "checks", report.PR, "--json", "name,bucket,link")
		var checks []RemoteCheck
		if decodeErr := json.Unmarshal([]byte(output), &checks); decodeErr != nil {
			if err != nil {
				return err
			}
			return fmt.Errorf("decode checks for %s: %w", report.PR, decodeErr)
		}
		report.Checks = checks
		failed := false
		pending := len(checks) == 0
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
			return fmt.Errorf("GitHub checks failed for %s", report.PR)
		}
		if len(checks) > 0 && !pending {
			break
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			if len(checks) == 0 {
				return fmt.Errorf("no GitHub checks appeared before timeout for %s", report.PR)
			}
			return fmt.Errorf("GitHub checks remained pending past timeout for %s", report.PR)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
	if _, _, err := runCommand(ctx, options.Timeout, options.Retry, report.WorktreeDir, "gh", "pr", "merge", report.PR, "--merge"); err != nil {
		return err
	}
	report.Merged = true
	report.Status = "merged"
	report.Reason = "all observed GitHub checks passed or skipped; pull request merged normally"
	return nil
}

func failRepository(report *RepositoryReport, err error) error {
	report.Status = "failed"
	report.Reason = err.Error()
	return fmt.Errorf("%s: %w", report.Repository, err)
}

func splitRepository(slug string) (string, string, error) {
	owner, name, ok := strings.Cut(slug, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return "", "", fmt.Errorf("invalid repository slug %q (want owner/repository)", slug)
	}
	return owner, name, nil
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

type operationLock struct{ path string }

func acquireLock(githubDir, operation string) (operationLock, error) {
	directory := filepath.Join(githubDir, ".wb", "worktrees", operation)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return operationLock{}, err
	}
	path := filepath.Join(directory, ".lock")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return operationLock{}, fmt.Errorf("dependency operation is already active or was interrupted: %s", path)
		}
		return operationLock{}, err
	}
	if _, err := fmt.Fprintf(file, "operation=%s\npid=%d\n", operation, os.Getpid()); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return operationLock{}, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return operationLock{}, err
	}
	return operationLock{path: path}, nil
}

func (lock operationLock) release() { _ = os.Remove(lock.path) }
