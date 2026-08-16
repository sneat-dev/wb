package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/wbhome"
	"golang.org/x/sys/unix"
)

// SecureRenameGitHelperArgument selects the private child that runs the
// linked-worktree Git mutations used by recycling. It receives retained
// canonical/common, worktrees-root/worktree, and linked Gitfile/admin-dir
// descriptors; it reauthorizes all of them immediately before Git, then
// passes the capability-confined linked Git path explicitly through GIT_DIR
// rather than letting Git rediscover mutable worktree/.git metadata.
const SecureRenameGitHelperArgument = "--wb-internal-rename-git"

const maxLinkedWorktreeGitFileSize = 64 << 10

// linkedWorktreeGitDir retains every part of the linked-checkout metadata
// Git would otherwise rediscover from worktree/.git. That file is mutable
// control-plane input: retaining only the checkout directory lets an attacker
// replace it with a pointer to a sibling administrative directory after the
// worktree has passed admission.
type linkedWorktreeGitDir struct {
	gitFile   *os.File
	adminRoot *os.File
	admin     *os.File
	adminName string
}

func (linked *linkedWorktreeGitDir) close() {
	if linked == nil {
		return
	}
	if linked.admin != nil {
		_ = linked.admin.Close()
	}
	if linked.adminRoot != nil {
		_ = linked.adminRoot.Close()
	}
	if linked.gitFile != nil {
		_ = linked.gitFile.Close()
	}
}

func runSecureRenameGit(ctx context.Context, canonicalDir, worktreesRoot, worktreePath string, gitArgs ...string) error {
	worktree, err := openAbsoluteDirectoryNoFollow(worktreePath, false)
	if err != nil {
		return fmt.Errorf("open managed worktree for rename Git: %w", err)
	}
	defer func() { _ = worktree.Close() }()
	return runSecureRenameGitWithHeldWorktree(ctx, canonicalDir, worktreesRoot, worktreePath, worktree, gitArgs...)
}

// runSecureRenameGitWithHeldWorktree uses the supplied checkout descriptor as
// the work-tree authority. Rename keeps the descriptor returned by renameat
// open through repair and registration verification, so a replacement at the
// destination spelling cannot become Git's work tree between those stages.
func runSecureRenameGitWithHeldWorktree(
	ctx context.Context,
	canonicalDir, worktreesRoot, worktreePath string,
	worktree *os.File,
	gitArgs ...string,
) error {
	canonical, err := openCanonicalRepository(canonicalDir)
	if err != nil {
		return err
	}
	defer canonical.close()
	if err := canonical.authorizeForGit(); err != nil {
		return fmt.Errorf("canonical repository path changed before rename Git operation: %w", err)
	}
	parent, err := openAbsoluteDirectoryNoFollow(worktreesRoot, false)
	if err != nil {
		return fmt.Errorf("open managed worktrees root for rename Git: %w", err)
	}
	defer func() { _ = parent.Close() }()
	if !directoryStillMatches(worktreesRoot, parent) || !directoryStillMatches(worktreePath, worktree) {
		return fmt.Errorf("managed rename path changed before Git operation")
	}
	linked, err := openLinkedWorktreeGitDir(canonical, worktree)
	if err != nil {
		return fmt.Errorf("retain linked worktree Git metadata for rename: %w", err)
	}
	defer linked.close()
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate WB rename Git helper: %w", err)
	}
	gitExecutable, err := trustedGitExecutable()
	if err != nil {
		return err
	}
	arguments := append([]string{SecureRenameGitHelperArgument, canonical.path, worktreePath, worktreesRoot, linked.adminName, gitExecutable}, gitArgs...)
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Env = console.Env()
	command.ExtraFiles = []*os.File{canonical.root, canonical.common, parent, worktree, linked.gitFile, linked.adminRoot, linked.admin}
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("run descriptor-anchored rename Git: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

// RunSecureRenameGitHelper is the child-side counterpart of
// runSecureRenameGit. The checkout becomes the helper's descriptor-anchored
// cwd. Git on Darwin rejects fdescfs directories as GIT_DIR, GIT_COMMON_DIR,
// and GIT_WORK_TREE, so the already-authorized administrative paths are
// protected by the same filesystem capability used for the mutation.
func RunSecureRenameGitHelper(args []string) int {
	if len(args) < 6 || !filepath.IsAbs(args[0]) || !filepath.IsAbs(args[1]) || !filepath.IsAbs(args[2]) || !validLinkedWorktreeAdminName(args[3]) {
		_, _ = fmt.Fprintln(os.Stderr, "wb secure rename helper: invalid arguments")
		return 1
	}
	canonical := os.NewFile(uintptr(3), "wb-rename-canonical")
	common := os.NewFile(uintptr(4), "wb-rename-canonical-git")
	parent := os.NewFile(uintptr(5), "wb-rename-worktrees-root")
	worktree := os.NewFile(uintptr(6), "wb-rename-worktree")
	gitFile := os.NewFile(uintptr(7), "wb-rename-worktree-gitfile")
	adminRoot := os.NewFile(uintptr(8), "wb-rename-linked-admin-root")
	admin := os.NewFile(uintptr(9), "wb-rename-linked-admin")
	if canonical == nil || common == nil || parent == nil || worktree == nil || gitFile == nil || adminRoot == nil || admin == nil {
		for _, file := range []*os.File{canonical, common, parent, worktree, gitFile, adminRoot, admin} {
			if file != nil {
				_ = file.Close()
			}
		}
		_, _ = fmt.Fprintln(os.Stderr, "wb secure rename helper: inherited descriptors are unavailable")
		return 1
	}
	defer func() { _ = canonical.Close() }()
	defer func() { _ = common.Close() }()
	defer func() { _ = parent.Close() }()
	defer func() { _ = worktree.Close() }()
	defer func() { _ = gitFile.Close() }()
	defer func() { _ = adminRoot.Close() }()
	defer func() { _ = admin.Close() }()
	if err := unix.Fchdir(int(canonical.Fd())); err != nil || !directoryStillMatches(args[0], canonical) || !directoryEntryStillMatches(canonical, ".git", common) {
		_, _ = fmt.Fprintln(os.Stderr, "wb secure rename helper: canonical repository changed before Git operation")
		return 1
	}
	if !directoryStillMatches(args[2], parent) || !directoryStillMatches(args[1], worktree) {
		_, _ = fmt.Fprintln(os.Stderr, "wb secure rename helper: managed worktree changed before Git operation")
		return 1
	}
	if !regularFileEntryStillMatches(worktree, ".git", gitFile) ||
		!directoryEntryStillMatches(common, "worktrees", adminRoot) ||
		!directoryEntryStillMatches(adminRoot, args[3], admin) {
		_, _ = fmt.Fprintln(os.Stderr, "wb secure rename helper: linked worktree Git metadata changed before Git operation")
		return 1
	}
	adminName, err := linkedWorktreeGitFileAdminName(&canonicalRepository{path: args[0], root: canonical, common: common}, gitFile)
	if err != nil || adminName != args[3] {
		_, _ = fmt.Fprintln(os.Stderr, "wb secure rename helper: linked worktree Git metadata changed before Git operation")
		return 1
	}
	// Enter the held worktree before Git. GIT_WORK_TREE=. then remains bound to
	// this exact directory even if its public entry is replaced after
	// authorization. Explicit GIT_DIR and GIT_COMMON_DIR below prevent Git from
	// consuming the mutable worktree .git and admin commondir files.
	if err := unix.Fchdir(int(worktree.Fd())); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure rename helper: enter inherited worktree: %v\n", err)
		return 1
	}
	if err := retainDescriptorsAcrossGitExec(common, admin); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure rename helper: retain descriptor paths for Git: %v\n", err)
		return 1
	}
	commonPath := filepath.Join(args[0], ".git")
	adminPath := filepath.Join(commonPath, "worktrees", args[3])
	if !directoryStillMatches(commonPath, common) || !directoryStillMatches(adminPath, admin) || !directoryStillMatches(args[1], worktree) {
		_, _ = fmt.Fprintln(os.Stderr, "wb secure rename helper: Git or worktree directory changed before capability installation")
		return 1
	}
	// The capability permits exactly the retained identities. Darwin Git does
	// not recognize directory descriptors supplied through its repository
	// environment, so the lexical admin/common paths are reauthorized
	// immediately above and frozen for the child by the capability. On Linux
	// the equivalent Landlock capability binds the same paths. The
	// descriptor-anchored worktree plus explicit admin/common paths mean hostile
	// .git and commondir replacements are never consulted.
	capability, err := newGitFilesystemCapability(
		gitFilesystemCapabilityRoot{path: commonPath, directory: common},
		gitFilesystemCapabilityRoot{path: adminPath, directory: admin},
		gitFilesystemCapabilityRoot{path: args[2], directory: parent},
	)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure rename helper: %v\n", err)
		return 1
	}
	return runGitWithFilesystemCapability(capability, args[4], args[5:], gitEnvironmentWithHeldLinkedWorktreeGitDir(adminPath, commonPath))
}

