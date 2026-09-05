package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/worktrees"
)

var beforeConflictCandidateRefreshCreate = func() {}
var beforeConflictCandidateRefreshFinalRevalidation = func() {}

// WorktreeMergeConflictCandidateRefresh is the unconsumed candidate created
// while a failed conflict receipt still owns its merger lane. The existing
// supersession acknowledgement remains the only transition that frees it.
type WorktreeMergeConflictCandidateRefresh struct {
	Status                      string                                   `json:"status"`
	ReceiptPath                 string                                   `json:"receipt_path"`
	ReceiptSHA256               string                                   `json:"receipt_sha256"`
	ImmutableClaimSHA256        string                                   `json:"immutable_claim_sha256"`
	CurrentTargetSHA            string                                   `json:"current_target_sha"`
	ObservedCandidateDescendant string                                   `json:"observed_candidate_descendant_sha,omitempty"`
	Sources                     []WorktreeMergeSource                    `json:"sources"`
	RequiredRoots               []WorktreeMergeValidationFailureSealRoot `json:"required_roots"`
	Candidate                   WorktreeMergeCandidate                   `json:"candidate"`
	Actor                       string                                   `json:"actor"`
	Reason                      string                                   `json:"reason"`
}

type WorktreeMergeConflictCandidateRefreshOptions struct {
	ProjectsRoot, Receipt, ExpectedReceiptSHA256, ExpectedImmutableClaimSHA256, ExpectedCurrentTargetSHA string
	Sources, ExpectedSourceSHAs                                                                          []string
	Apply                                                                                                bool
	Actor, Reason                                                                                        string
	Model, AgentRuntime, AgentID, Initiator, CLI, Provider                                               string
	SessionRequired                                                                                      bool
	Timeout                                                                                              time.Duration
	Retry                                                                                                int
	Progress                                                                                             progress.Reporter
}

