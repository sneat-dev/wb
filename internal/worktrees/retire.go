package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/githubobserver"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktreebranches"
	"github.com/sneat-dev/wb/internal/worktreeretire"
)

// RetireOptions selects one WB-managed checkout. Inspector and ArchiveRemote
// are replaceable only so bare-remote integration tests never contact GitHub.
type RetireOptions struct {
	ProjectsRoot string
	Task         string
	Repository   string
	Message      string
	// Preserve chooses the source receipt namespace. Empty retains the
	// established branch mode for callers and v1 receipts.
	Preserve         string
	Apply            bool
	Now              func() time.Time
	Inspect          RetiredArchiveInspector
	ArchiveRemote    string
	OpenPullRequests func(context.Context, string, string, string) (bool, error)
	RemoteOwnership  func(context.Context, string) error
	afterPhase       func(string) error // deterministic interruption point for integration tests
}

// RetireResult is the public durable retirement receipt. Its JSON shape is
// owned by worktreeretire so apply and removed-checkout recovery share it.
type RetireResult = worktreeretire.Transaction

// retireEntryPorts is scoped to one call. It makes the facade's preflight and
// held-checkout failure boundaries testable without replacing package globals.
type retireEntryPorts struct {
	absoluteRoot  func(string) (string, error)
	resolve       func(string) (wbhome.Resolution, error)
	inventory     func(context.Context, ListOptions) (ListOutcome, error)
	checkOwner    func(ListResult) error
	archivePlan   func(context.Context, string, RetiredArchiveInspector) (RetiredArchivePlan, error)
	checkPR       func(context.Context, ListResult, RetireOptions) error
	readClaim     func(string, string) (workLogClaim, workLogProjection, error)
	checkIgnored  func(context.Context, string) error
	remoteSHA     func(context.Context, string, string, string) (string, error)
	readReport    func(string) (RetireResult, error)
	checkAncestor func(context.Context, string, string, string) error
	acquireTask   func(string, string) (*cleanupTaskHandle, error)
	validateTask  func(*cleanupTaskHandle) error
	openWorktree  func(*cleanupTaskHandle, CleanupResult) (*cleanupWorktreeHandle, error)
	validateHeld  func(*cleanupWorktreeHandle) error
	ownerViews    func(string) ([]OwnerView, error)
	git           func(context.Context, string, ...string) (string, error)
}

func productionRetireEntryPorts() retireEntryPorts {
	return retireEntryPorts{
		absoluteRoot: absoluteProjectsRoot, resolve: wbhome.Resolve, inventory: ListWithDiagnostics,
		checkOwner: retireCheckOwner, archivePlan: PlanRetiredArchivePreflight,
		checkPR: retireCheckPR, readClaim: retireReadClaim, checkIgnored: retireCheckIgnored,
		remoteSHA: retireRemoteSHA, readReport: readRetireReport, checkAncestor: retireCheckRemoteAncestor,
		acquireTask:  acquireCleanupTaskAtOrCreate,
		validateTask: (*cleanupTaskHandle).validate, openWorktree: openCleanupWorktree,
		validateHeld: (*cleanupWorktreeHandle).validate, ownerViews: ownerViews, git: git,
	}
}

func Retire(ctx context.Context, options RetireOptions) (RetireResult, error) {
	return retireWithEntryPorts(ctx, options, productionRetireEntryPorts())
}