func openLinkedWorktreeGitDir(canonical *canonicalRepository, worktree *os.File) (*linkedWorktreeGitDir, error) {
	if canonical == nil || canonical.common == nil || worktree == nil {
		return nil, fmt.Errorf("linked worktree Git descriptors are unavailable")
	}
	gitFileFD, err := unix.Openat(int(worktree.Fd()), ".git", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open worktree .git file without following links: %w", err)
	}
	gitFile := os.NewFile(uintptr(gitFileFD), "wb-linked-worktree-gitfile")
	if gitFile == nil {
		_ = unix.Close(gitFileFD)
		return nil, fmt.Errorf("wrap linked worktree .git file")
	}
	linked := &linkedWorktreeGitDir{gitFile: gitFile}
	defer func() {
		if linked != nil {
			linked.close()
		}
	}()
	adminName, err := linkedWorktreeGitFileAdminName(canonical, gitFile)
	if err != nil {
		return nil, err
	}
	adminRootFD, err := unix.Openat(int(canonical.common.Fd()), "worktrees", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open canonical linked-worktree metadata root: %w", err)
	}
	adminRoot := os.NewFile(uintptr(adminRootFD), "wb-linked-worktree-admin-root")
	if adminRoot == nil {
		_ = unix.Close(adminRootFD)
		return nil, fmt.Errorf("wrap canonical linked-worktree metadata root")
	}
	linked.adminRoot = adminRoot
	adminFD, err := unix.Openat(int(adminRoot.Fd()), adminName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open linked-worktree Git directory: %w", err)
	}
	admin := os.NewFile(uintptr(adminFD), "wb-linked-worktree-admin")
	if admin == nil {
		_ = unix.Close(adminFD)
		return nil, fmt.Errorf("wrap linked-worktree Git directory")
	}
	linked.admin = admin
	linked.adminName = adminName
	if !regularFileEntryStillMatches(worktree, ".git", gitFile) ||
		!directoryEntryStillMatches(canonical.common, "worktrees", adminRoot) ||
		!directoryEntryStillMatches(adminRoot, adminName, admin) {
		return nil, fmt.Errorf("linked worktree Git metadata changed while retaining descriptors")
	}
	retained := linked
	linked = nil
	return retained, nil
}

func linkedWorktreeGitFileAdminName(canonical *canonicalRepository, gitFile *os.File) (string, error) {
	if canonical == nil || canonical.path == "" || gitFile == nil {
		return "", fmt.Errorf("linked worktree Gitfile descriptors are unavailable")
	}
	info, err := gitFile.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect linked worktree .git file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxLinkedWorktreeGitFileSize {
		return "", fmt.Errorf("linked worktree .git must be a regular file no larger than %d bytes", maxLinkedWorktreeGitFileSize)
	}
	if _, err := gitFile.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind linked worktree .git file: %w", err)
	}
	contents, err := io.ReadAll(io.LimitReader(gitFile, maxLinkedWorktreeGitFileSize+1))
	if err != nil {
		return "", fmt.Errorf("read linked worktree .git file: %w", err)
	}
	if len(contents) > maxLinkedWorktreeGitFileSize {
		return "", fmt.Errorf("linked worktree .git exceeds %d-byte limit", maxLinkedWorktreeGitFileSize)
	}
	line := strings.TrimSuffix(string(contents), "\n")
	line = strings.TrimSuffix(line, "\r")
	gitDir, found := strings.CutPrefix(line, "gitdir: ")
	if !found || gitDir == "" || !filepath.IsAbs(gitDir) || filepath.Clean(gitDir) != gitDir {
		return "", fmt.Errorf("linked worktree .git has an unsafe gitdir")
	}
	adminRoot := filepath.Join(canonical.path, ".git", "worktrees")
	adminName, err := filepath.Rel(adminRoot, gitDir)
	if err != nil || !validLinkedWorktreeAdminName(adminName) {
		return "", fmt.Errorf("linked worktree .git points outside canonical linked-worktree metadata")
	}
	return adminName, nil
}

func validLinkedWorktreeAdminName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !strings.ContainsRune(name, filepath.Separator)
}

