package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
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
		lock, err := AcquireOperationLock(options.GitHubDir, options.Operation, options.Resume)
		if err != nil {
			return results, err
		}
		defer func() { _ = lock.Release() }()
	}
	errorsByRepository := make([]error, len(repositories))
	var completedMu sync.Mutex
	completed := 0
	runParallel(len(repositories), options.Parallel, func(index int) {
		progress.Report(options.Progress, progress.Event{Operation: options.Operation, Phase: "repository", Repository: repositories[index].Slug, State: progress.Started, Total: len(repositories)})
		errorsByRepository[index] = processRepository(ctx, repositories[index], handler, options, &results[index])
		sort.Strings(results[index].ChangedFiles)
		completedMu.Lock()
		completed++
		state := progress.Completed
		if errorsByRepository[index] != nil {
			state = progress.Failed
		}
		completedSnapshot := completed
		completedMu.Unlock()
		progress.Report(options.Progress, progress.Event{Operation: options.Operation, Phase: "repository", Repository: repositories[index].Slug, State: state, Completed: completedSnapshot, Total: len(repositories)})
	})
	if options.Merge {
		runParallel(len(repositories), options.Parallel, func(index int) {
			if errorsByRepository[index] != nil || results[index].PR == "" {
				return
			}
			progress.Report(options.Progress, progress.Event{Operation: options.Operation, Phase: "wait_and_merge", Repository: repositories[index].Slug, State: progress.Waiting})
			if err := waitAndMerge(ctx, options, &results[index]); err != nil {
				results[index].Status = "failed"
				results[index].Reason = err.Error()
				errorsByRepository[index] = fmt.Errorf("%s: %w", repositories[index].Slug, err)
			}
		})
	} else if options.WaitForPRChecks {
		runParallel(len(repositories), options.Parallel, func(index int) {
			if errorsByRepository[index] != nil || results[index].PR == "" {
				return
			}
			progress.Report(options.Progress, progress.Event{Operation: options.Operation, Phase: "wait_for_pr_checks", Repository: repositories[index].Slug, State: progress.Waiting})
			if err := waitForPRChecks(ctx, options, &results[index]); err != nil {
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
	if options.WaitForPRChecks && options.Merge {
		return Options{}, fmt.Errorf("wait-only PR checks cannot be combined with --merge")
	}
	if options.WaitForPRChecks && !options.PR {
		return Options{}, fmt.Errorf("wait-only PR checks require pull-request publication")
	}
	if options.DryRun && (options.Commit || options.Push || options.PR || options.Merge || options.Resume) {
		return Options{}, fmt.Errorf("--dry-run cannot be combined with --commit, --push, --pr, --merge, or --resume")
	}
	if options.Verify && len(options.Checks) == 0 {
		options.Checks = []quality.Check{quality.CheckLint, quality.CheckTest, quality.CheckBuild}
	}
	if strings.TrimSpace(options.Model) == "" {
		options.Model = "unknown"
	}
	if strings.TrimSpace(options.Prompt) == "" {
		options.Prompt = fmt.Sprintf(
			"wb operation %q created and committed into this worktree on branch %q; the caller supplied no more specific instruction text.",
			options.Operation, options.Branch,
		)
	}
	return options, nil
}

func processRepository[T any](ctx context.Context, repository Repository, handler Handler[T], options Options, result *Result[T]) error {
	phase := func(name string) {
		progress.Report(options.Progress, progress.Event{Operation: options.Operation, Phase: name, Repository: repository.Slug, State: progress.Running})
	}
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
	phase("sync")
	resolvedBase, err := EnsureCanonical(ctx, repository, canonical, options)
	if err != nil {
		return failResult(result, err)
	}
	result.Ref = resolvedBase.Ref
	base := "origin/" + resolvedBase.Ref
	phase("inspect")
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
	home, err := wbhome.EnsureRoot(options.GitHubDir)
	if err != nil {
		return failResult(result, err)
	}
	worktree := filepath.Join(home, "worktrees", options.Operation, owner, name)
	result.WorktreeDir = worktree
	result.Branch = options.Branch
	phase("prepare_worktree")
	if err := prepareWorktree(ctx, canonical, worktree, options.Branch, base, options); err != nil {
		return failResult(result, err)
	}
	if err := recordWorktreeManifest(ctx, home, canonical, worktree, repository, resolvedBase, options); err != nil {
		return failResult(result, err)
	}
	phase("apply")
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
		phase("verify")
		verification := quality.VerifyWithOptions(ctx, repository.Slug, worktree, options.Checks, quality.RunOptions{Timeout: options.Timeout, Retry: options.Retry})
		result.Verifications = verification.Results
		if verification.Status == quality.StatusFailed {
			return failResult(result, fmt.Errorf("local verification failed"))
		}
	}
	if !options.Commit {
		result.Status = "changed"
		if options.Verify {
			result.Reason = "verified changes remain in the local operation worktree"
		} else {
			result.Reason = "changes remain in the local operation worktree; full local verification was not run"
		}
		return nil
	}
	if len(result.ChangedFiles) > 0 {
		phase("commit")
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
	if options.Verify {
		result.Reason = "verified operation committed locally"
	} else {
		result.Reason = "operation committed locally; full local verification was not run"
	}
	if options.Push {
		phase("push")
		// Invalidate before attempting: even a failed or ambiguous push leaves
		// this run's view of origin unsound, and the only cost of
		// over-invalidating is one extra fetch (see FetchMemo).
		options.FetchMemo.MarkTouched(repository.Slug)
		if _, _, err := runCommand(ctx, options.Timeout, options.Retry, worktree, "git", "push", "-u", "origin", options.Branch); err != nil {
			return failResult(result, err)
		}
		result.Pushed = true
		result.Status = "pushed"
		if options.Verify {
			result.Reason = "verified commit pushed to the operation branch"
		} else {
			result.Reason = "commit pushed to the operation branch; full local verification was not run"
		}
	}
	if options.PR {
		phase("open_pr")
		// A repository this run opened (or resumed) a pull request for is
		// permanently un-memoizable too: the PR can land server-side at any
		// later point of the run without a local push (see FetchMemo).
		options.FetchMemo.MarkTouched(repository.Slug)
		title, body := handler.PullRequest(repository)
		prURL, err := openPullRequest(ctx, worktree, options.Branch, resolvedBase.Ref, title, body, options)
		if err != nil {
			return failResult(result, err)
		}
		result.PR = prURL
		result.Status = "pr_open"
		if options.Verify {
			result.Reason = "pull request opened; local verification passed"
		} else if options.Merge {
			result.Reason = "pull request opened; exact PR-head GitHub checks are pending"
		} else {
			result.Reason = "pull request opened; full local verification was not run"
		}
	}
	return nil
}

// ResolvedBase is the git ref EnsureCanonical verified exists in a
// repository's canonical clone. Every remaining lifecycle stage for this
// repository — graph inspection, worktree creation, and any eventual pull
// request base — must use Ref rather than the operation's configured
// options.Ref, since the two differ exactly when Fallback is true.
type ResolvedBase struct {
	// Ref is the short branch name (no "origin/" prefix).
	Ref string
	// Fallback is true when the operation's configured ref did not exist for
	// this repository and Ref instead names its actual origin/HEAD default
	// branch. Nothing about this is silent: every caller that receives a
	// Fallback result is expected to surface it in its own report.
	Fallback bool
}

// EnsureCanonical clones a missing repository, fetches origin, and verifies
// a usable base ref without checking out or modifying the canonical tree. It
// first tries the operation's configured options.Ref (default "main"); a
// fleet inevitably contains repositories whose default branch is something
// else (commonly "master", or a branch renamed after the local clone was
// made), so a repository that lacks options.Ref falls back to its actual
// origin/HEAD default branch instead of failing outright. A repository for
// which neither ref resolves still fails loudly — this is a fallback to a
// known-good alternative, never a silent skip.
func EnsureCanonical(ctx context.Context, repository Repository, canonical string, options Options) (ResolvedBase, error) {
	if _, err := os.Stat(canonical); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
			return ResolvedBase{}, err
		}
		cloneURL := repository.CloneURL
		if cloneURL == "" {
			cloneURL = "https://github.com/" + repository.Slug + ".git"
		}
		if _, _, err := runCommand(ctx, options.Timeout, 0, filepath.Dir(canonical), "git", "clone", "--quiet", cloneURL, canonical); err != nil {
			return ResolvedBase{}, err
		}
	} else if err != nil {
		return ResolvedBase{}, err
	}
	if options.FetchMemo.SkipFetch(repository.Slug) {
		// This run already fetched this repository and has never pushed to,
		// opened a PR for, or merged into it, so its previous fetch is still an
		// authoritative view of origin for this run (see FetchMemo).
	} else {
		if _, _, err := runCommand(ctx, options.Timeout, options.Retry, canonical, "git", "fetch", "--quiet", "origin"); err != nil {
			return ResolvedBase{}, err
		}
		options.FetchMemo.MarkFetched(repository.Slug)
	}
	if verifyErr := verifyRemoteRef(ctx, canonical, options, options.Ref); verifyErr == nil {
		return ResolvedBase{Ref: options.Ref}, nil
	} else if defaultBranch, resolveErr := resolveOriginDefaultBranch(ctx, canonical, options); resolveErr != nil || defaultBranch == "" || defaultBranch == options.Ref {
		return ResolvedBase{}, fmt.Errorf("%s does not contain origin/%s: %w", repository.Slug, options.Ref, verifyErr)
	} else if verifyErr := verifyRemoteRef(ctx, canonical, options, defaultBranch); verifyErr != nil {
		return ResolvedBase{}, fmt.Errorf("%s does not contain origin/%s, and its default branch origin/%s also failed verification: %w", repository.Slug, options.Ref, defaultBranch, verifyErr)
	} else {
		return ResolvedBase{Ref: defaultBranch, Fallback: true}, nil
	}
}

// verifyRemoteRef reports whether origin/<ref> resolves to a commit in the
// canonical clone, without checking anything out.
func verifyRemoteRef(ctx context.Context, canonical string, options Options, ref string) error {
	_, _, err := runCommand(ctx, options.Timeout, options.Retry, canonical, "git", "rev-parse", "--verify", "origin/"+ref+"^{commit}")
	return err
}

// resolveOriginDefaultBranch determines the repository's actual default
// branch on origin. It prefers the locally cached origin/HEAD symref, which
// `git clone` sets automatically; a long-lived canonical clone assembled by
// `git remote add` + `git fetch` (common across an older fleet) never gets
// that symref, and a clone's cached symref can also go stale after the
// remote's default branch is renamed on GitHub — so a missing or unusable
// symref is refreshed from origin (`git remote set-head origin --auto`,
// falling back to `git ls-remote --symref`) before giving up.
func resolveOriginDefaultBranch(ctx context.Context, canonical string, options Options) (string, error) {
	if ref, err := readOriginHeadSymref(ctx, canonical, options); err == nil && ref != "" {
		return ref, nil
	}
	if _, _, err := runCommand(ctx, options.Timeout, options.Retry, canonical, "git", "remote", "set-head", "origin", "--auto"); err == nil {
		if ref, err := readOriginHeadSymref(ctx, canonical, options); err == nil && ref != "" {
			return ref, nil
		}
	}
	output, _, err := runCommand(ctx, options.Timeout, options.Retry, canonical, "git", "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return "", err
	}
	ref, err := parseLsRemoteSymref(output)
	if err != nil {
		return "", err
	}
	return ref, nil
}

func readOriginHeadSymref(ctx context.Context, canonical string, options Options) (string, error) {
	output, _, err := runCommand(ctx, options.Timeout, options.Retry, canonical, "git", "symbolic-ref", "refs/remotes/origin/HEAD")
	if err != nil {
		return "", err
	}
	ref := strings.TrimSpace(output)
	const prefix = "refs/remotes/origin/"
	if !strings.HasPrefix(ref, prefix) {
		return "", fmt.Errorf("unexpected origin/HEAD symref %q", ref)
	}
	return strings.TrimPrefix(ref, prefix), nil
}

// parseLsRemoteSymref extracts the branch name from `git ls-remote --symref
// origin HEAD` output, which looks like:
//
//	ref: refs/heads/master	HEAD
//	<sha>	HEAD
func parseLsRemoteSymref(output string) (string, error) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "ref: ")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		const prefix = "refs/heads/"
		if ref, ok := strings.CutPrefix(fields[0], prefix); ok {
			return ref, nil
		}
	}
	return "", fmt.Errorf("origin HEAD symref not found in ls-remote output")
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