func retireWithEntryPorts(ctx context.Context, options RetireOptions, ports retireEntryPorts) (RetireResult, error) {
	var err error
	options, err = normalizeRetireOptions(options)
	if err != nil {
		return RetireResult{}, err
	}
	root, err := ports.absoluteRoot(options.ProjectsRoot)
	if err != nil {
		return RetireResult{}, err
	}
	resolution, err := ports.resolve(root)
	if err != nil {
		return RetireResult{}, err
	}
	inventory, err := ports.inventory(ctx, ListOptions{ProjectsRoot: root, Task: options.Task, Filter: options.Repository, Workers: 1})
	if err != nil {
		return RetireResult{}, err
	}
	entry, resumeRemoved, err := selectRetirementEntry(inventory, options)
	if err != nil {
		return RetireResult{}, err
	}
	if resumeRemoved {
		return retireResumeRemoved(ctx, resolution.Write.Home, options)
	}
	if err := ports.checkOwner(entry); err != nil {
		return RetireResult{}, err
	}
	archivePlan, err := ports.archivePlan(ctx, entry.Repository, options.Inspect)
	if err != nil {
		return RetireResult{}, err
	}
	if archivePlan.Outcome != "planned" {
		return RetireResult{}, fmt.Errorf("retirement refused: %s", archivePlan.Refusal)
	}
	if options.RemoteOwnership == nil {
		return RetireResult{}, fmt.Errorf("retirement requires an authoritative remote owner check")
	}
	if err := options.RemoteOwnership(ctx, options.Task); err != nil {
		return RetireResult{}, fmt.Errorf("retirement remote owner preflight: %w", err)
	}
	if options.ArchiveRemote == "" {
		options.ArchiveRemote = "git@github.com:" + archivePlan.ArchiveRepository + ".git"
	}
	if options.OpenPullRequests == nil {
		options.OpenPullRequests = retireOpenPullRequests
	}
	if err := ports.checkPR(ctx, entry, options); err != nil {
		return RetireResult{}, err
	}
	claim, projection, err := ports.readClaim(resolution.Write.Home, entry.WorktreeDir)
	if err != nil {
		return RetireResult{}, fmt.Errorf("retirement requires a corroborated active Work Log: %w", err)
	}
	if claim.Task != entry.Task || claim.Repository != entry.Repository || claim.Branch != entry.Branch || projection.ClaimID != claim.ClaimID {
		return RetireResult{}, fmt.Errorf("work log claim does not bind the selected checkout")
	}
	if err := ports.checkIgnored(ctx, entry.WorktreeDir); err != nil {
		return RetireResult{}, err
	}
	remoteSHA, err := ports.remoteSHA(ctx, entry.CanonicalDir, "origin", "refs/heads/"+entry.Branch)
	if err != nil {
		return RetireResult{}, err
	}
	priorPath := retireReportPath(resolution.Write.Home, RetireResult{Task: entry.Task, Repository: entry.Repository})
	prior, priorErr := ports.readReport(priorPath)
	if priorErr != nil && !errors.Is(priorErr, os.ErrNotExist) {
		return RetireResult{}, priorErr
	}
	if errors.Is(priorErr, os.ErrNotExist) {
		if err := ports.checkAncestor(ctx, entry.CanonicalDir, remoteSHA, entry.HeadSHA); err != nil {
			return RetireResult{}, err
		}
	}
	result := RetireResult{Version: 1, Task: entry.Task, Repository: entry.Repository, ArchiveRepository: archivePlan.ArchiveRepository,
		Worktree: entry.WorktreeDir, Canonical: entry.CanonicalDir, WorktreesRoot: lifecycleTaskLockRoot(resolution.Write.Home, wbhome.Layout{WorktreesRoot: entry.WorktreesRoot, Local: entry.Local}), Local: entry.Local,
		Branch: entry.Branch, Preserve: options.Preserve, OriginalRemoteSHA: remoteSHA, SourceSHA: entry.HeadSHA, ClaimID: claim.ClaimID, EffortID: claim.EffortID, RunID: claim.RunID, Phase: "planned"}
	result.ReportPath = retireReportPath(resolution.Write.Home, result)
	if !options.Apply {
		result.RetiredRef = worktreebranches.RetiredBranchDestination(options.Now(), entry.Branch, entry.HeadSHA)
		result.ArchiveRef = retireArchiveRef(result)
		return result, nil
	}
	task, err := ports.acquireTask(result.WorktreesRoot, options.Task)
	if err != nil {
		return RetireResult{}, fmt.Errorf("acquire retirement task lock: %w", err)
	}
	defer task.close()
	defer func() { _ = task.lock.release() }()
	if err := ports.validateTask(task); err != nil {
		return RetireResult{}, err
	}
	if err := options.RemoteOwnership(ctx, options.Task); err != nil {
		return RetireResult{}, fmt.Errorf("retirement remote owner recheck: %w", err)
	}
	heldWorktree, err := ports.openWorktree(task, CleanupResult{ListResult: entry})
	if err != nil {
		return RetireResult{}, err
	}
	defer heldWorktree.close()
	if err := ports.validateHeld(heldWorktree); err != nil {
		return RetireResult{}, err
	}
	freshOwners, err := ports.ownerViews(entry.WorktreeDir)
	if err != nil {
		return RetireResult{}, err
	}
	entry.Owners = freshOwners
	if err := ports.checkOwner(entry); err != nil {
		return RetireResult{}, err
	}
	// Recheck under the held task lock. The first observation is for dry-run and
	// admission; all destructive steps use the second observation.
	current, err := ports.git(ctx, entry.WorktreeDir, "rev-parse", "HEAD")
	if err != nil || current != entry.HeadSHA {
		return RetireResult{}, fmt.Errorf("checkout HEAD changed before retirement")
	}
	branch, err := ports.git(ctx, entry.WorktreeDir, "branch", "--show-current")
	if err != nil || branch != entry.Branch {
		return RetireResult{}, fmt.Errorf("checkout branch changed before retirement")
	}
	if observed, err := ports.remoteSHA(ctx, entry.CanonicalDir, "origin", "refs/heads/"+entry.Branch); err != nil || observed != remoteSHA {
		return RetireResult{}, fmt.Errorf("original remote branch changed before retirement: %w", err)
	}
	if priorErr != nil {
		if err := ports.checkAncestor(ctx, entry.CanonicalDir, remoteSHA, current); err != nil {
			return RetireResult{}, err
		}
	}
	if err := ports.checkPR(ctx, entry, options); err != nil {
		return RetireResult{}, err
	}
	if err := ports.checkIgnored(ctx, entry.WorktreeDir); err != nil {
		return RetireResult{}, err
	}
	priorExists := priorErr == nil
	if priorExists {
		if err := corroborateRetireResumeReceipt(prior, result, options.Preserve, remoteSHA); err != nil {
			return RetireResult{}, err
		}
		result = prior
	}
	operation := retireTransactionOperation(options, resolution.Write.Home, entry, task, heldWorktree)
	return operation.ApplyTransaction(ctx, result, priorExists, current, options.Message)
}