func regularFileEntryStillMatches(parent *os.File, name string, expected *os.File) bool {
	if parent == nil || expected == nil {
		return false
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	candidate := os.NewFile(uintptr(fd), "wb-regular-entry-check")
	if candidate == nil {
		_ = unix.Close(fd)
		return false
	}
	defer func() { _ = candidate.Close() }()
	expectedInfo, expectedErr := expected.Stat()
	actualInfo, actualErr := candidate.Stat()
	return expectedErr == nil && actualErr == nil && expectedInfo.Mode().IsRegular() && actualInfo.Mode().IsRegular() && os.SameFile(expectedInfo, actualInfo)
}

func gitEnvironmentWithHeldLinkedWorktreeGitDir(adminDirectory, commonDirectory string) []string {
	environment := gitEnvironmentForLinkedWorktree(commonDirectory)
	return append(environment,
		// The helper's cwd is the exact inherited worktree directory.
		"GIT_WORK_TREE=.",
		"GIT_DIR="+adminDirectory,
		"GIT_COMMON_DIR="+commonDirectory,
	)
}

// retainDescriptorsAcrossGitExec clears close-on-exec for retained authority
// descriptors. The child uses capability-protected lexical admin/common paths
// because Darwin Git rejects fdescfs repository directories, while the held
// descriptors keep the authorized identities alive for the duration of Git.
func retainDescriptorsAcrossGitExec(files ...*os.File) error {
	for _, file := range files {
		if file == nil {
			return fmt.Errorf("missing inherited descriptor")
		}
		flags, err := unix.FcntlInt(file.Fd(), unix.F_GETFD, 0)
		if err != nil {
			return err
		}
		if _, err := unix.FcntlInt(file.Fd(), unix.F_SETFD, flags&^unix.FD_CLOEXEC); err != nil {
			return err
		}
	}
	return nil
}

func gitEnvironmentForLinkedWorktree(temporaryRoot string) []string {
	base := console.Env()
	environment := make([]string, 0, len(base)+1)
	for _, entry := range base {
		key, _, found := strings.Cut(entry, "=")
		if found && (key == "GIT_COMMON_DIR" || key == "GIT_DIR" || key == "GIT_WORK_TREE" || key == "TMPDIR") {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, "TMPDIR="+temporaryRoot)
}

// RenameOptions controls re-homing every worktree below one task to a new
// task name. Recycling is deliberately opt-in and starts from a clean base:
// every untracked and ignored path outside an explicit, safe cache allow-list
// makes the operation refuse. WB never broadly cleans those paths. Callers may
// preserve a cache path (for example "node_modules") when the setup-time
// saving is worth it. This prevents a previous effort's source, credentials,
// or generated artefacts leaking merely because Git happened to ignore them.
//
// The branch itself is never recycled. Every renamed worktree is switched
// onto a freshly created branch based on an up-to-date Base, matching the
// rule that "the branch always goes; the worktree may be recycled."
type RenameOptions struct {
	ProjectsRoot string
	OldTask      string
	NewTask      string
	// Filter narrows which of OldTask's repositories are renamed to those
	// whose owner/repository slug contains this substring — see
	// ListOptions.Filter for the exact semantics. An empty Filter renames
	// every repository under OldTask.
	Filter string
	// Branch is an exact feature branch. When empty, WB derives a branch from
	// BranchPrefix or layered worktrees policy; without a prefix the new task
	// slug itself is the branch name.
	Branch             string
	BranchChosen       bool
	BranchPrefix       string
	BranchPrefixChosen bool
	Base               string
	// DeleteOldBranch is retained for source compatibility; recycle always
	// deletes the old local branch. Force is the explicit discarded-work
	// authorization for an old branch not integrated into origin/Base.
	DeleteOldBranch bool
	// DeleteRemote must be explicit for apply. If origin/<old-branch> exists,
	// it must still equal the preflight head and is retired with an exact
	// force-with-lease after the old Work Log is durable.
	DeleteRemote bool
	Force        bool
	// PreserveCachePaths is the allow-list of ignored/untracked paths that may
	// survive recycle. Empty means no cache survives. Paths are repository
	// relative, safe, and audited in the rename report.
	PreserveCachePaths []string
	WorkLog            WorkLogOptions
	// Apply performs the rename. The default is a dry-run plan, exactly like
	// `wb worktree cleanup`.
	Apply     bool
	ReportDir string
	Now       func() time.Time
	// beforeRenamePreflight is a test-only seam between the initial plan's
	// fetched target-base snapshot and the final locked preflight. It proves a
	// moved target cannot apply stale branch policy or collision checks.
	beforeRenamePreflight func()
	// beforeRenameBind is a test-only failure seam after the old exact branch
	// was deleted and before the fresh claim is bound. It proves rollback can
	// recover a later repository without erasing earlier partial-result evidence.
	beforeRenameBind func(repository string) error
	// afterWorktreeMoveAuthorization is a test-only adversarial seam executed
	// after the retained source identity and both parent descriptors have been
	// authorized, immediately before the descriptor-relative no-replace move.
	afterWorktreeMoveAuthorization func(repository string)
	// beforeWorktreeRepair and beforeWorktreeRegistrationVerify inject failures
	// after the directory has moved. They prove the typed partial-mutation
	// outcome drives deterministic rollback instead of stranding the new path.
	beforeWorktreeRepair             func(repository string) error
	beforeWorktreeRegistrationVerify func(repository string) error
}

// RenameResult records one repository's rename decision and outcome.
type RenameResult struct {
	OldTask             string   `json:"old_task"`
	NewTask             string   `json:"new_task"`
	Repository          string   `json:"repository"`
	CanonicalDir        string   `json:"canonical_dir"`
	OldWorktreeDir      string   `json:"old_worktree_dir"`
	NewWorktreeDir      string   `json:"new_worktree_dir"`
	OldBranch           string   `json:"old_branch"`
	NewBranch           string   `json:"new_branch"`
	Base                string   `json:"base"`
	Eligible            bool     `json:"eligible"`
	Applied             bool     `json:"applied"`
	Repaired            bool     `json:"repaired,omitempty"`
	OldBranchDeleted    bool     `json:"old_branch_deleted"`
	OldRemoteDeleted    bool     `json:"old_remote_deleted"`
	PreservedCachePaths []string `json:"preserved_cache_paths,omitempty"`
	Reason              string   `json:"reason,omitempty"`
}

// RenameOutcome contains the decisions plus the durable audit report written
// before any destructive apply — see Cleanup's identical convention. A
// malformed candidate or an ineligible sibling blocks the whole task: moving
// part of a coordinated task to the new name and leaving the rest behind
// would strand exactly the recycling this verb exists to enable.
type RenameOutcome struct {
	Results     []RenameResult   `json:"results"`
	ReportPath  string           `json:"report_path,omitempty"`
	Diagnostics []ListDiagnostic `json:"diagnostics,omitempty"`
}

type renameReport struct {
	GeneratedAt        time.Time        `json:"generated_at"`
	Phase              string           `json:"phase"`
	OldTask            string           `json:"old_task"`
	NewTask            string           `json:"new_task"`
	Filter             string           `json:"filter,omitempty"`
	Branch             string           `json:"branch,omitempty"`
	Base               string           `json:"base"`
	DeleteOldBranch    bool             `json:"delete_old_branch"`
	DeleteRemote       bool             `json:"delete_remote"`
	Force              bool             `json:"force"`
	PreserveCachePaths []string         `json:"preserve_cache_paths,omitempty"`
	Apply              bool             `json:"apply"`
	Results            []RenameResult   `json:"results"`
	Diagnostics        []ListDiagnostic `json:"diagnostics,omitempty"`
}

// renamePlan bundles the validated local inventory (entry) with the public
// decision/result (result) so apply can use the former without exposing it.
type renamePlan struct {
	entry            ListResult
	refreshed        ListResult
	baseRevision     string
	remoteHead       string
	priorProjection  workLogProjection
	hadProjection    bool
	sealed           bool
	moved            bool
	newBranchCreated bool
	oldBranchDeleted bool
	remoteDeleted    bool
	result           RenameResult
}

// Rename re-homes every worktree under OldTask (optionally narrowed by
// Filter) to NewTask. WB moves the retained checkout identity with a
// descriptor-relative no-replace rename, repairs Git's administrative gitdir
// pointer from that held destination, and verifies the final registration.
func Rename(ctx context.Context, options RenameOptions) (RenameOutcome, error) {
	normalized, err := normalizeRenameOptions(options)
	if err != nil {
		return RenameOutcome{}, err
	}
	resolution, err := wbhome.Resolve(normalized.ProjectsRoot)
	if err != nil {
		return RenameOutcome{}, err
	}
	normalized.WorkLog, err = PrepareWorkLogOptions(normalized.ProjectsRoot, normalized.NewTask, normalized.WorkLog)
	if err != nil {
		return RenameOutcome{}, err
	}
	if normalized.Apply {
		if err := requireGitFilesystemCapability(); err != nil {
			return RenameOutcome{}, err
		}
	}
	now := normalized.Now()
	if normalized.ReportDir == "" && normalized.Apply {
		normalized.ReportDir = DefaultRenameReportDir(resolution.Write.Home, now)
	}

	listed, err := ListWithDiagnostics(ctx, ListOptions{
		ProjectsRoot: normalized.ProjectsRoot,
		Task:         normalized.OldTask,
		Base:         normalized.Base,
		Filter:       normalized.Filter,
		GitHub:       false,
	})
	if err != nil {
		return RenameOutcome{}, err
	}
	if len(listed.Results) == 0 && len(listed.Diagnostics) == 0 {
		return RenameOutcome{}, fmt.Errorf("WB worktree task %q was not found", normalized.OldTask)
	}
	worktreesRoots := make(map[string]bool, 1)
	for _, entry := range listed.Results {
		worktreesRoots[entry.WorktreesRoot] = true
	}
	if len(worktreesRoots) > 1 {
		return RenameOutcome{}, fmt.Errorf("task %q exists under more than one WB worktrees root; rename is not supported for a split task", normalized.OldTask)
	}

	destinationTaskPath := filepath.Join(resolution.Write.WorktreesRoot, normalized.NewTask)
	destinationReason := ""
	if _, statErr := os.Lstat(destinationTaskPath); statErr == nil {
		destinationReason = fmt.Sprintf("destination task already exists: %s", destinationTaskPath)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return RenameOutcome{}, fmt.Errorf("inspect destination task %s: %w", destinationTaskPath, statErr)
	}

	plans := make([]renamePlan, len(listed.Results))
	for index, entry := range listed.Results {
		owner, repository, splitErr := splitRepository(entry.Repository)
		if splitErr != nil {
			return RenameOutcome{}, splitErr
		}
		branch, baseRevision, branchErr := resolveRenameBranch(ctx, normalized, entry)
		if branchErr != nil {
			return RenameOutcome{}, branchErr
		}
		eligible, reason := renameEligibility(entry)
		plans[index] = renamePlan{entry: entry, baseRevision: baseRevision, result: RenameResult{
			OldTask: normalized.OldTask, NewTask: normalized.NewTask,
			Repository: entry.Repository, CanonicalDir: entry.CanonicalDir,
			OldWorktreeDir: entry.WorktreeDir,
			NewWorktreeDir: filepath.Join(destinationTaskPath, owner, repository),
			OldBranch:      entry.Branch, NewBranch: branch, Base: normalized.Base,
			Eligible: eligible, Reason: reason,
		}}
	}
	blockRenameTask(plans, listed.Diagnostics, destinationReason)

	outcome := RenameOutcome{Results: collectRenameResults(plans), Diagnostics: listed.Diagnostics}
	if !normalized.Apply {
		return outcome, nil
	}

	fail := func(renameErr error) (RenameOutcome, error) {
		if normalized.ReportDir != "" {
			path, reportErr := writeRenameReport(normalized, now, "failed", outcome.Results, outcome.Diagnostics)
			if reportErr != nil {
				return outcome, fmt.Errorf("%w; write failed rename report: %v", renameErr, reportErr)
			}
			outcome.ReportPath = path
		}
		return outcome, renameErr
	}
	if normalized.ReportDir != "" {
		if _, reportErr := writeRenameReport(normalized, now, "planned", outcome.Results, outcome.Diagnostics); reportErr != nil {
			return outcome, reportErr
		}
	}

	anyEligible := false
	for _, plan := range plans {
		if plan.result.Eligible {
			anyEligible = true
			break
		}
	}
	if !anyEligible {
		return fail(fmt.Errorf("no repository under task %q is eligible to rename: %s", normalized.OldTask, firstRenameReason(plans)))
	}

	oldWorktreesRoot := plans[0].entry.WorktreesRoot
	oldTaskDirectory, err := openExistingTaskDirectory(oldWorktreesRoot, normalized.OldTask)
	if err != nil {
		return fail(fmt.Errorf("open task %q: %w", normalized.OldTask, err))
	}
	defer func() { _ = oldTaskDirectory.Close() }()
	oldLock, err := acquireLockAt(oldTaskDirectory, normalized.OldTask)
	if err != nil {
		return fail(fmt.Errorf("lock task %q: %w", normalized.OldTask, err))
	}
	defer func() { _ = oldLock.release() }()

	// Preflight every repository while the source task lock is held before
	// creating the destination or terminalizing the first claim. This prevents
	// a second-repository branch/fetch failure from leaving a half-recycled
	// coordinated task.
	if normalized.beforeRenamePreflight != nil {
		normalized.beforeRenamePreflight()
	}
	for index := range plans {
		if !plans[index].result.Eligible {
			continue
		}
		if preflightErr := preflightRename(ctx, normalized, &plans[index]); preflightErr != nil {
			outcome.Results = collectRenameResults(plans)
			return fail(preflightErr)
		}
	}
	if err := reserveOriginalPromptArchive(resolution.Write.Home, normalized.NewTask, normalized.WorkLog); err != nil {
		return fail(fmt.Errorf("reserve new private Work Log prompt: %w", err))
	}

	newWorktreesDirectory, err := openOrCreateWorktreesRoot(resolution.Write.Home)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = newWorktreesDirectory.Close() }()
	newTaskDirectory, newTaskPath, err := createNewTaskDirectory(newWorktreesDirectory, resolution.Write.WorktreesRoot, normalized.NewTask)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = newTaskDirectory.Close() }()
	newLock, err := acquireLockAt(newTaskDirectory, normalized.NewTask)
	if err != nil {
		return fail(fmt.Errorf("lock task %q: %w", normalized.NewTask, err))
	}
	defer func() { _ = newLock.release() }()
	if err := prepareRenameDestinations(newTaskDirectory, newTaskPath, plans); err != nil {
		if cleanupErr := retireEmptyRenameDestination(newWorktreesDirectory, newTaskDirectory, &newLock, normalized.NewTask, plans); cleanupErr != nil {
			err = fmt.Errorf("%w; preserve failed destination for audit: %v", err, cleanupErr)
		}
		return fail(err)
	}

	for index := range plans {
		if !plans[index].result.Eligible {
			continue
		}
		if applyErr := applyRename(ctx, newTaskDirectory, newTaskPath, normalized, &plans[index]); applyErr != nil {
			rollbackErr := rollbackAppliedRenames(ctx, filepath.Dir(plans[index].entry.WorktreesRoot), plans[:index])
			outcome.Results = collectRenameResults(plans)
			if rollbackErr != nil {
				applyErr = fmt.Errorf("%w; coordinated rollback failed: %v", applyErr, rollbackErr)
			} else if cleanupErr := retireEmptyRenameDestination(newWorktreesDirectory, newTaskDirectory, &newLock, normalized.NewTask, plans); cleanupErr != nil {
				applyErr = fmt.Errorf("%w; rollback restored repositories but destination retirement failed: %v", applyErr, cleanupErr)
			}
			return fail(applyErr)
		}
	}
	outcome.Results = collectRenameResults(plans)

	// Keep the now-possibly-empty old task root in place while its descriptor
	// lock is live, exactly like Cleanup does: removing it after releasing the
	// lock would open an ABA window where a concurrent create makes a new,
	// unreachable task directory at the same pathname. A filtered rename that
	// leaves sibling repositories behind needs the root to stay anyway.
	if normalized.ReportDir != "" {
		outcome.ReportPath, err = writeRenameReport(normalized, now, "applied", outcome.Results, outcome.Diagnostics)
		if err != nil {
			return outcome, err
		}
	}
	return outcome, nil
}

