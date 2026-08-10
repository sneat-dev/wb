// Package worktrees creates and validates the isolated Git worktrees used for
// human and agent development. Canonical clones remain clean, current mirrors
// of their base branches; all feature work lives below .wb/worktrees.
package worktrees

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
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

// SecureCanonicalGitHelperArgument selects the private WB child-process path
// that validates retained canonical root and Git-directory descriptors before
// executing a canonical-clone Git operation.
const SecureCanonicalGitHelperArgument = "--wb-internal-canonical-git"

// SecureStageCanonicalGitHelperArgument selects the private WB child-process
// path that combines an inherited private stage with an inherited canonical
// Git capability. It is deliberately separate from SecureStageGitHelper so
// the small stage inspection helper never needs a Git capability.
const SecureStageCanonicalGitHelperArgument = "--wb-internal-stage-canonical-git"

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
	// afterSecureCheckoutAuthorization is a test-only seam after the exact
	// staged checkout inode has been authorized and immediately before publish.
	// It proves a late checkout substitution is restored rather than published.
	afterSecureCheckoutAuthorization func()
	// afterSecureCheckoutMove is a test-only seam immediately after the
	// no-replace publish rename, before both destination and source identities
	// are rechecked. It exercises a second source insertion (ABA) without
	// exposing this recovery-only hook to production callers.
	afterSecureCheckoutMove func()
	// afterPublishedWorktreeAuthorization is a test-only seam after the final
	// worktree inode is authorized and before descriptor-anchored Git repair.
	// It proves a late final-path substitution cannot receive Git mutation.
	afterPublishedWorktreeAuthorization func()
	// afterWorktreeRepair is a test-only seam after Git repairs registration but
	// before WB accepts the published checkout. It proves the final ownership
	// and registration checks reject a path swapped during repair.
	afterWorktreeRepair func()
	// afterStagedWorktreeAdd and beforeWorktreeRepair are test-only failure
	// seams. They model Git reporting an error after checkout creation, so the
	// rollback invariants are exercised without platform-specific hook tricks.
	afterStagedWorktreeAdd func() error
	beforeWorktreeRepair   func() error
	// afterCanonicalGitAuthorization is a test-only seam after the parent has
	// authorized a retained canonical root and `.git` pair, immediately before
	// a child reauthorizes the same pair. It proves the child, rather than a
	// stale lexical check, is the final authority for Git.
	afterCanonicalGitAuthorization func()
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
	canonical    *canonicalRepository
	baseRevision string
	branchExists bool
	resumed      bool
}

// canonicalRepository owns the root and common Git directory descriptors for
// one canonical clone through planning, staging, repair, and rollback. Paths
// remain diagnostics and result values only; every mutating Git handoff gets
// both retained descriptors.
type canonicalRepository struct {
	path            string
	root            *os.File
	common          *os.File
	afterValidation func()
}

func (canonical *canonicalRepository) close() {
	if canonical == nil {
		return
	}
	if canonical.common != nil {
		_ = canonical.common.Close()
	}
	if canonical.root != nil {
		_ = canonical.root.Close()
	}
}

func (canonical *canonicalRepository) validate() error {
	if canonical == nil || canonical.root == nil || canonical.common == nil {
		return fmt.Errorf("canonical repository descriptors are unavailable")
	}
	if !directoryStillMatches(canonical.path, canonical.root) {
		return fmt.Errorf("canonical repository path changed while preparing worktree: %s", canonical.path)
	}
	if !directoryEntryStillMatches(canonical.root, ".git", canonical.common) {
		return fmt.Errorf("canonical Git directory changed while preparing worktree: %s", canonical.path)
	}
	return nil
}

func (canonical *canonicalRepository) authorizeForGit() error {
	if err := canonical.validate(); err != nil {
		return err
	}
	if canonical.afterValidation != nil {
		canonical.afterValidation()
	}
	return nil
}

