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
	"golang.org/x/sys/unix"
)

var (
	safeSegment           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	safeRepositorySegment = regexp.MustCompile(`^[.A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// CreateOptions controls one coordinated worktree creation operation.
type CreateOptions struct {
	ProjectsRoot string
	Operation    string
	Branch       string
	Base         string
	Resume       bool
	// beforeSecureWorktreeAdd is test-only coordination for the filesystem
	// race regression. It is deliberately unexported, so production callers
	// cannot influence the creation flow.
	beforeSecureWorktreeAdd func()
	// afterStagedWorktreeAdd and beforeWorktreeRepair are test-only failure
	// seams. They model Git reporting an error after checkout creation, so the
	// rollback invariants are exercised without platform-specific hook tricks.
	afterStagedWorktreeAdd func() error
	beforeWorktreeRepair   func() error
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
	Transient     bool   `json:"transient,omitempty"`
}

// managedWorktreeLocation is the shared, boundary-aware interpretation of a
// linked checkout below one supported WB layout. The historic direct form
// <task>/<repository> is intentionally supported alongside the current
// <task>/<owner>/<repository> form.
type managedWorktreeLocation struct {
	Layout     wbhome.Layout
	Task       string
	Owner      string
	Repository string
	Worktree   string
}

type createPlan struct {
	result       CreateResult
	owner        string
	repository   string
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
	operationRoot, err := prepareOperationRoot(home, normalized.Operation)
	if err != nil {
		return nil, err
	}
	lock, err := acquireLock(operationRoot)
	if err != nil {
		return nil, err
	}
	defer lock.release()

	plans := make([]createPlan, 0, len(repositories))
	for _, repository := range repositories {
		owner, name, canonical, err := canonicalRepositoryPath(normalized.ProjectsRoot, repository)
		if err != nil {
			return nil, err
		}
		if err := synchronizeCanonical(ctx, canonical, repository, normalized.Base); err != nil {
			return nil, err
		}
		worktree, err := prepareWorktreeDestination(home, normalized.Operation, owner, name)
		if err != nil {
			return nil, err
		}
		plan := createPlan{owner: owner, repository: name, result: CreateResult{
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
			if err := addWorktreeAtSecureDestination(
				ctx,
				plan.result.CanonicalDir,
				operationRoot,
				plan.owner,
				plan.repository,
				plan.result.Branch,
				plan.result.Base,
				plan.branchExists,
				normalized.beforeSecureWorktreeAdd,
				normalized.afterStagedWorktreeAdd,
				normalized.beforeWorktreeRepair,
			); err != nil {
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
	if gitDir == commonDir {
		if branch == "" {
			return GuardResult{}, fmt.Errorf("detached HEAD is not allowed for development at %s", root)
		}
		result := GuardResult{Path: root, Branch: branch, Kind: "canonical"}
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

	resolution, err := wbhome.Resolve(projectsRoot)
	if err != nil {
		return GuardResult{}, err
	}
	location, err := locateManagedWorktree(ctx, projectsRoot, root, resolution.Read)
	if err != nil {
		return GuardResult{}, err
	}
	result := GuardResult{
		Path: root, Branch: branch, WorktreesRoot: location.Layout.WorktreesRoot,
		Kind: "linked",
	}
	canonical := filepath.Join(projectsRoot, location.Owner, location.Repository)
	result.CanonicalDir = canonical
	expectedCommon := filepath.Join(canonical, ".git")
	resolvedExpected, err := filepath.EvalSymlinks(expectedCommon)
	if err == nil {
		expectedCommon = resolvedExpected
	}
	if filepath.Clean(commonDir) != filepath.Clean(expectedCommon) {
		return GuardResult{}, fmt.Errorf(
			"linked worktree %s is stored under %s but belongs to a different canonical clone (%s)",
			root, location.Layout.WorktreesRoot, commonDir,
		)
	}
	if branch == "" {
		if !rebaseInProgress(ctx, root, gitDir) {
			return GuardResult{}, fmt.Errorf("detached HEAD is not allowed for development at %s", root)
		}
		result.Transient = true
		return result, nil
	}
	if branch == base {
		return GuardResult{}, fmt.Errorf("linked worktree %s is on protected base branch %q; use a feature branch", root, base)
	}
	return result, nil
}

func locateManagedWorktree(
	ctx context.Context,
	projectsRoot, root string,
	layouts []wbhome.Layout,
) (managedWorktreeLocation, error) {
	for _, layout := range layouts {
		relative, err := filepath.Rel(layout.WorktreesRoot, root)
		if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
			continue
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		location := managedWorktreeLocation{Layout: layout, Worktree: root}
		switch len(parts) {
		case 3:
			staging := isWorktreeStagingDirectory(parts[1])
			if !validSafeSegment(parts[0]) || (!validSafeSegment(parts[1]) && !staging) {
				return managedWorktreeLocation{}, invalidManagedWorktreePath(root, layout.WorktreesRoot)
			}
			owner, repository, coordinatesErr := managedWorktreeCanonicalCoordinates(ctx, projectsRoot, root)
			if coordinatesErr != nil || (!staging && owner != parts[1]) {
				return managedWorktreeLocation{}, fmt.Errorf("worktree %s has path owner %q but canonical clone owner %q", root, parts[1], owner)
			}
			if staging {
				location.Task, location.Owner, location.Repository = parts[0], owner, repository
				return location, nil
			}
			// A regular repository path must agree with its canonical clone. A
			// dot-prefixed registered directory is a supported hidden alias; its
			// canonical identity remains authoritative.
			if !validRepositorySegment(parts[2]) {
				return managedWorktreeLocation{}, invalidManagedWorktreePath(root, layout.WorktreesRoot)
			}
			// A dot-prefixed Git root may be a registered hidden alias for an
			// ordinary canonical repository. When it is the canonical repository
			// name itself (for example acme/.github), the identities naturally
			// agree. Ordinary names must always match exactly.
			if !strings.HasPrefix(parts[2], ".") && repository != parts[2] {
				return managedWorktreeLocation{}, fmt.Errorf("worktree %s has path repository %q but canonical clone repository %q", root, parts[2], repository)
			}
			location.Task, location.Owner, location.Repository = parts[0], owner, repository
			return location, nil
		case 2:
			if !validSafeSegment(parts[0]) || !validRepositorySegment(parts[1]) {
				return managedWorktreeLocation{}, invalidManagedWorktreePath(root, layout.WorktreesRoot)
			}
			owner, repository, coordinatesErr := managedWorktreeCanonicalCoordinates(ctx, projectsRoot, root)
			if coordinatesErr != nil || (!strings.HasPrefix(parts[1], ".") && repository != parts[1]) {
				return managedWorktreeLocation{}, fmt.Errorf("legacy direct worktree %s has path repository %q but canonical clone identity %s/%s", root, parts[1], owner, repository)
			}
			location.Task, location.Owner, location.Repository = parts[0], owner, repository
			return location, nil
		}
	}
	return managedWorktreeLocation{}, fmt.Errorf("linked worktree %s must be below a resolver-recognized .wb/worktrees hierarchy at <task>/<owner>/<repository> or legacy <task>/<repository>; recreate it with `wb worktree create`", root)
}

func isWorktreeStagingDirectory(name string) bool {
	return strings.HasPrefix(name, ".wb-stage-") && len(name) > len(".wb-stage-")
}

func managedWorktreeCanonicalCoordinates(ctx context.Context, projectsRoot, root string) (owner, repository string, err error) {
	_, commonDir, directoriesErr := gitDirectories(ctx, root)
	if directoriesErr != nil {
		return "", "", fmt.Errorf("derive linked worktree identity for %s: %w", root, directoriesErr)
	}
	canonical := filepath.Dir(commonDir)
	if resolved, resolveErr := filepath.EvalSymlinks(canonical); resolveErr == nil {
		canonical = resolved
	}
	return canonicalCoordinates(projectsRoot, canonical)
}

func invalidManagedWorktreePath(root, worktreesRoot string) error {
	return fmt.Errorf("linked worktree %s must be at %s/<task>/<owner>/<repository> or legacy %s/<task>/<repository>", root, worktreesRoot, worktreesRoot)
}

func rebaseInProgress(ctx context.Context, root, gitDir string) bool {
	return coherentRebaseState(ctx, root, filepath.Join(gitDir, "rebase-merge"), []string{
		"head-name", "orig-head", "onto", "git-rebase-todo", "git-rebase-todo.backup", "end", "msgnum",
	}, true) || coherentRebaseState(ctx, root, filepath.Join(gitDir, "rebase-apply"), []string{
		"head-name", "orig-head", "onto", "next", "last", "rebasing", "original-commit", "patch",
	}, false)
}

// coherentRebaseState accepts only the non-symlinked state Git leaves while a
// rebase is actually paused. A detached HEAD plus an empty or fabricated
// rebase-* directory is not a safe exception to the worktree policy.
func coherentRebaseState(ctx context.Context, root, directory string, required []string, merge bool) bool {
	info, err := os.Lstat(directory)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false
	}
	values := map[string]string{}
	for _, name := range required {
		path := filepath.Join(directory, name)
		file, readErr := os.Lstat(path)
		if readErr != nil || file.Mode()&os.ModeSymlink != 0 || !file.Mode().IsRegular() {
			return false
		}
		if name == "patch" || name == "rebasing" || name == "git-rebase-todo" {
			continue
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil || strings.TrimSpace(string(content)) == "" {
			return false
		}
		values[name] = strings.TrimSpace(string(content))
	}
	if !strings.HasPrefix(values["head-name"], "refs/heads/") ||
		!isGitObjectID(values["orig-head"]) || !isGitObjectID(values["onto"]) ||
		!gitCommitExists(ctx, root, values["orig-head"]) || !gitCommitExists(ctx, root, values["onto"]) ||
		!gitReferenceExists(ctx, root, values["head-name"]) {
		return false
	}
	if merge {
		return coherentMergeRebaseTodo(ctx, root, directory, values["git-rebase-todo.backup"], values["msgnum"], values["end"]) && gitRecognizesRebase(ctx, root, values["onto"])
	}
	if !isGitObjectID(values["original-commit"]) || !gitCommitExists(ctx, root, values["original-commit"]) || !positiveDecimal(values["next"]) || !positiveDecimal(values["last"]) || !decimalAtMost(values["next"], values["last"]) {
		return false
	}
	patch, err := os.ReadFile(filepath.Join(directory, "patch"))
	return err == nil && strings.TrimSpace(string(patch)) != "" && gitRecognizesRebase(ctx, root, values["onto"])
}

func coherentMergeRebaseTodo(ctx context.Context, root, directory, backup, messageNumber, end string) bool {
	if !positiveDecimal(messageNumber) || !positiveDecimal(end) || !decimalAtMost(messageNumber, end) || !rebaseTodoHasResolvableCommit(ctx, root, backup) {
		return false
	}
	todo, err := os.ReadFile(filepath.Join(directory, "git-rebase-todo"))
	if err != nil {
		return false
	}
	// Git leaves the active todo file empty after it has selected the final
	// commit, but it must still exist for `git rebase --continue` to resume the
	// state. Before that final step it must name at least one actionable entry.
	return rebaseTodoHasResolvableCommit(ctx, root, string(todo)) || strings.TrimSpace(messageNumber) == strings.TrimSpace(end)
}

func rebaseTodoHasResolvableCommit(ctx context.Context, root, todo string) bool {
	foundCommit := false
	for _, line := range strings.Split(todo, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		switch fields[0] {
		case "pick", "reword", "edit", "squash", "fixup", "drop":
			if len(fields) < 2 || !isGitRevisionID(fields[1]) || !gitCommitExists(ctx, root, fields[1]) {
				return false
			}
			foundCommit = true
		case "exec", "break", "label", "reset", "merge", "update-ref":
			// These are valid rebase commands but do not necessarily reference
			// an object. A coherent todo still has at least one real commit.
		default:
			return false
		}
	}
	return foundCommit
}

func isGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func isGitRevisionID(value string) bool {
	if len(value) < 4 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func gitCommitExists(ctx context.Context, root, revision string) bool {
	_, err := git(ctx, root, "rev-parse", "--verify", "--quiet", revision+"^{commit}")
	return err == nil
}

func gitReferenceExists(ctx context.Context, root, reference string) bool {
	_, err := git(ctx, root, "show-ref", "--verify", "--quiet", reference)
	return err == nil
}

func gitRecognizesRebase(ctx context.Context, root, onto string) bool {
	_, err := git(ctx, root, "rebase", "--show-current-patch")
	return err == nil && reflogShowsActiveRebase(ctx, root, onto)
}

// reflogShowsActiveRebase requires evidence outside the mutable rebase-* state
// directory. Git records a contiguous run of "rebase (...)" HEAD reflog
// entries when it detaches HEAD for a rebase; its start entry lands at the
// exact commit named by the rebase's onto file. A fabricated directory can
// make `git rebase --show-current-patch` succeed, but cannot satisfy this
// relationship without also forging Git's reflog history.
func reflogShowsActiveRebase(ctx context.Context, root, onto string) bool {
	output, err := git(ctx, root, "reflog", "show", "--format=%H%x00%gs", "-64", "HEAD")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(output, "\n") {
		commit, subject, found := strings.Cut(line, "\x00")
		if !found {
			return false
		}
		if strings.HasPrefix(subject, "rebase (start): checkout ") {
			return commit == onto
		}
		if !strings.HasPrefix(subject, "rebase (") {
			return false
		}
	}
	return false
}

func positiveDecimal(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return strings.TrimLeft(value, "0") != ""
}

func decimalAtMost(value, limit string) bool {
	value = strings.TrimLeft(value, "0")
	limit = strings.TrimLeft(limit, "0")
	if len(value) != len(limit) {
		return len(value) < len(limit)
	}
	return value <= limit
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
	if len(parts) != 2 || !safeSegment.MatchString(parts[0]) || !safeRepositorySegment.MatchString(parts[1]) ||
		parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
		return "", "", fmt.Errorf("repository %q must be owner/name using safe path segments", repository)
	}
	return parts[0], parts[1], nil
}

// CanonicalRepositoryPath validates one owner/repository slug with the same
// strict parser used by Create, then returns its canonical-clone path below
// projectsRoot. Callers that perform work before Create (for example managed
// hook refresh) must use this resolver rather than constructing a path from
// user-supplied segments themselves.
func CanonicalRepositoryPath(projectsRoot, repository string) (string, error) {
	root, err := absoluteProjectsRoot(projectsRoot)
	if err != nil {
		return "", err
	}
	_, _, canonical, err := canonicalRepositoryPath(root, repository)
	return canonical, err
}

func canonicalRepositoryPath(projectsRoot, repository string) (owner, name, canonical string, err error) {
	owner, name, err = splitRepository(repository)
	if err != nil {
		return "", "", "", err
	}
	return owner, name, filepath.Join(projectsRoot, owner, name), nil
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

func git(ctx context.Context, dir string, args ...string) (string, error) {
	return gitWithExtraFiles(ctx, dir, nil, args...)
}

func gitWithExtraFiles(ctx context.Context, dir string, extraFiles []*os.File, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	command.Env = console.Env()
	command.ExtraFiles = extraFiles
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

// prepareOperationRoot creates the fixed WB hierarchy one component at a
// time. os.MkdirAll would follow a pre-existing task or owner symlink; that
// could direct a worktree add outside WB_HOME before Git has a chance to
// validate anything.
func prepareOperationRoot(home, operation string) (string, error) {
	if err := os.MkdirAll(home, 0o755); err != nil {
		return "", fmt.Errorf("create WB home %s: %w", home, err)
	}
	if err := ensureManagedDirectory(home, home); err != nil {
		return "", err
	}
	worktreesRoot := filepath.Join(home, "worktrees")
	if err := ensureManagedDirectory(home, worktreesRoot); err != nil {
		return "", err
	}
	operationRoot := filepath.Join(worktreesRoot, operation)
	if err := ensureManagedDirectory(home, operationRoot); err != nil {
		return "", err
	}
	return operationRoot, nil
}

// prepareWorktreeDestination rejects symlinked hierarchy components and proves
// the resolved parent and eventual destination stay below the resolved WB
// home before Git mutates the filesystem.
func prepareWorktreeDestination(home, operation, owner, repository string) (string, error) {
	operationRoot := filepath.Join(home, "worktrees", operation)
	if err := ensureManagedDirectory(home, operationRoot); err != nil {
		return "", err
	}
	ownerRoot := filepath.Join(operationRoot, owner)
	if err := ensureManagedDirectory(home, ownerRoot); err != nil {
		return "", err
	}
	resolvedHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return "", fmt.Errorf("resolve WB home %s: %w", home, err)
	}
	resolvedOwner, err := filepath.EvalSymlinks(ownerRoot)
	if err != nil {
		return "", fmt.Errorf("resolve worktree parent %s: %w", ownerRoot, err)
	}
	worktree := filepath.Join(ownerRoot, repository)
	if !pathWithin(resolvedHome, filepath.Join(resolvedOwner, repository)) {
		return "", fmt.Errorf("worktree destination %s resolves outside WB home %s", worktree, home)
	}
	info, err := os.Lstat(worktree)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing symlinked worktree destination %s", worktree)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("worktree destination is not a directory: %s", worktree)
		}
		resolvedWorktree, resolveErr := filepath.EvalSymlinks(worktree)
		if resolveErr != nil || !pathWithin(resolvedHome, resolvedWorktree) {
			return "", fmt.Errorf("worktree destination %s resolves outside WB home %s", worktree, home)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect worktree destination %s: %w", worktree, err)
	}
	return worktree, nil
}

func ensureManagedDirectory(home, directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if mkdirErr := os.Mkdir(directory, 0o755); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
			return fmt.Errorf("create managed worktree directory %s: %w", directory, mkdirErr)
		}
		info, err = os.Lstat(directory)
	}
	if err != nil {
		return fmt.Errorf("inspect managed worktree directory %s: %w", directory, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing symlinked managed worktree directory %s", directory)
	}
	if !info.IsDir() {
		return fmt.Errorf("managed worktree path is not a directory: %s", directory)
	}
	resolvedHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return fmt.Errorf("resolve WB home %s: %w", home, err)
	}
	resolvedDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return fmt.Errorf("resolve managed worktree directory %s: %w", directory, err)
	}
	if !pathWithin(resolvedHome, resolvedDirectory) {
		return fmt.Errorf("managed worktree directory %s resolves outside WB home %s", directory, home)
	}
	return nil
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// addWorktreeAtSecureDestination asks Git to create the checkout beneath a
// fresh private staging directory, then publishes it with renameat between
// O_NOFOLLOW directory descriptors. A concurrent rename of <task>/<owner>
// therefore cannot redirect Git's checkout or worktree registration outside
// WB_HOME.
func addWorktreeAtSecureDestination(
	ctx context.Context,
	canonical, operationRoot, owner, repository, branch, base string,
	branchExists bool,
	beforeAdd func(),
	afterStagedAdd func() error,
	beforeRepair func() error,
) error {
	expectedBranchTip := ""
	if !branchExists {
		var err error
		expectedBranchTip, err = git(ctx, canonical, "rev-parse", "origin/"+base)
		if err != nil {
			return fmt.Errorf("resolve feature branch base origin/%s: %w", base, err)
		}
	}
	operationFD, err := openNoFollowDirectory(operationRoot)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(operationFD) }()
	ownerFD, err := openOrCreateNoFollowDirectory(operationFD, owner)
	if err != nil {
		return err
	}
	ownerDirectory := os.NewFile(uintptr(ownerFD), "wb-worktree-owner")
	if ownerDirectory == nil {
		_ = unix.Close(ownerFD)
		return fmt.Errorf("wrap secure worktree owner directory %s", owner)
	}
	defer func() { _ = ownerDirectory.Close() }()
	if err := requireAbsentNoFollowChild(ownerFD, repository); err != nil {
		return err
	}
	stageRoot, err := os.MkdirTemp(operationRoot, ".wb-stage-")
	if err != nil {
		return fmt.Errorf("create secure worktree staging directory: %w", err)
	}
	defer func() { _ = os.Remove(stageRoot) }()
	stageFD, err := openNoFollowDirectory(stageRoot)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(stageFD) }()
	stageTarget := filepath.Join(stageRoot, "checkout")
	rollback := func(creationErr error, finalPath string) error {
		if cleanupErr := rollbackCreatedWorktree(ctx, canonical, stageTarget, finalPath, branch, expectedBranchTip); cleanupErr != nil {
			return fmt.Errorf("%w; rollback incomplete worktree creation: %v", creationErr, cleanupErr)
		}
		return creationErr
	}
	if beforeAdd != nil {
		beforeAdd()
	}
	args := []string{"worktree", "add", "--quiet"}
	if branchExists {
		args = append(args, stageTarget, branch)
	} else {
		args = append(args, "-b", branch, stageTarget, "origin/"+base)
	}
	if _, err := git(ctx, canonical, args...); err != nil {
		return rollback(fmt.Errorf("create staged worktree: %w", err), "")
	}
	if afterStagedAdd != nil {
		if err := afterStagedAdd(); err != nil {
			return rollback(fmt.Errorf("create staged worktree: %w", err), "")
		}
	}
	ownerPath := filepath.Join(operationRoot, owner)
	if !directoryStillMatches(ownerPath, ownerDirectory) {
		return rollback(fmt.Errorf("secure worktree owner path changed during creation; refusing redirected checkout"), "")
	}
	if err := unix.Renameat(stageFD, "checkout", ownerFD, repository); err != nil {
		return rollback(fmt.Errorf("publish secure worktree: %w", err), "")
	}
	finalPath := filepath.Join(ownerPath, repository)
	if !directoryStillMatches(ownerPath, ownerDirectory) {
		if rollbackErr := unix.Renameat(ownerFD, repository, stageFD, "checkout"); rollbackErr != nil {
			return fmt.Errorf("secure worktree owner path changed after publish; rollback failed: %w", rollbackErr)
		}
		return rollback(fmt.Errorf("secure worktree owner path changed after publish; refusing redirected checkout"), "")
	}
	var repairErr error
	if beforeRepair != nil {
		repairErr = beforeRepair()
	} else {
		_, repairErr = git(ctx, canonical, "worktree", "repair", finalPath)
	}
	if repairErr != nil {
		if rollbackErr := unix.Renameat(ownerFD, repository, stageFD, "checkout"); rollbackErr != nil {
			return fmt.Errorf("repair published worktree metadata: %v; roll back published checkout: %w", repairErr, rollbackErr)
		}
		return rollback(fmt.Errorf("repair published worktree metadata: %w", repairErr), finalPath)
	}
	return nil
}

func openNoFollowDirectory(path string) (int, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, fmt.Errorf("open secure worktree directory %s: %w", path, err)
	}
	return fd, nil
}

func openOrCreateNoFollowDirectory(parentFD int, name string) (int, error) {
	if err := unix.Mkdirat(parentFD, name, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
		return -1, fmt.Errorf("create secure worktree directory %s: %w", name, err)
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, fmt.Errorf("open secure worktree directory %s: %w", name, err)
	}
	return fd, nil
}

func requireAbsentNoFollowChild(parentFD int, name string) error {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err == nil {
		_ = unix.Close(fd)
	}
	if err != nil {
		return fmt.Errorf("inspect secure worktree destination %s: %w", name, err)
	}
	return fmt.Errorf("secure worktree destination already exists: %s", name)
}

func directoryStillMatches(path string, directory *os.File) bool {
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.IsDir() {
		return false
	}
	held, err := directory.Stat()
	return err == nil && os.SameFile(current, held)
}

// rollbackCreatedWorktree makes every creation failure converge on the same
// state: no staging checkout, no Git worktree registration, and no branch
// created by this attempt. finalPath is supplied only after a failed metadata
// repair; the checkout has already been moved back through held descriptors,
// so it is used solely to prove Git did not retain a stale registration.
func rollbackCreatedWorktree(ctx context.Context, canonical, stageTarget, finalPath, branch, expectedBranchTip string) error {
	registered, err := registeredWorktreePaths(ctx, canonical)
	if err != nil {
		return err
	}
	stageTarget = filepath.Clean(stageTarget)
	if registered[stageTarget] {
		// The explicit removal handles normal partial checkouts. If Git itself
		// reported a failed add after writing only part of its state, the safe
		// filesystem cleanup and prune below finish the same rollback.
		_, _ = git(ctx, canonical, "worktree", "remove", "--force", stageTarget)
	}
	if err := removeStagingCheckout(stageTarget); err != nil {
		return err
	}
	if _, err := git(ctx, canonical, "worktree", "prune", "--expire", "now"); err != nil {
		return fmt.Errorf("prune incomplete worktree registration: %w", err)
	}
	registered, err = registeredWorktreePaths(ctx, canonical)
	if err != nil {
		return err
	}
	for _, path := range []string{stageTarget, finalPath} {
		if path != "" && registered[filepath.Clean(path)] {
			return fmt.Errorf("incomplete worktree remains registered at %s", path)
		}
	}
	if expectedBranchTip == "" {
		return nil
	}
	if err := deleteCreatedBranch(ctx, canonical, branch, expectedBranchTip); err != nil {
		return err
	}
	return nil
}

func registeredWorktreePaths(ctx context.Context, canonical string) (map[string]bool, error) {
	output, err := git(ctx, canonical, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("list worktree registrations: %w", err)
	}
	paths := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		if path, found := strings.CutPrefix(line, "worktree "); found {
			paths[filepath.Clean(path)] = true
		}
	}
	return paths, nil
}

func removeStagingCheckout(target string) error {
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect incomplete staging checkout %s: %w", target, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(target); err != nil {
			return fmt.Errorf("remove incomplete staging symlink %s: %w", target, err)
		}
		return nil
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("remove incomplete staging checkout %s: %w", target, err)
	}
	return nil
}

func deleteCreatedBranch(ctx context.Context, canonical, branch, expectedTip string) error {
	exists, err := localBranchExists(ctx, canonical, branch)
	if err != nil || !exists {
		return err
	}
	tip, err := git(ctx, canonical, "rev-parse", "refs/heads/"+branch)
	if err != nil {
		return fmt.Errorf("resolve incomplete branch %q: %w", branch, err)
	}
	if tip != expectedTip {
		return fmt.Errorf("refusing to delete incomplete branch %q: it moved from expected base %s to %s", branch, expectedTip, tip)
	}
	if _, err := git(ctx, canonical, "update-ref", "-d", "refs/heads/"+branch, tip); err != nil {
		return fmt.Errorf("delete incomplete branch %q: %w", branch, err)
	}
	return nil
}

type operationLock struct{ path string }

func acquireLock(operationRoot string) (operationLock, error) {
	info, err := os.Lstat(operationRoot)
	if err != nil {
		return operationLock{}, fmt.Errorf("inspect worktree operation directory %s: %w", operationRoot, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return operationLock{}, fmt.Errorf("worktree operation directory is not a real directory: %s", operationRoot)
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
