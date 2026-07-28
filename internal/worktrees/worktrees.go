// Package worktrees creates and validates the isolated Git worktrees used for
// human and agent development. Canonical clones remain clean, current mirrors
// of their base branches; all feature work lives below .wb/worktrees.
package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/wbhome"
)

var safeSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// CreateOptions controls one coordinated worktree creation operation.
type CreateOptions struct {
	ProjectsRoot string
	Operation    string
	Branch       string
	Base         string
	Resume       bool
}

// CreateResult identifies the isolated checkout prepared for one repository.
type CreateResult struct {
	Repository   string `json:"repository"`
	CanonicalDir string `json:"canonical_dir"`
	WorktreeDir  string `json:"worktree_dir"`
	Branch       string `json:"branch"`
	Base         string `json:"base"`
	Action       string `json:"action"`
}

// GuardOptions defines the local checkout policy checked by hooks and agents.
type GuardOptions struct {
	ProjectsRoot string
	Base         string
}

// GuardResult describes a checkout that satisfies the worktree policy.
type GuardResult struct {
	Path          string `json:"path"`
	CanonicalDir  string `json:"canonical_dir"`
	WorktreesRoot string `json:"worktrees_root"`
	Branch        string `json:"branch"`
	Kind          string `json:"kind"`
}

type createPlan struct {
	result       CreateResult
	branchExists bool
	resumed      bool
}

// Create synchronizes each clean canonical base branch with origin, then
// creates (or explicitly resumes) the corresponding isolated worktree.
//
// Synchronization happens before any branch is created. This is deliberate:
// every new feature branch must be based on the latest remote base, while an
// unsafe canonical clone causes the whole request to fail before feature work
// starts.
func Create(ctx context.Context, repositories []string, options CreateOptions) ([]CreateResult, error) {
	normalized, err := normalizeCreateOptions(options)
	if err != nil {
		return nil, err
	}
	if len(repositories) == 0 {
		return nil, fmt.Errorf("at least one owner/repository is required")
	}
	repositories = append([]string(nil), repositories...)
	sort.Strings(repositories)
	for index := 1; index < len(repositories); index++ {
		if repositories[index] == repositories[index-1] {
			return nil, fmt.Errorf("repository %q was supplied more than once", repositories[index])
		}
	}

	home, err := wbhome.Root(normalized.ProjectsRoot)
	if err != nil {
		return nil, err
	}
	operationRoot := filepath.Join(home, "worktrees", normalized.Operation)
	lock, err := acquireLock(operationRoot)
	if err != nil {
		return nil, err
	}
	defer lock.release()

	plans := make([]createPlan, 0, len(repositories))
	for _, repository := range repositories {
		owner, name, err := splitRepository(repository)
		if err != nil {
			return nil, err
		}
		canonical := filepath.Join(normalized.ProjectsRoot, owner, name)
		if err := synchronizeCanonical(ctx, canonical, repository, normalized.Base); err != nil {
			return nil, err
		}
		worktree := filepath.Join(operationRoot, owner, name)
		plan := createPlan{result: CreateResult{
			Repository: repository, CanonicalDir: canonical, WorktreeDir: worktree,
			Branch: normalized.Branch, Base: normalized.Base,
		}}
		if _, err := os.Stat(worktree); err == nil {
			if !normalized.Resume {
				return nil, fmt.Errorf("worktree already exists: %s (use --resume or choose another operation)", worktree)
			}
			if err := validateExistingWorktree(ctx, canonical, worktree, normalized.Branch); err != nil {
				return nil, err
			}
			plan.resumed = true
			plan.result.Action = "resumed"
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect worktree %s: %w", worktree, err)
		} else {
			plan.branchExists, err = localBranchExists(ctx, canonical, normalized.Branch)
			if err != nil {
				return nil, err
			}
			if plan.branchExists && !normalized.Resume {
				return nil, fmt.Errorf("branch %q already exists in %s (use --resume or choose --branch)", normalized.Branch, repository)
			}
			if plan.branchExists {
				if occupied, path, err := branchWorktree(ctx, canonical, normalized.Branch); err != nil {
					return nil, err
				} else if occupied {
					return nil, fmt.Errorf("branch %q is already checked out at %s", normalized.Branch, path)
				}
			}
			plan.result.Action = "created"
		}
		plans = append(plans, plan)
	}

	results := make([]CreateResult, 0, len(plans))
	for _, plan := range plans {
		if !plan.resumed {
			if err := os.MkdirAll(filepath.Dir(plan.result.WorktreeDir), 0o755); err != nil {
				return results, fmt.Errorf("create worktree parent: %w", err)
			}
			args := []string{"worktree", "add", "--quiet"}
			if plan.branchExists {
				args = append(args, plan.result.WorktreeDir, plan.result.Branch)
			} else {
				args = append(args, "-b", plan.result.Branch, plan.result.WorktreeDir, "origin/"+plan.result.Base)
			}
			if _, err := git(ctx, plan.result.CanonicalDir, args...); err != nil {
				return results, err
			}
		}
		results = append(results, plan.result)
	}
	return results, nil
}