func openCanonicalRepository(path string) (*canonicalRepository, error) {
	root, err := openAbsoluteDirectoryNoFollow(path, false)
	if err != nil {
		return nil, err
	}
	gitFD, err := unix.Openat(int(root.Fd()), ".git", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("open canonical Git directory without following links: %w", err)
	}
	common := os.NewFile(uintptr(gitFD), "wb-canonical-git-directory")
	if common == nil {
		_ = unix.Close(gitFD)
		_ = root.Close()
		return nil, fmt.Errorf("wrap canonical Git directory")
	}
	canonical := &canonicalRepository{path: path, root: root, common: common}
	if err := canonical.validate(); err != nil {
		canonical.close()
		return nil, err
	}
	return canonical, nil
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

// Create verifies each clean canonical clone, fetches its requested origin base
// without changing any local branch, then creates (or explicitly resumes) the
// corresponding isolated worktree.
//
// Synchronization happens before any branch is created. This is deliberate:
// every new feature branch must be based on a verified latest remote base. The
// canonical checkout is only a clean Git capability: WB never switches it to
// the base or fast-forwards a local branch as a side effect of creation.
func Create(ctx context.Context, repositories []string, options CreateOptions) ([]CreateResult, error) {
	normalized, err := normalizeCreateOptions(options)
	if err != nil {
		return nil, err
	}
	repositories, err = ValidateRepositories(repositories)
	if err != nil {
		return nil, err
	}
	if err := requireGitFilesystemCapability(); err != nil {
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
	defer func() {
		for index := range plans {
			plans[index].canonical.close()
		}
	}()
	for _, repository := range repositories {
		owner, name, canonical, err := canonicalRepositoryPath(normalized.ProjectsRoot, repository)
		if err != nil {
			return nil, err
		}
		canonicalHandle, err := openCanonicalRepository(canonical)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("canonical clone is missing for %s at %s; clone it with `wb sync` first", repository, canonical)
			}
			return nil, err
		}
		canonicalHandle.afterValidation = normalized.afterCanonicalGitAuthorization
		baseRevision, err := synchronizeCanonical(ctx, canonicalHandle, repository, normalized.Base)
		if err != nil {
			canonicalHandle.close()
			return nil, err
		}
		worktree, exists, err := prepareWorktreeDestination(operation.Path, operation.Directory, owner, name)
		if err != nil {
			canonicalHandle.close()
			return nil, err
		}
		plan := createPlan{owner: owner, repository: name, canonical: canonicalHandle, baseRevision: baseRevision, result: CreateResult{
			Repository: repository, CanonicalDir: canonical, WorktreeDir: worktree,
			Branch: normalized.Branch, Base: normalized.Base,
		}}
		if exists {
			if !normalized.Resume {
				canonicalHandle.close()
				return nil, fmt.Errorf("worktree already exists: %s (use --resume or choose another operation)", worktree)
			}
			if err := validateExistingWorktree(ctx, canonicalHandle, worktree, normalized.Branch); err != nil {
				canonicalHandle.close()
				return nil, err
			}
			plan.resumed = true
			plan.result.Action = "resumed"
		} else {
			plan.branchExists, err = localBranchExistsCanonical(ctx, canonicalHandle, normalized.Branch)
			if err != nil {
				canonicalHandle.close()
				return nil, err
			}
			if plan.branchExists && !normalized.Resume {
				canonicalHandle.close()
				return nil, fmt.Errorf("branch %q already exists in %s (use --resume or choose --branch)", normalized.Branch, repository)
			}
			if plan.branchExists {
				if occupied, path, err := branchWorktreeCanonical(ctx, canonicalHandle, normalized.Branch); err != nil {
					canonicalHandle.close()
					return nil, err
				} else if occupied {
					canonicalHandle.close()
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
				plan.canonical,
				operation.Path,
				operation.Directory,
				plan.owner,
				plan.repository,
				plan.result.Branch,
				plan.result.Base,
				plan.baseRevision,
				plan.branchExists,
				normalized.beforeSecureWorktreeAdd,
				normalized.afterSecureStageDirectoryCreated,
				normalized.afterSecureStageValidation,
				normalized.afterSecureStageVerification,
				normalized.afterSecureDestinationValidation,
				normalized.afterSecureCheckoutAuthorization,
				normalized.afterSecureCheckoutMove,
				normalized.afterPublishedWorktreeAuthorization,
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
				return managedWorktreeLocation{}, &RepositoryRenameMismatchError{
					Worktree: root, Owner: owner, PathRepository: parts[2], CanonicalRepository: repository,
				}
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

// RepositoryRenameMismatchError reports a managed worktree whose on-disk
// <task>/<owner>/<repository> path segment no longer names its canonical
// clone's current repository — exactly the signature a GitHub repository
// rename leaves behind on every worktree that predates it. Its Error text is
// unchanged from the plain fmt.Errorf this replaced, so `wb worktree guard`
// (which still treats this as a hard, single-checkout rejection) reports the
// same message as before. List and Cleanup recognize it structurally via
// errors.As to survive it: this is ordinary history, not corruption, and a
// stale path segment alone is not evidence that anyone's work is at risk.
type RepositoryRenameMismatchError struct {
	Worktree            string
	Owner               string
	PathRepository      string
	CanonicalRepository string
}

func (mismatch *RepositoryRenameMismatchError) Error() string {
	return fmt.Sprintf(
		"worktree %s has path repository %q but canonical clone repository %q",
		mismatch.Worktree, mismatch.PathRepository, mismatch.CanonicalRepository,
	)
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

// synchronizeCanonical validates that canonical is an ordinary clean clone and
// fetches exactly refs/heads/<base> from origin. It deliberately never checks
// out, pulls, resets, or updates a local branch: the requested base may be
// stale locally or checked out in another linked worktree. The returned object
// ID pins the new worktree to the commit just verified from origin, even if a
// later fetch advances origin/<base> before Git creates the branch.
func synchronizeCanonical(ctx context.Context, canonical *canonicalRepository, repository, base string) (string, error) {
	if err := canonical.validate(); err != nil {
		return "", err
	}
	root, err := gitCanonical(ctx, canonical, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	if filepath.Clean(root) != filepath.Clean(canonical.path) {
		return "", fmt.Errorf("%s is not the root of its canonical clone (root is %s)", canonical.path, root)
	}
	gitDir, commonDir, err := gitDirectoriesCanonical(ctx, canonical)
	if err != nil {
		return "", err
	}
	if gitDir != commonDir {
		return "", fmt.Errorf("%s is a linked worktree, not the canonical clone for %s", canonical.path, repository)
	}
	clean, err := cleanCanonicalWorktree(ctx, canonical)
	if err != nil {
		return "", err
	}
	if !clean {
		return "", fmt.Errorf("canonical clone %s is dirty; move or commit its changes before creating a worktree", canonical.path)
	}

	// Fetching this fully-qualified ref writes only fetch metadata. In
	// particular, it does not update refs/heads/<base>, which could be stale or
	// checked out in another worktree. A missing or inaccessible remote base
	// fails before WB creates a branch or publishes a checkout.
	if _, err := gitCanonical(ctx, canonical, "fetch", "--no-tags", "origin", "refs/heads/"+base); err != nil {
		return "", fmt.Errorf("fetch verified origin base %s/%s: %w", repository, base, err)
	}
	baseRevision, err := gitCanonical(ctx, canonical, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("verify fetched origin base %s/%s: %w", repository, base, err)
	}
	if !isGitObjectID(baseRevision) {
		return "", fmt.Errorf("verify fetched origin base %s/%s: Git returned invalid commit %q", repository, base, baseRevision)
	}
	return baseRevision, nil
}

func validateExistingWorktree(ctx context.Context, canonical *canonicalRepository, worktree, branch string) error {
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
	expected := filepath.Join(canonical.path, ".git")
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

func gitDirectoriesCanonical(ctx context.Context, canonical *canonicalRepository) (gitDir, commonDir string, err error) {
	gitDir, err = gitCanonical(ctx, canonical, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", "", err
	}
	commonDir, err = gitCanonical(ctx, canonical, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", "", err
	}
	return filepath.Clean(gitDir), filepath.Clean(commonDir), nil
}

func cleanCanonicalWorktree(ctx context.Context, canonical *canonicalRepository) (bool, error) {
	output, err := gitCanonical(ctx, canonical, "status", "--porcelain=v1")
	return output == "", err
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

func localBranchExistsCanonical(ctx context.Context, canonical *canonicalRepository, branch string) (bool, error) {
	output, err := gitCanonical(ctx, canonical, "branch", "--list", "--format=%(refname:short)", branch)
	if err != nil {
		return false, fmt.Errorf("inspect branch %q in %s: %w", branch, canonical.path, err)
	}
	return strings.TrimSpace(output) != "", nil
}

func branchWorktreeCanonical(ctx context.Context, canonical *canonicalRepository, branch string) (bool, string, error) {
	output, err := gitCanonical(ctx, canonical, "worktree", "list", "--porcelain")
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

const sandboxTempDirectoryWarning = "git: warning: confstr() failed with code 5: couldn't get path of DARWIN_USER_TEMP_DIR; using /tmp instead\n"

func trimSecureGitOutput(output []byte) string {
	return strings.TrimSpace(strings.ReplaceAll(string(output), sandboxTempDirectoryWarning, ""))
}

func gitCanonical(ctx context.Context, canonical *canonicalRepository, args ...string) (string, error) {
	if err := canonical.authorizeForGit(); err != nil {
		return "", err
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate WB canonical Git helper: %w", err)
	}
	gitExecutable, err := trustedGitExecutable()
	if err != nil {
		return "", err
	}
	command := exec.CommandContext(ctx, executable, append([]string{SecureCanonicalGitHelperArgument, canonical.path, gitExecutable}, args...)...)
	command.Env = console.Env()
	command.ExtraFiles = []*os.File{canonical.root, canonical.common}
	output, err := command.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("canonical Git %s: %s", strings.Join(args, " "), detail)
	}
	return trimSecureGitOutput(output), nil
}

// RunSecureCanonicalGitHelper runs Git from the inherited canonical root only
// after opening and comparing its `.git` entry with the inherited Git
// directory descriptor. This prevents Git's own discovery from treating a
// substituted `.git` pathname as authority.
func RunSecureCanonicalGitHelper(args []string) int {
	if len(args) < 2 || !filepath.IsAbs(args[0]) {
		_, _ = fmt.Fprintln(os.Stderr, "wb secure canonical helper: missing Git executable")
		return 1
	}
	root := os.NewFile(uintptr(3), "wb-canonical-root")
	common := os.NewFile(uintptr(4), "wb-canonical-git-directory")
	if root == nil || common == nil {
		if root != nil {
			_ = root.Close()
		}
		if common != nil {
			_ = common.Close()
		}
		_, _ = fmt.Fprintln(os.Stderr, "wb secure canonical helper: inherited canonical descriptors are unavailable")
		return 1
	}
	defer func() { _ = root.Close() }()
	defer func() { _ = common.Close() }()
	if err := unix.Fchdir(int(root.Fd())); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure canonical helper: enter inherited canonical root: %v\n", err)
		return 1
	}
	if !directoryEntryStillMatches(root, ".git", common) {
		_, _ = fmt.Fprintln(os.Stderr, "wb secure canonical helper: canonical Git directory changed before Git operation")
		return 1
	}
	if err := unix.Fchdir(int(common.Fd())); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure canonical helper: enter inherited Git directory: %v\n", err)
		return 1
	}
	if !directoryStillMatches(args[0], root) {
		_, _ = fmt.Fprintln(os.Stderr, "wb secure canonical helper: canonical repository path changed before Git operation")
		return 1
	}
	capability, err := newGitFilesystemCapability(gitFilesystemCapabilityRoot{path: args[0], directory: root})
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure canonical helper: %v\n", err)
		return 1
	}
	return runGitWithFilesystemCapability(capability, args[1], args[2:], gitEnvironmentWithHeldGitDir(filepath.Join(args[0], ".git")))
}

// gitEnvironmentWithHeldGitDir makes Git use the inherited `.git` descriptor
// and routes helper/Git temporary files into an already-authorized resource.
// A platform sandbox must never inherit an ambient system temporary directory.
func gitEnvironmentWithHeldGitDir(temporaryRoot string) []string {
	base := console.Env()
	environment := make([]string, 0, len(base)+3)
	for _, entry := range base {
		key, _, found := strings.Cut(entry, "=")
		if found {
			switch key {
			case "GIT_DIR", "GIT_WORK_TREE", "TMPDIR":
				continue
			}
		}
		environment = append(environment, entry)
	}
	return append(environment, "GIT_DIR=.", "GIT_WORK_TREE=..", "TMPDIR="+temporaryRoot)
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
	if err := wbhome.SeedReadme(home); err != nil {
		_ = homeDirectory.Close()
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
	canonical *canonicalRepository,
	operationRoot string,
	operationDirectory *os.File,
	owner, repository, branch, base string,
	baseRevision string,
	branchExists bool,
	beforeAdd func(),
	afterStageDirectoryCreated func(),
	afterStageValidation func(),
	afterStageVerification func(),
	afterDestinationValidation func(),
	afterCheckoutAuthorization func(),
	afterCheckoutMove func(),
	afterPublishedAuthorization func(),
	afterRepair func(),
	afterStagedAdd func() error,
	beforeRepair func() error,
) error {
	expectedBranchTip := ""
	if !branchExists {
		expectedBranchTip = baseRevision
		if !isGitObjectID(expectedBranchTip) {
			return fmt.Errorf("create feature branch from origin/%s: verified base commit is invalid", base)
		}
	}
	operationFD := int(operationDirectory.Fd())
	trustedOperationRoot, err := secureDirectoryPath(ctx, operationDirectory)
	if err != nil {
		return fmt.Errorf("resolve secure worktree operation directory: %w", err)
	}
	registrationsBefore, err := registeredWorktreePathsCanonical(ctx, canonical)
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
	stageName, err := makeSecureStageDirectory(operationDirectory)
	if err != nil {
		return fmt.Errorf("create secure worktree staging directory: %w", err)
	}
	var stageIdentity secureDirectoryIdentity
	stageIdentityKnown := false
	preserveStageReplacement := false
	// Install cleanup before opening or wrapping the stage descriptor. A
	// failure in either step must not strand a private stage directory.
	defer func() {
		if stageIdentityKnown && !preserveStageReplacement {
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
		if !preserveStageReplacement {
			_ = quarantineMatchingStageDirectoryAt(operationDirectory, stageDirectory)
		}
	}()
	rollback := func(creationErr error, finalPath string, checkoutDirectory *os.File) error {
		if checkoutDirectory != nil && !directoryEntryStillMatches(stageDirectory, "checkout", checkoutDirectory) {
			preserveStageReplacement = true
		}
		cleanupCtx, cancel := rollbackContext(ctx)
		defer cancel()
		if cleanupErr := rollbackCreatedWorktree(cleanupCtx, canonical, stageDirectory, checkoutDirectory, registrationsBefore, finalPath, branch, expectedBranchTip); cleanupErr != nil {
			return fmt.Errorf("%w; rollback incomplete worktree creation: %v", creationErr, cleanupErr)
		}
		return creationErr
	}
	if beforeAdd != nil {
		beforeAdd()
	}
	if !directoryStillMatches(stageRoot, stageDirectory) {
		return rollback(fmt.Errorf("secure staging directory path changed during creation; refusing redirected checkout"), "", nil)
	}
	if afterStageValidation != nil {
		afterStageValidation()
	}
	if err := gitWorktreeAddFromStageDirectory(ctx, canonical, trustedOperationRoot, stageDirectory, branch, baseRevision, branchExists); err != nil {
		return rollback(fmt.Errorf("create staged worktree: %w", err), "", nil)
	}
	checkoutFD, err := unix.Openat(stageFD, "checkout", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return rollback(fmt.Errorf("open staged worktree checkout: %w", err), "", nil)
	}
	checkoutDirectory := os.NewFile(uintptr(checkoutFD), "wb-worktree-staged-checkout")
	if checkoutDirectory == nil {
		_ = unix.Close(checkoutFD)
		return rollback(fmt.Errorf("wrap staged worktree checkout"), "", nil)
	}
	defer func() { _ = checkoutDirectory.Close() }()
	if afterStagedAdd != nil {
		if err := afterStagedAdd(); err != nil {
			return rollback(fmt.Errorf("create staged worktree: %w", err), "", checkoutDirectory)
		}
	}
	if err := verifySecureStageDirectory(ctx, stageDirectory, trustedOperationRoot); err != nil {
		return rollback(fmt.Errorf("verify secure staging directory before publish: %w", err), "", checkoutDirectory)
	}
	if afterStageVerification != nil {
		afterStageVerification()
	}
	ownerPath := filepath.Join(operationRoot, owner)
	if !directoryStillMatches(ownerPath, ownerDirectory) {
		return rollback(fmt.Errorf("secure worktree owner path changed during creation; refusing redirected checkout"), "", checkoutDirectory)
	}
	if afterDestinationValidation != nil {
		afterDestinationValidation()
	}
	finalDirectory, err := moveExpectedDirectoryNoReplace(stageDirectory, "checkout", ownerDirectory, repository, checkoutDirectory, afterCheckoutAuthorization, afterCheckoutMove)
	if err != nil {
		if finalDirectory != nil || errors.Is(err, errDirectoryMoveIdentityChanged) {
			if finalDirectory != nil {
				_ = finalDirectory.Close()
			}
			preserveStageReplacement = true
			return fmt.Errorf("publish secure worktree entered an ambiguous replacement state; preserved both paths for recovery: %w", err)
		}
		return rollback(fmt.Errorf("publish secure worktree: %w", err), "", checkoutDirectory)
	}
	defer func() { _ = finalDirectory.Close() }()
	finalPath := filepath.Join(ownerPath, repository)
	rollbackPublished := func(creationErr error) error {
		stagedDirectory, rollbackErr := moveExpectedDirectoryNoReplace(ownerDirectory, repository, stageDirectory, "checkout", finalDirectory, nil)
		if rollbackErr != nil {
			if stagedDirectory != nil {
				_ = stagedDirectory.Close()
			}
			if errors.Is(rollbackErr, errDirectoryMoveIdentityChanged) {
				preserveStageReplacement = true
				return fmt.Errorf("%w; published checkout rollback entered an ambiguous replacement state; preserved both paths for recovery: %v", creationErr, rollbackErr)
			}
			return rollback(fmt.Errorf("%w; roll back published checkout: %v", creationErr, rollbackErr), finalPath, nil)
		}
		defer func() { _ = stagedDirectory.Close() }()
		return rollback(creationErr, finalPath, stagedDirectory)
	}
	if !directoryStillMatches(ownerPath, ownerDirectory) {
		return rollbackPublished(fmt.Errorf("secure worktree owner path changed after publish; refusing redirected checkout"))
	}
	if !directoryStillMatches(finalPath, finalDirectory) {
		return rollbackPublished(fmt.Errorf("published worktree path changed before repair; refusing redirected checkout"))
	}
	var repairErr error
	if beforeRepair != nil {
		repairErr = beforeRepair()
	} else {
		if afterPublishedAuthorization != nil {
			afterPublishedAuthorization()
		}
		repairErr = runSecureCleanupGitHelper(ctx, canonical, ownerDirectory, finalDirectory, ownerPath, finalPath, "worktree", "repair", finalPath)
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

// gitWorktreeAddFromStageDirectory starts a private WB child with the stage,
// canonical root, and canonical Git directory capabilities. The child derives
// the Git target from the held stage only after containment validation, enters
// the held canonical root, and supplies Git's GIT_DIR through the inherited
// `.git` descriptor. Neither end of the handoff gives Git lexical authority.
func gitWorktreeAddFromStageDirectory(
	ctx context.Context,
	canonical *canonicalRepository,
	trustedOperationRoot string,
	stageDirectory *os.File,
	branch, baseRevision string,
	branchExists bool,
) error {
	gitExecutable, err := trustedGitExecutable()
	if err != nil {
		return err
	}
	if err := canonical.authorizeForGit(); err != nil {
		return err
	}
	output, err := runSecureStageCanonicalGitHelper(ctx, stageDirectory, canonical, trustedOperationRoot, gitExecutable, branch, baseRevision, branchExists)
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
	return platformTrustedGitExecutable()
}

func worktreeAddArguments(checkout, branch, baseRevision string, branchExists bool) []string {
	args := []string{"worktree", "add", "--quiet"}
	if branchExists {
		args = append(args, checkout, branch)
	} else {
		args = append(args, "-b", branch, checkout, baseRevision)
	}
	return args
}

const (
	secureStageCheckArgument = "--check"
	secureStagePathArgument  = "--path"
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

func runSecureStageCanonicalGitHelper(
	ctx context.Context,
	stageDirectory *os.File,
	canonical *canonicalRepository,
	trustedOperationRoot, gitExecutable, branch, baseRevision string,
	branchExists bool,
) ([]byte, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate WB secure staged canonical Git helper: %w", err)
	}
	exists := "0"
	if branchExists {
		exists = "1"
	}
	command := exec.CommandContext(ctx, executable, SecureStageCanonicalGitHelperArgument, trustedOperationRoot, canonical.path, gitExecutable, branch, baseRevision, exists)
	command.Env = console.Env()
	command.ExtraFiles = []*os.File{stageDirectory, canonical.root, canonical.common}
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

// RunSecureStageCanonicalGitHelper is the last authority before Git creates a
// staged checkout. FD 3 is the private stage, FD 4 is the canonical root, and
// FD 5 is its `.git` directory. The stage target is derived only after the
// inherited stage passes containment; Git itself receives the inherited `.git`
// directory through GIT_DIR instead of resolving a lexical canonical path.
func RunSecureStageCanonicalGitHelper(args []string) int {
	if len(args) != 6 || !filepath.IsAbs(args[0]) || !filepath.IsAbs(args[1]) || args[5] != "0" && args[5] != "1" {
		_, _ = fmt.Fprintln(os.Stderr, "wb secure staged canonical helper: invalid arguments")
		return 1
	}
	stage := os.NewFile(uintptr(3), "wb-worktree-stage")
	canonical := os.NewFile(uintptr(4), "wb-canonical-root")
	common := os.NewFile(uintptr(5), "wb-canonical-git-directory")
	if stage == nil || canonical == nil || common == nil {
		if stage != nil {
			_ = stage.Close()
		}
		if canonical != nil {
			_ = canonical.Close()
		}
		if common != nil {
			_ = common.Close()
		}
		_, _ = fmt.Fprintln(os.Stderr, "wb secure staged canonical helper: inherited descriptors are unavailable")
		return 1
	}
	defer func() { _ = stage.Close() }()
	defer func() { _ = canonical.Close() }()
	defer func() { _ = common.Close() }()
	if err := unix.Fchdir(int(stage.Fd())); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure staged canonical helper: enter inherited stage: %v\n", err)
		return 1
	}
	if code := verifySecureStageContainment(args[0]); code != 0 {
		return code
	}
	stagePath, err := os.Getwd()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure staged canonical helper: derive held stage path: %v\n", err)
		return 1
	}
	if err := unix.Fchdir(int(canonical.Fd())); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure staged canonical helper: enter inherited canonical root: %v\n", err)
		return 1
	}
	if !directoryStillMatches(args[1], canonical) || !directoryEntryStillMatches(canonical, ".git", common) {
		_, _ = fmt.Fprintln(os.Stderr, "wb secure staged canonical helper: canonical Git directory changed before Git operation")
		return 1
	}
	if err := unix.Fchdir(int(common.Fd())); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure staged canonical helper: enter inherited Git directory: %v\n", err)
		return 1
	}
	gitArgs := worktreeAddArguments(filepath.Join(filepath.Clean(stagePath), "checkout"), args[3], args[4], args[5] == "1")
	// Git needs to write only inside the retained stage (including its scoped
	// TMPDIR), never elsewhere in the operation root.
	capability, err := newGitFilesystemCapability(
		gitFilesystemCapabilityRoot{path: stagePath, directory: stage},
		gitFilesystemCapabilityRoot{path: args[1], directory: canonical},
	)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure staged canonical helper: %v\n", err)
		return 1
	}
	return runGitWithFilesystemCapability(capability, args[2], gitArgs, gitEnvironmentWithHeldGitDir(stagePath))
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
	canonical *canonicalRepository,
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
	registered, err := registeredWorktreePathsCanonical(ctx, canonical)
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
	registeredBranch, err := registeredWorktreeBranchCanonical(ctx, canonical, finalPath)
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

// makeSecureStageDirectory claims a previously retired, empty WB stage before
// creating a new one. Claiming is identity-bound and no-replace: an entry
// merely named like a retirement is never removed or overwritten. Successful
// operations therefore converge on a reusable bounded pool instead of adding
// one inert stage directory forever.
func makeSecureStageDirectory(parent *os.File) (string, error) {
	if name, claimed, err := claimRetiredStageDirectory(parent); err != nil {
		return "", err
	} else if claimed {
		return name, nil
	}
	parentFD := int(parent.Fd())
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

func claimRetiredStageDirectory(parent *os.File) (name string, claimed bool, err error) {
	if _, err := parent.Seek(0, 0); err != nil {
		return "", false, fmt.Errorf("rewind secure staging parent for retirement reap: %w", err)
	}
	entries, err := parent.ReadDir(-1)
	if err != nil {
		return "", false, fmt.Errorf("read secure staging parent for retirement reap: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".wb-retired-stage-") {
			continue
		}
		fd, openErr := unix.Openat(int(parent.Fd()), entry.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			continue
		}
		retired := os.NewFile(uintptr(fd), "wb-retired-stage-claim")
		if retired == nil {
			_ = unix.Close(fd)
			continue
		}
		empty, emptyErr := directoryEmpty(retired)
		if emptyErr != nil {
			_ = retired.Close()
			return "", false, fmt.Errorf("inspect retired staging directory %s: %w", entry.Name(), emptyErr)
		}
		if !empty {
			_ = retired.Close()
			continue
		}
		for attempt := 0; attempt < 16; attempt++ {
			var token [16]byte
			if _, err := rand.Read(token[:]); err != nil {
				_ = retired.Close()
				return "", false, fmt.Errorf("generate reclaimed staging name: %w", err)
			}
			name = fmt.Sprintf(".wb-stage-%x", token[:])
			moved, moveErr := moveExpectedDirectoryNoReplace(parent, entry.Name(), parent, name, retired, nil)
			if errors.Is(moveErr, unix.EEXIST) {
				continue
			}
			_ = retired.Close()
			if moveErr != nil {
				if moved != nil {
					_ = moved.Close()
				}
				if errors.Is(moveErr, errDirectoryMoveIdentityChanged) {
					return "", false, fmt.Errorf("reclaim retired staging directory %s: %w", entry.Name(), moveErr)
				}
				break // A stale/replaced candidate is left untouched; inspect siblings.
			}
			_ = moved.Close()
			return name, true, nil
		}
	}
	return "", false, nil
}

func directoryEmpty(directory *os.File) (bool, error) {
	if _, err := directory.Seek(0, 0); err != nil {
		return false, err
	}
	entries, err := directory.ReadDir(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	return len(entries) == 0, nil
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

func directoryEntryStillMatches(parent *os.File, name string, directory *os.File) bool {
	if parent == nil || directory == nil {
		return false
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return false
	}
	candidate := os.NewFile(uintptr(fd), "wb-worktree-entry-check")
	if candidate == nil {
		_ = unix.Close(fd)
		return false
	}
	defer func() { _ = candidate.Close() }()
	expected, expectedErr := directory.Stat()
	actual, actualErr := candidate.Stat()
	return expectedErr == nil && actualErr == nil && os.SameFile(expected, actual)
}

var errDirectoryMoveIdentityChanged = errors.New("directory move identity changed")

// moveExpectedDirectoryNoReplace moves a retained directory entry without
// replacing a destination. It verifies both halves after the no-replace move:
// the destination must be the expected descriptor and the old source name
// must still be absent. If a second source insertion races the move, the
// expected directory may already be safely published at the destination but
// no rollback is allowed to touch either name; callers receive the retained
// descriptor plus errDirectoryMoveIdentityChanged and must preserve both.
func moveExpectedDirectoryNoReplace(
	fromDirectory *os.File,
	fromName string,
	toDirectory *os.File,
	toName string,
	expected *os.File,
	afterAuthorization func(),
	afterMove ...func(),
) (*os.File, error) {
	if !directoryEntryStillMatches(fromDirectory, fromName, expected) {
		return nil, fmt.Errorf("directory entry %s changed after inspection; refusing mutation", fromName)
	}
	if afterAuthorization != nil {
		afterAuthorization()
	}
	if err := renameNoReplace(int(fromDirectory.Fd()), fromName, int(toDirectory.Fd()), toName); err != nil {
		return nil, err
	}
	if len(afterMove) > 0 && afterMove[0] != nil {
		afterMove[0]()
	}
	fd, err := unix.Openat(int(toDirectory.Fd()), toName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open moved directory %s: %w", toName, err)
	}
	moved := os.NewFile(uintptr(fd), "wb-worktree-moved-directory")
	if moved == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap moved directory %s", toName)
	}
	expectedInfo, expectedErr := expected.Stat()
	movedInfo, movedErr := moved.Stat()
	expectedMoved := expectedErr == nil && movedErr == nil && os.SameFile(expectedInfo, movedInfo)
	sourceAbsent, absentErr := noFollowChildAbsent(int(fromDirectory.Fd()), fromName)
	if absentErr != nil {
		_ = moved.Close()
		return nil, fmt.Errorf("inspect source %s after directory move: %w", fromName, absentErr)
	}
	if expectedMoved && sourceAbsent {
		return moved, nil
	}
	if !sourceAbsent {
		// A second source insertion means a restore could clobber it. Keep the
		// moved descriptor open for the caller's diagnostics but never attempt
		// another path mutation; both names now require explicit recovery.
		return moved, fmt.Errorf("%w: source %s was recreated after no-replace move", errDirectoryMoveIdentityChanged, fromName)
	}
	_ = moved.Close()
	if restoreErr := renameNoReplace(int(toDirectory.Fd()), toName, int(fromDirectory.Fd()), fromName); restoreErr != nil {
		return nil, fmt.Errorf("%w: destination %s changed after inspection; preserve replacement: %v", errDirectoryMoveIdentityChanged, toName, restoreErr)
	}
	return nil, fmt.Errorf("%w: destination %s was not the expected directory", errDirectoryMoveIdentityChanged, toName)
}

func noFollowChildAbsent(parentFD int, name string) (bool, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, unix.Close(fd)
}

func quarantineDirectoryEntry(parent *os.File, name string, expected *os.File, prefix string) (*os.File, error) {
	for attempt := 0; attempt < 16; attempt++ {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return nil, fmt.Errorf("generate directory retirement name: %w", err)
		}
		retired := fmt.Sprintf("%s%x", prefix, token[:])
		moved, err := moveExpectedDirectoryNoReplace(parent, name, parent, retired, expected, nil)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			if moved != nil {
				_ = moved.Close()
			}
			return nil, err
		}
		return moved, nil
	}
	return nil, fmt.Errorf("create collision-free directory retirement name")
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
// replacement. Empty WB-owned retirements are claimed by the identity-bound
// stage pool on a later operation; nonempty or substituted entries stay put.
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
	fd, err := unix.Openat(int(operationDirectory.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open secure staging directory %s for quarantine: %w", name, err)
	}
	stage := os.NewFile(uintptr(fd), "wb-worktree-stage-quarantine")
	if stage == nil {
		_ = unix.Close(fd)
		return fmt.Errorf("wrap secure staging directory %s for quarantine", name)
	}
	defer func() { _ = stage.Close() }()
	actual, err := secureDirectoryIdentityAt(int(operationDirectory.Fd()), name)
	if err != nil || actual != expected {
		return fmt.Errorf("%w: secure staging directory %s changed before quarantine", errDirectoryMoveIdentityChanged, name)
	}
	retired, err := quarantineDirectoryEntry(operationDirectory, name, stage, ".wb-retired-stage-")
	if err != nil {
		return err
	}
	return retired.Close()
}

const rollbackCleanupTimeout = 30 * time.Second

func rollbackContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), rollbackCleanupTimeout)
}

// rollbackCreatedWorktree makes every creation failure converge on no active
// checkout under the held staging descriptor, no newly-created Git worktree
// registration, and no branch created by this attempt. The exact checkout is
// first moved into an inert retirement name; pathname deletion is deliberately
// avoided because it could remove a replacement inserted after authorization.
func rollbackCreatedWorktree(
	ctx context.Context,
	canonical *canonicalRepository,
	stageDirectory *os.File,
	checkoutDirectory *os.File,
	registrationsBefore map[string]bool,
	finalPath, branch, expectedBranchTip string,
) error {
	var failures []error
	if checkoutDirectory != nil {
		if err := quarantineSecureStageCheckout(stageDirectory, checkoutDirectory); err != nil {
			failures = append(failures, err)
			if errors.Is(err, errDirectoryMoveIdentityChanged) {
				// The checkout namespace is no longer unambiguous. In particular,
				// pruning or deleting the feature ref could damage the surviving
				// expected checkout or a successor. Preserve all Git state for
				// explicit recovery rather than making a path-based guess.
				return errors.Join(failures...)
			}
		}
	}
	if _, err := gitCanonical(ctx, canonical, "worktree", "prune", "--expire", "now"); err != nil {
		failures = append(failures, fmt.Errorf("prune incomplete worktree registration: %w", err))
	}
	registered, err := registeredWorktreePathsCanonical(ctx, canonical)
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
		if err := deleteCreatedBranchCanonical(ctx, canonical, branch, expectedBranchTip); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func quarantineSecureStageCheckout(stageDirectory, checkoutDirectory *os.File) error {
	for attempt := 0; attempt < 16; attempt++ {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return fmt.Errorf("generate staged checkout quarantine name: %w", err)
		}
		name := fmt.Sprintf(".wb-retired-checkout-%x", token[:])
		moved, err := moveExpectedDirectoryNoReplace(stageDirectory, "checkout", stageDirectory, name, checkoutDirectory, nil)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			if moved != nil {
				_ = moved.Close()
			}
			return fmt.Errorf("quarantine staged checkout: %w", err)
		}
		_ = moved.Close()
		return nil
	}
	return fmt.Errorf("create collision-free staged checkout quarantine name")
}

func registeredWorktreePathsCanonical(ctx context.Context, canonical *canonicalRepository) (map[string]bool, error) {
	output, err := gitCanonical(ctx, canonical, "worktree", "list", "--porcelain")
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

func registeredWorktreeBranchCanonical(ctx context.Context, canonical *canonicalRepository, wantedPath string) (string, error) {
	output, err := gitCanonical(ctx, canonical, "worktree", "list", "--porcelain")
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

func deleteCreatedBranchCanonical(ctx context.Context, canonical *canonicalRepository, branch, expectedTip string) error {
	exists, err := localBranchExistsCanonical(ctx, canonical, branch)
	if err != nil || !exists {
		return err
	}
	tip, err := gitCanonical(ctx, canonical, "rev-parse", "refs/heads/"+branch)
	if err != nil {
		return fmt.Errorf("resolve incomplete branch %q: %w", branch, err)
	}
	if tip != expectedTip {
		return fmt.Errorf("refusing to delete incomplete branch %q: it moved from expected base %s to %s", branch, expectedTip, tip)
	}
	if _, err := gitCanonical(ctx, canonical, "update-ref", "-d", "refs/heads/"+branch, tip); err != nil {
		return fmt.Errorf("delete incomplete branch %q: %w", branch, err)
	}
	return nil
}

type operationLock struct {
	directory     *os.File
	file          *os.File
	identity      managedLockIdentity
	beforeRelease func()
}

type managedLockIdentity struct {
	device uint64
	inode  uint64
}

// acquireLockAt is the descriptor-relative form used while creating a new
// operation. It never follows a worktrees or task ancestor that was swapped
// after the operation directory was opened.
func acquireLockAt(operationDirectory *os.File) (operationLock, error) {
	file, reused, err := claimRetiredLock(operationDirectory)
	if err != nil {
		return operationLock{}, err
	}
	if !reused {
		fd, openErr := unix.Openat(int(operationDirectory.Fd()), ".lock", unix.O_RDONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW, 0o600)
		if openErr != nil {
			if errors.Is(openErr, unix.EEXIST) {
				return operationLock{}, fmt.Errorf("worktree operation is already active or was interrupted")
			}
			return operationLock{}, fmt.Errorf("acquire secure worktree operation lock: %w", openErr)
		}
		file = os.NewFile(uintptr(fd), "wb-worktree-operation-lock")
		if file == nil {
			_ = unix.Close(fd)
			return operationLock{}, fmt.Errorf("wrap secure worktree operation lock")
		}
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		_ = file.Close()
		return operationLock{}, fmt.Errorf("inspect secure worktree operation lock: %w", err)
	}
	lock := operationLock{
		directory: operationDirectory,
		file:      file,
		identity:  managedLockIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino)},
	}
	return lock, nil
}

func claimRetiredLock(directory *os.File) (*os.File, bool, error) {
	if _, err := directory.Seek(0, 0); err != nil {
		return nil, false, fmt.Errorf("rewind secure operation directory for lock reap: %w", err)
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, false, fmt.Errorf("read secure operation directory for lock reap: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".wb-retired-lock-") {
			continue
		}
		fd, openErr := unix.Openat(int(directory.Fd()), entry.Name(), unix.O_RDONLY|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			continue
		}
		retired := os.NewFile(uintptr(fd), "wb-retired-lock-claim")
		if retired == nil {
			_ = unix.Close(fd)
			continue
		}
		identity, identityErr := exclusivelyOwnedLockIdentity(retired)
		if identityErr != nil {
			_ = retired.Close()
			continue
		}
		claimed, moveErr := moveExpectedLockNoReplace(directory, entry.Name(), ".lock", identity)
		_ = retired.Close()
		if errors.Is(moveErr, unix.EEXIST) {
			return nil, false, nil // an active lock won; do not inspect another retirement.
		}
		if moveErr != nil {
			if claimed != nil {
				_ = claimed.Close()
			}
			if errors.Is(moveErr, errDirectoryMoveIdentityChanged) {
				return nil, false, fmt.Errorf("reclaim retired operation lock %s: %w", entry.Name(), moveErr)
			}
			continue // stale/replaced retirement remains untouched.
		}
		return claimed, true, nil
	}
	return nil, false, nil
}

func (lock operationLock) release() {
	if lock.file == nil || lock.directory == nil {
		return
	}
	defer func() { _ = lock.file.Close() }()
	if !lockEntryStillMatches(lock.directory, ".lock", lock.identity) {
		return
	}
	if lock.beforeRelease != nil {
		lock.beforeRelease()
	}
	_ = quarantineLockEntry(lock.directory, lock.identity)
}

func lockEntryStillMatches(directory *os.File, name string, expected managedLockIdentity) bool {
	var stat unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false
	}
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && uint64(stat.Dev) == expected.device && uint64(stat.Ino) == expected.inode
}

func lockIdentity(file *os.File) (managedLockIdentity, error) {
	if file == nil {
		return managedLockIdentity{}, fmt.Errorf("lock descriptor is unavailable")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return managedLockIdentity{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return managedLockIdentity{}, fmt.Errorf("lock descriptor is not a regular file")
	}
	return managedLockIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino)}, nil
}

// exclusivelyOwnedLockIdentity accepts only a retirement WB could have made
// itself. A lock is created with one directory entry and a rename preserves
// that count. In particular, never claim a hard-linked lookalike: even though
// lock acquisition is read-only, leaving an unowned entry untouched keeps the
// namespace and the external file fully outside WB's lifecycle.
func exclusivelyOwnedLockIdentity(file *os.File) (managedLockIdentity, error) {
	if file == nil {
		return managedLockIdentity{}, fmt.Errorf("lock descriptor is unavailable")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return managedLockIdentity{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return managedLockIdentity{}, fmt.Errorf("lock descriptor is not a regular file")
	}
	if stat.Nlink != 1 {
		return managedLockIdentity{}, fmt.Errorf("lock retirement has %d links", stat.Nlink)
	}
	return managedLockIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino)}, nil
}

func moveExpectedLockNoReplace(directory *os.File, fromName, toName string, expected managedLockIdentity) (*os.File, error) {
	if !lockEntryStillMatches(directory, fromName, expected) {
		return nil, fmt.Errorf("%w: operation lock %s changed before move", errDirectoryMoveIdentityChanged, fromName)
	}
	if err := renameNoReplace(int(directory.Fd()), fromName, int(directory.Fd()), toName); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(directory.Fd()), toName, unix.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open moved operation lock %s: %w", toName, err)
	}
	moved := os.NewFile(uintptr(fd), "wb-moved-operation-lock")
	if moved == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap moved operation lock %s", toName)
	}
	actual, identityErr := lockIdentity(moved)
	sourceAbsent, absentErr := noFollowChildAbsent(int(directory.Fd()), fromName)
	if identityErr == nil && actual == expected && absentErr == nil && sourceAbsent {
		return moved, nil
	}
	if absentErr != nil {
		_ = moved.Close()
		return nil, fmt.Errorf("inspect operation lock %s after move: %w", fromName, absentErr)
	}
	if !sourceAbsent {
		return moved, fmt.Errorf("%w: operation lock %s was recreated after no-replace move", errDirectoryMoveIdentityChanged, fromName)
	}
	_ = moved.Close()
	if restoreErr := renameNoReplace(int(directory.Fd()), toName, int(directory.Fd()), fromName); restoreErr != nil {
		return nil, fmt.Errorf("%w: operation lock %s changed before restoration: %v", errDirectoryMoveIdentityChanged, toName, restoreErr)
	}
	return nil, fmt.Errorf("%w: operation lock %s was not the expected file", errDirectoryMoveIdentityChanged, toName)
}

// quarantineLockEntry retires the exact lock inode. It never unlinks `.lock`,
// so a successor created after the final authorization cannot be deleted by a
// previous operation finishing late.
func quarantineLockEntry(directory *os.File, expected managedLockIdentity) error {
	for attempt := 0; attempt < 16; attempt++ {
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return err
		}
		name := fmt.Sprintf(".wb-retired-lock-%x", token[:])
		moved, err := moveExpectedLockNoReplace(directory, ".lock", name, expected)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			if moved != nil {
				_ = moved.Close()
			}
			return err
		}
		return moved.Close()
	}
	return fmt.Errorf("create collision-free retired lock name")
}
