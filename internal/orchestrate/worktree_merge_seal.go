package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// WorktreeMergeValidationFailureSealRoot names one immutable history root that
// a no-content recovery candidate must contain.
type WorktreeMergeValidationFailureSealRoot struct {
	Kind string `json:"kind"`
	SHA  string `json:"sha"`
}

// WorktreeMergeValidationFailureSeal is the result of preparing a clean,
// target-tree-identical replacement candidate. It is not a supersession
// acknowledgement; the historical receipt remains active until the caller
// separately records one with supersede-validation-failed.
type WorktreeMergeValidationFailureSeal struct {
	Status           string                                   `json:"status"`
	ReceiptPath      string                                   `json:"receipt_path"`
	ReceiptID        string                                   `json:"receipt_id"`
	ReceiptSHA256    string                                   `json:"receipt_sha256"`
	Repository       string                                   `json:"repository"`
	Target           string                                   `json:"target"`
	CurrentTargetSHA string                                   `json:"current_target_sha"`
	TargetTreeSHA    string                                   `json:"target_tree_sha"`
	RequiredRoots    []WorktreeMergeValidationFailureSealRoot `json:"required_roots"`
	Candidate        WorktreeMergeCandidate                   `json:"candidate"`
	Actor            string                                   `json:"actor"`
	Reason           string                                   `json:"reason"`
}

type WorktreeMergeValidationFailureSealOptions struct {
	ProjectsRoot    string
	Receipt         string
	Apply           bool
	Actor           string
	Reason          string
	Model           string
	AgentRuntime    string
	AgentID         string
	Initiator       string
	CLI             string
	Provider        string
	SessionRequired bool
	Timeout         time.Duration
	Retry           int
}