// recordWorktreeManifest gives every worktree this engine creates the WB
// manifest and originating-instruction record wb's own commit-admission
// hook requires (see internal/worktrees.CheckAdmission). Before this, a
// `wb deps bump`/`wb deps set` wave worktree had neither: wb created the
// worktree itself, applied a real change, and then its own pre-commit hook
// rejected the commit with "this worktree has no WB manifest, so nothing
// records what it is or who asked for it" — even though wb, not an
// unattended agent working around it, created the worktree. It is
// idempotent, so a --resume'd worktree that already carries a manifest and
// prompt from an earlier run is left untouched (a manifest is immutable by
// design; see worktrees.WriteManifest).
func recordWorktreeManifest(ctx context.Context, home, canonical, worktree string, repository Repository, resolvedBase ResolvedBase, options Options) error {
	baseSHA, _, err := runCommand(ctx, options.Timeout, options.Retry, canonical, "git", "rev-parse", "origin/"+resolvedBase.Ref)
	if err != nil {
		return err
	}
	owner, name, err := splitRepository(repository.Slug)
	if err != nil {
		return err
	}
	effortID := worktreeEffortID(options.Operation, owner, name)
	claimResult := worktrees.CreateResult{
		Repository: repository.Slug, WorktreeDir: worktree, Branch: options.Branch,
		Base: resolvedBase.Ref, BaseSHA: strings.TrimSpace(baseSHA),
	}
	claimID := worktrees.WorkLogClaimID(effortID, claimResult)
	createdAt := time.Now().UTC()
	manifest := worktrees.Manifest{
		Version: 1, EffortID: effortID, ParentEffort: worktrees.ParentEffort(effortID),
		EffortKind: worktrees.EffortKindFor(effortID), Repository: repository.Slug, Worktree: worktree,
		Branch: options.Branch, Base: resolvedBase.Ref, BaseSHA: strings.TrimSpace(baseSHA),
		CreatedAt: createdAt, Initiator: options.Initiator, AgentRuntime: options.AgentRuntime,
		Model: options.Model, CLI: options.CLI, Provider: options.Provider,
		DependencyCampaign: options.DependencyCampaign,
		RunID:              options.Operation, ClaimID: claimID, Provenance: worktrees.ProvenanceCreated,
	}
	if err := worktrees.EnsureManifest(worktree, manifest); err != nil {
		return fmt.Errorf("record worktree manifest: %w", err)
	}
	header := worktrees.PromptHeader{
		At: createdAt, Source: worktrees.PromptSourceAgent, Runtime: options.AgentRuntime,
		Model: options.Model, CLI: options.CLI, Provider: options.Provider, Slug: "operation",
	}
	if err := worktrees.EnsurePrompt(worktree, header, []byte(options.Prompt)); err != nil {
		return fmt.Errorf("record worktree originating instruction: %w", err)
	}
	if _, err := worktrees.EnsureWorkLogClaim(home, worktreeEffortID(options.Operation, owner, name), claimResult, worktrees.WorkLogOptions{
		EffortID: effortID, RunID: options.Operation, Initiator: options.Initiator,
		AgentRuntime: options.AgentRuntime, Model: options.Model,
		CLI: options.CLI, Provider: options.Provider,
	}); err != nil {
		return fmt.Errorf("record worktree Work Log claim: %w", err)
	}
	return nil
}