func corroborateRetireResumeReceipt(prior, planned RetireResult, preserve, remoteSHA string) error {
	remoteMatchesReceipt := remoteSHA == prior.OriginalRemoteSHA ||
		remoteSHA == "" && (prior.OriginalRemoteSHA == "" || prior.DeleteIntentSHA == prior.OriginalRemoteSHA)
	if prior.Phase == "original_deleted" {
		remoteMatchesReceipt = remoteSHA == ""
	}
	if prior.Task != planned.Task || prior.Repository != planned.Repository || prior.Worktree != planned.Worktree ||
		prior.Canonical != planned.Canonical || prior.WorktreesRoot != planned.WorktreesRoot || prior.Branch != planned.Branch ||
		retirePreserveMode(prior) != preserve || prior.ArchiveRepository != planned.ArchiveRepository ||
		prior.ClaimID != planned.ClaimID || prior.EffortID != planned.EffortID || prior.RunID != planned.RunID ||
		!remoteMatchesReceipt {
		return fmt.Errorf("retirement receipt conflicts with current checkout")
	}
	return nil
}

func normalizeRetireOptions(options RetireOptions) (RetireOptions, error) {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Preserve == "" {
		options.Preserve = "branch"
	}
	if options.Preserve != "branch" && options.Preserve != "tag" {
		return RetireOptions{}, fmt.Errorf("unsupported --preserve %q; use branch or tag", options.Preserve)
	}
	if !validSafeSegment(options.Task) {
		return RetireOptions{}, fmt.Errorf("invalid retirement task %q", options.Task)
	}
	if options.Repository != "" {
		if _, _, err := splitRepository(options.Repository); err != nil {
			return RetireOptions{}, err
		}
	}
	return options, nil
}

func selectRetirementEntry(inventory ListOutcome, options RetireOptions) (ListResult, bool, error) {
	if len(inventory.Diagnostics) != 0 {
		return ListResult{}, false, fmt.Errorf("retirement inventory has %d malformed candidate(s)", len(inventory.Diagnostics))
	}
	var selected []ListResult
	for _, entry := range inventory.Results {
		if options.Repository == "" || options.Repository == entry.Repository {
			selected = append(selected, entry)
		}
	}
	if len(selected) != 1 {
		if len(selected) == 0 && options.Apply {
			return ListResult{}, true, nil
		}
		return ListResult{}, false, fmt.Errorf("retirement requires exactly one managed repository; found %d", len(selected))
	}
	entry := selected[0]
	if entry.External || entry.Detached || entry.Branch == "" {
		return ListResult{}, false, fmt.Errorf("retirement requires a managed attached branch")
	}
	if entry.Locked {
		return ListResult{}, false, fmt.Errorf("task %s has a competing lifecycle lock", options.Task)
	}
	return entry, false, nil
}

func retireCheckOwner(entry ListResult) error {
	identity := CurrentIdentity()
	for _, owner := range entry.Owners {
		if owner.PIDStatus == "active" && (identity.PID <= 0 || owner.PID != identity.PID) {
			return fmt.Errorf("worktree has a competing active claim by PID %d", owner.PID)
		}
	}
	return nil
}

func retireReadClaim(home, worktree string) (workLogClaim, workLogProjection, error) {
	projection, err := readWorkLogProjectionForReadOnlyClaim(worktree)
	if err != nil {
		return workLogClaim{}, projection, err
	}
	if projection.Lifecycle != "active" && projection.Lifecycle != "terminal" {
		return workLogClaim{}, projection, fmt.Errorf("work log has unsupported lifecycle %s", projection.Lifecycle)
	}
	if err := corroborateProjectionWithPrivateClaim(home, worktree, projection); err != nil {
		return workLogClaim{}, projection, err
	}
	run, _, err := openWorkLogRun(home, projection.EffortID, projection.RunID, false)
	if err != nil {
		return workLogClaim{}, projection, err
	}
	defer func() { _ = run.Close() }()
	claim, err := readWorkLogClaimAt(run, projection.ClaimID)
	if err != nil {
		return workLogClaim{}, projection, err
	}
	return claim, projection, nil
}

func retireCheckPrivateArchive(ctx context.Context, result RetireResult, inspect RetiredArchiveInspector) error {
	plan, err := PlanRetiredArchivePreflight(ctx, result.Repository, inspect)
	if err != nil {
		return err
	}
	if plan.Outcome != "planned" || plan.ArchiveRepository != result.ArchiveRepository {
		return fmt.Errorf("retirement archive is no longer the configured private repository")
	}
	return nil
}

func retireCheckRemoteAncestor(ctx context.Context, canonical, remoteSHA, localSHA string) error {
	if remoteSHA == "" || remoteSHA == localSHA {
		return nil
	}
	if _, err := git(ctx, canonical, "merge-base", "--is-ancestor", remoteSHA, localSHA); err != nil {
		return fmt.Errorf("original remote branch moved ahead or diverged from local HEAD")
	}
	return nil
}