func (options WorktreeMergeConflictCandidateRefreshOptions) RefreshTask() string {
	hash := sha256.New()
	for _, value := range append([]string{strings.TrimSpace(options.ExpectedReceiptSHA256), strings.TrimSpace(options.ExpectedCurrentTargetSHA)}, options.ExpectedSourceSHAs...) {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return "conflict-candidate-refresh-" + hex.EncodeToString(hash.Sum(nil)[:8])
}

// PrepareConflictWorktreeMergeReplacement breaks only the cycle where a
// non-terminal, unpublished prepare conflict owns the lane that ordinary
// prepare would need to create the replacement. It writes no receipt or
// acknowledgement; callers pass the returned candidate to supersede-validation-failed.
func PrepareConflictWorktreeMergeReplacement(ctx context.Context, options WorktreeMergeConflictCandidateRefreshOptions) (result WorktreeMergeConflictCandidateRefresh, retErr error) {
	if err := requireConflictCandidateRefreshExpectations(options); err != nil {
		return result, err
	}
	if options.Apply && (strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "") {
		return result, errors.New("--actor and --reason are required with --apply")
	}
	receiptPath, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return result, err
	}
	receipt, err := readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return result, err
	}
	if err := validateConflictCandidateRefreshReceipt(receipt, receiptPath); err != nil {
		return result, err
	}
	reportWorktreeMergeProgress(options.Progress, "acquire_lane", progress.Started, receipt.Lane)
	lock, err := AcquireOperationLock(options.ProjectsRoot, receipt.Lane, true)
	if err != nil {
		return result, err
	}
	defer func() { _ = lock.Release() }()
	reportWorktreeMergeProgress(options.Progress, "acquire_lane", progress.Completed, receipt.Lane)

	state, err := inspectConflictCandidateRefresh(ctx, options, receiptPath)
	if err != nil {
		return result, err
	}
	result = WorktreeMergeConflictCandidateRefresh{
		Status: "conflict_candidate_refresh_planned", ReceiptPath: receiptPath, ReceiptSHA256: state.receiptHash,
		ImmutableClaimSHA256: state.claimHash, CurrentTargetSHA: state.currentTarget, ObservedCandidateDescendant: state.observed,
		Sources: append([]WorktreeMergeSource(nil), state.sources...), RequiredRoots: state.roots,
		Candidate: WorktreeMergeCandidate{Task: options.RefreshTask(), Branch: "wb/recovery/" + state.receipt.Target + "/" + mergeOperationSuffix(options.RefreshTask()) + "-conflict-replacement"},
		Actor:     strings.TrimSpace(options.Actor), Reason: strings.TrimSpace(options.Reason),
	}
	if !options.Apply {
		return result, nil
	}
	beforeConflictCandidateRefreshCreate()
	if err := revalidateConflictCandidateRefresh(ctx, options, receiptPath, state); err != nil {
		return WorktreeMergeConflictCandidateRefresh{}, err
	}
	listed, err := worktrees.List(ctx, worktrees.ListOptions{ProjectsRoot: options.ProjectsRoot, Task: result.Candidate.Task, Base: state.receipt.Target, Workers: 1})
	if err != nil {
		return WorktreeMergeConflictCandidateRefresh{}, fmt.Errorf("inspect conflict replacement worktree: %w", err)
	}
	if len(listed) > 1 {
		return WorktreeMergeConflictCandidateRefresh{}, fmt.Errorf("conflict replacement task %s resolves to %d worktrees, want at most one", result.Candidate.Task, len(listed))
	}
	prompt, err := writeConflictCandidateRefreshPrompt(state.receipt, state.currentTarget, state.sources, state.roots, result.Actor, result.Reason)
	if err != nil {
		return WorktreeMergeConflictCandidateRefresh{}, err
	}
	defer func() { _ = os.Remove(prompt) }()
	model := strings.TrimSpace(options.Model)
	if model == "" {
		model = "unknown"
	}
	reportWorktreeMergeProgress(options.Progress, "create_replacement", progress.Started, result.Candidate.Task)
	created, err := worktrees.Create(ctx, []string{state.receipt.Repository}, worktrees.CreateOptions{
		ProjectsRoot: options.ProjectsRoot, Operation: result.Candidate.Task, Branch: result.Candidate.Branch, BranchChosen: true, Base: state.receipt.Target,
		Resume: len(listed) == 1, SessionRequired: options.SessionRequired,
		WorkLog: worktrees.WorkLogOptions{EffortID: result.Candidate.Task, RunID: result.Candidate.Task, Initiator: options.Initiator, AgentID: options.AgentID, AgentRuntime: options.AgentRuntime, Model: model, CLI: options.CLI, Provider: options.Provider, OriginalPrompt: prompt, RequireOriginalPrompt: true},
	})
	if err != nil {
		return WorktreeMergeConflictCandidateRefresh{}, fmt.Errorf("create conflict replacement worktree: %w", err)
	}
	if len(created) != 1 {
		return WorktreeMergeConflictCandidateRefresh{}, fmt.Errorf("conflict replacement creation returned %d repositories", len(created))
	}
	candidateTask := result.Candidate.Task
	createdNew, completed := len(listed) == 0, false
	defer func() {
		if !createdNew || completed {
			return
		}
		_, abortErr := worktrees.Abort(ctx, worktrees.AbortOptions{ProjectsRoot: options.ProjectsRoot, Task: candidateTask, Base: state.receipt.Target, All: true, Disposition: worktrees.AbortDiscarded, DeleteRemote: true, Apply: true})
		if abortErr != nil && retErr != nil {
			retErr = fmt.Errorf("%w; retire invalid conflict replacement candidate: %v", retErr, abortErr)
		}
	}()
	if created[0].BaseSHA != state.currentTarget || filepath.Clean(created[0].CanonicalDir) != filepath.Clean(state.canonical) {
		return WorktreeMergeConflictCandidateRefresh{}, errors.New("target or canonical identity drifted while creating conflict replacement candidate")
	}
	candidate, claim, err := validateValidationFailureReplacement(ctx, options.ProjectsRoot, state.receipt, created[0].WorktreeDir)
	if err != nil {
		return WorktreeMergeConflictCandidateRefresh{}, err
	}
	if candidate.Task != result.Candidate.Task || candidate.Branch != result.Candidate.Branch || claim.BaseSHA != state.currentTarget {
		return WorktreeMergeConflictCandidateRefresh{}, errors.New("conflict replacement candidate has an unexpected identity")
	}
	reportWorktreeMergeProgress(options.Progress, "merge_required_roots", progress.Started, candidate.Worktree)
	if err := mergePublishedForwardRepairRoots(ctx, candidate.Worktree, state.roots, options.Timeout, options.Retry); err != nil {
		return WorktreeMergeConflictCandidateRefresh{}, err
	}
	candidate.SHA, err = mergeRevision(ctx, candidate.Worktree, "HEAD")
	if err != nil {
		return WorktreeMergeConflictCandidateRefresh{}, err
	}
	if err := requireCleanMergeWorktree(ctx, candidate.Worktree); err != nil {
		return WorktreeMergeConflictCandidateRefresh{}, fmt.Errorf("conflict replacement candidate is not clean: %w", err)
	}
	reportWorktreeMergeProgress(options.Progress, "merge_required_roots", progress.Completed, shortMergeRevision(candidate.SHA))
	beforeConflictCandidateRefreshFinalRevalidation()
	if err := revalidateConflictCandidateRefresh(ctx, options, receiptPath, state); err != nil {
		return WorktreeMergeConflictCandidateRefresh{}, err
	}
	for _, root := range state.roots {
		contains, ancestorErr := isMergeAncestor(ctx, candidate.Worktree, root.SHA, candidate.SHA)
		if ancestorErr != nil || !contains {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("conflict replacement candidate %s does not contain required %s root %s", candidate.SHA, root.Kind, root.SHA)
			}
			return WorktreeMergeConflictCandidateRefresh{}, ancestorErr
		}
	}
	result.Status, result.Candidate = "conflict_candidate_refresh_prepared", candidate
	completed = true
	reportWorktreeMergeProgress(options.Progress, "replacement_prepared", progress.Completed, candidate.Worktree)
	return result, nil
}

