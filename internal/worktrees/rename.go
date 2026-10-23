package worktrees

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/checkoutmarker"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/unixcompat"
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
	_, err := runSecureRenameGitBytesWithHeldWorktree(ctx, canonicalDir, worktreesRoot, worktreePath, worktree, gitArgs...)
	return err
}

// runSecureRenameGitBytesWithHeldWorktree is the byte-preserving query form
// of the same descriptor-authorized linked-worktree helper used by rename.
// Stdout stays separate from diagnostics so callers can authenticate exact
// Git bytes or inspect a held worktree without reopening its public path.
func runSecureRenameGitBytesWithHeldWorktree(
	ctx context.Context,
	canonicalDir, worktreesRoot, worktreePath string,
	worktree *os.File,
	gitArgs ...string,
) ([]byte, error) {
	return runSecureRenameGitWithObservation(ctx, canonicalDir, worktreesRoot, worktreePath, worktree, nil, os.Executable, trustedGitExecutable, gitArgs...)
}

func runSecureRenameGitWithObservation(ctx context.Context, canonicalDir, worktreesRoot, worktreePath string, worktree *os.File, afterOpen func(), self, gitPath func() (string, error), gitArgs ...string) ([]byte, error) {
	canonical, err := openCanonicalRepository(canonicalDir)
	if err != nil {
		return nil, err
	}
	defer canonical.close()
	if afterOpen != nil {
		afterOpen()
	}
	if err := canonical.authorizeForGit(); err != nil {
		return nil, fmt.Errorf("canonical repository path changed before rename Git operation: %w", err)
	}
	parent, err := openAbsoluteDirectoryNoFollow(worktreesRoot, false)
	if err != nil {
		return nil, fmt.Errorf("open managed worktrees root for rename Git: %w", err)
	}
	defer func() { _ = parent.Close() }()
	if !directoryStillMatches(worktreesRoot, parent) || !directoryStillMatches(worktreePath, worktree) {
		return nil, fmt.Errorf("managed rename path changed before Git operation")
	}
	linked, err := openLinkedWorktreeGitDir(canonical, worktree)
	if err != nil {
		return nil, fmt.Errorf("retain linked worktree Git metadata for rename: %w", err)
	}
	defer linked.close()
	executable, err := secureHelperExecutable(self, "rename Git")
	if err != nil {
		return nil, err
	}
	gitExecutable, err := gitPath()
	if err != nil {
		return nil, err
	}
	arguments := append([]string{SecureRenameGitHelperArgument, canonical.path, worktreePath, worktreesRoot, linked.adminName, gitExecutable}, gitArgs...)
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Env = console.Env()
	command.ExtraFiles = []*os.File{canonical.root, canonical.common, parent, worktree, linked.gitFile, linked.adminRoot, linked.admin}
	output, err := command.Output()
	if err != nil {
		detail := ""
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			detail = strings.TrimSpace(string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("run descriptor-anchored rename Git: %w: %s", err, detail)
	}
	return output, nil
}

// RunSecureRenameGitHelper is the child-side counterpart of
// runSecureRenameGit. The checkout becomes the helper's descriptor-anchored
// cwd. Git on Darwin rejects fdescfs directories as GIT_DIR, GIT_COMMON_DIR,
// and GIT_WORK_TREE, so the already-authorized administrative paths are
// protected by the same filesystem capability used for the mutation.
func RunSecureRenameGitHelper(args []string) int {
	return runSecureRenameGitHelperWithOps(args, defaultSecureHelperOps())
}

func runSecureRenameGitHelperWithOps(args []string, ops secureHelperOps) int {
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
	defer func() { _ = canonical.Close() }()
	defer func() { _ = common.Close() }()
	defer func() { _ = parent.Close() }()
	defer func() { _ = worktree.Close() }()
	defer func() { _ = gitFile.Close() }()
	defer func() { _ = adminRoot.Close() }()
	defer func() { _ = admin.Close() }()
	if err := ops.chdir(int(canonical.Fd())); err != nil || !directoryStillMatches(args[0], canonical) || !directoryEntryStillMatches(canonical, ".git", common) {
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
	if err := ops.chdir(int(worktree.Fd())); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "wb secure rename helper: enter inherited worktree: %v\n", err)
		return 1
	}
	if err := ops.retain(common, admin); err != nil {
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
	writeRoots := []gitFilesystemCapabilityRoot{
		gitFilesystemCapabilityRoot{path: commonPath, directory: common},
		gitFilesystemCapabilityRoot{path: adminPath, directory: admin},
		gitFilesystemCapabilityRoot{path: args[2], directory: parent},
	}
	// Retirement also uses this descriptor-bound linked-worktree helper for
	// ordinary commits. Its hooks need the same narrowly held runtime roots as
	// cleanup's Git helper; otherwise a healthy managed hook can be denied by
	// the filesystem capability before the source is preserved.
	return runSecureGitHelper("wb secure rename helper", args[0], writeRoots, args[4], args[5:], gitEnvironmentWithHeldLinkedWorktreeGitDir(adminPath, commonPath))
}

func openLinkedWorktreeGitDir(canonical *canonicalRepository, worktree *os.File, afterRetention ...func()) (*linkedWorktreeGitDir, error) {
	if canonical == nil || canonical.common == nil || worktree == nil {
		return nil, fmt.Errorf("linked worktree Git descriptors are unavailable")
	}
	gitFileFD, err := unix.Openat(int(worktree.Fd()), ".git", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open worktree .git file without following links: %w", err)
	}
	gitFile := os.NewFile(uintptr(gitFileFD), "wb-linked-worktree-gitfile")
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
	linked.adminRoot = adminRoot
	adminFD, err := unix.Openat(int(adminRoot.Fd()), adminName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open linked-worktree Git directory: %w", err)
	}
	admin := os.NewFile(uintptr(adminFD), "wb-linked-worktree-admin")
	linked.admin = admin
	linked.adminName = adminName
	if len(afterRetention) > 0 && afterRetention[0] != nil {
		afterRetention[0]()
	}
	if !regularFileEntryStillMatches(worktree, ".git", gitFile) ||
		!directoryEntryStillMatches(canonical.common, "worktrees", adminRoot) ||
		!directoryEntryStillMatches(adminRoot, adminName, admin) {
		return nil, fmt.Errorf("linked worktree Git metadata changed while retaining descriptors")
	}
	retained := linked
	linked = nil
	return retained, nil
}

func linkedWorktreeGitFileAdminName(canonical *canonicalRepository, gitFile *os.File, afterStat ...func()) (string, error) {
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
	if len(afterStat) > 0 && afterStat[0] != nil {
		afterStat[0]()
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
	return retainDescriptorsAcrossGitExecWith(unix.FcntlInt, files...)
}

func retainDescriptorsAcrossGitExecWith(fcntl func(uintptr, int, int) (int, error), files ...*os.File) error {
	for _, file := range files {
		if file == nil {
			return fmt.Errorf("missing inherited descriptor")
		}
		flags, err := fcntl(file.Fd(), unix.F_GETFD, 0)
		if err != nil {
			return err
		}
		if _, err := fcntl(file.Fd(), unix.F_SETFD, flags&^unix.FD_CLOEXEC); err != nil {
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
	// absoluteReportDir is a test-only filesystem seam for path-resolution
	// failures that otherwise require changing the process working directory.
	absoluteReportDir func(string) (string, error)
	// beforeRenamePreflight is a test-only seam between the initial plan's
	// fetched target-base snapshot and the final locked preflight. It proves a
	// moved target cannot apply stale branch policy or collision checks.
	beforeRenamePreflight func()
	// beforeRenameBind is a test-only failure seam after the old exact branch
	// was deleted and before the fresh claim is bound. It proves rollback can
	// recover a later repository without erasing earlier partial-result evidence.
	beforeRenameBind func(repository string) error
	// afterPreApplyReservation is a test-only interruption seam after the new
	// prompt/reservation is durable and before any source claim is sealed or
	// checkout path is moved.
	afterPreApplyReservation func() error
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
	destinationRoot  string
	destinationLocal bool
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
	return renameWithPorts(options, productionRenameFacadePorts(ctx))
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
		return false, lockedReason(entry, resumeInterruptedCommand(entry.Task))
	case entry.External:
		// Rename relocates the worktree onto a new task/owner/repository path.
		// An adopted worktree's entire point is staying exactly where it is;
		// recycle it with `wb worktree cleanup`/`abort` instead.
		return false, "adopted worktree cannot be renamed; it is not relocated under a WB worktrees root"
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
// created branch. The WB_HOME task lock is held by Rename; this function only
// prepares the physical parent that owns this repository's checkout.
func applyRename(ctx context.Context, home string, options RenameOptions, plan *renamePlan) (returnErr error) {
	if _, _, err := splitRepository(plan.entry.Repository); err != nil {
		return err
	}

	// Recheck safety immediately before mutating under the source task lock.
	// Rename never consults GitHub and adopted worktrees fail eligibility, so
	// this uses the same nested, non-external proof as coordinated preflight.
	refreshed, err := proveRenameSource(ctx, options, plan, renameApplyProof, productionRenameSourceProofPorts())
	if err != nil {
		return err
	}
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
	if err := sealPriorRenameClaimAndRemoveProjection(plan, refreshed,
		productionRenameClaimCutoverPorts(home, plan.entry.WorktreeDir)); err != nil {
		return err
	}
	if err := retireRenameRemote(options, plan, refreshed,
		productionRenameRemoteRetirementPorts(ctx, plan)); err != nil {
		return err
	}

	if err := prepareRenamePhysicalDestination(ctx, options.NewTask, plan); err != nil {
		return err
	}

	moveOutcome, err := moveWorktree(
		ctx,
		plan.entry.CanonicalDir,
		plan.destinationRoot,
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

	return finishRenameMember(options, plan, productionRenameFinishPorts(ctx, home, options, plan, refreshed))
}

// prepareRenamePhysicalDestination opens the destination through descriptors
// immediately before moveWorktree performs its own no-replace rename. Local
// placement has no task directory: the checkout itself is the task child.
// Shared placement retains the historical task/owner hierarchy, but its lock
// still belongs to WB_HOME and is held by Rename.
func prepareRenamePhysicalDestination(ctx context.Context, task string, plan *renamePlan) error {
	if plan.destinationLocal {
		root, err := openRenameDestinationRoot(ctx, plan, renameDestinationMove)
		if err != nil {
			return err
		}
		defer func() { _ = root.Close() }()
		return requireLocalRenameDestinationAbsent(root, task)
	}

	// The destination's relative shape below the shared root is
	// <task>/<host>/<owner>/<repository> in the central store, or the legacy
	// <task>/<owner>/<repository>. Derive the parent path from the destination
	// the plan already resolved rather than assuming a fixed depth.
	relative, err := filepath.Rel(filepath.Clean(plan.destinationRoot), filepath.Clean(plan.result.NewWorktreeDir))
	if err != nil {
		return fmt.Errorf("resolve shared rename destination: %w", err)
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) < 2 || parts[0] != task {
		return fmt.Errorf("shared rename destination %s is not below its task directory", plan.result.NewWorktreeDir)
	}
	parent := filepath.ToSlash(filepath.Join(parts[1 : len(parts)-1]...))
	repository := parts[len(parts)-1]
	root, err := openRenameDestinationRoot(ctx, plan, renameDestinationMove)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	taskFD, err := openOrCreateNoFollowDirectory(int(root.Fd()), task)
	if err != nil {
		return fmt.Errorf("create shared rename task destination: %w", err)
	}
	// The secure open returns a nonnegative descriptor on nil error; os.NewFile
	// returns nil only for a negative descriptor on Unix (Go os/file_unix.go).
	taskDirectory := os.NewFile(uintptr(taskFD), "wb-rename-shared-task")
	defer func() { _ = taskDirectory.Close() }()
	parentDirectory, _, err := openRelativeParentDirectory(taskDirectory, filepath.Join(plan.destinationRoot, task), parent)
	if err != nil {
		return fmt.Errorf("create shared rename destination parent: %w", err)
	}
	defer func() { _ = parentDirectory.Close() }()
	return requireAbsentNoFollowChild(int(parentDirectory.Fd()), repository)
}

// preflightRenamePhysicalDestination proves every target root is usable
// before the first source claim is sealed. It deliberately leaves the final
// checkout name absent; moveWorktree owns that no-replace publication.
func preflightRenamePhysicalDestination(ctx context.Context, task string, plan *renamePlan) error {
	root, err := openRenameDestinationRoot(ctx, plan, renameDestinationPreflight)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if plan.destinationLocal {
		return requireLocalRenameDestinationAbsent(root, task)
	}
	return probeRenameSharedDestination(root, unix.Mkdirat, unix.Unlinkat)
}

// probeRenameSharedDestination proves write access before any source claim is
// sealed. The per-invocation operations let fault tests verify both failure
// edges without changing process-wide filesystem state.
func probeRenameSharedDestination(root *os.File, mkdir func(int, string, uint32) error, unlink func(int, string, int) error) error {
	probe := ".wb-rename-probe-" + fmt.Sprintf("%d", time.Now().UnixNano())
	if err := mkdir(int(root.Fd()), probe, 0o700); err != nil {
		return fmt.Errorf("verify shared rename destination write access: %w", err)
	}
	if err := unlink(int(root.Fd()), probe, unix.AT_REMOVEDIR); err != nil {
		return fmt.Errorf("remove shared rename destination probe: %w", err)
	}
	return nil
}

func openLocalRenameDestination(ctx context.Context, plan *renamePlan) (string, *os.File, error) {
	canonical, err := openCanonicalRepository(plan.entry.CanonicalDir)
	if err != nil {
		return "", nil, err
	}
	defer canonical.close()
	return prepareCanonicalWorktreesRoot(ctx, canonical, plan.baseRevision)
}

func openSharedRenameDestinationRoot(plan *renamePlan, phase string, afterOpen ...func()) (*os.File, error) {
	root, err := openAbsoluteDirectoryNoFollow(plan.destinationRoot, true)
	if err != nil {
		return nil, fmt.Errorf("open shared rename destination root %s: %w", plan.destinationRoot, err)
	}
	if len(afterOpen) > 0 && afterOpen[0] != nil {
		afterOpen[0]()
	}
	if !directoryStillMatches(plan.destinationRoot, root) {
		_ = root.Close()
		return nil, fmt.Errorf("shared rename destination root changed %s: %s", phase, plan.destinationRoot)
	}
	return root, nil
}

func rollbackRenamePlan(ctx context.Context, home string, plan *renamePlan) error {
	currentPath := plan.entry.WorktreeDir
	if plan.moved {
		currentPath = plan.result.NewWorktreeDir
	}
	if err := retireFreshRenameClaimOnRollback(plan, productionRenameRollbackClaimPorts(ctx, home, currentPath)); err != nil {
		return err
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
		if err := restoreRetiredRenameRemote(plan, productionRenameRemoteRestorePorts(ctx, plan)); err != nil {
			return err
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
	if err := removeFailedRenameBranch(plan, productionRenameFailedBranchPorts(ctx, plan)); err != nil {
		return err
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
	return preflightRenameWithPorts(ctx, options, plan, productionRenamePreflightEntryPorts())
}

type renamePreflightEntryPorts struct {
	SourceProof   func(context.Context, RenameOptions, *renamePlan) (ListResult, error)
	OpenCanonical func(string) (*canonicalRepository, error)
}

func productionRenamePreflightEntryPorts() renamePreflightEntryPorts {
	return renamePreflightEntryPorts{
		SourceProof: func(ctx context.Context, options RenameOptions, plan *renamePlan) (ListResult, error) {
			return proveRenameSource(ctx, options, plan, renamePreflightProof, productionRenameSourceProofPorts())
		},
		OpenCanonical: openCanonicalRepository,
	}
}

func preflightRenameWithPorts(ctx context.Context, options RenameOptions, plan *renamePlan, ports renamePreflightEntryPorts) error {
	refreshed, err := ports.SourceProof(ctx, options, plan)
	if err != nil {
		return err
	}
	canonical, err := ports.OpenCanonical(plan.entry.CanonicalDir)
	if err != nil {
		return err
	}
	defer canonical.close()
	baseRevision, remoteHead, err := proveRenamePreflightPolicy(options, plan, refreshed,
		productionRenamePreflightPolicyPorts(ctx, options, plan, refreshed, canonical))
	if err != nil {
		return err
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
		// .worktree.md is a generated WB control-plane projection. Rename
		// regenerates it for the new task after the branch and path move.
		"-e", worktreeInstructionsName,
		"-e", legacyWorkLogProjectionName,
		// The generated per-checkout marker is WB control-plane metadata that
		// WB regenerates on demand, exactly like the journal above. It holds
		// no work and inherits nothing, so refusing a recycle because of it
		// would make every marked worktree unrecyclable — the marker tripping
		// the very policy it advertises.
		"-e", checkoutmarker.FileName,
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
	repair                   func(context.Context, string, string, string, *os.File) error
	verify                   func(context.Context, string, string, string, *os.File) error
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
	if hooks.beforeRepair != nil {
		if err := hooks.beforeRepair(); err != nil {
			return outcome, fmt.Errorf("before worktree metadata repair: %w", err)
		}
	}
	repair := hooks.repair
	if repair == nil {
		repair = func(ctx context.Context, canonicalDir, worktreesRoot, newPath string, moved *os.File) error {
			return runSecureRenameGitWithHeldWorktree(
				ctx, canonicalDir, worktreesRoot, newPath, moved,
				"worktree", "repair", ".",
			)
		}
	}
	if repairErr := repair(ctx, canonicalDir, worktreesRoot, newPath, moved); repairErr != nil {
		return outcome, fmt.Errorf("repair Git registration after descriptor-relative worktree move: %w", repairErr)
	}
	outcome.Repaired = true
	if hooks.beforeRegistrationVerify != nil {
		if err := hooks.beforeRegistrationVerify(); err != nil {
			return outcome, fmt.Errorf("before worktree registration verification: %w", err)
		}
	}
	verify := hooks.verify
	if verify == nil {
		verify = verifyWorktreeRegistered
	}
	if verifyErr := verify(ctx, canonicalDir, oldPath, newPath, moved); verifyErr != nil {
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
type moveRenameDirectoryHooks struct {
	beforeIdentityCheck func()
	beforeAuthorization func()
	afterAuthorization  func()
	afterMove           func()
}

func moveRenameDirectory(oldPath, newPath string, afterAuthorization func()) (*os.File, error) {
	return moveRenameDirectoryWithHooks(oldPath, newPath, moveRenameDirectoryHooks{afterAuthorization: afterAuthorization})
}

func moveRenameDirectoryWithHooks(oldPath, newPath string, hooks moveRenameDirectoryHooks) (*os.File, error) {
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
	if hooks.beforeIdentityCheck != nil {
		hooks.beforeIdentityCheck()
	}
	if !directoryStillMatches(oldParentPath, oldParent) || !directoryStillMatches(newParentPath, newParent) {
		return nil, fmt.Errorf("rename parent changed before descriptor-relative move")
	}
	moved, err := moveExpectedDirectoryNoReplaceAuthorized(oldParent, filepath.Base(oldPath), newParent, filepath.Base(newPath), oldDirectory, func() error {
		if hooks.beforeAuthorization != nil {
			hooks.beforeAuthorization()
		}
		if !directoryStillMatches(oldParentPath, oldParent) || !directoryStillMatches(newParentPath, newParent) ||
			!directoryEntryStillMatches(oldParent, filepath.Base(oldPath), oldDirectory) {
			return fmt.Errorf("rename path changed before descriptor-relative move")
		}
		if hooks.afterAuthorization != nil {
			hooks.afterAuthorization()
		}
		return nil
	}, hooks.afterMove)
	if moved == nil && err != nil && directoryStillMatches(newPath, oldDirectory) {
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
	for _, path := range worktreePathsFromPorcelain(output) {
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
	selection, err := normalizeBranchNamingOptions(branchNamingOptions{
		ExactBranch: options.Branch, ExactBranchChosen: options.BranchChosen,
		CLIPrefix: options.BranchPrefix, CLIPrefixChosen: options.BranchPrefixChosen,
	})
	if err != nil {
		return RenameOptions{}, err
	}
	options.Branch, options.BranchChosen = selection.ExactBranch, selection.ExactBranchChosen
	options.BranchPrefix, options.BranchPrefixChosen = selection.CLIPrefix, selection.CLIPrefixChosen
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
		absoluteReportDir := options.absoluteReportDir
		if absoluteReportDir == nil {
			absoluteReportDir = filepath.Abs
		}
		options.ReportDir, err = absoluteReportDir(options.ReportDir)
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

func writeRenameReport(
	options RenameOptions,
	generatedAt time.Time,
	phase string,
	results []RenameResult,
	diagnostics []ListDiagnostic,
) (string, error) {
	return writeRenameReportInjected(options, generatedAt, phase, results, diagnostics, nil)
}

// writeRenameReportInjected is writeRenameReport's test seam (task-9
// PR-3): every production call site reaches it only through
// writeRenameReport, which always passes a nil *filewrite.Injector, so
// production behaviour is unchanged; a test passes its own Injector
// directly to reach a write or rename failure branch deterministically.
// The original call site never called Sync -- WriteFile alone -- so this
// preserves that.
func writeRenameReportInjected(
	options RenameOptions,
	generatedAt time.Time,
	phase string,
	results []RenameResult,
	diagnostics []ListDiagnostic,
	inj *filewrite.Injector,
) (string, error) {
	report := renameReport{
		GeneratedAt: generatedAt, Phase: phase, OldTask: options.OldTask, NewTask: options.NewTask,
		Filter: options.Filter, Branch: options.Branch, Base: options.Base,
		DeleteOldBranch: options.DeleteOldBranch, DeleteRemote: options.DeleteRemote,
		Force: options.Force, PreserveCachePaths: options.PreserveCachePaths, Apply: options.Apply,
		Results: results, Diagnostics: diagnostics,
	}
	return writeLifecycleReportInjected(options.ReportDir, "rename", report, inj)
}
