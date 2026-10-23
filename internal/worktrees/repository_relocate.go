package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// RepositoryRelocateOptions describes a GitHub repository rename or transfer.
// SourceRepository is the identity encoded by the current projects-root path;
// DestinationRepository and RemoteURL are the canonical identity returned by
// GitHub after following the old repository URL.
type RepositoryRelocateOptions struct {
	ProjectsRoot          string
	SourceRepository      string
	DestinationRepository string
	RemoteURL             string
	DefaultBranch         string
	Apply                 bool
	Now                   func() time.Time
	// OnCleanupPending durably checkpoints the exact immutable cleanup receipt
	// before WB moves the source repository. Returning an error aborts the move
	// and restores the quarantined destination clone.
	OnCleanupPending func(receiptPath, recoveryCommand string) error
	// beforeReplacementRetirement is a test-only seam after the transferred
	// repository is fully verified and before WB retires the exact disposable
	// destination clone held in quarantine.
	beforeReplacementRetirement func() error
	// beforeReplacementCleanupCompleted is a test-only seam after secure
	// retirement but before its immutable terminal evidence is appended.
	beforeReplacementCleanupCompleted func() error
	// beforeWorkLogCompletion is a test-only seam after Git relocation is
	// verified and before each pending Work Log intent is completed.
	beforeWorkLogCompletion func(string) error
}

type RepositoryRelocateResult struct {
	SourceRepository          string   `json:"source_repository"`
	DestinationRepository     string   `json:"destination_repository"`
	SourceDir                 string   `json:"source_dir"`
	DestinationDir            string   `json:"destination_dir"`
	RemoteURL                 string   `json:"remote_url"`
	SourceFetchURL            string   `json:"source_fetch_url"`
	SourcePushURL             string   `json:"source_push_url"`
	DefaultBranch             string   `json:"default_branch"`
	Worktrees                 []string `json:"worktrees,omitempty"`
	ReceiptPaths              []string `json:"receipt_paths,omitempty"`
	RetiredDestinationDir     string   `json:"retired_destination_dir,omitempty"`
	ReplacementCleanupReceipt string   `json:"replacement_cleanup_receipt,omitempty"`
	ReplacementCleanupStatus  string   `json:"replacement_cleanup_status,omitempty"`
	CleanupPending            bool     `json:"cleanup_pending,omitempty"`
	RecoveryCommand           string   `json:"recovery_command,omitempty"`
	Eligible                  bool     `json:"eligible"`
	Applied                   bool     `json:"applied"`
	Reason                    string   `json:"reason,omitempty"`
}

type repositoryRelocateWorktree struct {
	source, destination, head string
	claim                     *workLogClaim
	intent                    *workLogRelocationIntent
}

// RelocateRepository atomically moves one canonical clone and every nested
// worktree, repairs Git's absolute administrative paths, changes both origin
// directions, and appends path-relocation evidence for active WB claims.
// It refuses any dirty checkout, ambiguous remote, or occupied destination.
func RelocateRepository(ctx context.Context, options RepositoryRelocateOptions) (RepositoryRelocateResult, error) {
	return relocateRepositoryWithReads(ctx, options, CanonicalRepositoryPath, gitRawOutput)
}