type conflictCandidateRefreshState struct {
	receipt                                                    WorktreeMergeReceipt
	receiptHash, claimHash, observed, currentTarget, canonical string
	sources                                                    []WorktreeMergeSource
	roots                                                      []WorktreeMergeValidationFailureSealRoot
}

func requireConflictCandidateRefreshExpectations(options WorktreeMergeConflictCandidateRefreshOptions) error {
	if strings.TrimSpace(options.ProjectsRoot) == "" || strings.TrimSpace(options.Receipt) == "" || strings.TrimSpace(options.ExpectedReceiptSHA256) == "" || strings.TrimSpace(options.ExpectedImmutableClaimSHA256) == "" || strings.TrimSpace(options.ExpectedCurrentTargetSHA) == "" || len(options.Sources) == 0 || len(options.Sources) != len(options.ExpectedSourceSHAs) {
		return errors.New("receipt, projects root, expected receipt, immutable claim, current target, and one expected SHA per source are required")
	}
	for _, sha := range options.ExpectedSourceSHAs {
		if strings.TrimSpace(sha) == "" {
			return errors.New("each replacement source must have an expected SHA")
		}
	}
	return nil
}

func validateConflictCandidateRefreshReceipt(receipt WorktreeMergeReceipt, receiptPath string) error {
	if err := validatePrepareFailureSupersessionReceipt(receipt, receiptPath); err != nil {
		return err
	}
	if receipt.Status != WorktreeMergeConflict {
		return errors.New("receipt is not an unpublished prepare conflict")
	}
	return nil
}