// worktreeEffortID derives a valid worktrees.ValidEffortPath from an
// operation identity and a repository's owner/name, e.g.
// "deps-bump-go-c787f43a90d5-wave-01.sneat-co-ext-competios". The operation
// is the parent (feature-like) effort; each repository's worktree is a task
// effort beneath it.
func worktreeEffortID(operation, owner, name string) string {
	segment := worktreeEffortSegment(owner + "-" + name)
	if segment == "" {
		segment = "repository"
	}
	return operation + "." + segment
}

// worktreeEffortSegment sanitizes a value into a single
// worktrees.ValidEffortPath segment: alphanumeric, '.', '_', and '-' only,
// starting with an alphanumeric character.
func worktreeEffortSegment(value string) string {
	var output strings.Builder
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z', character >= '0' && character <= '9',
			character == '.', character == '_', character == '-':
			output.WriteRune(character)
		default:
			output.WriteRune('-')
		}
	}
	segment := strings.Trim(output.String(), ".-_")
	if segment == "" {
		return ""
	}
	if !isASCIIAlphanumeric(segment[0]) {
		segment = "r" + segment
	}
	return segment
}

func isASCIIAlphanumeric(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
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
	output, err := githubobserver.Read(ctx, worktree, "pr", "list", "--head", branch, "--base", base,
		"--state", "open", "--json", "url", "--jq", ".[0].url")
	if err == nil {
		if existing := strings.TrimSpace(string(output)); existing != "" {
			return existing, nil
		}
	}
	created, _, err := runCommand(ctx, options.Timeout, options.Retry, worktree, "gh", "pr", "create", "--base", base, "--head", branch, "--title", title, "--body", body)
	if err != nil {
		return "", err
	}
	if url := lastNonEmptyLine(created); url != "" {
		return url, nil
	}
	return "", fmt.Errorf("gh pr create returned no pull request URL")
}