func retireCheckIgnored(ctx context.Context, worktree string) error {
	output, err := git(ctx, worktree, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	var unexpected []string
	for _, path := range strings.Split(output, "\x00") {
		if path == "" || path == ".worktree.md" || path == ".wb-worklog.json" || strings.HasPrefix(path, ".wb/local/") || strings.HasPrefix(path, ".wb-worklog/") {
			continue
		}
		unexpected = append(unexpected, path)
	}
	if len(unexpected) != 0 {
		sort.Strings(unexpected)
		return fmt.Errorf("ignored worktree files would be lost during retirement: %s", strings.Join(unexpected, ", "))
	}
	return nil
}

func retireValidateIntentCommit(ctx context.Context, worktree string, intent RetireResult, head string) error {
	if head == intent.IntentParentSHA {
		return fmt.Errorf("retirement commit was not created")
	}
	parent, err := git(ctx, worktree, "rev-parse", "HEAD^")
	if err != nil || parent != intent.IntentParentSHA {
		return fmt.Errorf("retirement commit parent does not match durable intent: %w", err)
	}
	tree, err := git(ctx, worktree, "rev-parse", "HEAD^{tree}")
	if err != nil || tree != intent.IntentTreeSHA {
		return fmt.Errorf("retirement commit tree does not match durable intent: %w", err)
	}
	message, err := git(ctx, worktree, "log", "-1", "--format=%B")
	if err != nil || strings.TrimSpace(message) != intent.IntentMessage {
		return fmt.Errorf("retirement commit message does not match durable intent: %w", err)
	}
	if clean, err := cleanWorktree(ctx, worktree); err != nil || !clean {
		return fmt.Errorf("checkout changed after retirement commit: %w", err)
	}
	return nil
}

// A crash between Git's worktree removal and exact local ref deletion leaves
// no inventory row. The held receipt and two freshly observed remote refs are
// enough to finish only that last local step.
func retireResumeRemoved(ctx context.Context, home string, options RetireOptions) (RetireResult, error) {
	return retireResumeRemovedWithPorts(ctx, home, options, productionRetireRemovedPorts())
}

type retireRemovedPorts struct {
	readDir       func(string) ([]os.DirEntry, error)
	readReport    func(string) (RetireResult, error)
	validateClaim func(string, RetireResult) error
	archivePlan   func(context.Context, string, RetiredArchiveInspector) (RetiredArchivePlan, error)
	acquireTask   func(string, string) (*cleanupTaskHandle, error)
	validateTask  func(*cleanupTaskHandle) error
	remote        worktreeretire.TransactionPorts
	lstat         func(string) (os.FileInfo, error)
	git           func(context.Context, string, ...string) (string, error)
	openCanonical func(string) (*canonicalRepository, error)
	branchExists  func(context.Context, string, string) (bool, error)
	deleteBranch  func(context.Context, *canonicalRepository, RetireResult) error
	writeReport   func(RetireResult) error
}

func productionRetireRemovedPorts() retireRemovedPorts {
	return retireRemovedPorts{
		readDir: os.ReadDir, readReport: readRetireReport, validateClaim: retireValidateRemovedClaim,
		archivePlan: PlanRetiredArchivePreflight, acquireTask: acquireCleanupTaskAtOrCreate,
		validateTask: (*cleanupTaskHandle).validate, remote: retireTransactionPorts(),
		lstat: os.Lstat, git: git, openCanonical: openCanonicalRepository,
		branchExists: localBranchExists, writeReport: writeRetireReport,
		deleteBranch: func(ctx context.Context, canonical *canonicalRepository, result RetireResult) error {
			return runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "update-ref", "-d", "refs/heads/"+result.Branch, result.SourceSHA)
		},
	}
}