func inspectConflictCandidateRefresh(ctx context.Context, options WorktreeMergeConflictCandidateRefreshOptions, receiptPath string) (conflictCandidateRefreshState, error) {
	receipt, err := readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return conflictCandidateRefreshState{}, err
	}
	if err := validateConflictCandidateRefreshReceipt(receipt, receiptPath); err != nil {
		return conflictCandidateRefreshState{}, err
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receiptPath)
	if err != nil || receiptHash != options.ExpectedReceiptSHA256 {
		if err == nil {
			err = fmt.Errorf("receipt SHA256 %s does not match expected %s", receiptHash, options.ExpectedReceiptSHA256)
		}
		return conflictCandidateRefreshState{}, err
	}
	claim, observed, err := validatePrepareFailureSupersessionCandidate(ctx, options.ProjectsRoot, receipt)
	if err != nil {
		return conflictCandidateRefreshState{}, fmt.Errorf("validate failed candidate: %w", err)
	}
	claimBytes, err := os.ReadFile(claim.ClaimPath)
	if err != nil {
		return conflictCandidateRefreshState{}, fmt.Errorf("read immutable failed candidate claim: %w", err)
	}
	claimDigest := sha256.Sum256(claimBytes)
	claimHash := hex.EncodeToString(claimDigest[:])
	if claimHash != options.ExpectedImmutableClaimSHA256 {
		return conflictCandidateRefreshState{}, fmt.Errorf("immutable claim SHA256 %s does not match expected %s", claimHash, options.ExpectedImmutableClaimSHA256)
	}
	remote, _, err := runCommand(ctx, 0, 0, receipt.Candidate.Worktree, "git", "ls-remote", "--heads", "origin", "refs/heads/"+receipt.Candidate.Branch)
	if err != nil {
		return conflictCandidateRefreshState{}, fmt.Errorf("inspect failed candidate publication state: %w", err)
	}
	if strings.TrimSpace(remote) != "" {
		return conflictCandidateRefreshState{}, errors.New("failed conflict candidate is published")
	}
	sources, repository, canonical, err := inspectPublishedForwardRepairSources(ctx, options.ProjectsRoot, options.Sources, receipt.Target)
	if err != nil {
		return conflictCandidateRefreshState{}, err
	}
	if repository != receipt.Repository {
		return conflictCandidateRefreshState{}, fmt.Errorf("replacement sources belong to %s, want receipt repository %s", repository, receipt.Repository)
	}
	if !sameWorktreeMergeSources(sources, receipt.Sources) {
		return conflictCandidateRefreshState{}, errors.New("replacement sources do not match immutable receipt identities")
	}
	if err := requireExpectedPublishedForwardRepairSources(sources, options.ExpectedSourceSHAs); err != nil {
		return conflictCandidateRefreshState{}, err
	}
	currentTarget, err := fetchExactMergeTarget(ctx, receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		return conflictCandidateRefreshState{}, err
	}
	if currentTarget != options.ExpectedCurrentTargetSHA {
		return conflictCandidateRefreshState{}, fmt.Errorf("current target %s does not match expected %s", currentTarget, options.ExpectedCurrentTargetSHA)
	}
	roots := []WorktreeMergeValidationFailureSealRoot{{Kind: "failed_candidate_claim_base", SHA: claim.BaseSHA}, {Kind: "receipt_target", SHA: receipt.TargetSHA}, {Kind: "current_remote_target", SHA: currentTarget}, {Kind: "receipted_candidate", SHA: receipt.Candidate.SHA}}
	if observed != "" {
		roots = append(roots, WorktreeMergeValidationFailureSealRoot{Kind: "observed_candidate_descendant", SHA: observed})
	}
	for _, source := range sources {
		roots = append(roots, WorktreeMergeValidationFailureSealRoot{Kind: "receipted_source:" + source.Task, SHA: source.SHA})
	}
	return conflictCandidateRefreshState{receipt: receipt, receiptHash: receiptHash, claimHash: claimHash, observed: observed, currentTarget: currentTarget, canonical: canonical, sources: sources, roots: roots}, nil
}

func revalidateConflictCandidateRefresh(ctx context.Context, options WorktreeMergeConflictCandidateRefreshOptions, receiptPath string, expected conflictCandidateRefreshState) error {
	current, err := inspectConflictCandidateRefresh(ctx, options, receiptPath)
	if err != nil {
		return fmt.Errorf("revalidate conflict replacement evidence: %w", err)
	}
	if current.receiptHash != expected.receiptHash || current.claimHash != expected.claimHash || current.observed != expected.observed || current.currentTarget != expected.currentTarget || filepath.Clean(current.canonical) != filepath.Clean(expected.canonical) || !sameWorktreeMergeSources(current.sources, expected.sources) {
		return errors.New("conflict replacement evidence changed during construction")
	}
	return nil
}

func writeConflictCandidateRefreshPrompt(receipt WorktreeMergeReceipt, target string, sources []WorktreeMergeSource, roots []WorktreeMergeValidationFailureSealRoot, actor, reason string) (string, error) {
	file, err := os.CreateTemp("", "wb-conflict-candidate-refresh-prompt-*.txt")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	var body strings.Builder
	fmt.Fprintf(&body, "WB prepares one receipt-bound conflict replacement for %s.\nTarget: %s@%s.\n", receipt.ReceiptPath, receipt.Target, target)
	for _, source := range sources {
		fmt.Fprintf(&body, "- source %s %s\n", source.Branch, source.SHA)
	}
	for _, root := range roots {
		fmt.Fprintf(&body, "- immutable root %s %s\n", root.Kind, root.SHA)
	}
	fmt.Fprintf(&body, "Actor: %s\nReason: %s\n", actor, reason)
	if _, err := file.WriteString(body.String()); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return filepath.Clean(path), nil
}