// rollbackAppliedRenames reverses every repository already moved by this
// coordinated call, in reverse order. Its durable terminal/new-claim history
// remains append-only, but the live projection is rebound to a recovery claim
// at the original path so the same command can be retried. This is the normal
// error transaction; process crashes remain recoverable from durable records
// but are not yet automatically replayed.
func rollbackAppliedRenames(ctx context.Context, home string, plans []renamePlan) error {
	var rollbackErrors []string
	for index := len(plans) - 1; index >= 0; index-- {
		plan := &plans[index]
		if !plan.result.Applied {
			continue
		}
		if err := rollbackRenamePlan(ctx, home, plan); err != nil {
			rollbackErrors = append(rollbackErrors, plan.entry.Repository+": "+err.Error())
			continue
		}
		resetRenameResultAfterRollback(plan)
	}
	if len(rollbackErrors) > 0 {
		return errors.New(strings.Join(rollbackErrors, "; "))
	}
	return nil
}

// resolveRenameBranch refreshes the same exact target base that an apply
// would use before it reads repository policy from that object. Rename's plan
// is therefore truthful even when a clean canonical checkout is parked on a
// different branch. A fetch updates only remote metadata; it never moves a
// local branch, worktree, Work Log, or rename report.
func resolveRenameBranch(ctx context.Context, options RenameOptions, entry ListResult) (string, string, error) {
	canonical, err := openCanonicalRepository(entry.CanonicalDir)
	if err != nil {
		return "", "", err
	}
	defer canonical.close()
	baseRevision, err := synchronizeCanonical(ctx, canonical, entry.Repository, options.Base)
	if err != nil {
		return "", "", err
	}
	branch, err := deriveBranchName(ctx, branchNamingOptions{
		Task: options.NewTask, ExactBranch: options.Branch, ExactBranchChosen: options.BranchChosen,
		CLIPrefix: options.BranchPrefix, CLIPrefixChosen: options.BranchPrefixChosen,
		Canonical: canonical, BaseRevision: baseRevision, Base: options.Base,
	})
	return branch, baseRevision, err
}

func renameEligibility(entry ListResult) (bool, string) {
	switch {
	case entry.Locked:
		return false, "task is locked by an active or interrupted operation"
	case !entry.Clean:
		return false, "worktree has local changes"
	default:
		return true, ""
	}
}

// blockRenameTask makes the whole rename all-or-nothing. It mirrors Cleanup's
// coordinated per-task blocking (see blockDiagnosedTasks/blockUnsafeTasks),
// simplified because a rename call is always scoped to exactly one task. A
// destination collision is an absolute blocker checked first: it means
// nothing about this task's own repositories, so it is reported verbatim
// rather than wrapped as "coordinated task blocked by ...".
func blockRenameTask(plans []renamePlan, diagnostics []ListDiagnostic, destinationReason string) {
	if destinationReason != "" {
		for index := range plans {
			plans[index].result.Eligible = false
			plans[index].result.Reason = destinationReason
		}
		return
	}
	reason := ""
	switch {
	case len(diagnostics) > 0:
		reason = "malformed candidate " + diagnostics[0].Path + ": " + diagnostics[0].Message
	default:
		for _, plan := range plans {
			if !plan.result.Eligible {
				reason = plan.result.Repository + ": " + plan.result.Reason
				break
			}
		}
	}
	if reason == "" {
		return
	}
	for index := range plans {
		if plans[index].result.Eligible {
			plans[index].result.Eligible = false
			plans[index].result.Reason = "coordinated task blocked by " + reason
		}
	}
}

func collectRenameResults(plans []renamePlan) []RenameResult {
	results := make([]RenameResult, len(plans))
	for index, plan := range plans {
		results[index] = plan.result
	}
	return results
}

func firstRenameReason(plans []renamePlan) string {
	for _, plan := range plans {
		if plan.result.Reason != "" {
			return plan.result.Reason
		}
	}
	return ""
}