func retireResumeRemovedWithPorts(ctx context.Context, home string, options RetireOptions, ports retireRemovedPorts) (RetireResult, error) {
	directory := filepath.Join(home, "reports", "worktree-retire", options.Task)
	entries, err := ports.readDir(directory)
	if err != nil {
		return RetireResult{}, fmt.Errorf("no managed checkout or retirement receipt for %s: %w", options.Task, err)
	}
	var reports []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			candidate := filepath.Join(directory, entry.Name())
			if options.Repository != "" && candidate != retireReportPath(home, RetireResult{Task: options.Task, Repository: options.Repository}) {
				continue
			}
			reports = append(reports, candidate)
		}
	}
	if len(reports) != 1 {
		return RetireResult{}, fmt.Errorf("removed retirement requires exactly one receipt; found %d", len(reports))
	}
	result, err := ports.readReport(reports[0])
	if err != nil {
		return RetireResult{}, err
	}
	if err := worktreeretire.ValidateRemovedReceipt(result, reports[0], options.Task, options.Repository); err != nil {
		return RetireResult{}, err
	}
	if err := ports.validateClaim(home, result); err != nil {
		return result, err
	}
	archivePlan, err := ports.archivePlan(ctx, result.Repository, options.Inspect)
	if err != nil || archivePlan.Outcome != "planned" || archivePlan.ArchiveRepository != result.ArchiveRepository {
		return RetireResult{}, fmt.Errorf("private archive preflight no longer holds")
	}
	if options.ArchiveRemote == "" {
		options.ArchiveRemote = "git@github.com:" + result.ArchiveRepository + ".git"
	}
	task, err := ports.acquireTask(result.WorktreesRoot, result.Task)
	if err != nil {
		return result, err
	}
	defer task.close()
	defer func() { _ = task.lock.release() }()
	if err := ports.validateTask(task); err != nil {
		return result, err
	}
	if options.RemoteOwnership == nil {
		return result, fmt.Errorf("retirement requires an authoritative remote owner check")
	}
	if err := options.RemoteOwnership(ctx, options.Task); err != nil {
		return result, fmt.Errorf("retirement remote owner recheck: %w", err)
	}
	operation := worktreeretire.Operation{Remote: ports.remote, ArchiveRemote: options.ArchiveRemote, WriteReport: ports.writeReport}
	if err := operation.VerifyRemovedRemote(ctx, result); err != nil {
		return result, err
	}
	if _, err := ports.lstat(result.Worktree); !errors.Is(err, os.ErrNotExist) {
		return result, fmt.Errorf("checkout path still exists or cannot be inspected: %w", err)
	}
	registered, err := ports.git(ctx, result.Canonical, "worktree", "list", "--porcelain")
	if err != nil {
		return result, err
	}
	if strings.Contains(registered, "worktree "+result.Worktree+"\n") {
		return result, fmt.Errorf("checkout is still registered")
	}
	canonical, err := ports.openCanonical(result.Canonical)
	if err != nil {
		return result, err
	}
	defer canonical.close()
	if exists, err := ports.branchExists(ctx, result.Canonical, result.Branch); err != nil {
		return result, err
	} else if exists {
		if sha, err := ports.git(ctx, result.Canonical, "rev-parse", "refs/heads/"+result.Branch); err != nil || sha != result.SourceSHA {
			return result, fmt.Errorf("local source branch changed: %w", err)
		}
		if err := ports.deleteBranch(ctx, canonical, result); err != nil {
			return result, err
		}
	}
	if err := operation.CompleteRemoved(&result); err != nil {
		return result, err
	}
	return result, nil
}

func retireValidateRemovedClaim(home string, result RetireResult) error {
	run, _, err := openWorkLogRun(home, result.EffortID, result.RunID, false)
	if err != nil {
		return err
	}
	defer func() { _ = run.Close() }()
	claim, err := readWorkLogClaimAt(run, result.ClaimID)
	if err != nil {
		return err
	}
	if claim.Task != result.Task || claim.Repository != result.Repository || claim.Branch != result.Branch || claim.ClaimID != result.ClaimID || claim.EffortID != result.EffortID || claim.RunID != result.RunID || filepath.Clean(claim.Worktree) != filepath.Clean(result.Worktree) {
		return fmt.Errorf("retirement receipt conflicts with immutable Work Log claim")
	}
	terminal, err := readWorkLogTerminalAt(run, result.ClaimID)
	if err != nil {
		return err
	}
	if terminal.ClaimID != result.ClaimID || terminal.FinalCommit != result.SourceSHA || terminal.Disposition != "retired" {
		return fmt.Errorf("retirement receipt conflicts with immutable Work Log terminal")
	}
	return nil
}

func retireCheckPR(ctx context.Context, entry ListResult, options RetireOptions) error {
	open, err := options.OpenPullRequests(ctx, entry.WorktreeDir, entry.Repository, entry.Branch)
	if err != nil {
		return fmt.Errorf("inspect open pull requests: %w", err)
	}
	if open {
		return fmt.Errorf("branch %s has an open pull request", entry.Branch)
	}
	return nil
}

func retireOpenPullRequests(ctx context.Context, worktree, repository, branch string) (bool, error) {
	owner, _, ok := strings.Cut(repository, "/")
	if !ok {
		return false, fmt.Errorf("invalid source repository")
	}
	query := url.Values{"head": []string{owner + ":" + branch}, "state": []string{"open"}}.Encode()
	response := githubobserver.Execute(ctx, worktree, "api", "--paginate", "repos/"+repository+"/pulls?"+query)
	if response.Err != nil {
		return false, response.Err
	}
	var pulls []githubPullRequest
	if err := json.Unmarshal(response.Stdout, &pulls); err != nil {
		return false, err
	}
	for _, pull := range pulls {
		if strings.EqualFold(pull.State, "open") {
			return true, nil
		}
	}
	base, err := openPullRequestUsingBranchAsBase(ctx, worktree, repository, branch)
	return base != nil, err
}

func retireRemoteSHA(ctx context.Context, directory, remote, ref string) (string, error) {
	output, err := git(ctx, directory, "ls-remote", remote, ref)
	if err != nil {
		return "", fmt.Errorf("inspect remote ref %s: %w", ref, err)
	}
	if output == "" {
		return "", nil
	}
	fields := strings.Fields(output)
	if len(fields) != 2 || fields[1] != ref || !isGitObjectID(fields[0]) {
		return "", fmt.Errorf("invalid remote ref observation for %s", ref)
	}
	return fields[0], nil
}