// Guard verifies that path is either a clean canonical checkout of the base
// branch or a non-base linked worktree in WB's central worktree hierarchy.
func Guard(ctx context.Context, path string, options GuardOptions) (GuardResult, error) {
	projectsRoot, err := absoluteProjectsRoot(options.ProjectsRoot)
	if err != nil {
		return GuardResult{}, err
	}
	base := strings.TrimSpace(options.Base)
	if base == "" {
		base = "main"
	}
	if !validBranch(ctx, base) {
		return GuardResult{}, fmt.Errorf("invalid base branch %q", base)
	}
	root, err := git(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return GuardResult{}, err
	}
	root = filepath.Clean(root)
	gitDir, commonDir, err := gitDirectories(ctx, root)
	if err != nil {
		return GuardResult{}, err
	}
	branch, err := git(ctx, root, "branch", "--show-current")
	if err != nil {
		return GuardResult{}, err
	}
	if branch == "" {
		return GuardResult{}, fmt.Errorf("detached HEAD is not allowed for development at %s", root)
	}

	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return GuardResult{}, err
	}
	worktreesRoot := filepath.Join(home, "worktrees")
	result := GuardResult{Path: root, Branch: branch, WorktreesRoot: worktreesRoot}
	if gitDir == commonDir {
		result.Kind = "canonical"
		result.CanonicalDir = root
		if _, _, err := canonicalCoordinates(projectsRoot, root); err != nil {
			return GuardResult{}, err
		}
		if branch != base {
			return GuardResult{}, fmt.Errorf(
				"canonical clone %s is on %q; it must stay on %q. Return it to %s, then create feature work with `wb worktree create <task> <owner/repository>`",
				root, branch, base, base,
			)
		}
		clean, err := cleanWorktree(ctx, root)
		if err != nil {
			return GuardResult{}, err
		}
		if !clean {
			return GuardResult{}, fmt.Errorf(
				"canonical clone %s has local changes; canonical clones must remain clean. Move the work to a checkout created by `wb worktree create`",
				root,
			)
		}
		return result, nil
	}

	result.Kind = "linked"
	operation, owner, name, err := worktreeCoordinates(worktreesRoot, root)
	if err != nil {
		return GuardResult{}, err
	}
	_ = operation
	canonical := filepath.Join(projectsRoot, owner, name)
	result.CanonicalDir = canonical
	expectedCommon := filepath.Join(canonical, ".git")
	resolvedExpected, err := filepath.EvalSymlinks(expectedCommon)
	if err == nil {
		expectedCommon = resolvedExpected
	}
	if filepath.Clean(commonDir) != filepath.Clean(expectedCommon) {
		return GuardResult{}, fmt.Errorf(
			"linked worktree %s is stored under %s but belongs to a different canonical clone (%s)",
			root, worktreesRoot, commonDir,
		)
	}
	if branch == base {
		return GuardResult{}, fmt.Errorf("linked worktree %s is on protected base branch %q; use a feature branch", root, base)
	}
	return result, nil
}