// applyRename moves one repository's worktree and switches it onto a freshly
// created branch. newTaskDirectory/newTaskPath are the already-created,
// already-locked destination task directory shared by every repository in
// this Rename call.
func applyRename(ctx context.Context, newTaskDirectory *os.File, newTaskPath string, options RenameOptions, plan *renamePlan) (returnErr error) {
	owner, repository, err := splitRepository(plan.entry.Repository)
	if err != nil {
		return err
	}

	// Recheck safety immediately before mutating under the source task lock.
	refreshed, err := inspectLifecycleWorktree(
		ctx, options.ProjectsRoot, wbhome.Layout{WorktreesRoot: plan.entry.WorktreesRoot},
		// Rename never consults GitHub, so no landing receipt applies here.
		options.OldTask, plan.entry.WorktreeDir, options.Base, "", false, false,
	)
	if err != nil {
		return fmt.Errorf("recheck %s before renaming: %w", plan.entry.Repository, err)
	}
	if !refreshed.Clean {
		return fmt.Errorf("rename safety changed for %s: worktree has local changes", refreshed.Repository)
	}
	if refreshed.HeadSHA != plan.entry.HeadSHA {
		return fmt.Errorf("rename safety changed for %s: branch head moved", refreshed.Repository)
	}
	if err := verifyRecycleState(ctx, plan.entry.WorktreeDir, options.PreserveCachePaths); err != nil {
		return fmt.Errorf("prepare %s for recycle: %w", refreshed.Repository, err)
	}
	if refreshed.HeadSHA != plan.refreshed.HeadSHA {
		return fmt.Errorf("rename safety changed for %s after coordinated preflight", refreshed.Repository)
	}
	home := filepath.Dir(plan.entry.WorktreesRoot)
	priorProjection, projectionErr := readWorkLogProjectionForClaim(home, plan.entry.WorktreeDir)
	plan.priorProjection = priorProjection
	plan.hadProjection = projectionErr == nil
	if projectionErr != nil && !errors.Is(projectionErr, errWorkLogProjectionNotFound) {
		return projectionErr
	}
	defer func() {
		if returnErr == nil || !plan.sealed {
			return
		}
		if rollbackErr := rollbackRenamePlan(ctx, home, plan); rollbackErr != nil {
			returnErr = fmt.Errorf("%w; deterministic recycle rollback failed: %v", returnErr, rollbackErr)
		} else {
			resetRenameResultAfterRollback(plan)
		}
	}()
	if err := sealWorkLogForRecycle(home, plan.entry.WorktreeDir, refreshed.HeadSHA, "recycled"); err != nil {
		return fmt.Errorf("seal previous work log for %s: %w", refreshed.Repository, err)
	}
	plan.sealed = true
	if err := removeWorkLogProjection(plan.entry.WorktreeDir); err != nil {
		return err
	}
	currentRemoteHead, err := remoteBranchHead(ctx, plan.entry.CanonicalDir, plan.entry.Branch)
	if err != nil {
		return fmt.Errorf("recheck remote branch before recycling %s: %w", plan.entry.Repository, err)
	}
	if currentRemoteHead != plan.remoteHead || (currentRemoteHead != "" && currentRemoteHead != refreshed.HeadSHA) {
		return fmt.Errorf("recycle safety changed for %s: remote branch moved from %q to %q", plan.entry.Repository, plan.remoteHead, currentRemoteHead)
	}
	if currentRemoteHead != "" {
		if !options.DeleteRemote {
			return fmt.Errorf("origin/%s still exists; recycle requires explicit --remote retirement", plan.entry.Branch)
		}
		canonical, openErr := openCanonicalRepository(plan.entry.CanonicalDir)
		if openErr != nil {
			return openErr
		}
		deleteErr := runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "",
			"push", "--force-with-lease=refs/heads/"+plan.entry.Branch+":"+refreshed.HeadSHA, "origin", ":refs/heads/"+plan.entry.Branch)
		canonical.close()
		if deleteErr != nil {
			return fmt.Errorf("retire old remote branch %s at %s: %w", plan.entry.Branch, refreshed.HeadSHA, deleteErr)
		}
		plan.remoteDeleted = true
		plan.result.OldRemoteDeleted = true
	}

	if !directoryStillMatches(newTaskPath, newTaskDirectory) {
		return fmt.Errorf("destination task path changed before creating owner directory: %s", newTaskPath)
	}
	ownerFD, err := openOrCreateNoFollowDirectory(int(newTaskDirectory.Fd()), owner)
	if err != nil {
		return fmt.Errorf("create destination owner directory: %w", err)
	}
	ownerDirectory := os.NewFile(uintptr(ownerFD), "wb-rename-owner")
	if ownerDirectory == nil {
		_ = unix.Close(ownerFD)
		return fmt.Errorf("wrap destination owner directory %s", owner)
	}
	defer func() { _ = ownerDirectory.Close() }()
	if err := requireAbsentNoFollowChild(ownerFD, repository); err != nil {
		return err
	}

	moveOutcome, err := moveWorktree(
		ctx,
		plan.entry.CanonicalDir,
		plan.entry.WorktreesRoot,
		plan.entry.WorktreeDir,
		plan.result.NewWorktreeDir,
		worktreeMoveHooks{
			afterAuthorization: func() {
				if options.afterWorktreeMoveAuthorization != nil {
					options.afterWorktreeMoveAuthorization(plan.entry.Repository)
				}
			},
			beforeRepair: func() error {
				if options.beforeWorktreeRepair == nil {
					return nil
				}
				return options.beforeWorktreeRepair(plan.entry.Repository)
			},
			beforeRegistrationVerify: func() error {
				if options.beforeWorktreeRegistrationVerify == nil {
					return nil
				}
				return options.beforeWorktreeRegistrationVerify(plan.entry.Repository)
			},
		},
	)
	plan.moved = moveOutcome.Moved
	plan.result.Repaired = moveOutcome.Repaired
	if err != nil {
		return err
	}
	plan.result.PreservedCachePaths = append([]string(nil), options.PreserveCachePaths...)

	if checkoutErr := runSecureRenameGit(ctx, plan.entry.CanonicalDir, plan.entry.WorktreesRoot, plan.result.NewWorktreeDir,
		"checkout", "-b", plan.result.NewBranch, plan.baseRevision); checkoutErr != nil {
		return fmt.Errorf("check out new branch %s in %s: %w", plan.result.NewBranch, plan.result.NewWorktreeDir, checkoutErr)
	}
	plan.newBranchCreated = true

	if _, guardErr := Guard(ctx, plan.result.NewWorktreeDir, GuardOptions{ProjectsRoot: options.ProjectsRoot, Base: options.Base}); guardErr != nil {
		return fmt.Errorf("renamed worktree %s failed guard: %w", plan.result.NewWorktreeDir, guardErr)
	}
	canonical, openErr := openCanonicalRepository(plan.entry.CanonicalDir)
	if openErr != nil {
		return openErr
	}
	deleted, _, deleteErr := deleteOldBranchIfSafe(
		ctx, canonical, plan.entry.Branch, refreshed.HeadSHA, plan.result.NewBranch, options.Base, options.Force,
	)
	canonical.close()
	if deleteErr != nil {
		return deleteErr
	}
	if !deleted {
		return fmt.Errorf("old branch %q was not deleted; recycle is incomplete", plan.entry.Branch)
	}
	plan.oldBranchDeleted = true
	plan.result.OldBranchDeleted = true
	if options.beforeRenameBind != nil {
		if err := options.beforeRenameBind(plan.entry.Repository); err != nil {
			return fmt.Errorf("bind preflight for %s: %w", plan.entry.Repository, err)
		}
	}
	if _, logErr := recordWorkLog(filepath.Dir(filepath.Dir(newTaskPath)), options.NewTask, CreateResult{
		Repository: plan.entry.Repository, CanonicalDir: plan.entry.CanonicalDir,
		WorktreeDir: plan.result.NewWorktreeDir, Branch: plan.result.NewBranch, Base: options.Base,
		BaseSHA: plan.baseRevision, Action: "recycled",
	}, options.WorkLog); logErr != nil {
		return fmt.Errorf("bind recycled worktree to a new work log: %w", logErr)
	}
	plan.result.Applied = true
	return nil
}

func prepareRenameDestinations(taskDirectory *os.File, taskPath string, plans []renamePlan) error {
	for _, plan := range plans {
		if !plan.result.Eligible {
			continue
		}
		owner, repository, err := splitRepository(plan.entry.Repository)
		if err != nil {
			return err
		}
		if !directoryStillMatches(taskPath, taskDirectory) {
			return fmt.Errorf("destination task path changed before preparing %s", plan.entry.Repository)
		}
		ownerFD, err := openOrCreateNoFollowDirectory(int(taskDirectory.Fd()), owner)
		if err != nil {
			return fmt.Errorf("prepare destination owner %s: %w", owner, err)
		}
		ownerDirectory := os.NewFile(uintptr(ownerFD), "wb-rename-preflight-owner")
		if ownerDirectory == nil {
			_ = unix.Close(ownerFD)
			return fmt.Errorf("wrap destination owner %s", owner)
		}
		if err := requireAbsentNoFollowChild(ownerFD, repository); err != nil {
			_ = ownerDirectory.Close()
			return err
		}
		_ = ownerDirectory.Close()
	}
	return nil
}

// retireEmptyRenameDestination atomically removes a rolled-back destination
// from the active task namespace while its exact lock and directory
// descriptors are still held. The directory is first renamed to an opaque
// retirement name, so even a later cleanup failure cannot strand NewTask or
// block an exact retry. Any unexpected entry makes WB preserve the directory
// and its lock for explicit recovery rather than deleting unknown state.
func retireEmptyRenameDestination(
	worktreesDirectory, taskDirectory *os.File,
	lock *operationLock,
	task string,
	plans []renamePlan,
) error {
	owners := map[string]bool{}
	for _, plan := range plans {
		owner, _, err := splitRepository(plan.entry.Repository)
		if err == nil {
			owners[owner] = true
		}
	}
	for owner := range owners {
		fd, err := unix.Openat(int(taskDirectory.Fd()), owner, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect rollback destination owner %s: %w", owner, err)
		}
		ownerDirectory := os.NewFile(uintptr(fd), "wb-rename-retire-owner")
		if ownerDirectory == nil {
			_ = unix.Close(fd)
			return fmt.Errorf("wrap rollback destination owner %s", owner)
		}
		entries, readErr := ownerDirectory.ReadDir(-1)
		_ = ownerDirectory.Close()
		if readErr != nil {
			return fmt.Errorf("inspect rollback destination owner %s: %w", owner, readErr)
		}
		if len(entries) != 0 {
			return fmt.Errorf("rollback destination owner %s contains unexpected state", owner)
		}
		if err := unix.Unlinkat(int(taskDirectory.Fd()), owner, unix.AT_REMOVEDIR); err != nil {
			return fmt.Errorf("remove empty rollback destination owner %s: %w", owner, err)
		}
	}
	if lock == nil || lock.file == nil || !lockEntryStillMatches(taskDirectory, ".lock", lock.identity) {
		return fmt.Errorf("rollback destination lock changed before retirement")
	}
	if !directoryEntryStillMatches(worktreesDirectory, task, taskDirectory) {
		return fmt.Errorf("rollback destination task changed before retirement")
	}
	retired, retiredName, err := quarantineDirectoryEntryNamed(worktreesDirectory, task, taskDirectory, ".wb-retired-task-")
	if err != nil {
		return fmt.Errorf("retire rollback destination task: %w", err)
	}
	defer func() { _ = retired.Close() }()
	_ = lock.release()
	lock.file = nil
	lock.directory = nil
	if _, err := retired.Seek(0, 0); err != nil {
		return fmt.Errorf("rewind retired destination: %w", err)
	}
	entries, err := retired.ReadDir(-1)
	if err != nil {
		return fmt.Errorf("inspect retired destination: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".wb-retired-lock-") || !lockEntryStillMatches(retired, entry.Name(), lock.identity) {
			return fmt.Errorf("retired destination contains unexpected state %q", entry.Name())
		}
		if err := unix.Unlinkat(int(retired.Fd()), entry.Name(), 0); err != nil {
			return fmt.Errorf("remove retired destination lock: %w", err)
		}
	}
	if !directoryEntryStillMatches(worktreesDirectory, retiredName, retired) {
		return fmt.Errorf("retired destination identity changed before removal")
	}
	if err := unix.Unlinkat(int(worktreesDirectory.Fd()), retiredName, unix.AT_REMOVEDIR); err != nil {
		return fmt.Errorf("remove retired destination: %w", err)
	}
	return worktreesDirectory.Sync()
}