func retireCommitSource(ctx context.Context, canonical string, held *cleanupWorktreeHandle, message string, prepared func(tree, message string) error) (bool, error) {
	return retireCommitSourceWithPorts(message, prepared, retireCommitPorts{
		gitHeld: func(args ...string) ([]byte, error) {
			if err := held.validate(); err != nil {
				return nil, err
			}
			return runSecureRenameGitBytesWithHeldWorktree(ctx, canonical, held.parentPath, held.worktreePath, held.worktree, args...)
		},
		checkUntracked: func(path string) error { return retireCheckUntrackedPath(held.worktreePath, path) },
	})
}

type retireCommitPorts struct {
	gitHeld        func(...string) ([]byte, error)
	checkUntracked func(string) error
}

func retireCommitSourceWithPorts(message string, prepared func(tree, message string) error, ports retireCommitPorts) (bool, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "Retire worktree source changes"
	}
	// Check every path that could introduce file bytes before touching the
	// index. --others expands untracked directories to actual files.
	for _, args := range [][]string{{"diff", "--name-only", "-z", "--diff-filter=ACMR"}, {"diff", "--cached", "--name-only", "-z", "--diff-filter=ACMR"}, {"ls-files", "--others", "--exclude-standard", "-z"}} {
		paths, err := ports.gitHeld(args...)
		if err != nil {
			return false, err
		}
		for _, path := range strings.Split(string(paths), "\x00") {
			if path == "" {
				continue
			}
			if retireLooksLikeSecretPath(path) {
				return false, fmt.Errorf("refusing secret-looking source commit path %q", path)
			}
			if args[0] == "ls-files" {
				if err := ports.checkUntracked(path); err != nil {
					return false, err
				}
			}
		}
	}
	if _, err := ports.gitHeld("add", "-A"); err != nil {
		return false, err
	}
	_, _ = ports.gitHeld("reset", "-q", "--", ".worktree.md")
	paths, err := ports.gitHeld("diff", "--cached", "--name-only", "-z")
	if err != nil {
		return false, err
	}
	secretPaths, err := ports.gitHeld("diff", "--cached", "--name-only", "-z", "--diff-filter=ACMR")
	if err != nil {
		return false, err
	}
	for _, path := range strings.Split(string(secretPaths), "\x00") {
		if path != "" && retireLooksLikeSecretPath(path) {
			return false, fmt.Errorf("refusing secret-looking source commit path %q", path)
		}
	}
	if len(paths) == 0 {
		return false, nil
	}
	tree, err := ports.gitHeld("write-tree")
	if err != nil {
		return false, err
	}
	if prepared != nil {
		if err := prepared(strings.TrimSpace(string(tree)), message); err != nil {
			return false, err
		}
	}
	if _, err := ports.gitHeld("commit", "-m", message); err != nil {
		return false, fmt.Errorf("commit source with hooks: %w", err)
	}
	return true, nil
}

func retireCheckUntrackedPath(worktree, relative string) error {
	parent, err := openAbsoluteDirectoryNoFollow(filepath.Join(worktree, filepath.Dir(relative)), false)
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	var stat unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), filepath.Base(relative), &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT == unix.S_IFLNK {
		return fmt.Errorf("refusing untracked symlink %q", relative)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("refusing nonregular untracked path %q", relative)
	}
	return nil
}

// Keep this conservative filename rule aligned with `wb pr create --commit-all`.
func retireLooksLikeSecretPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return base == ".env" || strings.HasPrefix(base, ".env.") || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key") || strings.HasSuffix(base, ".p12") || strings.HasPrefix(base, "id_rsa") || strings.HasPrefix(base, "id_ed25519") || strings.HasPrefix(base, "id_ecdsa") || base == ".worktree.md"
}

func retireArchiveRef(result RetireResult) string { return worktreeretire.ArchiveRef(result) }

func retirePreserveMode(result RetireResult) string { return worktreeretire.PreserveMode(result) }

func retireSourceRef(result RetireResult) string { return worktreeretire.SourceRef(result) }

func retireReportPath(home string, result RetireResult) string {
	return worktreeretire.ReportPath(home, result)
}

func readRetireReport(path string) (RetireResult, error) { return worktreeretire.ReadReport(path) }

func writeRetireReport(result RetireResult) error { return worktreeretire.WriteReport(result) }

// writeRetireReportInjected keeps the existing filewrite failure seam at the
// facade boundary while the leaf owns durable report persistence.
func writeRetireReportInjected(result RetireResult, inj *filewrite.Injector) error {
	return worktreeretire.WriteReportInjected(result, inj)
}

func retirePublishSource(ctx context.Context, result *RetireResult) error {
	return worktreeretire.PublishSource(ctx, result, retireTransactionPorts())
}

func retireVerifyReceipts(ctx context.Context, canonical, archiveRemote string, result RetireResult) error {
	return worktreeretire.VerifyReceipts(ctx, canonical, archiveRemote, result, retireTransactionPorts())
}

func retireDeletionProofRef(result RetireResult) string {
	return worktreeretire.DeletionProofRef(result)
}

func retireDeleteOriginal(ctx context.Context, result *RetireResult, afterPhase func(string) error) error {
	return worktreeretire.DeleteOriginal(ctx, result, afterPhase, retireTransactionPorts())
}