func relocateRepositoryWithReads(ctx context.Context, options RepositoryRelocateOptions, canonicalPath func(string, string) (string, error), readOutput repositoryTransferOutput) (RepositoryRelocateResult, error) {
	result := RepositoryRelocateResult{SourceRepository: options.SourceRepository, DestinationRepository: options.DestinationRepository,
		RemoteURL: options.RemoteURL, DefaultBranch: options.DefaultBranch}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.SourceRepository == options.DestinationRepository {
		return result, fmt.Errorf("source and destination repository are identical")
	}
	if _, _, err := splitRepository(options.SourceRepository); err != nil {
		return result, fmt.Errorf("invalid source repository: %w", err)
	}
	destinationOwner, _, err := splitRepository(options.DestinationRepository)
	if err != nil {
		return result, fmt.Errorf("invalid destination repository: %w", err)
	}
	remote, err := gitremote.Parse(options.RemoteURL)
	if err != nil || remote.Identity.Repository != options.DestinationRepository {
		return result, fmt.Errorf("destination remote does not identify %s", options.DestinationRepository)
	}
	if !validBranch(ctx, options.DefaultBranch) {
		return result, fmt.Errorf("invalid destination default branch %q", options.DefaultBranch)
	}
	result.SourceDir, err = canonicalPath(options.ProjectsRoot, options.SourceRepository)
	if err != nil {
		return result, err
	}
	result.DestinationDir, err = canonicalPath(options.ProjectsRoot, options.DestinationRepository)
	if err != nil {
		return result, err
	}
	canonical, err := openCanonicalRepository(result.SourceDir)
	if err != nil {
		return result, fmt.Errorf("open source canonical repository: %w", err)
	}
	defer canonical.close()
	lock, err := acquireRepositoryRegistrationLock(canonical, time.Now, time.Sleep)
	if err != nil {
		return result, fmt.Errorf("lock source repository registration: %w", err)
	}
	defer func() { _ = lock.release() }()

	plan, err := prepareRepositoryTransfer(ctx, options, &result, readOutput)
	if err != nil || !result.Eligible || !options.Apply {
		return result, err
	}
	return applyRepositoryTransfer(ctx, options, result, destinationOwner, plan, readOutput)
}

type repositoryTransferOutput func(context.Context, string, ...string) (string, error)

type repositoryTransferPlan struct {
	worktrees         []repositoryRelocateWorktree
	home, remoteHead  string
	destinationExists bool
}

// prepareRepositoryTransfer only inspects admission. Its caller retains the
// canonical descriptor and registration lock through both inspection and apply.
func prepareRepositoryTransfer(ctx context.Context, options RepositoryRelocateOptions, result *RepositoryRelocateResult, readOutput repositoryTransferOutput) (repositoryTransferPlan, error) {
	fetchURLs, err := exactOriginURLsWithRead(ctx, result.SourceDir, false, readOutput)
	if err != nil || len(fetchURLs) != 1 {
		return repositoryTransferPlan{}, fmt.Errorf("source origin fetch URL is ambiguous")
	}
	pushURLs, err := exactOriginURLsWithRead(ctx, result.SourceDir, true, readOutput)
	if err != nil || len(pushURLs) != 1 {
		return repositoryTransferPlan{}, fmt.Errorf("source origin push URL is ambiguous")
	}
	parsedSource, err := gitremote.Parse(fetchURLs[0])
	if err != nil || parsedSource.Identity.Repository != options.SourceRepository {
		return repositoryTransferPlan{}, fmt.Errorf("source origin does not identify %s", options.SourceRepository)
	}
	result.SourceFetchURL = fetchURLs[0]
	result.SourcePushURL = pushURLs[0]

	worktrees, err := repositoryRelocateWorktreesWithReads(ctx, result.SourceDir, result.DestinationDir, filepath.Rel, readOutput)
	if err != nil {
		return repositoryTransferPlan{}, err
	}
	result.Worktrees = make([]string, 0, len(worktrees))
	for index := range worktrees {
		entry := &worktrees[index]
		result.Worktrees = append(result.Worktrees, entry.destination)
		clean, cleanErr := cleanWorktree(ctx, entry.source)
		if cleanErr != nil {
			return repositoryTransferPlan{}, fmt.Errorf("inspect worktree %s: %w", entry.source, cleanErr)
		}
		if !clean {
			result.Reason = "worktree has local changes: " + entry.source
			return repositoryTransferPlan{}, nil
		}
	}
	destinationExists := false
	if _, statErr := os.Lstat(result.DestinationDir); statErr == nil {
		destinationExists = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return repositoryTransferPlan{}, fmt.Errorf("inspect repository destination: %w", statErr)
	}
	expectedRemoteHead, err := remoteDefaultHeadWithRead(ctx, result.SourceDir, options.RemoteURL, options.DefaultBranch, readOutput)
	if err != nil {
		return repositoryTransferPlan{}, fmt.Errorf("verify destination remote: %w", err)
	}
	if destinationExists {
		if reason := disposableDestinationReason(ctx, result.DestinationDir, options, expectedRemoteHead, readOutput); reason != "" {
			result.Reason = "destination is not safely replaceable: " + reason
			return repositoryTransferPlan{}, nil
		}
		result.RetiredDestinationDir = filepath.Join(filepath.Dir(result.DestinationDir), ".wb-replaced-"+filepath.Base(result.DestinationDir)+"-"+expectedRemoteHead[:12])
		if _, statErr := os.Lstat(result.RetiredDestinationDir); !errors.Is(statErr, os.ErrNotExist) {
			result.Reason = "replacement quarantine already exists: " + result.RetiredDestinationDir
			return repositoryTransferPlan{}, nil
		}
	}
	home, err := wbhome.Root(options.ProjectsRoot)
	if err != nil {
		return repositoryTransferPlan{}, err
	}
	for index := range worktrees {
		entry := &worktrees[index]
		claim, claimErr := optionalRepositoryTransferClaim(home, entry.source)
		if claimErr != nil {
			result.Reason = "WB claim is ambiguous for " + entry.source + ": " + claimErr.Error()
			return repositoryTransferPlan{}, nil
		}
		entry.claim = claim
	}
	result.Eligible = true
	return repositoryTransferPlan{worktrees: worktrees, home: home, remoteHead: expectedRemoteHead, destinationExists: destinationExists}, nil
}