func rollbackRenamePlan(ctx context.Context, home string, plan *renamePlan) error {
	currentPath := plan.entry.WorktreeDir
	if plan.moved {
		currentPath = plan.result.NewWorktreeDir
	}
	// If a fresh claim was partially or fully bound, archive it before removing
	// its projection. Failure here stops rollback rather than losing evidence.
	if projection, err := readWorkLogProjection(currentPath); err == nil && projection.ClaimID != plan.priorProjection.ClaimID {
		head, headErr := git(ctx, currentPath, "rev-parse", "HEAD")
		if headErr != nil {
			return headErr
		}
		if err := sealWorkLogForRecycle(home, currentPath, head, "recycle_failed"); err != nil {
			return err
		}
		if err := removeWorkLogProjection(currentPath); err != nil {
			return err
		}
	}
	if plan.oldBranchDeleted {
		canonical, openErr := openCanonicalRepository(plan.entry.CanonicalDir)
		if openErr != nil {
			return openErr
		}
		_, err := gitCanonical(ctx, canonical, "update-ref", "refs/heads/"+plan.entry.Branch, plan.entry.HeadSHA, "")
		canonical.close()
		if err != nil {
			return fmt.Errorf("restore old branch %s: %w", plan.entry.Branch, err)
		}
	}
	if plan.remoteDeleted {
		remoteHead, err := remoteBranchHead(ctx, plan.entry.CanonicalDir, plan.entry.Branch)
		if err != nil {
			return fmt.Errorf("inspect remote before recycle rollback: %w", err)
		}
		if remoteHead != "" {
			return fmt.Errorf("refuse to restore remote branch %s: another actor created it at %s", plan.entry.Branch, remoteHead)
		}
		canonical, err := openCanonicalRepository(plan.entry.CanonicalDir)
		if err != nil {
			return err
		}
		restoreErr := runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "",
			"push", "--force-with-lease=refs/heads/"+plan.entry.Branch+":", "origin",
			"refs/heads/"+plan.entry.Branch+":refs/heads/"+plan.entry.Branch)
		canonical.close()
		if restoreErr != nil {
			return fmt.Errorf("restore retired remote branch %s at %s: %w", plan.entry.Branch, plan.entry.HeadSHA, restoreErr)
		}
		restoredHead, err := remoteBranchHead(ctx, plan.entry.CanonicalDir, plan.entry.Branch)
		if err != nil {
			return err
		}
		if restoredHead != plan.entry.HeadSHA {
			return fmt.Errorf("restored remote branch %s is %s, expected %s", plan.entry.Branch, restoredHead, plan.entry.HeadSHA)
		}
	}
	if plan.moved {
		branch, err := git(ctx, currentPath, "branch", "--show-current")
		if err != nil {
			return err
		}
		if branch != plan.entry.Branch {
			if err := runSecureRenameGit(ctx, plan.entry.CanonicalDir, plan.entry.WorktreesRoot, currentPath, "checkout", plan.entry.Branch); err != nil {
				return fmt.Errorf("restore old branch checkout: %w", err)
			}
		}
		if _, err := moveWorktree(ctx, plan.entry.CanonicalDir, plan.entry.WorktreesRoot, plan.result.NewWorktreeDir, plan.entry.WorktreeDir, worktreeMoveHooks{}); err != nil {
			return fmt.Errorf("move failed recycle back to source: %w", err)
		}
	}
	if exists, err := localBranchExists(ctx, plan.entry.CanonicalDir, plan.result.NewBranch); err != nil {
		return err
	} else if exists {
		canonical, openErr := openCanonicalRepository(plan.entry.CanonicalDir)
		if openErr != nil {
			return openErr
		}
		newHead, err := gitCanonical(ctx, canonical, "rev-parse", "refs/heads/"+plan.result.NewBranch)
		if err == nil && newHead == plan.baseRevision {
			_, err = gitCanonical(ctx, canonical, "update-ref", "-d", "refs/heads/"+plan.result.NewBranch, plan.baseRevision)
		}
		canonical.close()
		if err != nil {
			return err
		}
		if newHead != plan.baseRevision {
			return fmt.Errorf("refuse to remove failed recycle branch %s: expected %s, found %s", plan.result.NewBranch, plan.baseRevision, newHead)
		}
	}
	if plan.hadProjection {
		if err := recoverFailedRecycleClaim(home, plan.entry.WorktreeDir, plan.entry.HeadSHA, plan.priorProjection); err != nil {
			return fmt.Errorf("bind recovery claim after failed recycle: %w", err)
		}
	}
	return nil
}

func resetRenameResultAfterRollback(plan *renamePlan) {
	plan.result.Applied = false
	plan.result.OldBranchDeleted = false
	plan.result.OldRemoteDeleted = false
	plan.result.Repaired = false
	plan.result.PreservedCachePaths = nil
	plan.sealed = false
	plan.moved = false
	plan.newBranchCreated = false
	plan.oldBranchDeleted = false
	plan.remoteDeleted = false
}

func preflightRename(ctx context.Context, options RenameOptions, plan *renamePlan) error {
	refreshed, err := inspectLifecycleWorktree(ctx, options.ProjectsRoot,
		wbhome.Layout{WorktreesRoot: plan.entry.WorktreesRoot}, options.OldTask,
		plan.entry.WorktreeDir, options.Base, "", false, false)
	if err != nil {
		return fmt.Errorf("preflight %s: %w", plan.entry.Repository, err)
	}
	if !refreshed.Clean || refreshed.HeadSHA != plan.entry.HeadSHA {
		return fmt.Errorf("preflight %s: worktree/head changed", plan.entry.Repository)
	}
	if err := verifyRecycleState(ctx, refreshed.WorktreeDir, options.PreserveCachePaths); err != nil {
		return fmt.Errorf("preflight %s: %w", plan.entry.Repository, err)
	}
	canonical, err := openCanonicalRepository(plan.entry.CanonicalDir)
	if err != nil {
		return err
	}
	defer canonical.close()
	baseRevision, err := synchronizeCanonical(ctx, canonical, plan.entry.Repository, options.Base)
	if err != nil {
		return fmt.Errorf("fetch base before recycling %s: %w", plan.entry.Repository, err)
	}
	if baseRevision != plan.baseRevision {
		return fmt.Errorf("origin/%s advanced for %s during rename preflight from %s to %s; rerun so every branch policy and collision check is pinned to one exact base", options.Base, plan.entry.Repository, plan.baseRevision, baseRevision)
	}
	branch, branchErr := deriveBranchName(ctx, branchNamingOptions{
		Task: options.NewTask, ExactBranch: options.Branch, ExactBranchChosen: options.BranchChosen,
		CLIPrefix: options.BranchPrefix, CLIPrefixChosen: options.BranchPrefixChosen,
		Canonical: canonical, BaseRevision: baseRevision, Base: options.Base,
	})
	if branchErr != nil {
		return branchErr
	}
	if branch != plan.result.NewBranch {
		return fmt.Errorf("branch policy changed for %s during rename preflight from %q to %q; rerun so the planned report is exact", plan.entry.Repository, plan.result.NewBranch, branch)
	}
	if exists, existsErr := localBranchExistsCanonical(ctx, canonical, plan.result.NewBranch); existsErr != nil {
		return existsErr
	} else if exists {
		return fmt.Errorf("branch %q already exists in %s; choose another --branch", plan.result.NewBranch, plan.entry.Repository)
	}
	merged, err := isAncestor(ctx, plan.entry.CanonicalDir, refreshed.HeadSHA, "origin/"+options.Base)
	if err != nil {
		return err
	}
	if !merged && !options.Force {
		return fmt.Errorf("branch %q is not integrated into origin/%s; use `wb worktree abort --disposition handoff|not_landed`, or explicitly authorize discard before recycle", refreshed.Branch, options.Base)
	}
	remoteHead, err := remoteBranchHead(ctx, plan.entry.CanonicalDir, refreshed.Branch)
	if err != nil {
		return fmt.Errorf("inspect old remote branch before recycling %s: %w", plan.entry.Repository, err)
	}
	if remoteHead != "" && remoteHead != refreshed.HeadSHA {
		return fmt.Errorf("refuse to recycle %s: origin/%s is %s, expected exact old head %s", refreshed.Repository, refreshed.Branch, remoteHead, refreshed.HeadSHA)
	}
	if remoteHead != "" && !options.DeleteRemote {
		return fmt.Errorf("origin/%s remains cleanup backlog; rerun recycle with --remote", refreshed.Branch)
	}
	if err := preflightWorkLogSeal(filepath.Dir(plan.entry.WorktreesRoot), refreshed.WorktreeDir, refreshed.HeadSHA); err != nil {
		return fmt.Errorf("preflight Work Log for %s: %w", plan.entry.Repository, err)
	}
	plan.refreshed = refreshed
	plan.baseRevision = baseRevision
	plan.remoteHead = remoteHead
	return nil
}