// The three remote ports bind proof decisions to the facade's exact canonical
// repository handle and existing secure Git mutation helper.
func retireTransactionPorts() worktreeretire.TransactionPorts {
	return worktreeretire.TransactionPorts{
		RemoteSHA: retireRemoteSHA,
		PublishSourceRef: func(ctx context.Context, result RetireResult, ref string) error {
			canonical, err := openCanonicalRepository(result.Canonical)
			if err != nil {
				return err
			}
			defer canonical.close()
			return runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "push", "--force-with-lease="+ref+":", "origin", result.SourceSHA+":"+ref)
		},
		DeleteAndTag: func(ctx context.Context, result RetireResult, ref, proofRef string) error {
			canonical, err := openCanonicalRepository(result.Canonical)
			if err != nil {
				return err
			}
			defer canonical.close()
			return runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "push", "--atomic", "--force-with-lease="+ref+":"+result.OriginalRemoteSHA, "--force-with-lease="+proofRef+":", "origin", ":"+ref, result.SourceSHA+":"+proofRef)
		},
	}
}

func retireTransactionOperation(options RetireOptions, home string, entry ListResult, task *cleanupTaskHandle, heldWorktree *cleanupWorktreeHandle) worktreeretire.Operation {
	return worktreeretire.Operation{
		Remote: retireTransactionPorts(), ArchiveRemote: options.ArchiveRemote,
		Now: options.Now, AfterPhase: options.afterPhase, WriteReport: writeRetireReport,
		Apply: worktreeretire.ApplyPorts{
			ValidateHeld: heldWorktree.validate,
			CommitSource: func(ctx context.Context, message string, before func(string, string) error) (bool, error) {
				return retireCommitSource(ctx, entry.CanonicalDir, heldWorktree, message, before)
			},
			ValidateIntentCommit: func(ctx context.Context, result RetireResult, head string) error {
				return retireValidateIntentCommit(ctx, entry.WorktreeDir, result, head)
			},
			CurrentHead: func(ctx context.Context) (string, error) { return git(ctx, entry.WorktreeDir, "rev-parse", "HEAD") },
			Clean:       func(ctx context.Context) (bool, error) { return cleanWorktree(ctx, entry.WorktreeDir) },
			SealWorkLog: func(result RetireResult) error {
				return sealWorkLogForRecycle(home, entry.WorktreeDir, result.SourceSHA, "retired")
			},
			CheckPrivateArchive: func(ctx context.Context, result RetireResult) error {
				return retireCheckPrivateArchive(ctx, result, options.Inspect)
			},
			PublishArchive: func(ctx context.Context, result *RetireResult) error {
				return retirePublishArchive(ctx, home, options.ArchiveRemote, result)
			},
			BeforeDeletion: func(ctx context.Context) error {
				if err := retireCheckPR(ctx, entry, options); err != nil {
					return err
				}
				if err := options.RemoteOwnership(ctx, options.Task); err != nil {
					return fmt.Errorf("retirement remote owner final recheck: %w", err)
				}
				return retireCheckIgnored(ctx, entry.WorktreeDir)
			},
			RemoveLocal: func(ctx context.Context, result *RetireResult) error {
				return retireRemoveLocal(ctx, task, entry, result, options.afterPhase)
			},
		},
	}
}

func retireRemoveLocal(ctx context.Context, task *cleanupTaskHandle, entry ListResult, result *RetireResult, afterPhase func(string) error) error {
	return retireRemoveLocalWithPorts(ctx, task, entry, result, afterPhase, productionRetireLocalRemovalPorts())
}

type retireLocalRemovalPorts struct {
	openWorktree   func(*cleanupTaskHandle, CleanupResult) (*cleanupWorktreeHandle, error)
	validateHeld   func(*cleanupWorktreeHandle) error
	openCanonical  func(string) (*canonicalRepository, error)
	head           func(context.Context, string) (string, error)
	clean          func(context.Context, string) (bool, error)
	removeWorktree func(context.Context, *canonicalRepository, *cleanupWorktreeHandle, string) error
	deleteBranch   func(context.Context, *canonicalRepository, RetireResult) error
	removeParent   func(*cleanupWorktreeHandle) error
}

func productionRetireLocalRemovalPorts() retireLocalRemovalPorts {
	return retireLocalRemovalPorts{
		openWorktree: openCleanupWorktree, validateHeld: (*cleanupWorktreeHandle).validate,
		openCanonical: openCanonicalRepository,
		head: func(ctx context.Context, worktree string) (string, error) {
			return git(ctx, worktree, "rev-parse", "HEAD")
		},
		clean: cleanWorktree,
		removeWorktree: func(ctx context.Context, canonical *canonicalRepository, worktree *cleanupWorktreeHandle, path string) error {
			return runSecureCleanupGitHelper(ctx, canonical, worktree.parent, worktree.worktree, worktree.parentPath, path, "worktree", "remove", path)
		},
		deleteBranch: func(ctx context.Context, canonical *canonicalRepository, result RetireResult) error {
			return runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "update-ref", "-d", "refs/heads/"+result.Branch, result.SourceSHA)
		},
		removeParent: func(worktree *cleanupWorktreeHandle) error { return worktree.removeEmptyParent(nil, nil) },
	}
}