func applyRepositoryTransfer(ctx context.Context, options RepositoryRelocateOptions, result RepositoryRelocateResult, destinationOwner string, plan repositoryTransferPlan, readOutput repositoryTransferOutput) (RepositoryRelocateResult, error) {
	worktrees, home, expectedRemoteHead, destinationExists := plan.worktrees, plan.home, plan.remoteHead, plan.destinationExists
	projects, err := openAbsoluteDirectoryNoFollow(options.ProjectsRoot, false)
	if err != nil {
		return result, fmt.Errorf("open projects root: %w", err)
	}
	ownerFD, err := openOrCreateNoFollowDirectory(int(projects.Fd()), destinationOwner)
	_ = projects.Close()
	if err != nil {
		return result, fmt.Errorf("prepare destination owner: %w", err)
	}
	_ = os.NewFile(uintptr(ownerFD), "repository-transfer-owner").Close()
	if _, statErr := os.Lstat(result.DestinationDir); destinationExists && statErr != nil || !destinationExists && !errors.Is(statErr, os.ErrNotExist) {
		return result, fmt.Errorf("repository destination changed after planning")
	}
	if destinationExists {
		if reason := disposableDestinationReason(ctx, result.DestinationDir, options, expectedRemoteHead, readOutput); reason != "" {
			return result, fmt.Errorf("repository destination safety changed after planning: %s", reason)
		}
	}

	for index := range worktrees {
		entry := &worktrees[index]
		if entry.claim == nil {
			continue
		}
		intent, _, intentErr := appendRelocationIntentForRepository(home, *entry.claim, entry.source, entry.destination, "repository", entry.head,
			options.SourceRepository, options.DestinationRepository, options.RemoteURL, relocationPlacementRecord{}, options.Now().UTC())
		if intentErr != nil {
			return result, fmt.Errorf("record repository relocation intent for %s: %w", entry.source, intentErr)
		}
		entry.intent = intent
	}
	var replacement *os.File
	var cleanupIntent repositoryTransferCleanupReceipt
	if destinationExists {
		retired, retireErr := moveRenameDirectory(result.DestinationDir, result.RetiredDestinationDir, nil)
		if retireErr != nil {
			return result, fmt.Errorf("temporarily quarantine disposable destination: %w", retireErr)
		}
		replacement = retired
		defer func() { _ = replacement.Close() }()
		cleanupIntent, result.ReplacementCleanupReceipt, err = recordRepositoryTransferCleanupIntent(options, result, expectedRemoteHead, replacement)
		if err != nil {
			restored, restoreErr := moveRenameDirectory(result.RetiredDestinationDir, result.DestinationDir, nil)
			if restored != nil {
				_ = restored.Close()
			}
			return result, errors.Join(fmt.Errorf("record replacement cleanup intent: %w", err), restoreErr)
		}
		result.ReplacementCleanupStatus = repositoryTransferCleanupPending
		result.RecoveryCommand = repositoryTransferCleanupCommand(options.ProjectsRoot, result.ReplacementCleanupReceipt)
	}
	restoreReplacement := func(cause error) error {
		if replacement == nil {
			return cause
		}
		restored, restoreErr := moveRenameDirectory(result.RetiredDestinationDir, result.DestinationDir, nil)
		if restored != nil {
			_ = restored.Close()
		}
		if restoreErr != nil {
			return errors.Join(cause, fmt.Errorf("restore quarantined destination: %w", restoreErr))
		}
		if _, receiptErr := recordRepositoryTransferCleanupTerminal(options.ProjectsRoot, cleanupIntent, repositoryTransferCleanupRestored, options.Now().UTC()); receiptErr != nil {
			return errors.Join(cause, fmt.Errorf("record restored replacement: %w", receiptErr))
		}
		return cause
	}
	if replacement != nil && options.OnCleanupPending != nil {
		if err := options.OnCleanupPending(result.ReplacementCleanupReceipt, result.RecoveryCommand); err != nil {
			return result, restoreReplacement(fmt.Errorf("checkpoint replacement cleanup: %w", err))
		}
	}
	moved, err := moveRenameDirectory(result.SourceDir, result.DestinationDir, nil)
	if err != nil {
		return result, restoreReplacement(fmt.Errorf("move canonical repository: %w", err))
	}
	_ = moved.Close()
	rollback := func(cause error) error {
		_, _ = git(ctx, result.DestinationDir, "remote", "set-url", "origin", result.SourceFetchURL)
		_, _ = git(ctx, result.DestinationDir, "remote", "set-url", "--push", "origin", result.SourcePushURL)
		restored, _ := moveRenameDirectory(result.DestinationDir, result.SourceDir, nil)
		if restored != nil {
			_ = restored.Close()
		}
		oldPaths := make([]string, 0, len(worktrees))
		for _, entry := range worktrees {
			oldPaths = append(oldPaths, entry.source)
		}
		_, _ = git(ctx, result.SourceDir, append([]string{"worktree", "repair"}, oldPaths...)...)
		return restoreReplacement(cause)
	}
	if _, err := git(ctx, result.DestinationDir, "remote", "set-url", "origin", options.RemoteURL); err != nil {
		return result, rollback(err)
	}
	if _, err := git(ctx, result.DestinationDir, "remote", "set-url", "--push", "origin", options.RemoteURL); err != nil {
		return result, rollback(err)
	}
	paths := make([]string, 0, len(worktrees))
	for _, entry := range worktrees {
		paths = append(paths, entry.destination)
	}
	if _, err := git(ctx, result.DestinationDir, append([]string{"worktree", "repair"}, paths...)...); err != nil {
		return result, rollback(err)
	}
	if _, err := git(ctx, result.DestinationDir, "fetch", "--prune", "origin", "+refs/heads/"+options.DefaultBranch+":refs/remotes/origin/"+options.DefaultBranch); err != nil {
		return result, rollback(fmt.Errorf("fetch destination default branch: %w", err))
	}
	if fetched, fetchErr := git(ctx, result.DestinationDir, "rev-parse", "refs/remotes/origin/"+options.DefaultBranch); fetchErr != nil || fetched != expectedRemoteHead {
		return result, rollback(fmt.Errorf("fetched destination default branch does not match verified remote head"))
	}
	verifyMovedWorktree := func(entry *repositoryRelocateWorktree) error {
		head, headErr := git(ctx, entry.destination, "rev-parse", "HEAD")
		if headErr != nil || head != entry.head {
			return fmt.Errorf("verify relocated worktree %s", entry.destination)
		}
		_, common, commonErr := gitDirectories(ctx, entry.destination)
		if commonErr != nil || filepath.Clean(common) != filepath.Join(result.DestinationDir, ".git") {
			return fmt.Errorf("verify relocated Git administration for %s", entry.destination)
		}
		return nil
	}
	// No immutable Work Log receipt may be published until every moved
	// worktree has passed verification. Failures in this phase can still
	// restore the source repository without contradicting any receipt.
	for index := range worktrees {
		if err := verifyMovedWorktree(&worktrees[index]); err != nil {
			return result, rollback(err)
		}
	}
	// Once publication starts, an error must leave the repository at its
	// destination for pending-intent recovery. Recheck every worktree, including
	// unclaimed ones, after any per-claim hook so drift since the first pass is
	// still detected without rolling a published receipt back to the source.
	for index := range worktrees {
		entry := &worktrees[index]
		if entry.claim != nil && entry.intent != nil && options.beforeWorkLogCompletion != nil {
			if err := options.beforeWorkLogCompletion(entry.destination); err != nil {
				return result, fmt.Errorf("record repository relocation receipt for %s: %w", entry.destination, err)
			}
		}
		if err := verifyMovedWorktree(entry); err != nil {
			return result, err
		}
		if entry.claim != nil && entry.intent != nil {
			_, receipt, receiptErr := appendRelocationReceipt(home, *entry.claim, entry.intent, options.Now().UTC())
			if receiptErr != nil {
				return result, fmt.Errorf("record repository relocation receipt for %s: %w", entry.destination, receiptErr)
			}
			result.ReceiptPaths = append(result.ReceiptPaths, receipt)
		}
	}
	result.Applied = true
	if replacement != nil {
		if options.beforeReplacementRetirement != nil {
			if err := options.beforeReplacementRetirement(); err != nil {
				result.CleanupPending = true
				result.Reason = "repository transfer completed; replacement cleanup_pending: " + err.Error()
				return result, nil
			}
		}
		if err := retireRepositoryTransferReplacement(result.RetiredDestinationDir, replacement); err != nil {
			result.CleanupPending = true
			result.Reason = "repository transfer completed; replacement cleanup_pending: " + err.Error()
			return result, nil
		}
		if options.beforeReplacementCleanupCompleted != nil {
			if err := options.beforeReplacementCleanupCompleted(); err != nil {
				result.CleanupPending = true
				result.Reason = "repository transfer and replacement retirement completed; cleanup evidence_pending: " + err.Error()
				return result, nil
			}
		}
		completedPath, err := recordRepositoryTransferCleanupCompleted(options.ProjectsRoot, cleanupIntent, options.Now().UTC())
		if err != nil {
			result.CleanupPending = true
			result.Reason = "repository transfer and replacement retirement completed; cleanup evidence_pending: " + err.Error()
			return result, nil
		}
		result.ReplacementCleanupReceipt = completedPath
		result.ReplacementCleanupStatus = repositoryTransferCleanupRetired
		result.RecoveryCommand = ""
	}
	return result, nil
}