// verifyRecycleState proves that only explicitly allow-listed cache paths are
// ignored or untracked. It deliberately refuses rather than deleting unknown
// files: recycle must never turn a stale agent's local evidence into silent
// data loss. The caller can archive or remove that state, then retry.
func verifyRecycleState(ctx context.Context, worktree string, preserve []string) error {
	args := []string{"clean", "-ndx"}
	// The journal is reset after the new branch has been created; it is WB
	// control-plane metadata, not a cache inherited by the new effort. Without
	// these exclusions every managed worktree looks like unapproved state to
	// recycle, because every managed worktree now carries a journal.
	args = append(args,
		"-e", journalRootDirectory+"/"+journalLocalDirectory,
		"-e", workLogProjectionDirectory,
		"-e", legacyWorkLogProjectionName,
	)
	for _, path := range preserve {
		args = append(args, "-e", path)
	}
	remaining, err := git(ctx, worktree, args...)
	if err != nil {
		return err
	}
	if remaining != "" {
		return fmt.Errorf("unapproved untracked or ignored state would leak into the new effort: %s; archive/remove it or explicitly preserve a safe cache path", remaining)
	}
	return nil
}

type worktreeMoveOutcome struct {
	Moved    bool
	Repaired bool
}

type worktreeMoveHooks struct {
	afterAuthorization       func()
	beforeRepair             func() error
	beforeRegistrationVerify func() error
}

// moveWorktree binds every stage to one retained checkout identity. Git's
// `worktree move` accepts mutable path arguments after WB authorizes them, so
// WB performs the namespace mutation itself with renameat/no-replace, keeps
// the moved descriptor open, repairs Git from that held destination using
// `worktree repair .`, and verifies both registration and path identity.
//
// The outcome records a successful directory relocation even when repair or
// verification fails. Callers must use it before handling err so rollback can
// restore a partial mutation from whichever endpoint now owns the checkout.
func moveWorktree(
	ctx context.Context,
	canonicalDir, worktreesRoot, oldPath, newPath string,
	hooks worktreeMoveHooks,
) (outcome worktreeMoveOutcome, err error) {
	moved, moveErr := moveRenameDirectory(oldPath, newPath, hooks.afterAuthorization)
	if moved != nil {
		defer func() { _ = moved.Close() }()
		outcome.Moved = true
	}
	if moveErr != nil {
		return outcome, fmt.Errorf("descriptor-relative worktree move: %w", moveErr)
	}
	if moved == nil {
		return outcome, fmt.Errorf("descriptor-relative worktree move returned no retained destination")
	}
	if hooks.beforeRepair != nil {
		if err := hooks.beforeRepair(); err != nil {
			return outcome, fmt.Errorf("before worktree metadata repair: %w", err)
		}
	}
	if repairErr := runSecureRenameGitWithHeldWorktree(
		ctx, canonicalDir, worktreesRoot, newPath, moved,
		"worktree", "repair", ".",
	); repairErr != nil {
		return outcome, fmt.Errorf("repair Git registration after descriptor-relative worktree move: %w", repairErr)
	}
	outcome.Repaired = true
	if hooks.beforeRegistrationVerify != nil {
		if err := hooks.beforeRegistrationVerify(); err != nil {
			return outcome, fmt.Errorf("before worktree registration verification: %w", err)
		}
	}
	if verifyErr := verifyWorktreeRegistered(ctx, canonicalDir, oldPath, newPath, moved); verifyErr != nil {
		return outcome, verifyErr
	}
	return outcome, nil
}

// moveRenameDirectory is descriptor-relative: a checked pathname must refer
// to the retained old checkout before authorization, the destination is never
// overwritten, and the returned descriptor remains the authority after the
// namespace move. A post-authorization substitution is detected by the
// helper's post-move identity proof and restored when doing so cannot clobber
// another actor's entry.
func moveRenameDirectory(oldPath, newPath string, afterAuthorization func()) (*os.File, error) {
	oldParent, err := openAbsoluteDirectoryNoFollow(filepath.Dir(oldPath), false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = oldParent.Close() }()
	newParent, err := openAbsoluteDirectoryNoFollow(filepath.Dir(newPath), false)
	if err != nil {
		return nil, err
	}
	defer func() { _ = newParent.Close() }()
	oldDirectory, err := openAbsoluteDirectoryNoFollow(oldPath, false)
	if err != nil {
		return nil, err
	}
	returnOldDirectory := false
	defer func() {
		if !returnOldDirectory {
			_ = oldDirectory.Close()
		}
	}()
	oldParentPath := filepath.Dir(oldPath)
	newParentPath := filepath.Dir(newPath)
	if !directoryStillMatches(oldParentPath, oldParent) || !directoryStillMatches(newParentPath, newParent) {
		return nil, fmt.Errorf("rename parent changed before descriptor-relative move")
	}
	moved, err := moveExpectedDirectoryNoReplaceAuthorized(oldParent, filepath.Base(oldPath), newParent, filepath.Base(newPath), oldDirectory, func() error {
		if !directoryStillMatches(oldParentPath, oldParent) || !directoryStillMatches(newParentPath, newParent) ||
			!directoryEntryStillMatches(oldParent, filepath.Base(oldPath), oldDirectory) {
			return fmt.Errorf("rename path changed before descriptor-relative move")
		}
		if afterAuthorization != nil {
			afterAuthorization()
		}
		return nil
	})
	if moved == nil && err != nil && directoryEntryStillMatches(newParent, filepath.Base(newPath), oldDirectory) {
		// The namespace move succeeded but the shared helper could not wrap its
		// destination descriptor. Preserve the original retained descriptor so
		// the caller still records Moved and rolls back from the correct endpoint.
		returnOldDirectory = true
		return oldDirectory, err
	}
	return moved, err
}

// verifyWorktreeRegistered proves the move actually took, straight from
// Git's own bookkeeping, rather than trusting a zero exit code alone.
func verifyWorktreeRegistered(ctx context.Context, canonicalDir, oldPath, newPath string, expected *os.File) error {
	if expected == nil || !directoryStillMatches(newPath, expected) {
		return fmt.Errorf("moved worktree identity changed before registration verification: %s", newPath)
	}
	canonical, openErr := openCanonicalRepository(canonicalDir)
	if openErr != nil {
		return openErr
	}
	output, err := gitCanonical(ctx, canonical, "worktree", "list", "--porcelain")
	canonical.close()
	if err != nil {
		return fmt.Errorf("verify worktree registration: %w", err)
	}
	found := false
	for _, line := range strings.Split(output, "\n") {
		path, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		switch filepath.Clean(path) {
		case filepath.Clean(newPath):
			found = true
		case filepath.Clean(oldPath):
			return fmt.Errorf("worktree registration still lists the old path %s after moving to %s", oldPath, newPath)
		}
	}
	if !found {
		return fmt.Errorf("worktree registration does not list %s after moving %s", newPath, oldPath)
	}
	if !directoryStillMatches(newPath, expected) {
		return fmt.Errorf("moved worktree identity changed during registration verification: %s", newPath)
	}
	return nil
}