// PrepareValidationFailedWorktreeMergeSeal creates a WB-managed candidate at
// the freshly fetched target and, when necessary, adds only merge ancestry via
// Git's ours strategy. It refuses unless the candidate's final tree is exactly
// the fetched target tree and every approved immutable root is an ancestor.
// It never edits the historical merge receipt or any existing Work Log.
func PrepareValidationFailedWorktreeMergeSeal(ctx context.Context, options WorktreeMergeValidationFailureSealOptions) (WorktreeMergeValidationFailureSeal, error) {
	receiptPath, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return WorktreeMergeValidationFailureSeal{}, err
	}
	receipt, err := readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeValidationFailureSeal{}, err
	}
	if receipt.Phase != WorktreeMergePhasePrepare || receipt.Status != WorktreeMergeValidationFailed || receipt.LandingSHA != "" {
		return WorktreeMergeValidationFailureSeal{}, fmt.Errorf("receipt %s is %s/%s, want prepare validation_failed without a landing", receiptPath, receipt.Phase, receipt.Status)
	}
	if err := validateLandedFailureAcknowledgementReceipt(receipt, receiptPath); err != nil {
		return WorktreeMergeValidationFailureSeal{}, err
	}
	if receipt.Repository == "" || receipt.Target == "" || receipt.TargetSHA == "" || receipt.Candidate.SHA == "" || len(receipt.Sources) == 0 {
		return WorktreeMergeValidationFailureSeal{}, fmt.Errorf("receipt %s lacks complete immutable target, candidate, or source identity", receiptPath)
	}
	if options.Apply && (strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "") {
		return WorktreeMergeValidationFailureSeal{}, errors.New("--actor and --reason are required with --apply")
	}

	lock, err := AcquireOperationLock(options.ProjectsRoot, receipt.Lane, true)
	if err != nil {
		return WorktreeMergeValidationFailureSeal{}, err
	}
	defer func() { _ = lock.Release() }()

	originalClaim, err := validateMergeAcknowledgementCandidate(ctx, options.ProjectsRoot, receipt, receipt.Candidate)
	if err != nil {
		return WorktreeMergeValidationFailureSeal{}, fmt.Errorf("validate failed candidate: %w", err)
	}
	currentTarget, err := fetchExactMergeTarget(ctx, receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		return WorktreeMergeValidationFailureSeal{}, err
	}
	targetTree, err := mergeTreeRevision(ctx, receipt.Candidate.Worktree, currentTarget)
	if err != nil {
		return WorktreeMergeValidationFailureSeal{}, fmt.Errorf("read current target tree: %w", err)
	}
	observedSourceDescendants := make(map[string]string)
	for _, source := range receipt.Sources {
		observedHead, sourceErr := validateValidationFailureSealSource(ctx, options.ProjectsRoot, receipt, source, targetTree)
		if sourceErr != nil {
			return WorktreeMergeValidationFailureSeal{}, sourceErr
		}
		if observedHead != source.SHA {
			observedSourceDescendants[source.Task] = observedHead
		}
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receiptPath)
	if err != nil {
		return WorktreeMergeValidationFailureSeal{}, err
	}
	roots := validationFailureSealRoots(originalClaim.BaseSHA, receipt, currentTarget, observedSourceDescendants)
	task := receipt.ID + "-ancestry-seal"
	branch := "wb/recovery/" + receipt.Target + "/" + mergeOperationSuffix(receipt.ID) + "-ancestry-seal"
	result := WorktreeMergeValidationFailureSeal{
		Status: "validation_failure_seal_planned", ReceiptPath: receiptPath, ReceiptID: receipt.ID, ReceiptSHA256: receiptHash,
		Repository: receipt.Repository, Target: receipt.Target, CurrentTargetSHA: currentTarget, TargetTreeSHA: targetTree,
		RequiredRoots: roots, Candidate: WorktreeMergeCandidate{Task: task, Branch: branch},
		Actor: strings.TrimSpace(options.Actor), Reason: strings.TrimSpace(options.Reason),
	}
	if !options.Apply {
		return result, nil
	}

	listed, err := worktrees.List(ctx, worktrees.ListOptions{ProjectsRoot: options.ProjectsRoot, Task: task, Base: receipt.Target, Workers: 1})
	if err != nil {
		return WorktreeMergeValidationFailureSeal{}, fmt.Errorf("inspect ancestry seal worktree: %w", err)
	}
	if len(listed) > 1 {
		return WorktreeMergeValidationFailureSeal{}, fmt.Errorf("ancestry seal task %s resolves to %d worktrees, want at most one", task, len(listed))
	}
	prompt, err := writeValidationFailureSealPrompt(receipt, currentTarget, targetTree, roots, result.Actor, result.Reason)
	if err != nil {
		return WorktreeMergeValidationFailureSeal{}, err
	}
	defer func() { _ = os.Remove(prompt) }()
	model := strings.TrimSpace(options.Model)
	if model == "" {
		model = "unknown"
	}
	created, err := worktrees.Create(ctx, []string{receipt.Repository}, worktrees.CreateOptions{
		ProjectsRoot: options.ProjectsRoot, Operation: task, Branch: branch, BranchChosen: true,
		Base: receipt.Target, Resume: len(listed) == 1, SessionRequired: options.SessionRequired,
		WorkLog: worktrees.WorkLogOptions{
			EffortID: task, RunID: task, Initiator: options.Initiator, AgentID: options.AgentID,
			AgentRuntime: options.AgentRuntime, Model: model, CLI: options.CLI, Provider: options.Provider,
			OriginalPrompt: prompt, RequireOriginalPrompt: true,
		},
	})
	if err != nil {
		return WorktreeMergeValidationFailureSeal{}, fmt.Errorf("create ancestry seal worktree: %w", err)
	}
	if len(created) != 1 {
		return WorktreeMergeValidationFailureSeal{}, fmt.Errorf("ancestry seal creation returned %d repositories", len(created))
	}
	createdCandidate := created[0]
	if createdCandidate.BaseSHA != currentTarget {
		return WorktreeMergeValidationFailureSeal{}, fmt.Errorf("remote target drifted from %s to %s while creating ancestry seal; refusing mutation", currentTarget, createdCandidate.BaseSHA)
	}
	replacement, replacementClaim, err := validateValidationFailureReplacement(ctx, options.ProjectsRoot, receipt, createdCandidate.WorktreeDir)
	if err != nil {
		return WorktreeMergeValidationFailureSeal{}, err
	}
	if replacement.Task != task || replacement.Branch != branch || replacementClaim.BaseSHA != currentTarget {
		return WorktreeMergeValidationFailureSeal{}, errors.New("ancestry seal Work Log does not match the exact task, branch, and fetched target identity")
	}
	beforeTree, err := mergeTreeRevision(ctx, replacement.Worktree, replacement.SHA)
	if err != nil {
		return WorktreeMergeValidationFailureSeal{}, err
	}
	if beforeTree != targetTree {
		return WorktreeMergeValidationFailureSeal{}, fmt.Errorf("ancestry seal candidate tree %s differs from fetched target tree %s before sealing", beforeTree, targetTree)
	}

	missing := make([]string, 0, len(roots))
	seen := map[string]bool{}
	for _, root := range roots {
		if seen[root.SHA] {
			continue
		}
		seen[root.SHA] = true
		contains, ancestorErr := isMergeAncestor(ctx, replacement.Worktree, root.SHA, replacement.SHA)
		if ancestorErr != nil {
			return WorktreeMergeValidationFailureSeal{}, ancestorErr
		}
		if !contains {
			missing = append(missing, root.SHA)
		}
	}
	if len(missing) > 0 {
		args := []string{"merge", "--strategy=ours", "--no-edit", "--message", "chore(wb): seal validation-failure ancestry"}
		args = append(args, missing...)
		if _, _, err := runCommand(ctx, options.Timeout, options.Retry, replacement.Worktree, "git", args...); err != nil {
			_, _, _ = runCommand(ctx, options.Timeout, 0, replacement.Worktree, "git", "merge", "--abort")
			return WorktreeMergeValidationFailureSeal{}, fmt.Errorf("create no-content ancestry seal: %w", err)
		}
	}
	if err := requireCleanMergeWorktree(ctx, replacement.Worktree); err != nil {
		return WorktreeMergeValidationFailureSeal{}, fmt.Errorf("ancestry seal is not clean: %w", err)
	}
	replacement.SHA, err = mergeRevision(ctx, replacement.Worktree, "HEAD")
	if err != nil {
		return WorktreeMergeValidationFailureSeal{}, err
	}
	finalTree, err := mergeTreeRevision(ctx, replacement.Worktree, replacement.SHA)
	if err != nil {
		return WorktreeMergeValidationFailureSeal{}, err
	}
	if finalTree != targetTree {
		return WorktreeMergeValidationFailureSeal{}, fmt.Errorf("ancestry seal changed target tree from %s to %s", targetTree, finalTree)
	}
	for _, root := range roots {
		contains, ancestorErr := isMergeAncestor(ctx, replacement.Worktree, root.SHA, replacement.SHA)
		if ancestorErr != nil || !contains {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("ancestry seal candidate %s does not contain required %s root %s", replacement.SHA, root.Kind, root.SHA)
			}
			return WorktreeMergeValidationFailureSeal{}, ancestorErr
		}
	}
	// Re-read every mutable external boundary after the commit. A source or
	// target that moved during construction invalidates the result.
	if _, err := validateMergeAcknowledgementCandidate(ctx, options.ProjectsRoot, receipt, receipt.Candidate); err != nil {
		return WorktreeMergeValidationFailureSeal{}, fmt.Errorf("failed candidate changed while sealing: %w", err)
	}
	for _, source := range receipt.Sources {
		observedHead, sourceErr := validateValidationFailureSealSource(ctx, options.ProjectsRoot, receipt, source, targetTree)
		if sourceErr != nil {
			return WorktreeMergeValidationFailureSeal{}, fmt.Errorf("receipted source changed while sealing: %w", sourceErr)
		}
		if want := observedSourceDescendants[source.Task]; observedHead != source.SHA && observedHead != want {
			return WorktreeMergeValidationFailureSeal{}, fmt.Errorf("receipted source %s advanced from observed %s to %s while sealing", source.Worktree, want, observedHead)
		}
		if observedHead == source.SHA && observedSourceDescendants[source.Task] != "" {
			return WorktreeMergeValidationFailureSeal{}, fmt.Errorf("receipted source %s no longer matches observed descendant %s", source.Worktree, observedSourceDescendants[source.Task])
		}
	}
	refetchedTarget, err := fetchExactMergeTarget(ctx, replacement.Worktree, receipt.Target)
	if err != nil {
		return WorktreeMergeValidationFailureSeal{}, err
	}
	if refetchedTarget != currentTarget {
		return WorktreeMergeValidationFailureSeal{}, fmt.Errorf("remote target drifted from %s to %s while sealing; refusing result", currentTarget, refetchedTarget)
	}

	result.Status = "validation_failure_seal_prepared"
	result.Candidate = replacement
	return result, nil
}