// FinalizeRepositoryTransferWorkLogs completes durable relocation intents left
// after the repository and its linked worktrees moved successfully. Every
// pending intent must bind the exact transfer identities and live destination
// origin before WB appends its immutable completion.
func FinalizeRepositoryTransferWorkLogs(ctx context.Context, options RepositoryRelocateOptions) ([]string, error) {
	return finalizeRepositoryTransferWorkLogsWithPending(ctx, options, pendingRelocationIntent)
}

func finalizeRepositoryTransferWorkLogsWithPending(ctx context.Context, options RepositoryRelocateOptions, pending func(string, workLogClaim, string, string, string) (*workLogRelocationIntent, string, error)) ([]string, error) {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	destination, err := CanonicalRepositoryPath(options.ProjectsRoot, options.DestinationRepository)
	if err != nil {
		return nil, err
	}
	entries, err := repositoryRelocateWorktrees(ctx, destination, destination)
	if err != nil {
		return nil, err
	}
	home, err := wbhome.Root(options.ProjectsRoot)
	if err != nil {
		return nil, err
	}
	var receipts []string
	for _, entry := range entries {
		claim, claimErr := optionalRepositoryTransferClaim(home, entry.destination)
		if claimErr != nil {
			return nil, fmt.Errorf("resolve transferred Work Log claim for %s: %w", entry.destination, claimErr)
		}
		if claim == nil {
			continue
		}
		intent, _, intentErr := pending(home, *claim, entry.destination, claim.Branch, entry.head)
		if intentErr != nil {
			return nil, intentErr
		}
		if intent == nil {
			continue
		}
		if intent.To != "repository" || intent.SourceRepository != options.SourceRepository ||
			intent.DestinationRepository != options.DestinationRepository || intent.RemoteURL != options.RemoteURL {
			return nil, fmt.Errorf("pending Work Log relocation intent does not match repository transfer")
		}
		if err := corroborateRepositoryRelocation(ctx, entry.destination, options.DestinationRepository); err != nil {
			return nil, err
		}
		_, receiptPath, receiptErr := appendRelocationReceipt(home, *claim, intent, now().UTC())
		if receiptErr != nil {
			return nil, fmt.Errorf("complete transferred Work Log intent for %s: %w", entry.destination, receiptErr)
		}
		receipts = append(receipts, receiptPath)
	}
	return receipts, nil
}

