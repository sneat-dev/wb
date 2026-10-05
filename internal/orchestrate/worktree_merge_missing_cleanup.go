package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// AcknowledgeMissingWorktreeMergeCleanup records independently reproducible
// evidence for legacy cleanup which removed every exact asset but lost one or
// more terminal Work Logs. It never creates replacement Work Log evidence.
func AcknowledgeMissingWorktreeMergeCleanup(ctx context.Context, options WorktreeMergeMissingCleanupAcknowledgementOptions) (WorktreeMergeMissingCleanupAcknowledgement, error) {
	return acknowledgeMissingWorktreeMergeCleanup(ctx, options, defaultRunner, readWorktreeMergeReceipt, worktreeMergeReceiptSHA256, persistMissingCleanupAcknowledgement)
}

func acknowledgeMissingWorktreeMergeCleanup(ctx context.Context, options WorktreeMergeMissingCleanupAcknowledgementOptions, run runner.Runner, readReceipt func(string) (WorktreeMergeReceipt, error), hashReceipt func(string) (string, error), persist func(string, WorktreeMergeMissingCleanupAcknowledgement) error) (WorktreeMergeMissingCleanupAcknowledgement, error) {
	receiptPath, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return WorktreeMergeMissingCleanupAcknowledgement{}, err
	}
	receipt, err := readReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeMissingCleanupAcknowledgement{}, err
	}
	if receipt.Lane == "" {
		return WorktreeMergeMissingCleanupAcknowledgement{}, fmt.Errorf("receipt %s has no lane identity", receiptPath)
	}
	if options.Apply && (strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "") {
		return WorktreeMergeMissingCleanupAcknowledgement{}, errors.New("--actor and --reason are required with --apply")
	}
	lock, err := AcquireOperationLock(options.ProjectsRoot, receipt.Lane, true)
	if err != nil {
		return WorktreeMergeMissingCleanupAcknowledgement{}, err
	}
	defer func() { _ = lock.Release() }()
	receipt, err = readReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeMissingCleanupAcknowledgement{}, err
	}
	ack, err := inspectMissingWorktreeMergeCleanup(ctx, run, hashReceipt, options.ProjectsRoot, receipt, strings.TrimSpace(options.Actor), strings.TrimSpace(options.Reason), 0, 0)
	if err != nil {
		return WorktreeMergeMissingCleanupAcknowledgement{}, err
	}
	if !options.Apply {
		return ack, nil
	}
	if err := persist(ack.AcknowledgementPath, ack); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return WorktreeMergeMissingCleanupAcknowledgement{}, err
		}
		existing, readErr := validateMissingCleanupAcknowledgementWithRunner(ctx, run, hashReceipt, options.ProjectsRoot, receipt, ack.AcknowledgementPath, 0, 0)
		if readErr != nil {
			return WorktreeMergeMissingCleanupAcknowledgement{}, readErr
		}
		return existing, nil
	}
	return ack, nil
}

func inspectMissingWorktreeMergeCleanup(ctx context.Context, run runner.Runner, hashReceipt func(string) (string, error), projectsRoot string, receipt WorktreeMergeReceipt, actor, reason string, timeout time.Duration, retry int) (WorktreeMergeMissingCleanupAcknowledgement, error) {
	if receipt.SchemaVersion != WorktreeMergeSchemaVersion || receipt.Phase != WorktreeMergePhaseLand || receipt.Status != WorktreeMergeLanded ||
		!receipt.Cleanup || receipt.ID == "" || receipt.Lane == "" || receipt.Repository == "" || receipt.Target == "" || receipt.LandingSHA == "" ||
		receipt.ID != worktreeMergeOperationID(receipt.Lane, receipt.Sources) || receipt.Candidate.Task != receipt.ID {
		return WorktreeMergeMissingCleanupAcknowledgement{}, fmt.Errorf("receipt %s is not an exact landed cleanup-pending receipt", receipt.ReceiptPath)
	}
	if receipt.Checks.Status != PullRequestWaitPassed || (receipt.CanonicalSync != "fast_forwarded" && receipt.CanonicalSync != "not_checked_out") {
		return WorktreeMergeMissingCleanupAcknowledgement{}, fmt.Errorf("receipt %s lacks completed exact checks or canonical synchronization", receipt.ReceiptPath)
	}
	assets, err := terminalWorkLogExpectations(receipt)
	if err != nil {
		return WorktreeMergeMissingCleanupAcknowledgement{}, err
	}
	for _, asset := range assets {
		if _, statErr := os.Lstat(asset.Worktree); statErr == nil {
			return WorktreeMergeMissingCleanupAcknowledgement{}, fmt.Errorf("missing-cleanup acknowledgement refuses task %s because worktree %s remains", asset.Task, asset.Worktree)
		} else if !os.IsNotExist(statErr) {
			return WorktreeMergeMissingCleanupAcknowledgement{}, fmt.Errorf("inspect receipted cleanup worktree %s: %w", asset.Worktree, statErr)
		}
	}
	canonical, canonicalErr := worktrees.CanonicalRepositoryPath(projectsRoot, receipt.Repository)
	if canonicalErr != nil {
		return WorktreeMergeMissingCleanupAcknowledgement{}, canonicalErr
	}
	currentTarget, err := fetchExactMergeTargetWithRunner(ctx, run, canonical, receipt.Target)
	if err != nil {
		return WorktreeMergeMissingCleanupAcknowledgement{}, err
	}
	contains, err := isMergeAncestorWithRunner(ctx, run, canonical, receipt.LandingSHA, currentTarget)
	if err != nil || !contains {
		if err == nil {
			err = fmt.Errorf("exact current remote target %s does not contain receipted landing %s", currentTarget, receipt.LandingSHA)
		}
		return WorktreeMergeMissingCleanupAcknowledgement{}, err
	}
	if err := requireTerminalCleanupBranchesAbsentWithRunner(ctx, run, projectsRoot, receipt, assets, timeout, retry); err != nil {
		return WorktreeMergeMissingCleanupAcknowledgement{}, err
	}
	receiptHash, err := hashReceipt(receipt.ReceiptPath)
	if err != nil {
		return WorktreeMergeMissingCleanupAcknowledgement{}, err
	}
	ack := WorktreeMergeMissingCleanupAcknowledgement{
		SchemaVersion: worktreeMergeMissingCleanupAcknowledgementSchemaVersion,
		Status:        "missing_cleanup_acknowledged", ReceiptPath: receipt.ReceiptPath,
		AcknowledgementPath: receipt.ReceiptPath + worktreeMergeMissingCleanupAcknowledgementSuffix,
		ReceiptSHA256:       receiptHash, ReceiptID: receipt.ID, Lane: receipt.Lane,
		Repository: receipt.Repository, Target: receipt.Target, LandingSHA: receipt.LandingSHA,
		CurrentTargetSHA: currentTarget, Assets: append([]worktrees.TerminalWorkLogExpectation(nil), assets...),
		Actor: actor, Reason: reason, RecordedAt: time.Now().UTC(),
	}
	ack.ID = missingCleanupAcknowledgementID(ack)
	return ack, nil
}