func validationFailureSealRoots(claimBase string, receipt WorktreeMergeReceipt, currentTarget string, observedSourceDescendants map[string]string) []WorktreeMergeValidationFailureSealRoot {
	roots := []WorktreeMergeValidationFailureSealRoot{
		{Kind: "failed_candidate_claim_base", SHA: claimBase},
		{Kind: "receipt_target", SHA: receipt.TargetSHA},
		{Kind: "current_remote_target", SHA: currentTarget},
	}
	for _, source := range receipt.Sources {
		roots = append(roots, WorktreeMergeValidationFailureSealRoot{Kind: "receipted_source:" + source.Task, SHA: source.SHA})
		if observed := observedSourceDescendants[source.Task]; observed != "" {
			roots = append(roots, WorktreeMergeValidationFailureSealRoot{Kind: "landed_source_descendant:" + source.Task, SHA: observed})
		}
	}
	return roots
}

func validateValidationFailureSealSource(ctx context.Context, projectsRoot string, receipt WorktreeMergeReceipt, source WorktreeMergeSource, targetTree string) (string, error) {
	head, err := mergeRevision(ctx, source.Worktree, "HEAD")
	if err != nil {
		return "", fmt.Errorf("read receipted source %s HEAD: %w", source.Worktree, err)
	}
	allowedDescendant := ""
	if head != source.SHA {
		allowedDescendant = head
	}
	if err := validateLandedFailureAcknowledgementSource(ctx, projectsRoot, receipt, source, allowedDescendant); err != nil {
		return "", err
	}
	if allowedDescendant == "" {
		return head, nil
	}
	sourceTree, err := mergeTreeRevision(ctx, source.Worktree, head)
	if err != nil {
		return "", fmt.Errorf("read advanced receipted source tree: %w", err)
	}
	if sourceTree != targetTree {
		return "", fmt.Errorf("advanced receipted source %s tree %s differs from landed target tree %s", source.Worktree, sourceTree, targetTree)
	}
	return head, nil
}

func writeValidationFailureSealPrompt(receipt WorktreeMergeReceipt, currentTarget, targetTree string, roots []WorktreeMergeValidationFailureSealRoot, actor, reason string) (string, error) {
	file, err := os.CreateTemp("", "wb-validation-failure-seal-prompt-*.txt")
	if err != nil {
		return "", err
	}
	path := file.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	if err = file.Chmod(0o600); err != nil {
		_ = file.Close()
		return "", err
	}
	var body strings.Builder
	fmt.Fprintf(&body, "WB prepares a no-content ancestry seal for validation-failed receipt %s.\n", receipt.ReceiptPath)
	fmt.Fprintf(&body, "Repository: %s\nTarget: %s at %s\nRequired tree: %s\n", receipt.Repository, receipt.Target, currentTarget, targetTree)
	for _, root := range roots {
		fmt.Fprintf(&body, "- %s %s\n", root.Kind, root.SHA)
	}
	fmt.Fprintf(&body, "Actor: %s\nReason: %s\n", actor, reason)
	if _, err = file.WriteString(body.String()); err != nil {
		_ = file.Close()
		return "", err
	}
	if err = file.Close(); err != nil {
		return "", err
	}
	return filepath.Clean(path), nil
}