func optionalRepositoryTransferClaim(home, worktree string) (*workLogClaim, error) {
	claim, _, _, err := activeWorkLogClaim(home, worktree)
	if errors.Is(err, errWorkLogProjectionNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &claim, nil
}

func exactOriginURLs(ctx context.Context, repository string, push bool) ([]string, error) {
	return exactOriginURLsWithRead(ctx, repository, push, gitRawOutput)
}

func exactOriginURLsWithRead(ctx context.Context, repository string, push bool, readOutput repositoryTransferOutput) ([]string, error) {
	args := []string{"remote", "get-url", "--all"}
	if push {
		args = append(args, "--push")
	}
	args = append(args, "origin")
	out, err := readOutput(ctx, repository, args...)
	if err != nil {
		return nil, err
	}
	var urls []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			urls = append(urls, line)
		}
	}
	return urls, nil
}

func repositoryRelocateWorktrees(ctx context.Context, source, destination string) ([]repositoryRelocateWorktree, error) {
	return repositoryRelocateWorktreesWithReads(ctx, source, destination, filepath.Rel, gitRawOutput)
}

func repositoryRelocateWorktreesWithReads(ctx context.Context, source, destination string, relativePath func(string, string) (string, error), readOutput repositoryTransferOutput) ([]repositoryRelocateWorktree, error) {
	out, err := readOutput(ctx, source, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var entries []repositoryRelocateWorktree
	for _, path := range worktreePathsFromPorcelain(out) {
		mapped := path
		if path == source || strings.HasPrefix(path, source+string(os.PathSeparator)) {
			relative, relErr := relativePath(source, path)
			if relErr != nil {
				return nil, relErr
			}
			mapped = filepath.Join(destination, relative)
		}
		// An unborn HEAD (no commit yet — a freshly initialized repository
		// with nothing committed) is not a refusal: a directory rename moves
		// it exactly as safely as any other worktree. head stays "" for it;
		// callers that need to verify a specific commit survived the move
		// (RelocateRepository's remote-head check) already require real
		// history to exist for other reasons.
		head, headErr := git(ctx, path, "rev-parse", "HEAD")
		if headErr != nil {
			if _, unbornErr := git(ctx, path, "symbolic-ref", "--quiet", "HEAD"); unbornErr != nil {
				return nil, headErr
			}
			head = ""
		}
		entries = append(entries, repositoryRelocateWorktree{source: path, destination: mapped, head: head})
	}
	if len(entries) == 0 || entries[0].source != source {
		return nil, fmt.Errorf("canonical repository is absent from its worktree registry")
	}
	return entries, nil
}

func remoteDefaultHead(ctx context.Context, repository, remoteURL, defaultBranch string) (string, error) {
	return remoteDefaultHeadWithRead(ctx, repository, remoteURL, defaultBranch, gitRawOutput)
}

func remoteDefaultHeadWithRead(ctx context.Context, repository, remoteURL, defaultBranch string, readOutput repositoryTransferOutput) (string, error) {
	out, err := readOutput(ctx, repository, "ls-remote", "--symref", "--", remoteURL, "HEAD", "refs/heads/"+defaultBranch)
	if err != nil {
		return "", err
	}
	var symbolic, head string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "ref:" && fields[2] == "HEAD" {
			symbolic = strings.TrimPrefix(fields[1], "refs/heads/")
		}
		if len(fields) == 2 && fields[1] == "refs/heads/"+defaultBranch {
			head = fields[0]
		}
	}
	if symbolic != defaultBranch || !isGitObjectID(head) {
		return "", fmt.Errorf("remote HEAD does not name expected default branch %s", defaultBranch)
	}
	return head, nil
}

