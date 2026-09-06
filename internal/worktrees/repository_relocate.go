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
	// beforeReplacementRetirement is a test-only seam after the transferred
	// repository is fully verified and before WB retires the exact disposable
	// destination clone held in quarantine.
	beforeReplacementRetirement func() error
	// beforeReplacementCleanupCompleted is a test-only seam after secure
	// retirement but before its immutable terminal evidence is appended.
	beforeReplacementCleanupCompleted func() error
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
	result.SourceDir, err = CanonicalRepositoryPath(options.ProjectsRoot, options.SourceRepository)
	if err != nil {
		return result, err
	}
	result.DestinationDir, err = CanonicalRepositoryPath(options.ProjectsRoot, options.DestinationRepository)
	if err != nil {
		return result, err
	}
	canonical, err := openCanonicalRepository(result.SourceDir)
	if err != nil {
		return result, fmt.Errorf("open source canonical repository: %w", err)
	}
	defer canonical.close()
	lock, err := acquireRepositoryRegistrationLock(canonical)
	if err != nil {
		return result, fmt.Errorf("lock source repository registration: %w", err)
	}
	defer func() { _ = lock.release() }()

	fetchURLs, err := exactOriginURLs(ctx, result.SourceDir, false)
	if err != nil || len(fetchURLs) != 1 {
		return result, fmt.Errorf("source origin fetch URL is ambiguous")
	}
	pushURLs, err := exactOriginURLs(ctx, result.SourceDir, true)
	if err != nil || len(pushURLs) != 1 {
		return result, fmt.Errorf("source origin push URL is ambiguous")
	}
	parsedSource, err := gitremote.Parse(fetchURLs[0])
	if err != nil || parsedSource.Identity.Repository != options.SourceRepository {
		return result, fmt.Errorf("source origin does not identify %s", options.SourceRepository)
	}
	result.SourceFetchURL = fetchURLs[0]
	result.SourcePushURL = pushURLs[0]

	worktrees, err := repositoryRelocateWorktrees(ctx, result.SourceDir, result.DestinationDir)
	if err != nil {
		return result, err
	}
	result.Worktrees = make([]string, 0, len(worktrees))
	for index := range worktrees {
		entry := &worktrees[index]
		result.Worktrees = append(result.Worktrees, entry.destination)
		clean, cleanErr := cleanWorktree(ctx, entry.source)
		if cleanErr != nil {
			return result, fmt.Errorf("inspect worktree %s: %w", entry.source, cleanErr)
		}
		if !clean {
			result.Reason = "worktree has local changes: " + entry.source
			return result, nil
		}
	}
	destinationExists := false
	if _, statErr := os.Lstat(result.DestinationDir); statErr == nil {
		destinationExists = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return result, fmt.Errorf("inspect repository destination: %w", statErr)
	}
	expectedRemoteHead, err := remoteDefaultHead(ctx, result.SourceDir, options.RemoteURL, options.DefaultBranch)
	if err != nil {
		return result, fmt.Errorf("verify destination remote: %w", err)
	}
	if destinationExists {
		if reason := disposableDestinationReason(ctx, result.DestinationDir, options, expectedRemoteHead); reason != "" {
			result.Reason = "destination is not safely replaceable: " + reason
			return result, nil
		}
		result.RetiredDestinationDir = filepath.Join(filepath.Dir(result.DestinationDir), ".wb-replaced-"+filepath.Base(result.DestinationDir)+"-"+expectedRemoteHead[:12])
		if _, statErr := os.Lstat(result.RetiredDestinationDir); !errors.Is(statErr, os.ErrNotExist) {
			result.Reason = "replacement quarantine already exists: " + result.RetiredDestinationDir
			return result, nil
		}
	}
	home, err := wbhome.Root(options.ProjectsRoot)
	if err != nil {
		return result, err
	}
	for index := range worktrees {
		entry := &worktrees[index]
		claim, _, _, claimErr := activeWorkLogClaim(home, entry.source)
		switch {
		case claimErr == nil:
			entry.claim = &claim
		case errors.Is(claimErr, errWorkLogProjectionNotFound):
		default:
			result.Reason = "WB claim is ambiguous for " + entry.source + ": " + claimErr.Error()
			return result, nil
		}
	}
	result.Eligible = true
	if !options.Apply {
		return result, nil
	}

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
		if reason := disposableDestinationReason(ctx, result.DestinationDir, options, expectedRemoteHead); reason != "" {
			return result, fmt.Errorf("repository destination safety changed after planning: %s", reason)
		}
	}

	for index := range worktrees {
		entry := &worktrees[index]
		if entry.claim == nil {
			continue
		}
		intent, _, intentErr := appendRelocationIntentForRepository(home, *entry.claim, entry.source, entry.destination, "repository", entry.head,
			options.SourceRepository, options.DestinationRepository, options.RemoteURL, options.Now().UTC())
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
	moved, err := moveRenameDirectory(result.SourceDir, result.DestinationDir, nil)
	if err != nil {
		return result, restoreReplacement(fmt.Errorf("move canonical repository: %w", err))
	}
	_ = moved.Close()
	rollback := func(cause error) error {
		_, _ = git(ctx, result.DestinationDir, "remote", "set-url", "origin", fetchURLs[0])
		_, _ = git(ctx, result.DestinationDir, "remote", "set-url", "--push", "origin", pushURLs[0])
		_, _ = moveRenameDirectory(result.DestinationDir, result.SourceDir, nil)
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
	for index := range worktrees {
		entry := &worktrees[index]
		head, headErr := git(ctx, entry.destination, "rev-parse", "HEAD")
		if headErr != nil || head != entry.head {
			return result, rollback(fmt.Errorf("verify relocated worktree %s", entry.destination))
		}
		_, common, commonErr := gitDirectories(ctx, entry.destination)
		if commonErr != nil || filepath.Clean(common) != filepath.Join(result.DestinationDir, ".git") {
			return result, rollback(fmt.Errorf("verify relocated Git administration for %s", entry.destination))
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

func exactOriginURLs(ctx context.Context, repository string, push bool) ([]string, error) {
	args := []string{"remote", "get-url", "--all"}
	if push {
		args = append(args, "--push")
	}
	args = append(args, "origin")
	out, err := gitRawOutput(ctx, repository, args...)
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
	out, err := gitRawOutput(ctx, source, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var entries []repositoryRelocateWorktree
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "worktree ") {
			continue
		}
		path := strings.TrimPrefix(line, "worktree ")
		mapped := path
		if path == source || strings.HasPrefix(path, source+string(os.PathSeparator)) {
			relative, relErr := filepath.Rel(source, path)
			if relErr != nil {
				return nil, relErr
			}
			mapped = filepath.Join(destination, relative)
		}
		head, headErr := git(ctx, path, "rev-parse", "HEAD")
		if headErr != nil {
			return nil, headErr
		}
		entries = append(entries, repositoryRelocateWorktree{source: path, destination: mapped, head: head})
	}
	if len(entries) == 0 || entries[0].source != source {
		return nil, fmt.Errorf("canonical repository is absent from its worktree registry")
	}
	return entries, nil
}

func remoteDefaultHead(ctx context.Context, repository, remoteURL, defaultBranch string) (string, error) {
	out, err := gitRawOutput(ctx, repository, "ls-remote", "--symref", "--", remoteURL, "HEAD", "refs/heads/"+defaultBranch)
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

func disposableDestinationReason(ctx context.Context, destination string, options RepositoryRelocateOptions, expectedHead string) string {
	canonical, err := openCanonicalRepository(destination)
	if err != nil {
		return err.Error()
	}
	defer canonical.close()
	urls, err := exactOriginURLs(ctx, destination, false)
	if err != nil || len(urls) != 1 {
		return "origin fetch URL is ambiguous"
	}
	remote, err := gitremote.Parse(urls[0])
	if err != nil || remote.Identity.Repository != options.DestinationRepository {
		return "origin does not identify the destination repository"
	}
	push, err := exactOriginURLs(ctx, destination, true)
	if err != nil || len(push) != 1 {
		return "origin push URL is ambiguous"
	}
	pushRemote, err := gitremote.Parse(push[0])
	if err != nil || pushRemote.Identity.Repository != options.DestinationRepository {
		return "push origin does not identify the destination repository"
	}
	if _, err := git(ctx, destination, "fetch", "--prune", "--tags", "origin"); err != nil {
		return "cannot refresh destination from origin"
	}
	status, err := gitops.Status(destination)
	if err != nil {
		return "cannot inspect destination status"
	}
	if status.Dirty() {
		return status.Summary()
	}
	worktrees, err := repositoryRelocateWorktrees(ctx, destination, destination)
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
	branches, err := git(ctx, destination, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return "cannot inspect destination branches"
	}
	for _, local := range strings.Fields(branches) {
		localHead, localErr := git(ctx, destination, "rev-parse", "refs/heads/"+local)
		remoteHead, remoteErr := git(ctx, destination, "rev-parse", "refs/remotes/origin/"+local)
		if localErr != nil || remoteErr != nil || localHead != remoteHead {
			return "destination has a local-only or unpushed branch " + local
		}
	}
	tags, err := git(ctx, destination, "for-each-ref", "--format=%(refname:short)", "refs/tags")
	if err != nil {
		return "cannot inspect destination tags"
	}
	for _, tag := range strings.Fields(tags) {
		local, localErr := git(ctx, destination, "rev-parse", "refs/tags/"+tag)
		remoteOutput, remoteErr := git(ctx, destination, "ls-remote", "--tags", "origin", "refs/tags/"+tag)
		fields := strings.Fields(remoteOutput)
		if localErr != nil || remoteErr != nil || len(fields) != 2 || fields[0] != local {
			return "destination has a local-only or changed tag " + tag
		}
	}
	return ""
}
