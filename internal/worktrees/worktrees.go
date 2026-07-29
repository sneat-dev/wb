// Package worktrees creates and validates the isolated Git worktrees used for
// human and agent development. Canonical clones remain clean, current mirrors
// of their base branches; all feature work lives below .wb/worktrees.
package worktrees

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/wbhome"
	"golang.org/x/sys/unix"
)

var (
	safeSegment           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	safeRepositorySegment = regexp.MustCompile(`^[.A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// SecureStageGitHelperArgument selects the private WB child-process path that
// enters the stage directory from inherited file descriptor 3 before running
// Git. It is handled before normal CLI parsing and is not a user command.
const SecureStageGitHelperArgument = "--wb-internal-stage-git"

// CreateOptions controls one coordinated worktree creation operation.
type CreateOptions struct {
	ProjectsRoot string
	Operation    string
	Branch       string
	Base         string
	Resume       bool
	// beforeHomeDirectoryOpen is a test-only seam before WB opens or creates
	// its resolved home hierarchy. It proves a substituted WB_HOME leaf cannot
	// redirect the initial descriptor chain.
	beforeHomeDirectoryOpen func()
	// beforeSecureWorktreeAdd is test-only coordination for the filesystem
	// race regression. It is deliberately unexported, so production callers
	// cannot influence the creation flow.
	beforeSecureWorktreeAdd func()
	// afterSecureStageDirectoryCreated is a test-only seam immediately after
	// mkdirat creates a stage and before WB opens it. It exercises cleanup on
	// an Openat or descriptor-wrap failure.
	afterSecureStageDirectoryCreated func()
	// afterOperationRootPrepared is a test-only seam after WB has opened the
	// operation descriptor but before planning creates or inspects owner paths.
	// It proves a swapped worktrees ancestor cannot redirect that phase.
	afterOperationRootPrepared func()
	// afterSecureStageValidation is a test-only seam for the narrow interval
	// between validating the staging pathname and handing its held descriptor
	// to Git. It proves a later pathname substitution cannot redirect Git.
	afterSecureStageValidation func()
	// afterSecureStageVerification is a test-only seam immediately before the
	// descriptor-relative publish. It exercises the final unavoidable rename
	// window without exposing the seam to production callers.
	afterSecureStageVerification func()
	// afterSecureDestinationValidation is a test-only seam after WB has made
	// its last lexical owner-path check and immediately before atomic publish.
	// It proves publication never clobbers a destination created in that gap.
	afterSecureDestinationValidation func()
	// afterWorktreeRepair is a test-only seam after Git repairs registration but
	// before WB accepts the published checkout. It proves the final ownership
	// and registration checks reject a path swapped during repair.
	afterWorktreeRepair func()
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

// preparedOperationRoot keeps the operation directory descriptor alive from
// hierarchy creation through locking and worktree publication. Its Path is
// only the user-facing/result spelling; filesystem mutation uses Directory.
type preparedOperationRoot struct {
	Path      string
	Home      *os.File
	Worktrees *os.File
	Directory *os.File
}

func (operation preparedOperationRoot) close() {
	if operation.Directory != nil {
		_ = operation.Directory.Close()
	}
	if operation.Worktrees != nil {
		_ = operation.Worktrees.Close()
	}
	if operation.Home != nil {
		_ = operation.Home.Close()
	}
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
	repositories, err = ValidateRepositories(repositories)
	if err != nil {
		return nil, err
	}

	home, err := wbhome.Root(normalized.ProjectsRoot)
	if err != nil {
		return nil, err
	}
	operation, err := prepareOperationRoot(home, normalized.Operation, normalized.beforeHomeDirectoryOpen)
	if err != nil {
		return nil, err
	}
	defer operation.close()
	lock, err := acquireLockAt(operation.Directory)
	if err != nil {
		return nil, err
	}
	defer lock.release()
	if normalized.afterOperationRootPrepared != nil {
		normalized.afterOperationRootPrepared()
	}

	plans := make([]createPlan, 0, len(repositories))
	for _, repository := range repositories {
		owner, name, canonical, err := canonicalRepositoryPath(normalized.ProjectsRoot, repository)
		if err != nil {
			return nil, err
		}
		if err := synchronizeCanonical(ctx, canonical, repository, normalized.Base); err != nil {
			return nil, err
		}
		worktree, exists, err := prepareWorktreeDestination(operation.Path, operation.Directory, owner, name)
		if err != nil {
			return nil, err
		}
		plan := createPlan{owner: owner, repository: name, result: CreateResult{
			Repository: repository, CanonicalDir: canonical, WorktreeDir: worktree,
			Branch: normalized.Branch, Base: normalized.Base,
		}}
		if exists {
			if !normalized.Resume {
				return nil, fmt.Errorf("worktree already exists: %s (use --resume or choose another operation)", worktree)
			}
			if err := validateExistingWorktree(ctx, canonical, worktree, normalized.Branch); err != nil {
				return nil, err
			}
			plan.resumed = true
			plan.result.Action = "resumed"
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
				operation.Path,
				operation.Directory,
				plan.owner,
				plan.repository,
				plan.result.Branch,
				plan.result.Base,
				plan.branchExists,
				normalized.beforeSecureWorktreeAdd,
				normalized.afterSecureStageDirectoryCreated,
				normalized.afterSecureStageValidation,
				normalized.afterSecureStageVerification,
				normalized.afterSecureDestinationValidation,
				normalized.afterWorktreeRepair,
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

// ValidateRepositories rejects unsafe and duplicate repository coordinates
// before callers mutate canonical clones, hooks, or WB home. It also returns a
// sorted copy so every later phase has deterministic order.
func ValidateRepositories(repositories []string) ([]string, error) {
	if len(repositories) == 0 {
		return nil, fmt.Errorf("at least one owner/repository is required")
	}
	normalized := append([]string(nil), repositories...)
	for _, repository := range normalized {
		if _, _, err := splitRepository(repository); err != nil {
			return nil, err
		}
	}
	sort.Strings(normalized)
	for index := 1; index < len(normalized); index++ {
		if normalized[index] == normalized[index-1] {
			return nil, fmt.Errorf("repository %q was supplied more than once", normalized[index])
		}
	}
	identities := make(map[string]string, len(normalized))
	for _, repository := range normalized {
		identity := strings.ToLower(repository) // validated slugs are ASCII.
		if original, exists := identities[identity]; exists {
			return nil, fmt.Errorf("repository %q duplicates case-insensitive identity %q", repository, original)
		}
		identities[identity] = repository
	}
	return normalized, nil
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
		case "pick", "p", "reword", "r", "edit", "e", "squash", "s", "fixup", "f", "drop", "d":
			if len(fields) < 2 || !isGitRevisionID(fields[1]) || !gitCommitExists(ctx, root, fields[1]) {
				return false
			}
			foundCommit = true
		case "exec", "x", "break", "b", "label", "l", "reset", "t", "merge", "m", "update-ref", "u":
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
// directory. Git records a contiguous run of rebase-related HEAD reflog
// entries when it detaches HEAD for a rebase; an interactive `edit` followed
// by `commit --amend` contributes one `commit (amend)` entry to that same run.
// Its start entry lands at the exact commit named by the rebase's onto file.
// A fabricated directory can make `git rebase --show-current-patch` succeed,
// but cannot satisfy this relationship without also forging Git's reflog
// history.
func reflogShowsActiveRebase(ctx context.Context, root, onto string) bool {
	output, err := git(ctx, root, "reflog", "show", "--format=%H%x00%gs", "HEAD")
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
		if !strings.HasPrefix(subject, "rebase (") && !strings.HasPrefix(subject, "commit (amend): ") {
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
	if repository != strings.TrimSpace(repository) {
		return "", "", fmt.Errorf("repository %q must not have surrounding whitespace", repository)
	}
	parts := strings.Split(repository, "/")
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
func prepareOperationRoot(home, operation string, beforeHomeOpen func()) (preparedOperationRoot, error) {
	if beforeHomeOpen != nil {
		beforeHomeOpen()
	}
	homeDirectory, err := openAbsoluteDirectoryNoFollow(home, true)
	if err != nil {
		return preparedOperationRoot{}, err
	}
	homeFD := int(homeDirectory.Fd())
	worktreesFD, err := openOrCreateNoFollowDirectory(homeFD, "worktrees")
	if err != nil {
		_ = homeDirectory.Close()
		return preparedOperationRoot{}, err
	}
	worktreesDirectory := os.NewFile(uintptr(worktreesFD), "wb-worktrees-root")
	if worktreesDirectory == nil {
		_ = unix.Close(worktreesFD)
		_ = homeDirectory.Close()
		return preparedOperationRoot{}, fmt.Errorf("wrap secure worktrees root")
	}
	operationFD, err := openOrCreateNoFollowDirectory(worktreesFD, operation)
	if err != nil {
		_ = worktreesDirectory.Close()
		_ = homeDirectory.Close()
		return preparedOperationRoot{}, err
	}
	operationDirectory := os.NewFile(uintptr(operationFD), "wb-worktree-operation")
	if operationDirectory == nil {
		_ = unix.Close(operationFD)
		_ = worktreesDirectory.Close()
		_ = homeDirectory.Close()
		return preparedOperationRoot{}, fmt.Errorf("wrap secure worktree operation directory")
	}
	return preparedOperationRoot{
		Path:      filepath.Join(home, "worktrees", operation),
		Home:      homeDirectory,
		Worktrees: worktreesDirectory,
		Directory: operationDirectory,
	}, nil
}

// prepareWorktreeDestination walks from the held operation descriptor. It
// never calls MkdirAll, Stat, or EvalSymlinks on the mutable WB hierarchy.
// The returned path is display/result text only; descriptor-relative add owns
// the real destination mutation.
func prepareWorktreeDestination(operationRoot string, operationDirectory *os.File, owner, repository string) (string, bool, error) {
	if !directoryStillMatches(operationRoot, operationDirectory) {
		return "", false, fmt.Errorf("secure worktree operation path changed before planning; refusing redirected checkout")
	}
	ownerFD, err := openOrCreateNoFollowDirectory(int(operationDirectory.Fd()), owner)
	if err != nil {
		return "", false, err
	}
	ownerDirectory := os.NewFile(uintptr(ownerFD), "wb-worktree-owner-plan")
	if ownerDirectory == nil {
		_ = unix.Close(ownerFD)
		return "", false, fmt.Errorf("wrap secure worktree owner directory %s", owner)
	}
	defer func() { _ = ownerDirectory.Close() }()
	ownerPath := filepath.Join(operationRoot, owner)
	if !directoryStillMatches(ownerPath, ownerDirectory) {
		return "", false, fmt.Errorf("secure worktree owner path changed before planning; refusing redirected checkout")
	}
	worktree := filepath.Join(ownerPath, repository)
	fd, err := unix.Openat(ownerFD, repository, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return worktree, false, nil
	}
	if err != nil {
		var info unix.Stat_t
		if statErr := unix.Fstatat(ownerFD, repository, &info, unix.AT_SYMLINK_NOFOLLOW); statErr == nil && info.Mode&unix.S_IFMT == unix.S_IFLNK {
			return "", false, fmt.Errorf("refusing symlinked worktree destination %s", worktree)
		}
		return "", false, fmt.Errorf("inspect secure worktree destination %s: %w", worktree, err)
	}
	destination := os.NewFile(uintptr(fd), "wb-worktree-destination-plan")
	if destination == nil {
		_ = unix.Close(fd)
		return "", false, fmt.Errorf("wrap secure worktree destination %s", worktree)
	}
	defer func() { _ = destination.Close() }()
	info, err := destination.Stat()
	if err != nil {
		return "", false, fmt.Errorf("inspect secure worktree destination %s: %w", worktree, err)
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("worktree destination is not a directory: %s", worktree)
	}
	if !directoryStillMatches(worktree, destination) {
		return "", false, fmt.Errorf("secure worktree destination path changed before planning; refusing redirected checkout")
	}
	return worktree, true, nil
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// addWorktreeAtSecureDestination asks Git to create the checkout beneath a
// fresh private staging directory, then publishes it with renameat between
// O_NOFOLLOW directory descriptors. The child helper checks that the held
// stage remains under the original operation directory before and after Git;
// a later rename cannot redirect descriptor-relative publication, and a
// detected escape is rolled back through the held stage descriptor.
func addWorktreeAtSecureDestination(
	ctx context.Context,
	canonical, operationRoot string,
	operationDirectory *os.File,
	owner, repository, branch, base string,
	branchExists bool,
	beforeAdd func(),
	afterStageDirectoryCreated func(),
	afterStageValidation func(),
	afterStageVerification func(),
	afterDestinationValidation func(),
	afterRepair func(),
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
	operationFD := int(operationDirectory.Fd())
	trustedOperationRoot, err := secureDirectoryPath(ctx, operationDirectory)
	if err != nil {
		return fmt.Errorf("resolve secure worktree operation directory: %w", err)
	}
	registrationsBefore, err := registeredWorktreePaths(ctx, canonical)
	if err != nil {
		return err
	}
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
	stageName, err := makeSecureStageDirectory(operationFD)
	if err != nil {
		return fmt.Errorf("create secure worktree staging directory: %w", err)
	}
	var stageIdentity secureDirectoryIdentity
	stageIdentityKnown := false
	// Install cleanup before opening or wrapping the stage descriptor. A
	// failure in either step must not strand a private stage directory.
	defer func() {
		if stageIdentityKnown {
			_ = quarantineStageDirectoryByIdentityAt(operationDirectory, stageIdentity)
		}
	}()
	stageIdentity, err = secureDirectoryIdentityAt(operationFD, stageName)
	if err != nil {
		return fmt.Errorf("inspect secure worktree staging directory: %w", err)
	}
	stageIdentityKnown = true
	if afterStageDirectoryCreated != nil {
		afterStageDirectoryCreated()
	}
	stageRoot := filepath.Join(operationRoot, stageName)
	stageFD, err := unix.Openat(operationFD, stageName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open secure worktree staging directory: %w", err)
	}
	stageDirectory := os.NewFile(uintptr(stageFD), "wb-worktree-stage")
	if stageDirectory == nil {
		_ = unix.Close(stageFD)
		return fmt.Errorf("wrap secure worktree staging directory")
	}
	defer func() { _ = stageDirectory.Close() }()
	defer func() {
		_ = quarantineMatchingStageDirectoryAt(operationDirectory, stageDirectory)
	}()
	rollback := func(creationErr error, finalPath string) error {
		cleanupCtx, cancel := rollbackContext(ctx)
		defer cancel()
		if cleanupErr := rollbackCreatedWorktree(cleanupCtx, canonical, stageDirectory, registrationsBefore, finalPath, branch, expectedBranchTip); cleanupErr != nil {
			return fmt.Errorf("%w; rollback incomplete worktree creation: %v", creationErr, cleanupErr)
		}
		return creationErr
	}
	if beforeAdd != nil {
		beforeAdd()
	}
	if !directoryStillMatches(stageRoot, stageDirectory) {
		return rollback(fmt.Errorf("secure staging directory path changed during creation; refusing redirected checkout"), "")
	}
	if afterStageValidation != nil {
		afterStageValidation()
	}
	if err := gitWorktreeAddFromStageDirectory(ctx, canonical, trustedOperationRoot, stageDirectory, branch, base, branchExists); err != nil {
		return rollback(fmt.Errorf("create staged worktree: %w", err), "")
	}
	if afterStagedAdd != nil {
		if err := afterStagedAdd(); err != nil {
			return rollback(fmt.Errorf("create staged worktree: %w", err), "")
		}
	}
	if err := verifySecureStageDirectory(ctx, stageDirectory, trustedOperationRoot); err != nil {
		return rollback(fmt.Errorf("verify secure staging directory before publish: %w", err), "")
	}
	if afterStageVerification != nil {
		afterStageVerification()
	}
	ownerPath := filepath.Join(operationRoot, owner)
	if !directoryStillMatches(ownerPath, ownerDirectory) {
		return rollback(fmt.Errorf("secure worktree owner path changed during creation; refusing redirected checkout"), "")
	}
	if afterDestinationValidation != nil {
		afterDestinationValidation()
	}
	if err := renameNoReplace(stageFD, "checkout", ownerFD, repository); err != nil {
		return rollback(fmt.Errorf("publish secure worktree: %w", err), "")
	}
	finalPath := filepath.Join(ownerPath, repository)
	rollbackPublished := func(creationErr error) error {
		if rollbackErr := renameNoReplace(ownerFD, repository, stageFD, "checkout"); rollbackErr != nil {
			return rollback(fmt.Errorf("%w; roll back published checkout: %v", creationErr, rollbackErr), finalPath)
		}
		return rollback(creationErr, finalPath)
	}
	finalFD, err := unix.Openat(ownerFD, repository, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return rollbackPublished(fmt.Errorf("open published worktree: %w", err))
	}
	finalDirectory := os.NewFile(uintptr(finalFD), "wb-worktree-final")
	if finalDirectory == nil {
		_ = unix.Close(finalFD)
		return rollbackPublished(fmt.Errorf("wrap published worktree"))
	}
	defer func() { _ = finalDirectory.Close() }()
	if !directoryStillMatches(ownerPath, ownerDirectory) {
		return rollbackPublished(fmt.Errorf("secure worktree owner path changed after publish; refusing redirected checkout"))
	}
	var repairErr error
	if beforeRepair != nil {
		repairErr = beforeRepair()
	} else {
		_, repairErr = git(ctx, canonical, "worktree", "repair", finalPath)
	}
	if repairErr != nil {
		return rollbackPublished(fmt.Errorf("repair published worktree metadata: %w", repairErr))
	}
	if afterRepair != nil {
		afterRepair()
	}
	if err := verifyPublishedWorktree(ctx, canonical, registrationsBefore, ownerPath, ownerDirectory, finalPath, finalDirectory, branch); err != nil {
		return rollbackPublished(fmt.Errorf("verify published worktree after repair: %w", err))
	}
	return nil
}

// gitWorktreeAddFromStageDirectory starts a private WB child from the staging
// directory held by its descriptor, not from its mutable pathname. The child
// fchdirs itself from inherited fd 3 and starts Git with a relative checkout
// name. Keeping fchdir out of this parent process means concurrent WB work
// cannot observe or use a changed current working directory.
func gitWorktreeAddFromStageDirectory(
	ctx context.Context,
	canonical, trustedOperationRoot string,
	stageDirectory *os.File,
	branch, base string,
	branchExists bool,
) error {
	gitExecutable, err := trustedGitExecutable()
	if err != nil {
		return err
	}
	output, err := runSecureStageHelper(ctx, stageDirectory, append([]string{trustedOperationRoot, gitExecutable}, worktreeAddArguments(canonical, branch, base, branchExists)...)...)
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("git worktree add in secure staging directory: %s", detail)
	}
	return nil
}

func trustedGitExecutable() (string, error) {
	gitExecutable, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("locate Git before secure staging handoff: %w", err)
	}
	gitExecutable, err = filepath.Abs(gitExecutable)
	if err != nil {
		return "", fmt.Errorf("make Git path absolute before secure staging handoff: %w", err)
	}
	return gitExecutable, nil
}

func worktreeAddArguments(canonical, branch, base string, branchExists bool) []string {
	args := []string{"--git-dir=" + filepath.Join(canonical, ".git"), "worktree", "add", "--quiet"}
	if branchExists {
		args = append(args, "checkout", branch)
	} else {
		args = append(args, "-b", branch, "checkout", "origin/"+base)
	}
	return args
}

const (
	secureStageCheckArgument   = "--check"
	secureStageCleanupArgument = "--cleanup"
	secureStagePathArgument    = "--path"
)

func runSecureStageHelper(ctx context.Context, stageDirectory *os.File, args ...string) ([]byte, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate WB secure staging helper: %w", err)
	}
	command := exec.CommandContext(ctx, executable, append([]string{SecureStageGitHelperArgument}, args...)...)
	command.Env = console.Env()
	command.ExtraFiles = []*os.File{stageDirectory}
	return command.CombinedOutput()
}

func verifySecureStageDirectory(ctx context.Context, stageDirectory *os.File, trustedOperationRoot string) error {
	output, err := runSecureStageHelper(ctx, stageDirectory, secureStageCheckArgument, trustedOperationRoot)
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		detail = err.Error()
	}
	return fmt.Errorf("secure staging directory escaped its operation root: %s", detail)
}

func secureDirectoryPath(ctx context.Context, directory *os.File) (string, error) {
	output, err := runSecureStageHelper(ctx, directory, secureStagePathArgument)
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("derive held directory path: %s", detail)
	}
	path := filepath.Clean(strings.TrimSpace(string(output)))
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("held directory path is not absolute: %q", path)
	}
	return path, nil
}

func cleanupSecureStageCheckout(ctx context.Context, stageDirectory *os.File) error {
	output, err := runSecureStageHelper(ctx, stageDirectory, secureStageCleanupArgument)
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		detail = err.Error()
	}
	return fmt.Errorf("remove secure staging checkout through descriptor: %s", detail)
}

// RunSecureStageGitHelper is the child-side half of a secure worktree add.
// The caller passes the stage directory in fd 3 via exec.Cmd.ExtraFiles. This
// child alone changes its current directory from that immutable descriptor,
// then runs Git with only the already-constructed arguments supplied by its
// parent. It returns an ordinary process exit code for cmd/wb's early main
// dispatch and for the worktrees package's test helper.
func RunSecureStageGitHelper(args []string) int {
	stageDirectory := os.NewFile(uintptr(3), "wb-worktree-stage")
	if stageDirectory == nil {
		_, _ = fmt.Fprintln(os.Stderr, "wb secure stage helper: inherited stage directory is unavailable")
		return 1
	}
	defer func() { _ = stageDirectory.Close() }()
	if err := unix.Fchdir(int(stageDirectory.Fd())); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure stage helper: enter inherited stage directory: %v\n", err)
		return 1
	}
	if len(args) == 0 {
		_, _ = fmt.Fprintln(os.Stderr, "wb secure stage helper: missing operation")
		return 1
	}
	switch args[0] {
	case secureStagePathArgument:
		if len(args) != 1 {
			_, _ = fmt.Fprintln(os.Stderr, "wb secure stage helper: invalid path arguments")
			return 1
		}
		path, err := os.Getwd()
		if err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "wb secure stage helper: determine held directory: %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintln(os.Stdout, filepath.Clean(path))
		return 0
	case secureStageCheckArgument:
		if len(args) != 2 {
			_, _ = fmt.Fprintln(os.Stderr, "wb secure stage helper: invalid containment check arguments")
			return 1
		}
		return verifySecureStageContainment(args[1])
	case secureStageCleanupArgument:
		if len(args) != 1 {
			_, _ = fmt.Fprintln(os.Stderr, "wb secure stage helper: invalid cleanup arguments")
			return 1
		}
		if err := os.RemoveAll("checkout"); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "wb secure stage helper: remove staging checkout: %v\n", err)
			return 1
		}
		return 0
	}
	if len(args) < 3 || !filepath.IsAbs(args[1]) {
		_, _ = fmt.Fprintln(os.Stderr, "wb secure stage helper: invalid Git handoff arguments")
		return 1
	}
	if code := verifySecureStageContainment(args[0]); code != 0 {
		return code
	}
	command := exec.Command(args[1], args[2:]...)
	command.Env = console.Env()
	output, err := command.CombinedOutput()
	if len(output) > 0 {
		_, _ = os.Stderr.Write(output)
	}
	if err == nil {
		return verifySecureStageContainment(args[0])
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	_, _ = fmt.Fprintf(os.Stderr, "wb secure stage helper: run git: %v\n", err)
	return 1
}

func verifySecureStageContainment(trustedOperationRoot string) int {
	workingDirectory, err := os.Getwd()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure stage helper: determine staging directory: %v\n", err)
		return 1
	}
	workingDirectory = filepath.Clean(workingDirectory)
	trustedOperationRoot = filepath.Clean(trustedOperationRoot)
	if workingDirectory == trustedOperationRoot || !pathWithin(trustedOperationRoot, workingDirectory) {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure stage helper: staging directory %s is outside trusted operation root %s\n", workingDirectory, trustedOperationRoot)
		return 1
	}
	return 0
}

func verifyPublishedWorktree(
	ctx context.Context,
	canonical string,
	registrationsBefore map[string]bool,
	ownerPath string,
	ownerDirectory *os.File,
	finalPath string,
	finalDirectory *os.File,
	branch string,
) error {
	if !directoryStillMatches(ownerPath, ownerDirectory) {
		return fmt.Errorf("secure worktree owner path changed during repair")
	}
	if !directoryStillMatches(finalPath, finalDirectory) {
		return fmt.Errorf("published worktree path changed during repair")
	}
	registered, err := registeredWorktreePaths(ctx, canonical)
	if err != nil {
		return err
	}
	if len(registered) != len(registrationsBefore)+1 {
		return fmt.Errorf("worktree registrations changed unexpectedly during repair")
	}
	for path := range registrationsBefore {
		if !registered[path] {
			return fmt.Errorf("existing worktree registration disappeared during repair: %s", path)
		}
	}
	if !registered[filepath.Clean(finalPath)] {
		return fmt.Errorf("published worktree is not registered at %s", finalPath)
	}
	registeredBranch, err := registeredWorktreeBranch(ctx, canonical, finalPath)
	if err != nil {
		return err
	}
	if registeredBranch != "refs/heads/"+branch {
		return fmt.Errorf("published worktree registration branch is %q, want refs/heads/%s", registeredBranch, branch)
	}
	return nil
}

// openAbsoluteDirectoryNoFollow walks an absolute directory path one segment
// at a time from an already-open filesystem root. Unlike os.MkdirAll followed
// by os.Open, no unresolved home ancestor can be substituted between creation
// and the first descriptor open. The returned descriptor remains valid even
// if a later pathname swap makes the lexical spelling unsafe.
func openAbsoluteDirectoryNoFollow(path string, create bool) (*os.File, error) {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("secure directory path must be absolute: %s", path)
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open filesystem root for secure directory %s: %w", path, err)
	}
	if path == string(filepath.Separator) {
		directory := os.NewFile(uintptr(fd), "wb-secure-directory")
		if directory == nil {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("wrap secure directory %s", path)
		}
		return directory, nil
	}
	for _, segment := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		if segment == "" || segment == "." || segment == ".." {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("invalid secure directory segment %q", segment)
		}
		var next int
		if create {
			next, err = openOrCreateNoFollowDirectory(fd, segment)
		} else {
			next, err = unix.Openat(fd, segment, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
			if err != nil {
				var info unix.Stat_t
				if statErr := unix.Fstatat(fd, segment, &info, unix.AT_SYMLINK_NOFOLLOW); statErr == nil && info.Mode&unix.S_IFMT == unix.S_IFLNK {
					err = fmt.Errorf("refusing symlinked secure worktree directory %s", segment)
				} else {
					err = fmt.Errorf("open secure worktree directory %s: %w", segment, err)
				}
			}
		}
		_ = unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
	}
	directory := os.NewFile(uintptr(fd), "wb-secure-directory")
	if directory == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap secure directory %s", path)
	}
	return directory, nil
}

func makeSecureStageDirectory(parentFD int) (string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return "", fmt.Errorf("generate staging directory name: %w", err)
		}
		name := fmt.Sprintf(".wb-stage-%x", token[:])
		if err := unix.Mkdirat(parentFD, name, 0o700); err == nil {
			return name, nil
		} else if !errors.Is(err, unix.EEXIST) {
			return "", err
		}
	}
	return "", fmt.Errorf("create collision-free secure staging directory")
}

func openOrCreateNoFollowDirectory(parentFD int, name string) (int, error) {
	if err := unix.Mkdirat(parentFD, name, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
		return -1, fmt.Errorf("create secure worktree directory %s: %w", name, err)
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		var info unix.Stat_t
		if statErr := unix.Fstatat(parentFD, name, &info, unix.AT_SYMLINK_NOFOLLOW); statErr == nil && info.Mode&unix.S_IFMT == unix.S_IFLNK {
			return -1, fmt.Errorf("refusing symlinked secure worktree directory %s", name)
		}
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

type secureDirectoryIdentity struct {
	device uint64
	inode  uint64
}

func secureDirectoryIdentityAt(parentFD int, name string) (secureDirectoryIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return secureDirectoryIdentity{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return secureDirectoryIdentity{}, fmt.Errorf("%s is not a directory", name)
	}
	return secureDirectoryIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino)}, nil
}

// quarantineStageDirectoryByIdentityAt moves the WB-created stage out of its
// active namespace without unlinking a pathname. POSIX has no unlink-by-inode
// primitive; a verify-then-rmdir sequence could therefore remove a later
// replacement. The retired directory is intentionally left for a future safe
// maintenance pass rather than risking data loss under an attacker-controlled
// replacement.
func quarantineStageDirectoryByIdentityAt(operationDirectory *os.File, wanted secureDirectoryIdentity) error {
	if _, err := operationDirectory.Seek(0, 0); err != nil {
		return fmt.Errorf("rewind secure staging parent: %w", err)
	}
	entries, err := operationDirectory.ReadDir(-1)
	if err != nil {
		return fmt.Errorf("read secure staging parent: %w", err)
	}
	for _, entry := range entries {
		var stat unix.Stat_t
		if statErr := unix.Fstatat(int(operationDirectory.Fd()), entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW); statErr != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR {
			continue
		}
		if wanted.device != uint64(stat.Dev) || wanted.inode != uint64(stat.Ino) {
			continue
		}
		return quarantineStageDirectoryAt(operationDirectory, entry.Name(), wanted)
	}
	return nil
}

func quarantineMatchingStageDirectoryAt(operationDirectory, stageDirectory *os.File) error {
	held, err := stageDirectory.Stat()
	if err != nil {
		return fmt.Errorf("inspect held staging directory: %w", err)
	}
	var heldStat unix.Stat_t
	if err := unix.Fstat(int(stageDirectory.Fd()), &heldStat); err != nil {
		return fmt.Errorf("inspect held staging directory identity: %w", err)
	}
	expected := secureDirectoryIdentity{device: uint64(heldStat.Dev), inode: uint64(heldStat.Ino)}
	if _, err := operationDirectory.Seek(0, 0); err != nil {
		return fmt.Errorf("rewind secure staging parent: %w", err)
	}
	entries, err := operationDirectory.ReadDir(-1)
	if err != nil {
		return fmt.Errorf("read secure staging parent: %w", err)
	}
	for _, entry := range entries {
		fd, openErr := unix.Openat(int(operationDirectory.Fd()), entry.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			continue
		}
		candidate := os.NewFile(uintptr(fd), "wb-worktree-stage-cleanup")
		if candidate == nil {
			_ = unix.Close(fd)
			continue
		}
		info, statErr := candidate.Stat()
		_ = candidate.Close()
		if statErr == nil && os.SameFile(held, info) {
			return quarantineStageDirectoryAt(operationDirectory, entry.Name(), expected)
		}
	}
	return nil
}

func quarantineStageDirectoryAt(operationDirectory *os.File, name string, expected secureDirectoryIdentity) error {
	for attempt := 0; attempt < 16; attempt++ {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return fmt.Errorf("generate secure staging quarantine name: %w", err)
		}
		quarantineName := fmt.Sprintf(".wb-retired-stage-%x", token[:])
		err := renameNoReplace(int(operationDirectory.Fd()), name, int(operationDirectory.Fd()), quarantineName)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return fmt.Errorf("quarantine secure staging directory %s: %w", name, err)
		}
		actual, inspectErr := secureDirectoryIdentityAt(int(operationDirectory.Fd()), quarantineName)
		if inspectErr == nil && actual == expected {
			return nil
		}
		if restoreErr := renameNoReplace(int(operationDirectory.Fd()), quarantineName, int(operationDirectory.Fd()), name); restoreErr != nil {
			return fmt.Errorf("secure staging directory %s changed before quarantine; preserve replacement: %v", name, restoreErr)
		}
		return fmt.Errorf("secure staging directory %s changed before quarantine; refusing removal", name)
	}
	return fmt.Errorf("create collision-free secure staging quarantine name")
}

const rollbackCleanupTimeout = 30 * time.Second

func rollbackContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), rollbackCleanupTimeout)
}

// rollbackCreatedWorktree makes every creation failure converge on the same
// state: no checkout under the held staging descriptor, no newly-created Git
// worktree registration, and no branch created by this attempt. The stage
// directory might have been renamed outside its original parent, so cleanup
// must never rediscover it through a mutable pathname.
func rollbackCreatedWorktree(
	ctx context.Context,
	canonical string,
	stageDirectory *os.File,
	registrationsBefore map[string]bool,
	finalPath, branch, expectedBranchTip string,
) error {
	var failures []error
	if err := cleanupSecureStageCheckout(ctx, stageDirectory); err != nil {
		failures = append(failures, err)
	}
	if _, err := git(ctx, canonical, "worktree", "prune", "--expire", "now"); err != nil {
		failures = append(failures, fmt.Errorf("prune incomplete worktree registration: %w", err))
	}
	registered, err := registeredWorktreePaths(ctx, canonical)
	if err != nil {
		failures = append(failures, err)
	} else {
		for path := range registered {
			if !registrationsBefore[path] {
				failures = append(failures, fmt.Errorf("incomplete worktree remains registered at %s", path))
			}
		}
		if finalPath != "" && registered[filepath.Clean(finalPath)] {
			failures = append(failures, fmt.Errorf("incomplete worktree remains registered at %s", finalPath))
		}
	}
	if expectedBranchTip != "" {
		if err := deleteCreatedBranch(ctx, canonical, branch, expectedBranchTip); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
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

func registeredWorktreeBranch(ctx context.Context, canonical, wantedPath string) (string, error) {
	output, err := git(ctx, canonical, "worktree", "list", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("list worktree registrations: %w", err)
	}
	wantedPath = filepath.Clean(wantedPath)
	currentPath := ""
	for _, line := range strings.Split(output, "\n") {
		if path, found := strings.CutPrefix(line, "worktree "); found {
			currentPath = filepath.Clean(path)
			continue
		}
		if branch, found := strings.CutPrefix(line, "branch "); found && currentPath == wantedPath {
			return branch, nil
		}
	}
	return "", fmt.Errorf("worktree registration %s has no branch", wantedPath)
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

type operationLock struct {
	releaseFn func()
}

// acquireLockAt is the descriptor-relative form used while creating a new
// operation. It never follows a worktrees or task ancestor that was swapped
// after the operation directory was opened.
func acquireLockAt(operationDirectory *os.File) (operationLock, error) {
	fd, err := unix.Openat(int(operationDirectory.Fd()), ".lock", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		if errors.Is(err, unix.EEXIST) {
			return operationLock{}, fmt.Errorf("worktree operation is already active or was interrupted")
		}
		return operationLock{}, fmt.Errorf("acquire secure worktree operation lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), "wb-worktree-operation-lock")
	if file == nil {
		_ = unix.Close(fd)
		_ = unix.Unlinkat(int(operationDirectory.Fd()), ".lock", 0)
		return operationLock{}, fmt.Errorf("wrap secure worktree operation lock")
	}
	if _, err := fmt.Fprintf(file, "pid=%d\n", os.Getpid()); err != nil {
		_ = file.Close()
		_ = unix.Unlinkat(int(operationDirectory.Fd()), ".lock", 0)
		return operationLock{}, err
	}
	if err := file.Close(); err != nil {
		_ = unix.Unlinkat(int(operationDirectory.Fd()), ".lock", 0)
		return operationLock{}, err
	}
	return operationLock{releaseFn: func() {
		_ = unix.Unlinkat(int(operationDirectory.Fd()), ".lock", 0)
	}}, nil
}

func (lock operationLock) release() {
	if lock.releaseFn != nil {
		lock.releaseFn()
	}
}