func waitAndMerge[T any](ctx context.Context, options Options, result *Result[T]) error {
	slice := 8 * time.Minute
	if options.Timeout > 0 && options.Timeout < slice {
		slice = options.Timeout
	}
	interval := githubChecksPollInterval(options)
	if interval >= slice {
		return fmt.Errorf("CI poll interval %s must be shorter than bounded merge slice %s", interval, slice)
	}
	receipt, err := WaitForCommitChecks(ctx, PullRequestWaitOptions{
		Repository:        result.Repository,
		PullRequest:       result.PR,
		Target:            result.Ref,
		Head:              result.Commit,
		Slice:             slice,
		CheckPollInterval: interval,
	})
	if err != nil {
		return err
	}
	result.Checks = receipt.Checks
	switch receipt.Status {
	case PullRequestWaitPassed:
	case PullRequestWaitPending:
		return fmt.Errorf("GitHub CI receipt is pending for %s at %s; resume the orchestrated merge or run wb ci wait with the same exact identity: %s", result.PR, result.Commit, receipt.Reason)
	default:
		return fmt.Errorf("GitHub CI receipt failed for %s at %s: %s", result.PR, result.Commit, receipt.Reason)
	}
	// The merge is server-side: `gh pr merge` lands commits on the default
	// branch with no local push at all, so this repository's previous fetches
	// can never again be reused within this run (see FetchMemo). The push and
	// PR stages already invalidated it; this hook keeps the merge stage
	// self-sufficiently sound rather than relying on that ordering.
	options.FetchMemo.MarkTouched(result.Repository)
	mergeArgs := []string{"pr", "merge", result.PR, "--match-head-commit", result.Commit, "--merge"}
	if _, _, err := runCommand(ctx, options.Timeout, options.Retry, result.WorktreeDir, "gh", mergeArgs...); err != nil {
		return err
	}
	result.Merged = true
	result.Status = "merged"
	result.Reason = "producer-aware required policy and the exact-head observed check set were stable before the pull request merged"
	return nil
}