func disposableDestinationReason(ctx context.Context, destination string, options RepositoryRelocateOptions, expectedHead string, readOutput repositoryTransferOutput) string {
	canonical, err := openCanonicalRepository(destination)
	if err != nil {
		return err.Error()
	}
	defer canonical.close()
	urls, err := exactOriginURLsWithRead(ctx, destination, false, readOutput)
	if err != nil || len(urls) != 1 {
		return "origin fetch URL is ambiguous"
	}
	remote, err := gitremote.Parse(urls[0])
	if err != nil || remote.Identity.Repository != options.DestinationRepository {
		return "origin does not identify the destination repository"
	}
	push, err := exactOriginURLsWithRead(ctx, destination, true, readOutput)
	if err != nil || len(push) != 1 {
		return "origin push URL is ambiguous"
	}
	pushRemote, err := gitremote.Parse(push[0])
	if err != nil || pushRemote.Identity.Repository != options.DestinationRepository {
		return "push origin does not identify the destination repository"
	}
	status, err := gitops.Status(destination)
	if err != nil {
		return "cannot inspect destination status"
	}
	if status.Dirty() {
		return status.Summary()
	}
	worktrees, err := repositoryRelocateWorktreesWithReads(ctx, destination, destination, filepath.Rel, readOutput)
	if err != nil || len(worktrees) != 1 {
		return "destination has linked worktrees"
	}
	branch, err := git(ctx, destination, "branch", "--show-current")
	if err != nil || branch != options.DefaultBranch {
		return "destination is not on the expected default branch"
	}
	head, err := git(ctx, destination, "rev-parse", "HEAD")
	if err != nil || head != expectedHead {
		return "destination HEAD differs from the canonical remote default branch"
	}
	remoteOutput, err := readOutput(ctx, destination, "ls-remote", "--refs", "origin")
	if err != nil {
		return "cannot inspect destination remote refs"
	}
	remoteRefs := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(remoteOutput), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !isGitObjectID(fields[0]) || !strings.HasPrefix(fields[1], "refs/") {
			return "destination remote returned an invalid ref advertisement"
		}
		remoteRefs[fields[1]] = fields[0]
	}
	localOutput, err := readOutput(ctx, destination, "for-each-ref", "--format=%(refname) %(objectname)")
	if err != nil {
		return "cannot inspect destination refs"
	}
	for _, line := range strings.Split(strings.TrimSpace(localOutput), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !isGitObjectID(fields[1]) {
			return "destination contains an invalid local ref"
		}
		ref, object := fields[0], fields[1]
		switch {
		case strings.HasPrefix(ref, "refs/remotes/origin/"):
			// Remote-tracking refs are derived cache state. All other local refs
			// must either match the live remote advertisement or be refused.
		case strings.HasPrefix(ref, "refs/heads/"):
			if remoteRefs[ref] != object {
				return "destination has a local-only or unpushed branch " + strings.TrimPrefix(ref, "refs/heads/")
			}
		case strings.HasPrefix(ref, "refs/tags/"):
			if remoteRefs[ref] != object {
				return "destination has a local-only or changed tag " + strings.TrimPrefix(ref, "refs/tags/")
			}
		default:
			return "destination has unsupported local ref " + ref
		}
	}
	return ""
}