// OriginSlug returns the owner/repository identity of path's origin remote.
func OriginSlug(ctx context.Context, path string) (string, error) {
	remote, err := git(ctx, path, "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	remote = strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	remote = strings.TrimSuffix(remote, "/")
	if marker := strings.LastIndex(remote, "github.com:"); marker >= 0 {
		remote = remote[marker+len("github.com:"):]
	} else if marker := strings.LastIndex(remote, "github.com/"); marker >= 0 {
		remote = remote[marker+len("github.com/"):]
	} else {
		parts := strings.Split(remote, "/")
		if len(parts) < 2 {
			return "", fmt.Errorf("cannot derive owner/repository from origin %q", remote)
		}
		remote = strings.Join(parts[len(parts)-2:], "/")
	}
	if _, _, err := splitRepository(remote); err != nil {
		return "", fmt.Errorf("origin remote does not identify owner/repository: %w", err)
	}
	return remote, nil
}

func normalizeCreateOptions(options CreateOptions) (CreateOptions, error) {
	root, err := absoluteProjectsRoot(options.ProjectsRoot)
	if err != nil {
		return CreateOptions{}, err
	}
	options.ProjectsRoot = root
	options.Operation = strings.TrimSpace(options.Operation)
	if !safeSegment.MatchString(options.Operation) || options.Operation == "." || options.Operation == ".." {
		return CreateOptions{}, fmt.Errorf("operation %q must be one safe path segment", options.Operation)
	}
	options.Base = strings.TrimSpace(options.Base)
	if options.Base == "" {
		options.Base = "main"
	}
	if options.Branch == "" {
		options.Branch = "codex/" + options.Operation
	}
	options.Branch = strings.TrimSpace(options.Branch)
	ctx := context.Background()
	if !validBranch(ctx, options.Base) {
		return CreateOptions{}, fmt.Errorf("invalid base branch %q", options.Base)
	}
	if !validBranch(ctx, options.Branch) {
		return CreateOptions{}, fmt.Errorf("invalid feature branch %q", options.Branch)
	}
	if options.Branch == options.Base {
		return CreateOptions{}, fmt.Errorf("feature branch must differ from base branch %q", options.Base)
	}
	return options, nil
}

func absoluteProjectsRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("projects root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = resolved
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return absolute, nil
}

func splitRepository(repository string) (owner, name string, err error) {
	parts := strings.Split(strings.TrimSpace(repository), "/")
	if len(parts) != 2 || !safeSegment.MatchString(parts[0]) || !safeSegment.MatchString(parts[1]) ||
		parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
		return "", "", fmt.Errorf("repository %q must be owner/name using safe path segments", repository)
	}
	return parts[0], parts[1], nil
}