// deleteOldBranchIfSafe removes oldBranch once it is safe to lose: merged
// into origin/base, or Force is set. It never touches newBranch even if the
// caller asked for the same name, and it deletes by exact expected SHA (like
// Cleanup's own branch deletion) so a branch that moved after the safety
// check is refused rather than silently discarded.
func deleteOldBranchIfSafe(ctx context.Context, canonical *canonicalRepository, oldBranch, oldHead, newBranch, base string, force bool) (deleted bool, reason string, err error) {
	if oldBranch == "" || oldBranch == newBranch {
		return false, fmt.Sprintf("old branch %q is unchanged; nothing to delete", oldBranch), nil
	}
	merged, err := isAncestor(ctx, canonical.path, oldHead, "origin/"+base)
	if err != nil {
		return false, "", err
	}
	if !merged && !force {
		return false, fmt.Sprintf("branch %q is not merged into origin/%s; rerun with --force to delete it anyway", oldBranch, base), nil
	}
	if _, updateErr := gitCanonical(ctx, canonical, "update-ref", "-d", "refs/heads/"+oldBranch, oldHead); updateErr != nil {
		return false, "", fmt.Errorf("delete old branch %s at %s: %w", oldBranch, oldHead, updateErr)
	}
	return true, "", nil
}

func normalizeRenameOptions(options RenameOptions) (RenameOptions, error) {
	projectsRoot, oldTask, base, filter, err := normalizeListOptions(ListOptions{
		ProjectsRoot: options.ProjectsRoot, Task: options.OldTask, Base: options.Base, Filter: options.Filter,
	})
	if err != nil {
		return RenameOptions{}, err
	}
	if oldTask == "" {
		return RenameOptions{}, fmt.Errorf("old task is required")
	}
	options.ProjectsRoot = projectsRoot
	options.OldTask = oldTask
	options.Base = base
	options.Filter = filter

	options.NewTask = strings.TrimSpace(options.NewTask)
	if !validSafeSegment(options.NewTask) {
		return RenameOptions{}, fmt.Errorf("task %q must be one safe path segment", options.NewTask)
	}
	if options.NewTask == options.OldTask {
		return RenameOptions{}, fmt.Errorf("new task %q must differ from old task %q", options.NewTask, options.OldTask)
	}
	branchProvided := options.Branch != ""
	prefixProvided := options.BranchPrefix != ""
	options.Branch = strings.TrimSpace(options.Branch)
	// Existing direct Go callers pass nonempty branch fields without a separate
	// presence bit. Retain that contract; the booleans only carry an explicit
	// empty CLI value that must not silently fall back to policy.
	options.BranchChosen = options.BranchChosen || branchProvided
	options.BranchPrefixChosen = options.BranchPrefixChosen || prefixProvided
	if options.BranchPrefixChosen && strings.TrimSpace(options.BranchPrefix) != options.BranchPrefix {
		return RenameOptions{}, fmt.Errorf("branch prefix must not have surrounding whitespace")
	}
	if options.BranchChosen && options.BranchPrefixChosen {
		return RenameOptions{}, fmt.Errorf("--branch and --branch-prefix cannot be used together")
	}
	if options.BranchChosen && options.Branch == "" {
		return RenameOptions{}, fmt.Errorf("--branch must not be empty when explicitly provided")
	}
	ctx := context.Background()
	if options.Branch != "" && !validBranch(ctx, options.Branch) {
		return RenameOptions{}, fmt.Errorf("invalid feature branch %q", options.Branch)
	}
	if options.Branch != "" && options.Branch == options.Base {
		return RenameOptions{}, fmt.Errorf("feature branch must differ from base branch %q", options.Base)
	}
	cachePaths, err := normalizePreserveCachePaths(options.PreserveCachePaths)
	if err != nil {
		return RenameOptions{}, err
	}
	options.PreserveCachePaths = cachePaths
	options.DeleteOldBranch = true
	if options.Apply && !options.DeleteRemote {
		return RenameOptions{}, fmt.Errorf("recycle apply requires --remote so an old source branch cannot remain cleanup backlog")
	}
	if strings.TrimSpace(options.WorkLog.RunID) == "" {
		options.WorkLog.RunID = "wb-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	}
	if err := PreflightWorkLogOptions(options.NewTask, options.WorkLog); err != nil {
		return RenameOptions{}, err
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.ReportDir != "" {
		options.ReportDir, err = filepath.Abs(options.ReportDir)
		if err != nil {
			return RenameOptions{}, fmt.Errorf("resolve rename report directory: %w", err)
		}
		options.ReportDir = filepath.Clean(options.ReportDir)
	}
	return options, nil
}

func normalizePreserveCachePaths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		path = filepath.ToSlash(strings.TrimSpace(path))
		if path == "" || strings.HasPrefix(path, "/") || strings.Contains(path, "//") {
			return nil, fmt.Errorf("preserve cache path %q must be a non-empty repository-relative path", path)
		}
		parts := strings.Split(path, "/")
		for _, part := range parts {
			if !validSafeSegment(part) || part == "." || part == ".." {
				return nil, fmt.Errorf("preserve cache path %q contains an unsafe segment", path)
			}
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

// DefaultRenameReportDir returns the durable audit directory for one apply,
// below the already-resolved WB write home — see DefaultCleanupReportDir.
func DefaultRenameReportDir(home string, now time.Time) string {
	return filepath.Join(
		home,
		"reports",
		"worktree-rename",
		now.UTC().Format("20060102T150405.000000000Z"),
	)
}

// openExistingTaskDirectory opens an already-registered task directory
// without following a symlink at either segment, mirroring
// acquireCleanupTask's identical two-step open.
func openExistingTaskDirectory(worktreesRoot, task string) (*os.File, error) {
	root, err := openAbsoluteDirectoryNoFollow(worktreesRoot, false)
	if err != nil {
		return nil, fmt.Errorf("open worktrees root %s: %w", worktreesRoot, err)
	}
	defer func() { _ = root.Close() }()
	taskFD, err := unix.Openat(int(root.Fd()), task, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open task %s without following links: %w", task, err)
	}
	directory := os.NewFile(uintptr(taskFD), "wb-rename-old-task")
	if directory == nil {
		_ = unix.Close(taskFD)
		return nil, fmt.Errorf("wrap task directory %s", task)
	}
	return directory, nil
}

// openOrCreateWorktreesRoot opens (creating if absent) <home>/worktrees one
// segment at a time, mirroring the equivalent half of prepareOperationRoot.
func openOrCreateWorktreesRoot(home string) (*os.File, error) {
	homeDirectory, err := openAbsoluteDirectoryNoFollow(home, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = homeDirectory.Close() }()
	if err := wbhome.SeedReadme(home); err != nil {
		return nil, err
	}
	worktreesFD, err := openOrCreateNoFollowDirectory(int(homeDirectory.Fd()), "worktrees")
	if err != nil {
		return nil, err
	}
	worktreesDirectory := os.NewFile(uintptr(worktreesFD), "wb-rename-worktrees-root")
	if worktreesDirectory == nil {
		_ = unix.Close(worktreesFD)
		return nil, fmt.Errorf("wrap secure worktrees root")
	}
	return worktreesDirectory, nil
}

// createNewTaskDirectory creates the destination task directory atomically:
// unix.Mkdirat fails with EEXIST rather than silently reusing an existing
// directory, which is what turns "the destination task already exists" into
// a hard, race-free refusal instead of a plan-time-only check.
func createNewTaskDirectory(worktreesDirectory *os.File, worktreesRootPath, task string) (*os.File, string, error) {
	path := filepath.Join(worktreesRootPath, task)
	if !directoryStillMatches(worktreesRootPath, worktreesDirectory) {
		return nil, "", fmt.Errorf("secure worktrees root path changed before creating task %s", task)
	}
	if err := unix.Mkdirat(int(worktreesDirectory.Fd()), task, 0o755); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return nil, "", fmt.Errorf("destination task already exists: %s", path)
		}
		return nil, "", fmt.Errorf("create task directory %s: %w", path, err)
	}
	fd, err := unix.Openat(int(worktreesDirectory.Fd()), task, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, "", fmt.Errorf("open created task directory %s: %w", path, err)
	}
	directory := os.NewFile(uintptr(fd), "wb-rename-new-task")
	if directory == nil {
		_ = unix.Close(fd)
		return nil, "", fmt.Errorf("wrap created task directory %s", path)
	}
	return directory, path, nil
}

func writeRenameReport(
	options RenameOptions,
	generatedAt time.Time,
	phase string,
	results []RenameResult,
	diagnostics []ListDiagnostic,
) (string, error) {
	if err := os.MkdirAll(options.ReportDir, 0o755); err != nil {
		return "", fmt.Errorf("create rename report directory: %w", err)
	}
	report := renameReport{
		GeneratedAt: generatedAt, Phase: phase, OldTask: options.OldTask, NewTask: options.NewTask,
		Filter: options.Filter, Branch: options.Branch, Base: options.Base,
		DeleteOldBranch: options.DeleteOldBranch, DeleteRemote: options.DeleteRemote,
		Force: options.Force, PreserveCachePaths: options.PreserveCachePaths, Apply: options.Apply,
		Results: results, Diagnostics: diagnostics,
	}
	content, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode rename report: %w", err)
	}
	content = append(content, '\n')
	path := filepath.Join(options.ReportDir, "rename.json")
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, content, 0o644); err != nil {
		return "", fmt.Errorf("write rename report: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return "", fmt.Errorf("activate rename report: %w", err)
	}
	return path, nil
}