func waitForPRChecks[T any](ctx context.Context, options Options, result *Result[T]) error {
	slice := 8 * time.Minute
	if options.Timeout > 0 && options.Timeout < slice {
		slice = options.Timeout
	}
	interval := githubChecksPollInterval(options)
	if interval >= slice {
		return fmt.Errorf("CI poll interval %s must be shorter than bounded PR-check slice %s", interval, slice)
	}
	receipt, err := WaitForCommitChecks(ctx, PullRequestWaitOptions{
		Repository: result.Repository, PullRequest: result.PR, Target: result.Ref, Head: result.Commit,
		AllowUnfenced: true, Slice: slice, CheckPollInterval: interval,
		Progress: reportWorktreeMergeCheckProgress(options.Progress, "pr_checks"),
	})
	if err != nil {
		return err
	}
	result.Checks = receipt.Checks
	switch receipt.Status {
	case PullRequestWaitPassed:
		result.Status = "validated"
		result.Reason = "exact PR-head GitHub checks passed; pull request is awaiting merge"
		return nil
	case PullRequestWaitPending:
		result.Status = "awaiting_merge"
		result.Reason = "exact PR-head GitHub checks remain pending; pull request is awaiting merge: " + receipt.Reason
		return nil
	default:
		return fmt.Errorf("GitHub CI receipt failed for %s at %s: %s", result.PR, result.Commit, receipt.Reason)
	}
}

func decodePullRequestChecks(pr, output, stderr string, commandErr error) ([]RemoteCheck, bool, error) {
	var checks []RemoteCheck
	if err := json.Unmarshal([]byte(output), &checks); err == nil {
		// `gh pr checks` uses non-zero exit statuses for both pending (8) and
		// failed checks, while still returning its requested JSON receipt. The
		// normalized buckets, not the transport exit code, decide whether this
		// exact CI observation is resumable or terminal.
		return checks, len(checks) == 0, nil
	} else if commandErr == nil {
		return nil, false, fmt.Errorf("decode checks for %s: %w", pr, err)
	}
	lowerOutput := strings.ToLower(output)
	lowerStderr := strings.ToLower(stderr)
	if strings.Contains(lowerOutput, "no checks reported") || strings.Contains(lowerOutput, "no required checks reported") ||
		strings.Contains(lowerStderr, "no checks reported") || strings.Contains(lowerStderr, "no required checks reported") {
		return nil, true, nil
	}
	return nil, false, commandErr
}