func retireRemoveLocalWithPorts(ctx context.Context, task *cleanupTaskHandle, entry ListResult, result *RetireResult, afterPhase func(string) error, ports retireLocalRemovalPorts) error {
	worktree, err := ports.openWorktree(task, CleanupResult{ListResult: entry})
	if err != nil {
		return err
	}
	defer worktree.close()
	if err := ports.validateHeld(worktree); err != nil {
		return err
	}
	canonical, err := ports.openCanonical(result.Canonical)
	if err != nil {
		return err
	}
	defer canonical.close()
	if head, err := ports.head(ctx, result.Worktree); err != nil || head != result.SourceSHA {
		return fmt.Errorf("checkout moved before removal")
	}
	if clean, err := ports.clean(ctx, result.Worktree); err != nil || !clean {
		return fmt.Errorf("checkout changed before removal: %w", err)
	}
	if err := ports.removeWorktree(ctx, canonical, worktree, result.Worktree); err != nil {
		return err
	}
	if afterPhase != nil {
		if err := afterPhase("worktree_removed"); err != nil {
			return err
		}
	}
	if err := ports.deleteBranch(ctx, canonical, *result); err != nil {
		return err
	}
	if err := ports.removeParent(worktree); err != nil {
		return err
	}
	result.Phase = "complete"
	return nil
}

// These facade adapters retain the retirement transaction and its public
// receipt while worktreeretire owns archive capture, publication, and proof.
func retireCaptureFile(source, destination string) (string, error) {
	return worktreeretire.CaptureFile(source, destination)
}

func retireCaptureFileInjected(source, destination string, inj *filewrite.Injector) (string, error) {
	return worktreeretire.CaptureFileInjected(source, destination, inj)
}

func retireCaptureTree(source, destination string, include func(string) bool, hashes map[string]string, prefix string) error {
	return worktreeretire.CaptureTree(source, destination, include, hashes, prefix)
}

type retireArchiveManifest = worktreeretire.Manifest

func retireArchivePorts() worktreeretire.Ports {
	return worktreeretire.Ports{
		ReadClaim: func(home, worktree string) (worktreeretire.Claim, error) {
			claim, _, err := retireReadClaim(home, worktree)
			if err != nil {
				return worktreeretire.Claim{}, err
			}
			return worktreeretire.Claim{ClaimID: claim.ClaimID, Repository: claim.Repository, Branch: claim.Branch,
				PromptArchive: claim.PromptArchive, PromptDigest: claim.PromptDigest}, nil
		},
		ReadTerminal: func(home, worktree string) (*worktreeretire.Terminal, error) {
			record, err := readWorkLogTerminalRecordReadOnly(home, worktree)
			if err != nil || record == nil {
				return nil, err
			}
			report := ""
			if record.FinalizeReport != nil {
				report = record.FinalizeReport.ReportPath
			}
			return &worktreeretire.Terminal{ClaimID: record.ClaimID, FinalCommit: record.FinalCommit, ReportPath: report}, nil
		},
		ReportFileName: finalizeReportFileName,
		RemoteSHA:      retireRemoteSHA,
		Git:            git,
		GitBytes:       worktreeretire.GitBytes,
		GitObjectSHA:   worktreeretire.GitObjectSHA,
	}
}

func retirePublishArchive(ctx context.Context, home, remote string, result *RetireResult) error {
	receipt := worktreeretire.Receipt{Task: result.Task, Repository: result.Repository, Branch: result.Branch,
		Preserve: result.Preserve, SourceSHA: result.SourceSHA, RetiredRef: result.RetiredRef,
		Worktree: result.Worktree, Canonical: result.Canonical, ArchiveRef: result.ArchiveRef,
		ArchiveSHA: result.ArchiveSHA, ClaimID: result.ClaimID, EffortID: result.EffortID,
		RunID: result.RunID, Phase: result.Phase}
	if err := worktreeretire.PublishArchive(ctx, home, remote, &receipt, retireArchivePorts()); err != nil {
		return err
	}
	result.ArchiveSHA, result.Phase = receipt.ArchiveSHA, receipt.Phase
	return nil
}

func retireArchiveIncludesRunPath(claimID, reportName, path string) bool {
	return worktreeretire.ArchiveIncludesRunPath(claimID, reportName, path)
}

func retireVerifyArchive(ctx context.Context, working, remote, ref, expectedSHA string, expected retireArchiveManifest) error {
	return worktreeretire.VerifyArchive(ctx, working, remote, ref, expectedSHA, expected, retireArchivePorts())
}

func validateRetireArchiveManifest(expected, actual retireArchiveManifest) ([]string, error) {
	return worktreeretire.ValidateArchiveManifest(expected, actual)
}

func validateRetireArchiveTree(files map[string]string, paths, listed []string) error {
	return worktreeretire.ValidateArchiveTree(files, paths, listed)
}

func retireArchiveManifestPreserve(manifest retireArchiveManifest) string {
	return worktreeretire.ArchiveManifestPreserve(manifest)
}

func retireGitObjectSHA(ctx context.Context, directory, object string) (string, error) {
	return worktreeretire.GitObjectSHA(ctx, directory, object)
}

func retireGitBytes(ctx context.Context, directory string, args ...string) ([]byte, error) {
	return worktreeretire.GitBytes(ctx, directory, args...)
}