func synchronizeCanonical(ctx context.Context, canonical, repository, base string) error {
	info, err := os.Stat(canonical)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("canonical clone is missing for %s at %s; clone it with `wb sync` first", repository, canonical)
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("canonical clone path is not a directory: %s", canonical)
	}
	root, err := git(ctx, canonical, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	if filepath.Clean(root) != filepath.Clean(canonical) {
		return fmt.Errorf("%s is not the root of its canonical clone (root is %s)", canonical, root)
	}
	gitDir, commonDir, err := gitDirectories(ctx, canonical)
	if err != nil {
		return err
	}
	if gitDir != commonDir {
		return fmt.Errorf("%s is a linked worktree, not the canonical clone for %s", canonical, repository)
	}
	branch, err := git(ctx, canonical, "branch", "--show-current")
	if err != nil {
		return err
	}
	if branch != base {
		return fmt.Errorf("canonical clone %s is on %q; switch it to %q before creating a worktree", canonical, branch, base)
	}
	clean, err := cleanWorktree(ctx, canonical)
	if err != nil {
		return err
	}
	if !clean {
		return fmt.Errorf("canonical clone %s is dirty; move or commit its changes before creating a worktree", canonical)
	}

	// Pulling here, instead of merely branching from a possibly stale
	// origin/<base>, keeps the canonical clone useful as an exact local mirror
	// and guarantees the new worktree starts from the latest remote base.
	if _, err := git(ctx, canonical, "pull", "--ff-only", "--no-tags", "origin", base); err != nil {
		return fmt.Errorf("update canonical %s/%s: %w", repository, base, err)
	}
	head, err := git(ctx, canonical, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	remote, err := git(ctx, canonical, "rev-parse", "origin/"+base)
	if err != nil {
		return err
	}
	if head != remote {
		return fmt.Errorf("canonical clone %s is not at origin/%s after pull", canonical, base)
	}
	return nil
}

func validateExistingWorktree(ctx context.Context, canonical, worktree, branch string) error {
	root, err := git(ctx, worktree, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("cannot resume %s: %w", worktree, err)
	}
	if filepath.Clean(root) != filepath.Clean(worktree) {
		return fmt.Errorf("cannot resume %s: Git root is %s", worktree, root)
	}
	gitDir, commonDir, err := gitDirectories(ctx, worktree)
	if err != nil {
		return err
	}
	if gitDir == commonDir {
		return fmt.Errorf("cannot resume %s: it is not a linked worktree", worktree)
	}
	expected := filepath.Join(canonical, ".git")
	if filepath.Clean(commonDir) != filepath.Clean(expected) {
		return fmt.Errorf("cannot resume %s: it belongs to %s, not %s", worktree, commonDir, expected)
	}
	current, err := git(ctx, worktree, "branch", "--show-current")
	if err != nil {
		return err
	}
	if current != branch {
		return fmt.Errorf("cannot resume %s on branch %q; expected %q", worktree, current, branch)
	}
	return nil
}

func gitDirectories(ctx context.Context, root string) (gitDir, commonDir string, err error) {
	gitDir, err = git(ctx, root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", "", err
	}
	commonDir, err = git(ctx, root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", "", err
	}
	return filepath.Clean(gitDir), filepath.Clean(commonDir), nil
}

func cleanWorktree(ctx context.Context, root string) (bool, error) {
	output, err := git(ctx, root, "status", "--porcelain=v1")
	return output == "", err
}

func localBranchExists(ctx context.Context, root, branch string) (bool, error) {
	command := exec.CommandContext(ctx, "git", "-C", root, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	command.Env = console.Env()
	err := command.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("inspect branch %q in %s: %w", branch, root, err)
}

func branchWorktree(ctx context.Context, root, branch string) (bool, string, error) {
	output, err := git(ctx, root, "worktree", "list", "--porcelain")
	if err != nil {
		return false, "", err
	}
	path := ""
	for _, line := range strings.Split(output, "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimPrefix(line, "worktree ")
		case line == "branch refs/heads/"+branch:
			return true, path, nil
		}
	}
	return false, "", nil
}

func validBranch(ctx context.Context, branch string) bool {
	command := exec.CommandContext(ctx, "git", "check-ref-format", "--branch", branch)
	command.Env = console.Env()
	return command.Run() == nil
}

func canonicalCoordinates(projectsRoot, root string) (owner, name string, err error) {
	relative, err := filepath.Rel(projectsRoot, root)
	if err != nil {
		return "", "", err
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("canonical clone %s must be at <projects-root>/<owner>/<repository>", root)
	}
	return splitRepository(strings.Join(parts, "/"))
}

func worktreeCoordinates(worktreesRoot, root string) (operation, owner, name string, err error) {
	relative, err := filepath.Rel(worktreesRoot, root)
	if err != nil {
		return "", "", "", err
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	if len(parts) != 3 || !safeSegment.MatchString(parts[0]) || parts[0] == "." || parts[0] == ".." {
		return "", "", "", fmt.Errorf(
			"linked worktree %s must be at %s/<task>/<owner>/<repository> (WB's current .wb/worktrees root); recreate it with `wb worktree create`",
			root, worktreesRoot,
		)
	}
	owner, name, err = splitRepository(parts[1] + "/" + parts[2])
	return parts[0], owner, name, err
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	command.Env = console.Env()
	output, err := command.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("git %s in %s: %s", strings.Join(args, " "), dir, detail)
	}
	return strings.TrimSpace(string(output)), nil
}

type operationLock struct{ path string }

func acquireLock(operationRoot string) (operationLock, error) {
	if err := os.MkdirAll(operationRoot, 0o755); err != nil {
		return operationLock{}, err
	}
	path := filepath.Join(operationRoot, ".lock")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return operationLock{}, fmt.Errorf("worktree operation is already active or was interrupted: %s", path)
		}
		return operationLock{}, err
	}
	if _, err := fmt.Fprintf(file, "pid=%d\n", os.Getpid()); err != nil {
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

func (lock operationLock) release() {
	_ = os.Remove(lock.path)
}