func githubChecksPollInterval(options Options) time.Duration {
	if options.CheckPollInterval > 0 {
		return options.CheckPollInterval
	}
	return DefaultCheckPollInterval
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
type OperationLock struct {
	directory *os.File
	lock      *worktrees.HeldOperationLock
}

// AcquireOperationLock creates an exclusive lock below the operation root. An
// unheld remnant is reclaimable only by an explicit resume and only when its
// descriptor proves exact ownership of this operation.
func AcquireOperationLock(githubDir, operation string, resume bool) (OperationLock, error) {
	home, err := wbhome.EnsureRoot(githubDir)
	if err != nil {
		return OperationLock{}, err
	}
	directoryPath := filepath.Join(home, "worktrees", operation)
	directory, err := worktrees.OpenOperationLockDirectory(directoryPath)
	if err != nil {
		return OperationLock{}, err
	}
	lock, err := worktrees.AcquireOperationLock(directory, resume)
	if err != nil {
		_ = directory.Close()
		return OperationLock{}, operationLockAcquisitionError(operation, err)
	}
	if lock.ReclaimedInterrupted() {
		pid, valid := operationLockMetadataPID(lock.File(), operation)
		if !valid {
			lock.Preserve()
			_ = directory.Close()
			return OperationLock{}, fmt.Errorf("operation %q has an ambiguous lock at %s", operation, filepath.Join(directoryPath, ".lock"))
		}
		if operationLockPIDMayBeLive(pid) {
			lock.Preserve()
			_ = directory.Close()
			return OperationLock{}, fmt.Errorf("operation %q is already active or ownership is ambiguous at %s", operation, filepath.Join(directoryPath, ".lock"))
		}
		return OperationLock{directory: directory, lock: lock}, nil
	}
	file := lock.File()
	if file == nil {
		_ = lock.Release()
		_ = directory.Close()
		return OperationLock{}, fmt.Errorf("initialize operation %q lock: descriptor is unavailable", operation)
	}
	if err := file.Truncate(0); err != nil {
		_ = lock.Release()
		_ = directory.Close()
		return OperationLock{}, fmt.Errorf("initialize operation %q lock: %w", operation, err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = lock.Release()
		_ = directory.Close()
		return OperationLock{}, err
	}
	if _, err := fmt.Fprintf(file, "operation=%s\npid=%d\n", operation, os.Getpid()); err != nil {
		_ = lock.Release()
		_ = directory.Close()
		return OperationLock{}, err
	}
	return OperationLock{directory: directory, lock: lock}, nil
}

func operationLockAcquisitionError(operation string, err error) error {
	if strings.Contains(err.Error(), "already active in another process") {
		return fmt.Errorf("operation %q is already active: %w", operation, err)
	}
	return fmt.Errorf("operation %q has an ambiguous or interrupted lock: %w", operation, err)
}

func operationLockMetadataPID(file *os.File, operation string) (int, bool) {
	if file == nil {
		return 0, false
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, false
	}
	contents, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(contents) > 4096 {
		return 0, false
	}
	lines := strings.Split(string(contents), "\n")
	if len(lines) != 3 || lines[2] != "" || lines[0] != "operation="+operation {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimPrefix(lines[1], "pid="))
	return pid, err == nil && pid > 0 && lines[1] == fmt.Sprintf("pid=%d", pid)
}

// operationLockPIDMayBeLive distinguishes a dead legacy O_EXCL-only lock
// from a process that could still own it. A permission denial is ambiguous,
// so recovery stays closed rather than guessing that the process is gone.
func operationLockPIDMayBeLive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM) || !errors.Is(err, syscall.ESRCH)
}

// Release retires the exact held lock inode. It is safe to call from defer and
// cannot unlink a successor lock installed after this operation acquired one.
func (lock OperationLock) Release() error {
	var err error
	if lock.lock != nil {
		err = lock.lock.Release()
	}
	if lock.directory != nil {
		_ = lock.directory.Close()
	}
	return err
}