func validateMissingCleanupAcknowledgement(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, path string, timeout time.Duration, retry int) (WorktreeMergeMissingCleanupAcknowledgement, error) {
	return validateMissingCleanupAcknowledgementWithRunner(ctx, defaultRunner, worktreeMergeReceiptSHA256, projectsRoot, receipt, path, timeout, retry)
}

func validateMissingCleanupAcknowledgementWithRunner(ctx context.Context, run runner.Runner, hashReceipt func(string) (string, error), projectsRoot string, receipt WorktreeMergeReceipt, path string, timeout time.Duration, retry int) (WorktreeMergeMissingCleanupAcknowledgement, error) {
	ack, err := readMissingCleanupAcknowledgement(path, receipt)
	if err != nil {
		return ack, err
	}
	observed, err := inspectMissingWorktreeMergeCleanup(ctx, run, hashReceipt, projectsRoot, receipt, ack.Actor, ack.Reason, timeout, retry)
	if err != nil {
		return ack, err
	}
	canonical, canonicalErr := worktrees.CanonicalRepositoryPath(projectsRoot, receipt.Repository)
	if canonicalErr != nil {
		return ack, canonicalErr
	}
	containsAcknowledgedTarget, ancestorErr := isMergeAncestorWithRunner(ctx, run, canonical, ack.CurrentTargetSHA, observed.CurrentTargetSHA)
	if ancestorErr != nil || !containsAcknowledgedTarget {
		if ancestorErr == nil {
			ancestorErr = fmt.Errorf("current remote target %s no longer contains acknowledged target %s", observed.CurrentTargetSHA, ack.CurrentTargetSHA)
		}
		return ack, ancestorErr
	}
	// A forward target advance is benign. Preserve and compare the exact target
	// which the immutable acknowledgement originally observed.
	observed.CurrentTargetSHA = ack.CurrentTargetSHA
	observed.ID = missingCleanupAcknowledgementID(observed)
	if !sameMissingCleanupAcknowledgement(ack, observed) {
		return ack, fmt.Errorf("missing-cleanup acknowledgement %s no longer matches its receipt or absent assets", path)
	}
	return ack, nil
}

// requireTerminalCleanupBranchesAbsent prevents a sealed-but-interrupted
// cleanup from being mistaken for a complete one. A removed Work Log proves
// the worktree terminalization; local and origin branch absence independently
// prove the remaining branch-retirement part of cleanup.
func requireTerminalCleanupBranchesAbsent(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, expectations []worktrees.TerminalWorkLogExpectation, timeout time.Duration, retry int) error {
	return requireTerminalCleanupBranchesAbsentWithRunner(ctx, defaultRunner, projectsRoot, receipt, expectations, timeout, retry)
}

func requireTerminalCleanupBranchesAbsentWithRunner(ctx context.Context, run runner.Runner, projectsRoot string, receipt WorktreeMergeReceipt, expectations []worktrees.TerminalWorkLogExpectation, timeout time.Duration, retry int) error {
	canonical, canonicalErr := worktrees.CanonicalRepositoryPath(projectsRoot, receipt.Repository)
	if canonicalErr != nil {
		return canonicalErr
	}
	for _, expectation := range expectations {
		if expectation.Branch == receipt.Target {
			return fmt.Errorf("terminal cleanup recovery refuses receipt task %s because its branch is the target %s", expectation.Task, receipt.Target)
		}
		local, _, err := runCommand(ctx, run, timeout, retry, canonical, "git", "branch", "--list", "--format=%(refname:short)", expectation.Branch)
		if err != nil {
			return fmt.Errorf("inspect local cleanup branch for task %s: %w", expectation.Task, err)
		}
		if strings.TrimSpace(local) != "" {
			return fmt.Errorf("terminal cleanup recovery refuses task %s because local branch %s remains", expectation.Task, expectation.Branch)
		}
		remote, _, err := runCommand(ctx, run, timeout, retry, canonical, "git", "ls-remote", "--heads", "origin", "refs/heads/"+expectation.Branch)
		if err != nil {
			return fmt.Errorf("inspect remote cleanup branch for task %s: %w", expectation.Task, err)
		}
		if strings.TrimSpace(remote) != "" {
			return fmt.Errorf("terminal cleanup recovery refuses task %s because remote branch %s remains", expectation.Task, expectation.Branch)
		}
	}
	return nil
}
