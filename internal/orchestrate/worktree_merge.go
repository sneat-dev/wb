package orchestrate

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

const WorktreeMergeSchemaVersion = 1

type WorktreeMergePhase string

const (
	WorktreeMergePhasePrepare WorktreeMergePhase = "prepare"
	WorktreeMergePhaseLand    WorktreeMergePhase = "land"
	WorktreeMergePhaseRevert  WorktreeMergePhase = "revert"
)

type WorktreeMergeStatus string

const (
	WorktreeMergePreparing            WorktreeMergeStatus = "preparing"
	WorktreeMergePrepared             WorktreeMergeStatus = "prepared"
	WorktreeMergeConflict             WorktreeMergeStatus = "conflict"
	WorktreeMergeValidationFailed     WorktreeMergeStatus = "validation_failed"
	WorktreeMergeChecksPending        WorktreeMergeStatus = "checks_pending"
	WorktreeMergeChecksFailed         WorktreeMergeStatus = "checks_failed"
	WorktreeMergeLanded               WorktreeMergeStatus = "landed_cleanup_pending"
	WorktreeMergeCanonicalSyncBlocked WorktreeMergeStatus = "landed_canonical_sync_blocked"
	WorktreeMergePostTargetCIFailed   WorktreeMergeStatus = "landed_post_target_ci_failed"
	WorktreeMergeComplete             WorktreeMergeStatus = "complete"
)

type WorktreeMergeRoute string

const (
	WorktreeMergeRouteAuto        WorktreeMergeRoute = "auto"
	WorktreeMergeRouteDirect      WorktreeMergeRoute = "direct"
	WorktreeMergeRoutePullRequest WorktreeMergeRoute = "pr"
	WorktreeMergeRouteUnsupported WorktreeMergeRoute = "unsupported"
)

type WorktreeMergeRouteDecision struct {
	Requested WorktreeMergeRoute `json:"requested"`
	Route     WorktreeMergeRoute `json:"route"`
	Reason    string             `json:"reason"`
}

type WorktreeMergeSource struct {
	Task     string `json:"task"`
	Worktree string `json:"worktree"`
	Branch   string `json:"branch"`
	SHA      string `json:"sha"`
	Merged   bool   `json:"merged"`
}

type WorktreeMergeCandidate struct {
	Task     string `json:"task"`
	Worktree string `json:"worktree"`
	Branch   string `json:"branch"`
	SHA      string `json:"sha"`
}

type WorktreeMergeRebaseReceipt struct {
	CandidateBefore string `json:"candidate_before"`
	TargetBefore    string `json:"target_before"`
	TargetAfter     string `json:"target_after"`
	CandidateAfter  string `json:"candidate_after"`
}

type WorktreeMergeRevertReceipt struct {
	PreviousTargetSHA string `json:"previous_target_sha"`
	LandingSHA        string `json:"landing_sha"`
	CandidateSHA      string `json:"candidate_sha"`
}

type WorktreeMergeSourceRefresh struct {
	RecordedAt time.Time             `json:"recorded_at"`
	Sources    []WorktreeMergeSource `json:"sources"`
}

type WorktreeMergePushGateReceipt struct {
	Remote            string    `json:"remote"`
	RemoteRef         string    `json:"remote_ref"`
	PreviousRemoteSHA string    `json:"previous_remote_sha"`
	LocalSHA          string    `json:"local_sha"`
	Status            string    `json:"status"`
	ObservedAt        time.Time `json:"observed_at"`
}

// WorktreeMergeForwardRepairReceipt preserves the exact landed attempt whose
// failed target CI required a new forward repair. The active lane reuses its
// candidate and receipt instead of abandoning either or pretending the prior
// remote landing never happened.
type WorktreeMergeForwardRepairReceipt struct {
	Status       WorktreeMergeStatus   `json:"status"`
	TargetSHA    string                `json:"target_sha"`
	CandidateSHA string                `json:"candidate_sha"`
	LandingSHA   string                `json:"landing_sha"`
	PullRequest  string                `json:"pull_request,omitempty"`
	Checks       PullRequestWaitResult `json:"checks"`
	Failure      string                `json:"failure"`
}

type WorktreeMergeReceipt struct {
	SchemaVersion         int                                 `json:"schema_version"`
	ID                    string                              `json:"id"`
	Lane                  string                              `json:"lane"`
	Phase                 WorktreeMergePhase                  `json:"phase"`
	Status                WorktreeMergeStatus                 `json:"status"`
	Repository            string                              `json:"repository"`
	Target                string                              `json:"target"`
	TargetSHA             string                              `json:"target_sha"`
	Sources               []WorktreeMergeSource               `json:"sources"`
	Candidate             WorktreeMergeCandidate              `json:"candidate"`
	Rebase                *WorktreeMergeRebaseReceipt         `json:"rebase,omitempty"`
	RevertOf              *WorktreeMergeRevertReceipt         `json:"revert_of,omitempty"`
	Route                 WorktreeMergeRouteDecision          `json:"route,omitempty"`
	PullRequest           string                              `json:"pull_request,omitempty"`
	PublishedCandidateSHA string                              `json:"published_candidate_sha,omitempty"`
	PreviousTargetSHA     string                              `json:"previous_target_sha,omitempty"`
	LandingSHA            string                              `json:"landing_sha,omitempty"`
	CanonicalSync         string                              `json:"canonical_sync,omitempty"`
	Validation            quality.VerificationReport          `json:"validation,omitempty"`
	BaselineValidation    quality.VerificationReport          `json:"baseline_validation,omitempty"`
	Checks                PullRequestWaitResult               `json:"checks,omitempty"`
	PushGate              *WorktreeMergePushGateReceipt       `json:"push_gate,omitempty"`
	ForwardRepairs        []WorktreeMergeForwardRepairReceipt `json:"forward_repairs,omitempty"`
	Cleanup               bool                                `json:"cleanup_requested"`
	OnFailure             string                              `json:"on_failure,omitempty"`
	CleanupReports        []string                            `json:"cleanup_reports,omitempty"`
	CleanedTasks          []string                            `json:"cleaned_tasks,omitempty"`
	SourceRefreshes       []WorktreeMergeSourceRefresh        `json:"source_refreshes,omitempty"`
	Failure               string                              `json:"failure,omitempty"`
	ResumeArgs            []string                            `json:"resume_args,omitempty"`
	ReceiptPath           string                              `json:"receipt_path"`
	CreatedAt             time.Time                           `json:"created_at"`
	UpdatedAt             time.Time                           `json:"updated_at"`
}

type WorktreeMergeLandOptions struct {
	ProjectsRoot      string
	Receipt           string
	Route             WorktreeMergeRoute
	Cleanup           bool
	OnFailure         string
	Timeout           time.Duration
	Retry             int
	CheckPollInterval time.Duration
	Progress          progress.Reporter
	ProgressRequested bool
}

type WorktreeMergePrepareOptions struct {
	ProjectsRoot      string
	Sources           []string
	Target            string
	Model             string
	AgentRuntime      string
	AgentID           string
	Initiator         string
	CLI               string
	Provider          string
	Timeout           time.Duration
	Retry             int
	Progress          progress.Reporter
	ProgressRequested bool
}

func PrepareWorktreeMerge(ctx context.Context, options WorktreeMergePrepareOptions) (WorktreeMergeReceipt, error) {
	reportWorktreeMergeProgress(options.Progress, "inspect_sources", progress.Started, "validating source worktrees and target")
	projectsRoot, err := filepath.Abs(strings.TrimSpace(options.ProjectsRoot))
	if err != nil || strings.TrimSpace(options.ProjectsRoot) == "" {
		return WorktreeMergeReceipt{}, fmt.Errorf("projects root is required")
	}
	if len(options.Sources) == 0 {
		return WorktreeMergeReceipt{}, fmt.Errorf("at least one source worktree is required")
	}
	target := strings.TrimSpace(options.Target)
	if target == "" {
		canonicalProbe, probeErr := canonicalForMergeSource(ctx, options.Sources[0])
		if probeErr != nil {
			return WorktreeMergeReceipt{}, fmt.Errorf("resolve source canonical clone: %w", probeErr)
		}
		target, probeErr = gitops.DefaultBranch(canonicalProbe)
		if probeErr != nil {
			return WorktreeMergeReceipt{}, fmt.Errorf("resolve remote default branch: %w", probeErr)
		}
	}
	sources, repository, canonical, err := inspectWorktreeMergeSources(ctx, projectsRoot, options.Sources, target)
	if err != nil {
		return WorktreeMergeReceipt{}, err
	}
	reportWorktreeMergeProgress(options.Progress, "inspect_sources", progress.Completed, fmt.Sprintf("%s: %d source worktree(s) targeting %s", repository, len(sources), target))
	if !validMergeBranch(ctx, canonical, target) {
		return WorktreeMergeReceipt{}, fmt.Errorf("invalid target branch %q", target)
	}
	for _, source := range sources {
		if source.Branch == target {
			return WorktreeMergeReceipt{}, fmt.Errorf("source worktree %s is on target branch %q", source.Worktree, target)
		}
	}
	lane := worktreeMergeLaneID(repository, target)
	operation := worktreeMergeOperationID(lane, sources)
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return WorktreeMergeReceipt{}, err
	}
	reportsDir := filepath.Join(home, "reports", "worktree-merge")
	receiptPath := filepath.Join(reportsDir, operation+".json")
	if existing, readErr := readWorktreeMergeReceipt(receiptPath); readErr == nil {
		if !sameWorktreeMergeSources(existing.Sources, sources) || existing.Repository != repository || existing.Target != target {
			return existing, fmt.Errorf("merger lane %s already owns a different candidate at %s", lane, receiptPath)
		}
		if existing.Status != WorktreeMergePreparing && existing.Status != WorktreeMergeConflict && existing.Status != WorktreeMergeValidationFailed {
			return existing, nil
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return WorktreeMergeReceipt{}, readErr
	}
	var prior *WorktreeMergeReceipt
	forwardRepair := false
	if active, activeErr := activeWorktreeMergeLaneReceipt(reportsDir, lane, receiptPath); activeErr != nil {
		return WorktreeMergeReceipt{}, activeErr
	} else if active != nil {
		canRefresh, refreshErr := canRefreshWorktreeMergeReceipt(ctx, *active, sources)
		if refreshErr != nil {
			return *active, refreshErr
		}
		if !canRefresh {
			canRepair, repairErr := canPreparePostTargetRepair(ctx, *active, sources)
			if repairErr != nil {
				return *active, repairErr
			}
			if !canRepair {
				return *active, fmt.Errorf("merger lane %s is still owned by non-terminal receipt %s with status %s", lane, active.ReceiptPath, active.Status)
			}
			forwardRepair = true
		}
		prior = active
		operation, receiptPath = active.ID, active.ReceiptPath
	}

	branch := "wb/integration/" + target + "/" + mergeOperationSuffix(operation)
	if prior != nil {
		branch = prior.Candidate.Branch
	}
	resume := prior != nil
	listed, listErr := worktrees.List(ctx, worktrees.ListOptions{ProjectsRoot: projectsRoot, Task: operation, Base: target, Workers: 1})
	if listErr != nil {
		return WorktreeMergeReceipt{}, fmt.Errorf("inspect candidate lane: %w", listErr)
	}
	if len(listed) > 0 {
		resume = true
	}
	lock, err := AcquireOperationLock(projectsRoot, lane, resume)
	if err != nil {
		return WorktreeMergeReceipt{}, err
	}
	defer func() { _ = lock.Release() }()
	reportWorktreeMergeProgress(options.Progress, "acquire_lane", progress.Completed, lane)
	// Re-read the durable lane after acquiring exclusivity: another prepare may
	// have advanced the same resumable candidate before this lock was held.
	if prior != nil {
		current, readErr := readWorktreeMergeReceipt(receiptPath)
		if readErr != nil {
			return WorktreeMergeReceipt{}, readErr
		}
		canRefresh, refreshErr := canRefreshWorktreeMergeReceipt(ctx, current, sources)
		if refreshErr != nil {
			return current, refreshErr
		}
		if !canRefresh {
			canRepair, repairErr := canPreparePostTargetRepair(ctx, current, sources)
			if repairErr != nil {
				return current, repairErr
			}
			if !canRepair {
				return current, fmt.Errorf("merger lane %s can no longer refresh receipt %s with the advanced source heads", lane, receiptPath)
			}
			forwardRepair = true
		}
		prior = &current
	}
	if active, activeErr := activeWorktreeMergeLaneReceipt(reportsDir, lane, receiptPath); activeErr != nil {
		return WorktreeMergeReceipt{}, activeErr
	} else if active != nil {
		return *active, fmt.Errorf("merger lane %s is still owned by non-terminal receipt %s with status %s", lane, active.ReceiptPath, active.Status)
	}
	promptSources := sources
	if prior != nil {
		promptSources = prior.Sources
		if len(prior.SourceRefreshes) > 0 {
			promptSources = prior.SourceRefreshes[0].Sources
		}
	}
	prompt, err := writeWorktreeMergePrompt(repository, target, promptSources)
	if err != nil {
		return WorktreeMergeReceipt{}, err
	}
	defer func() { _ = os.Remove(prompt) }()
	model := strings.TrimSpace(options.Model)
	if model == "" {
		model = "unknown"
	}
	created, err := worktrees.Create(ctx, []string{repository}, worktrees.CreateOptions{
		ProjectsRoot: projectsRoot,
		Operation:    operation,
		Branch:       branch,
		BranchChosen: true,
		Base:         target,
		Resume:       resume,
		WorkLog: worktrees.WorkLogOptions{
			EffortID: operation, RunID: operation, Initiator: options.Initiator, AgentID: options.AgentID,
			AgentRuntime: options.AgentRuntime, Model: model, CLI: options.CLI, Provider: options.Provider,
			OriginalPrompt: prompt, RequireOriginalPrompt: true,
		},
	})
	if err != nil {
		return WorktreeMergeReceipt{}, fmt.Errorf("create candidate worktree: %w", err)
	}
	if len(created) != 1 {
		return WorktreeMergeReceipt{}, fmt.Errorf("candidate creation returned %d repositories", len(created))
	}
	candidate := created[0]
	reportWorktreeMergeProgress(options.Progress, "create_candidate", progress.Completed, candidate.WorktreeDir)
	if forwardRepair {
		remoteTarget, fetchErr := fetchExactMergeTarget(ctx, candidate.WorktreeDir, target)
		if fetchErr != nil {
			return *prior, fetchErr
		}
		containsLanding, ancestorErr := isMergeAncestor(ctx, candidate.WorktreeDir, prior.LandingSHA, remoteTarget)
		if ancestorErr != nil || !containsLanding {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("remote target %s no longer contains prior landing %s", remoteTarget, prior.LandingSHA)
			}
			return *prior, ancestorErr
		}
		absorbedCandidate, targetContainsCandidate, absorptionErr := worktreeMergeCandidateAbsorbed(ctx, candidate.WorktreeDir, *prior, remoteTarget)
		if absorptionErr != nil || !absorbedCandidate {
			if absorptionErr == nil {
				absorptionErr = fmt.Errorf("remote target %s neither contains prior candidate %s nor proves its exact receipted squash landing", remoteTarget, prior.Candidate.SHA)
			}
			return *prior, absorptionErr
		}
		mergeArgs := []string{"merge", "--ff-only", remoteTarget}
		if !targetContainsCandidate {
			mergeArgs = []string{"merge", "--no-ff", "--no-edit", remoteTarget}
		}
		if _, _, mergeErr := runCommand(ctx, options.Timeout, options.Retry, candidate.WorktreeDir, "git", mergeArgs...); mergeErr != nil {
			if !targetContainsCandidate {
				_, _, _ = runCommand(ctx, options.Timeout, 0, candidate.WorktreeDir, "git", "merge", "--abort")
			}
			return *prior, fmt.Errorf("advance repair candidate to landed target %s: %w", remoteTarget, mergeErr)
		}
		candidate.BaseSHA = remoteTarget
	}

	now := time.Now().UTC()
	createdAt := now
	var refreshes []WorktreeMergeSourceRefresh
	if prior != nil {
		createdAt = prior.CreatedAt
		refreshes = append(refreshes, prior.SourceRefreshes...)
		refreshes = append(refreshes, WorktreeMergeSourceRefresh{RecordedAt: now, Sources: append([]WorktreeMergeSource(nil), prior.Sources...)})
	}
	receipt := WorktreeMergeReceipt{
		SchemaVersion: WorktreeMergeSchemaVersion,
		ID:            operation, Lane: lane, Phase: WorktreeMergePhasePrepare, Status: WorktreeMergePreparing,
		Repository: repository, Target: target, TargetSHA: candidate.BaseSHA,
		Sources:         sources,
		Candidate:       WorktreeMergeCandidate{Task: operation, Worktree: candidate.WorktreeDir, Branch: candidate.Branch},
		SourceRefreshes: refreshes,
		ResumeArgs:      worktreeMergePrepareResumeArgs(receiptPath, options.ProgressRequested),
		ReceiptPath:     receiptPath, CreatedAt: createdAt, UpdatedAt: now,
	}
	if prior != nil {
		receipt.Route = prior.Route
		receipt.Cleanup = prior.Cleanup
		receipt.OnFailure = prior.OnFailure
		receipt.ResumeArgs = append([]string(nil), prior.ResumeArgs...)
		if forwardRepair {
			receipt.ForwardRepairs = append([]WorktreeMergeForwardRepairReceipt(nil), prior.ForwardRepairs...)
			receipt.ForwardRepairs = append(receipt.ForwardRepairs, WorktreeMergeForwardRepairReceipt{
				Status: prior.Status, TargetSHA: prior.TargetSHA, CandidateSHA: prior.Candidate.SHA,
				LandingSHA: prior.LandingSHA, PullRequest: prior.PullRequest, Checks: prior.Checks, Failure: prior.Failure,
			})
		} else {
			receipt.PullRequest = prior.PullRequest
			receipt.PublishedCandidateSHA = prior.PublishedCandidateSHA
			if receipt.PullRequest != "" && receipt.PublishedCandidateSHA == "" {
				receipt.PublishedCandidateSHA = prior.Candidate.SHA
			}
			receipt.PreviousTargetSHA = prior.PreviousTargetSHA
		}
	}
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		return receipt, err
	}
	if err := requireCleanMergeWorktree(ctx, candidate.WorktreeDir); err != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
	}

	for index := range receipt.Sources {
		source := &receipt.Sources[index]
		ancestor, ancestorErr := isMergeAncestor(ctx, candidate.WorktreeDir, source.SHA, "HEAD")
		if ancestorErr != nil {
			return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, ancestorErr)
		}
		if !ancestor {
			if _, _, mergeErr := runCommand(ctx, options.Timeout, options.Retry, candidate.WorktreeDir, "git", "merge", "--no-edit", source.SHA); mergeErr != nil {
				_, _, _ = runCommand(ctx, options.Timeout, 0, candidate.WorktreeDir, "git", "merge", "--abort")
				conflict := fmt.Errorf("merge conflict while integrating %s at %s: %w", source.Branch, source.SHA, mergeErr)
				return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, conflict)
			}
		}
		source.Merged = true
		progress.Report(options.Progress, progress.Event{Operation: "worktree_merge", Phase: "integrate_sources", Repository: repository,
			State: progress.Running, Completed: index + 1, Total: len(receipt.Sources), Detail: source.Branch + "@" + shortMergeRevision(source.SHA)})
		receipt.Candidate.SHA, err = mergeRevision(ctx, candidate.WorktreeDir, "HEAD")
		if err != nil {
			return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
		}
		receipt.UpdatedAt = time.Now().UTC()
		if err := persistWorktreeMergeReceipt(receipt); err != nil {
			return receipt, err
		}
	}
	if err := recheckWorktreeMergeSources(ctx, receipt.Sources); err != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
	}
	if err := requireCleanMergeWorktree(ctx, candidate.WorktreeDir); err != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
	}
	// Validate both the exact target baseline and the integrated candidate.
	reportWorktreeMergeProgress(options.Progress, "validate_candidate", progress.Started, shortMergeRevision(receipt.Candidate.SHA))
	if validationErr := validateWorktreeMergeCandidate(ctx, &receipt, options.Timeout, options.Retry, options.Progress); validationErr != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeValidationFailed, validationErr)
	}
	reportWorktreeMergeProgress(options.Progress, "validate_candidate", progress.Completed, string(receipt.Validation.Status))
	receipt.Status = WorktreeMergePrepared
	receipt.Candidate.SHA, err = mergeRevision(ctx, candidate.WorktreeDir, "HEAD")
	if err != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
	}
	receipt.Failure = ""
	receipt.UpdatedAt = time.Now().UTC()
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		return receipt, err
	}
	reportWorktreeMergeProgress(options.Progress, "prepared", progress.Completed, receipt.ReceiptPath)
	return receipt, nil
}

// LandWorktreeMerge resumes a prepared receipt from its first incomplete
// boundary. It never reconstructs identity from a branch name and never
// force-pushes either the candidate or target branch.
func LandWorktreeMerge(ctx context.Context, options WorktreeMergeLandOptions) (WorktreeMergeReceipt, error) {
	reportWorktreeMergeProgress(options.Progress, "read_receipt", progress.Started, options.Receipt)
	receiptPath, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return WorktreeMergeReceipt{}, err
	}
	receipt, err := readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeReceipt{}, err
	}
	reportWorktreeMergeProgress(options.Progress, "read_receipt", progress.Completed, string(receipt.Status)+" at "+receiptPath)
	if receipt.Status == WorktreeMergeComplete {
		return receipt, nil
	}
	if receipt.Candidate.Worktree == "" || receipt.Candidate.SHA == "" {
		return receipt, fmt.Errorf("receipt %s has no prepared candidate", receiptPath)
	}
	lockID := receipt.Lane
	if lockID == "" {
		lockID = worktreeMergeLaneID(receipt.Repository, receipt.Target)
	}
	lock, err := AcquireOperationLock(options.ProjectsRoot, lockID, true)
	if err != nil {
		return receipt, err
	}
	locked := true
	defer func() {
		if locked {
			_ = lock.Release()
		}
	}()
	if retainWorktreeMergeLandIntent(&receipt, &options) {
		receipt.UpdatedAt = time.Now().UTC()
		if err := persistWorktreeMergeReceipt(receipt); err != nil {
			return receipt, err
		}
	}
	if receipt.PullRequest != "" && receipt.LandingSHA == "" {
		serverLanding, merged, observeErr := pullRequestLandingReceipt(ctx, receipt, options)
		if observeErr != nil {
			return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, observeErr)
		}
		if merged {
			remoteTarget, fetchErr := fetchExactMergeTarget(ctx, receipt.Candidate.Worktree, receipt.Target)
			if fetchErr != nil {
				return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, fetchErr)
			}
			containsLanding, ancestorErr := isMergeAncestor(ctx, receipt.Candidate.Worktree, serverLanding, remoteTarget)
			if ancestorErr != nil || !containsLanding {
				if ancestorErr == nil {
					ancestorErr = fmt.Errorf("exact remote target %s does not contain already-merged pull-request result %s", remoteTarget, serverLanding)
				}
				return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, ancestorErr)
			}
			receipt.LandingSHA = remoteTarget
			receipt.Status = WorktreeMergeLanded
			receipt.UpdatedAt = time.Now().UTC()
			if persistErr := persistWorktreeMergeReceipt(receipt); persistErr != nil {
				return receipt, persistErr
			}
			if releaseErr := lock.Release(); releaseErr != nil {
				return receipt, releaseErr
			}
			locked = false
			return LandWorktreeMerge(ctx, options)
		}
	}
	if receipt.LandingSHA != "" {
		if receipt.Checks.Status != PullRequestWaitPassed {
			reportWorktreeMergeProgress(options.Progress, "target_checks", progress.Waiting, shortMergeRevision(receipt.LandingSHA))
			postChecks, postErr := waitForWorktreeMergeChecks(ctx, receipt, options, "", receipt.LandingSHA)
			receipt.Checks = postChecks
			if postErr != nil {
				status := WorktreeMergePostTargetCIFailed
				if postChecks.Status == PullRequestWaitPending {
					status = WorktreeMergeChecksPending
				}
				return failWorktreeMergeReceipt(receipt, status, postErr)
			}
		}
		if receipt.CanonicalSync != "fast_forwarded" && receipt.CanonicalSync != "not_checked_out" {
			reportWorktreeMergeProgress(options.Progress, "sync_canonical", progress.Started, receipt.Target+"@"+shortMergeRevision(receipt.LandingSHA))
			canonical := filepath.Join(options.ProjectsRoot, filepath.FromSlash(receipt.Repository))
			receipt.CanonicalSync, err = syncCanonicalMergeTarget(ctx, canonical, receipt.Target, receipt.LandingSHA, options.Timeout, options.Retry)
			if err != nil {
				return failWorktreeMergeReceipt(receipt, WorktreeMergeCanonicalSyncBlocked, err)
			}
		}
		reportWorktreeMergeProgress(options.Progress, "sync_canonical", progress.Completed, receipt.CanonicalSync)
		receipt.Status = WorktreeMergeLanded
		receipt.Cleanup = receipt.Cleanup || options.Cleanup
		receipt.UpdatedAt = time.Now().UTC()
		if err := persistWorktreeMergeReceipt(receipt); err != nil {
			return receipt, err
		}
		if !receipt.Cleanup {
			reportWorktreeMergeProgress(options.Progress, "landed", progress.Completed, receipt.ReceiptPath)
			return receipt, nil
		}
		if err := lock.Release(); err != nil {
			return receipt, err
		}
		locked = false
		if err := cleanupWorktreeMergeAssets(ctx, options.ProjectsRoot, &receipt); err != nil {
			return failWorktreeMergeReceipt(receipt, WorktreeMergeLanded, err)
		}
		reportWorktreeMergeProgress(options.Progress, "cleanup", progress.Completed, strings.Join(receipt.CleanedTasks, ", "))
		receipt.Status = WorktreeMergeComplete
		receipt.UpdatedAt = time.Now().UTC()
		if err := persistWorktreeMergeReceipt(receipt); err != nil {
			return receipt, err
		}
		return receipt, nil
	}

	if err := requireCleanMergeWorktree(ctx, receipt.Candidate.Worktree); err != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
	}
	head, err := mergeRevision(ctx, receipt.Candidate.Worktree, "HEAD")
	if err != nil || head != receipt.Candidate.SHA {
		if err == nil {
			err = fmt.Errorf("candidate head drifted from %s to %s", receipt.Candidate.SHA, head)
		}
		return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
	}
	if err := recheckWorktreeMergeSources(ctx, receipt.Sources); err != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
	}

	remoteTarget, err := fetchExactMergeTarget(ctx, receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
	}
	reportWorktreeMergeProgress(options.Progress, "refresh_target", progress.Completed, receipt.Target+"@"+shortMergeRevision(remoteTarget))
	containsTarget, err := isMergeAncestor(ctx, receipt.Candidate.Worktree, remoteTarget, receipt.Candidate.SHA)
	if err != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
	}
	if !containsTarget {
		if receipt.PullRequest != "" {
			return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, fmt.Errorf("target advanced to %s after candidate %s was published in %s; refusing to rewrite the published branch without force-push", remoteTarget, receipt.Candidate.SHA, receipt.PullRequest))
		}
		preparedCandidate, preparedTarget := receipt.Candidate.SHA, receipt.TargetSHA
		containsPreparedTarget, ancestorErr := isMergeAncestor(ctx, receipt.Candidate.Worktree, preparedTarget, preparedCandidate)
		if ancestorErr != nil || !containsPreparedTarget {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("prepared candidate %s no longer contains recorded target %s", preparedCandidate, preparedTarget)
			}
			return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, ancestorErr)
		}
		reportWorktreeMergeProgress(options.Progress, "rebase_candidate", progress.Started, shortMergeRevision(remoteTarget))
		if _, _, err := runCommand(ctx, options.Timeout, options.Retry, receipt.Candidate.Worktree,
			"git", "rebase", "--rebase-merges", "--onto", remoteTarget, preparedTarget, receipt.Candidate.Branch); err != nil {
			_, _, _ = runCommand(ctx, options.Timeout, 0, receipt.Candidate.Worktree, "git", "rebase", "--abort")
			return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, fmt.Errorf("target drift conflicts while rebasing the isolated candidate onto %s: %w", remoteTarget, err))
		}
		receipt.Candidate.SHA, err = mergeRevision(ctx, receipt.Candidate.Worktree, "HEAD")
		if err != nil {
			return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
		}
		receipt.TargetSHA = remoteTarget
		receipt.Rebase = &WorktreeMergeRebaseReceipt{
			CandidateBefore: preparedCandidate, TargetBefore: preparedTarget,
			TargetAfter: remoteTarget, CandidateAfter: receipt.Candidate.SHA,
		}
		receipt.UpdatedAt = time.Now().UTC()
		if err := persistWorktreeMergeReceipt(receipt); err != nil {
			return receipt, err
		}
		if err := requireCleanMergeWorktree(ctx, receipt.Candidate.Worktree); err != nil {
			return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
		}
		reportWorktreeMergeProgress(options.Progress, "validate_rebased_candidate", progress.Started, shortMergeRevision(receipt.Candidate.SHA))
		if validationErr := validateWorktreeMergeCandidate(ctx, &receipt, options.Timeout, options.Retry, options.Progress); validationErr != nil {
			return failWorktreeMergeReceipt(receipt, WorktreeMergeValidationFailed, fmt.Errorf("candidate validation failed after incorporating target drift: %w", validationErr))
		}
	}

	decision, err := ResolveWorktreeMergeRoute(ctx, receipt.Repository, receipt.Target, options.Route)
	if err != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
	}
	if decision.Route == WorktreeMergeRouteUnsupported {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, fmt.Errorf("unsupported target policy: %s", decision.Reason))
	}
	reportWorktreeMergeProgress(options.Progress, "resolve_route", progress.Completed, string(decision.Route)+": "+decision.Reason)
	receipt.Phase, receipt.Route = WorktreeMergePhaseLand, decision
	if receipt.PreviousTargetSHA == "" {
		receipt.PreviousTargetSHA = remoteTarget
	}
	receipt.Cleanup = receipt.Cleanup || options.Cleanup
	receipt.UpdatedAt = time.Now().UTC()
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		return receipt, err
	}

	serverLanding := receipt.Candidate.SHA
	if decision.Route == WorktreeMergeRouteDirect {
		remoteRef := "refs/heads/" + receipt.Target
		reportWorktreeMergeProgress(options.Progress, "pre_push_gate", progress.Started, remoteRef)
		receipt.PushGate, err = runWorktreeMergePrePushGate(ctx, receipt.Candidate.Worktree, receipt.Candidate.SHA, remoteRef, options.Timeout, options.Retry)
		if err != nil {
			return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
		}
		if err := persistWorktreeMergeReceipt(receipt); err != nil {
			return receipt, err
		}
		if err := pushWorktreeMergeRef(ctx, receipt.Candidate.Worktree, receipt.Candidate.SHA, remoteRef, false, options.Timeout, options.Retry); err != nil {
			return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, fmt.Errorf("direct target push failed without force: %w", err))
		}
		reportWorktreeMergeProgress(options.Progress, "publish_target", progress.Completed, remoteRef+"@"+shortMergeRevision(receipt.Candidate.SHA))
	} else {
		remoteRef := "refs/heads/" + receipt.Candidate.Branch
		reportWorktreeMergeProgress(options.Progress, "pre_push_gate", progress.Started, remoteRef)
		receipt.PushGate, err = runWorktreeMergePrePushGate(ctx, receipt.Candidate.Worktree, receipt.Candidate.SHA, remoteRef, options.Timeout, options.Retry)
		if err != nil {
			return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
		}
		if err := persistWorktreeMergeReceipt(receipt); err != nil {
			return receipt, err
		}
		if err := pushWorktreeMergeRef(ctx, receipt.Candidate.Worktree, receipt.Candidate.SHA, remoteRef, true, options.Timeout, options.Retry); err != nil {
			return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, fmt.Errorf("candidate push failed without force: %w", err))
		}
		reportWorktreeMergeProgress(options.Progress, "publish_candidate", progress.Completed, remoteRef+"@"+shortMergeRevision(receipt.Candidate.SHA))
		title, body, err := worktreeMergePRText(ctx, receipt)
		if err != nil {
			return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
		}
		receipt.PullRequest, err = openPullRequest(ctx, receipt.Candidate.Worktree, receipt.Candidate.Branch, receipt.Target, title, body,
			Options{Timeout: options.Timeout, Retry: options.Retry})
		if err != nil {
			return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
		}
		reportWorktreeMergeProgress(options.Progress, "open_pull_request", progress.Completed, receipt.PullRequest)
		receipt.PublishedCandidateSHA = receipt.Candidate.SHA
		receipt.UpdatedAt = time.Now().UTC()
		if err := persistWorktreeMergeReceipt(receipt); err != nil {
			return receipt, err
		}
		reportWorktreeMergeProgress(options.Progress, "candidate_checks", progress.Waiting, receipt.PullRequest)
		checks, err := waitForWorktreeMergeChecks(ctx, receipt, options, receipt.PullRequest, receipt.Candidate.SHA)
		receipt.Checks = checks
		if err != nil {
			status := WorktreeMergeChecksFailed
			if checks.Status == PullRequestWaitPending {
				status = WorktreeMergeChecksPending
			}
			return failWorktreeMergeReceipt(receipt, status, err)
		}
		serverLanding, err = mergeExactPullRequest(ctx, receipt, options)
		if err != nil {
			return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
		}
		reportWorktreeMergeProgress(options.Progress, "merge_pull_request", progress.Completed, shortMergeRevision(serverLanding))
	}

	landing, err := fetchExactMergeTarget(ctx, receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
	}
	containsServerLanding, err := isMergeAncestor(ctx, receipt.Candidate.Worktree, serverLanding, landing)
	if err != nil || !containsServerLanding {
		if err == nil {
			err = fmt.Errorf("exact remote target %s does not contain server landing %s", landing, serverLanding)
		}
		return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
	}
	receipt.LandingSHA = landing
	reportWorktreeMergeProgress(options.Progress, "verify_remote_landing", progress.Completed, receipt.Target+"@"+shortMergeRevision(landing))
	receipt.Status = WorktreeMergeLanded
	receipt.UpdatedAt = time.Now().UTC()
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		return receipt, err
	}

	reportWorktreeMergeProgress(options.Progress, "target_checks", progress.Waiting, shortMergeRevision(landing))
	postChecks, postErr := waitForWorktreeMergeChecks(ctx, receipt, options, "", landing)
	receipt.Checks = postChecks
	if postErr != nil {
		status := WorktreeMergePostTargetCIFailed
		if postChecks.Status == PullRequestWaitPending {
			status = WorktreeMergeChecksPending
		}
		failed, failure := failWorktreeMergeReceipt(receipt, status, postErr)
		if status == WorktreeMergePostTargetCIFailed && strings.TrimSpace(options.OnFailure) == "revert" {
			_, _ = PrepareWorktreeMergeRevert(ctx, options.ProjectsRoot, failed.ReceiptPath, options.Timeout, options.Retry)
		}
		return failed, failure
	}

	canonical := filepath.Join(options.ProjectsRoot, filepath.FromSlash(receipt.Repository))
	reportWorktreeMergeProgress(options.Progress, "sync_canonical", progress.Started, canonical)
	receipt.CanonicalSync, err = syncCanonicalMergeTarget(ctx, canonical, receipt.Target, landing, options.Timeout, options.Retry)
	if err != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeCanonicalSyncBlocked, err)
	}
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		return receipt, err
	}
	reportWorktreeMergeProgress(options.Progress, "sync_canonical", progress.Completed, receipt.CanonicalSync)
	if !receipt.Cleanup {
		reportWorktreeMergeProgress(options.Progress, "landed", progress.Completed, receipt.ReceiptPath)
		return receipt, nil
	}
	if err := lock.Release(); err != nil {
		return receipt, err
	}
	locked = false
	if err := cleanupWorktreeMergeAssets(ctx, options.ProjectsRoot, &receipt); err != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeLanded, err)
	}
	reportWorktreeMergeProgress(options.Progress, "cleanup", progress.Completed, strings.Join(receipt.CleanedTasks, ", "))
	receipt.Status = WorktreeMergeComplete
	receipt.UpdatedAt = time.Now().UTC()
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		return receipt, err
	}
	return receipt, nil
}

func canonicalForMergeSource(ctx context.Context, source string) (string, error) {
	rootOutput, _, err := runCommand(ctx, 0, 0, source, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(rootOutput)
	commonOutput, _, err := runCommand(ctx, 0, 0, root, "git", "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	common := strings.TrimSpace(commonOutput)
	if !filepath.IsAbs(common) {
		common = filepath.Join(root, common)
	}
	return filepath.Dir(filepath.Clean(common)), nil
}

func ResumeWorktreeMerge(ctx context.Context, options WorktreeMergeLandOptions) (WorktreeMergeReceipt, error) {
	return LandWorktreeMerge(ctx, options)
}

func runWorktreeMergePrePushGate(ctx context.Context, worktree, localSHA, remoteRef string, timeout time.Duration, retry int) (*WorktreeMergePushGateReceipt, error) {
	remoteOutput, _, err := runCommand(ctx, timeout, retry, worktree, "git", "ls-remote", "--heads", "origin", remoteRef)
	if err != nil {
		return nil, fmt.Errorf("inspect exact remote ref before pre-push gate: %w", err)
	}
	previousRemoteSHA := strings.Repeat("0", 40)
	if fields := strings.Fields(remoteOutput); len(fields) > 0 {
		previousRemoteSHA = fields[0]
	}
	remoteURL, _, err := runCommand(ctx, timeout, retry, worktree, "git", "remote", "get-url", "--push", "origin")
	if err != nil {
		return nil, fmt.Errorf("resolve push remote for pre-push gate: %w", err)
	}
	localRef, _, err := runCommand(ctx, timeout, retry, worktree, "git", "symbolic-ref", "-q", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("resolve local branch for pre-push gate: %w", err)
	}
	input, err := os.CreateTemp("", "wb-worktree-merge-pre-push-*.txt")
	if err != nil {
		return nil, err
	}
	inputPath := input.Name()
	defer func() { _ = os.Remove(inputPath) }()
	if err := input.Chmod(0o600); err != nil {
		_ = input.Close()
		return nil, err
	}
	if _, err := fmt.Fprintf(input, "%s %s %s %s\n", strings.TrimSpace(localRef), localSHA, remoteRef, previousRemoteSHA); err != nil {
		_ = input.Close()
		return nil, err
	}
	if err := input.Close(); err != nil {
		return nil, err
	}
	if _, _, err := runCommand(ctx, timeout, retry, worktree, "git", "hook", "run", "--ignore-missing", "--to-stdin", inputPath,
		"pre-push", "--", "origin", strings.TrimSpace(remoteURL)); err != nil {
		return nil, fmt.Errorf("managed pre-push gate failed before opening the push connection: %w", err)
	}
	return &WorktreeMergePushGateReceipt{
		Remote: "origin", RemoteRef: remoteRef, PreviousRemoteSHA: previousRemoteSHA,
		LocalSHA: localSHA, Status: "passed", ObservedAt: time.Now().UTC(),
	}, nil
}

func pushWorktreeMergeRef(ctx context.Context, worktree, localSHA, remoteRef string, setUpstream bool, timeout time.Duration, retry int) error {
	args := []string{"push", "--no-verify"}
	if setUpstream {
		args = append(args, "-u")
	}
	args = append(args, "origin", localSHA+":"+remoteRef)
	_, _, err := runCommand(ctx, timeout, retry, worktree, "git", args...)
	return err
}

// retainWorktreeMergeLandIntent makes a combined command's requested landing
// semantics part of the durable receipt before any target-drift, policy, push,
// or check boundary can interrupt it. A bare resume therefore cannot silently
// downgrade direct to auto, forget cleanup, or lose a requested forward revert.
func retainWorktreeMergeLandIntent(receipt *WorktreeMergeReceipt, options *WorktreeMergeLandOptions) bool {
	requestedRoute := options.Route
	if requestedRoute == "" {
		requestedRoute = WorktreeMergeRouteAuto
	}
	if receipt.Route.Requested != "" && (options.Route == "" || options.Route == WorktreeMergeRouteAuto) {
		requestedRoute = receipt.Route.Requested
	}
	onFailure := strings.TrimSpace(options.OnFailure)
	if onFailure == "" {
		onFailure = "stop"
	}
	if receipt.OnFailure != "" && (strings.TrimSpace(options.OnFailure) == "" || options.OnFailure == "stop") {
		onFailure = receipt.OnFailure
	}
	cleanup := receipt.Cleanup || options.Cleanup
	progressRequested := options.ProgressRequested || stringSliceContains(receipt.ResumeArgs, "--progress")
	resumeArgs := []string{"worktree", "merge", "resume", receipt.ReceiptPath, "--route", string(requestedRoute)}
	if cleanup {
		resumeArgs = append(resumeArgs, "--cleanup")
	}
	if progressRequested {
		resumeArgs = append(resumeArgs, "--progress")
	}
	resumeArgs = append(resumeArgs, "--on-failure", onFailure)
	changed := receipt.Route.Requested != requestedRoute || receipt.Cleanup != cleanup || receipt.OnFailure != onFailure ||
		strings.Join(receipt.ResumeArgs, "\x00") != strings.Join(resumeArgs, "\x00")
	receipt.Route.Requested = requestedRoute
	receipt.Cleanup = cleanup
	receipt.OnFailure = onFailure
	receipt.ResumeArgs = resumeArgs
	options.Route = requestedRoute
	options.Cleanup = cleanup
	options.OnFailure = onFailure
	options.ProgressRequested = progressRequested
	return changed
}

func worktreeMergePrepareResumeArgs(receiptPath string, progressRequested bool) []string {
	args := []string{"worktree", "merge", "resume", receiptPath}
	if progressRequested {
		args = append(args, "--progress")
	}
	return args
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func RunWorktreeMerge(ctx context.Context, prepare WorktreeMergePrepareOptions, land WorktreeMergeLandOptions) (WorktreeMergeReceipt, error) {
	receipt, err := PrepareWorktreeMerge(ctx, prepare)
	if err != nil {
		return receipt, err
	}
	land.ProjectsRoot, land.Receipt = prepare.ProjectsRoot, receipt.ReceiptPath
	return LandWorktreeMerge(ctx, land)
}

func inspectWorktreeMergeSources(ctx context.Context, projectsRoot string, paths []string, target string) ([]WorktreeMergeSource, string, string, error) {
	sources := make([]WorktreeMergeSource, 0, len(paths))
	seen := map[string]bool{}
	var repository, canonical string
	for _, input := range paths {
		guard, err := worktrees.Guard(ctx, input, worktrees.GuardOptions{ProjectsRoot: projectsRoot, Base: target})
		if err != nil {
			return nil, "", "", fmt.Errorf("guard source %s: %w", input, err)
		}
		if guard.Kind != "linked" || guard.Transient {
			return nil, "", "", fmt.Errorf("source %s must be a non-transient WB linked worktree", input)
		}
		if err := requireCleanMergeWorktree(ctx, guard.Path); err != nil {
			return nil, "", "", fmt.Errorf("source %s: %w", input, err)
		}
		view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: projectsRoot, Worktree: guard.Path})
		if err != nil {
			return nil, "", "", fmt.Errorf("load Work Log for source %s: %w", input, err)
		}
		if view.Claim == nil {
			return nil, "", "", fmt.Errorf("source %s has no authoritative active Work Log claim", input)
		}
		if repository == "" {
			repository, canonical = view.Claim.Repository, guard.CanonicalDir
		} else if view.Claim.Repository != repository || filepath.Clean(guard.CanonicalDir) != filepath.Clean(canonical) {
			return nil, "", "", fmt.Errorf("all source worktrees must belong to one repository; got %s and %s", repository, view.Claim.Repository)
		}
		head, err := mergeRevision(ctx, guard.Path, "HEAD")
		if err != nil {
			return nil, "", "", err
		}
		if seen[head] {
			return nil, "", "", fmt.Errorf("source head %s was supplied more than once", head)
		}
		seen[head] = true
		sources = append(sources, WorktreeMergeSource{Task: view.Claim.Task, Worktree: guard.Path, Branch: guard.Branch, SHA: head})
	}
	return sources, repository, canonical, nil
}

func ResolveWorktreeMergeRoute(ctx context.Context, repository, target string, requested WorktreeMergeRoute) (WorktreeMergeRouteDecision, error) {
	if requested == "" {
		requested = WorktreeMergeRouteAuto
	}
	if requested != WorktreeMergeRouteAuto && requested != WorktreeMergeRouteDirect && requested != WorktreeMergeRoutePullRequest {
		return WorktreeMergeRouteDecision{}, fmt.Errorf("unsupported merge route %q", requested)
	}
	escapedTarget := url.PathEscape(target)
	branchEndpoint := "repos/" + repository + "/branches/" + escapedTarget
	branchOutput, _, err := runCommand(ctx, 0, 0, "", "gh", "api", branchEndpoint)
	if err != nil {
		return conservativeWorktreeMergePRRoute(requested, fmt.Sprintf("target branch policy is unavailable: %v", err))
	}
	var branch struct {
		Protected  *bool `json:"protected"`
		Protection struct {
			RequiredPullRequestReviews json.RawMessage `json:"required_pull_request_reviews"`
		} `json:"protection"`
	}
	if err := json.Unmarshal([]byte(branchOutput), &branch); err != nil || branch.Protected == nil {
		return conservativeWorktreeMergePRRoute(requested, fmt.Sprintf("authoritative target branch policy for %s is incomplete", target))
	}
	rulesEndpoint := "repos/" + repository + "/rules/branches/" + escapedTarget + "?per_page=100"
	rulesOutput, _, err := runCommand(ctx, 0, 0, "", "gh", "api", "--paginate", "--slurp", rulesEndpoint)
	if err != nil {
		return conservativeWorktreeMergePRRoute(requested, fmt.Sprintf("active target rules are unavailable: %v", err))
	}
	var pages [][]githubActiveBranchRule
	if err := json.Unmarshal([]byte(rulesOutput), &pages); err != nil || len(pages) == 0 {
		return conservativeWorktreeMergePRRoute(requested, fmt.Sprintf("authoritative active target rules for %s are incomplete", target))
	}
	requiresPR := len(branch.Protection.RequiredPullRequestReviews) > 0 && string(branch.Protection.RequiredPullRequestReviews) != "null"
	mergeQueue := false
	unknownRule := false
	for _, page := range pages {
		for _, rule := range page {
			switch strings.TrimSpace(rule.Type) {
			case "pull_request":
				requiresPR = true
			case "merge_queue":
				mergeQueue = true
			case "required_status_checks", "creation", "update", "deletion", "non_fast_forward", "required_linear_history", "required_signatures", "commit_author_email_pattern", "commit_message_pattern", "branch_name_pattern", "tag_name_pattern":
			default:
				unknownRule = true
			}
		}
	}
	decision := WorktreeMergeRouteDecision{Requested: requested}
	if mergeQueue {
		decision.Route, decision.Reason = WorktreeMergeRouteUnsupported, "target requires a merge queue, whose merge-group receipt is not implemented"
		return decision, nil
	}
	if requested == WorktreeMergeRouteDirect {
		if *branch.Protected || requiresPR || unknownRule || activeRuleCount(pages) != 0 {
			return decision, fmt.Errorf("direct route is not authoritatively permitted by target policy")
		}
		decision.Route, decision.Reason = WorktreeMergeRouteDirect, "explicit direct route is permitted by authoritative target policy"
		return decision, nil
	}
	if requested == WorktreeMergeRoutePullRequest {
		decision.Route, decision.Reason = WorktreeMergeRoutePullRequest, "explicit pull-request route"
		return decision, nil
	}
	if !*branch.Protected && !requiresPR && !unknownRule && activeRuleCount(pages) == 0 {
		decision.Route, decision.Reason = WorktreeMergeRouteDirect, "target is authoritatively unprotected and has no active rules"
	} else {
		decision.Route, decision.Reason = WorktreeMergeRoutePullRequest, "target protection or conservative policy requires a pull request"
	}
	return decision, nil
}

func conservativeWorktreeMergePRRoute(requested WorktreeMergeRoute, reason string) (WorktreeMergeRouteDecision, error) {
	decision := WorktreeMergeRouteDecision{Requested: requested, Route: WorktreeMergeRoutePullRequest, Reason: reason + "; selecting a pull request conservatively"}
	if requested == WorktreeMergeRouteDirect {
		return decision, fmt.Errorf("direct route is not authoritatively permitted: %s", reason)
	}
	return decision, nil
}

func resolveWorktreeMergeReceiptPath(projectsRoot, input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", fmt.Errorf("candidate worktree or receipt is required")
	}
	absolute, err := filepath.Abs(input)
	if err != nil {
		return "", err
	}
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return "", err
	}
	reports := filepath.Join(home, "reports", "worktree-merge")
	if info, statErr := os.Stat(absolute); statErr == nil && !info.IsDir() {
		if !pathInsideWorktreeMergeReports(reports, absolute) {
			return "", fmt.Errorf("merge receipt %s is outside the authoritative WB report store %s", absolute, reports)
		}
		return absolute, nil
	}
	entries, err := os.ReadDir(reports)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(reports, entry.Name())
		receipt, readErr := readWorktreeMergeReceipt(path)
		if readErr == nil && filepath.Clean(receipt.Candidate.Worktree) == filepath.Clean(absolute) {
			return path, nil
		}
	}
	return "", fmt.Errorf("no worktree merge receipt owns %s", input)
}

func pathInsideWorktreeMergeReports(reports, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(reports), filepath.Clean(path))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	resolvedReports, reportsErr := filepath.EvalSymlinks(reports)
	resolvedPath, pathErr := filepath.EvalSymlinks(path)
	if reportsErr != nil || pathErr != nil {
		return false
	}
	resolvedRelative, err := filepath.Rel(resolvedReports, resolvedPath)
	return err == nil && resolvedRelative != "." && resolvedRelative != ".." && !strings.HasPrefix(resolvedRelative, ".."+string(filepath.Separator))
}

func fetchExactMergeTarget(ctx context.Context, worktree, target string) (string, error) {
	refspec := "+refs/heads/" + target + ":refs/remotes/origin/" + target
	if _, _, err := runCommand(ctx, 0, 0, worktree, "git", "fetch", "--no-tags", "origin", refspec); err != nil {
		return "", fmt.Errorf("fetch exact remote target %s: %w", target, err)
	}
	return mergeRevision(ctx, worktree, "refs/remotes/origin/"+target)
}

func worktreeMergePRText(ctx context.Context, receipt WorktreeMergeReceipt) (string, string, error) {
	output, _, err := runCommand(ctx, 0, 0, receipt.Candidate.Worktree, "git", "log", "--format=%s", receipt.TargetSHA+".."+receipt.Candidate.SHA)
	if err != nil {
		return "", "", err
	}
	var subjects []string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			subjects = append(subjects, line)
		}
	}
	title := worktreeMergePRTitle(subjects, len(receipt.Sources), receipt.Target)
	var body strings.Builder
	fmt.Fprintf(&body, "Mechanically prepared by `wb worktree merge` from exact source heads.\n\n")
	for _, source := range receipt.Sources {
		fmt.Fprintf(&body, "- `%s` at `%s`\n", source.Branch, source.SHA)
	}
	if len(subjects) > 0 {
		body.WriteString("\nCommits:\n\n")
		for index := len(subjects) - 1; index >= 0; index-- {
			fmt.Fprintf(&body, "- %s\n", subjects[index])
		}
	}
	fmt.Fprintf(&body, "\nCandidate: `%s`\n", receipt.Candidate.SHA)
	return title, body.String(), nil
}

var conventionalWorktreeMergeSubject = regexp.MustCompile(`^([[:alpha:]]+)(\([^)]*\))?(!)?:[[:space:]]+`)

func worktreeMergePRTitle(subjects []string, sourceCount int, target string) string {
	if len(subjects) == 1 {
		return subjects[0]
	}
	type choice struct {
		prefix   string
		priority int
	}
	selected := choice{prefix: "fix:", priority: 1}
	for _, subject := range subjects {
		match := conventionalWorktreeMergeSubject.FindStringSubmatch(strings.TrimSpace(subject))
		if len(match) == 0 {
			continue
		}
		kind := strings.ToLower(match[1])
		breaking := match[3] == "!"
		candidate := choice{prefix: kind + ":", priority: 2}
		switch {
		case breaking:
			candidate.prefix, candidate.priority = kind+"!:", 5
		case kind == "feat":
			candidate.prefix, candidate.priority = "feat:", 4
		case kind == "fix" || kind == "perf" || kind == "revert":
			candidate.prefix, candidate.priority = "fix:", 3
		}
		if candidate.priority > selected.priority {
			selected = candidate
		}
	}
	return fmt.Sprintf("%s merge %d worktree candidates into %s", selected.prefix, sourceCount, target)
}

func waitForWorktreeMergeChecks(ctx context.Context, receipt WorktreeMergeReceipt, options WorktreeMergeLandOptions, pullRequest, head string) (PullRequestWaitResult, error) {
	slice := options.Timeout
	if slice <= 0 || slice > 8*time.Minute {
		slice = 8 * time.Minute
	}
	interval := options.CheckPollInterval
	if interval <= 0 {
		interval = DefaultCheckPollInterval
	}
	if interval >= slice {
		return PullRequestWaitResult{}, fmt.Errorf("CI poll interval %s must be shorter than wait slice %s", interval, slice)
	}
	result, err := WaitForCommitChecks(ctx, PullRequestWaitOptions{
		Repository: receipt.Repository, PullRequest: pullRequest, Target: receipt.Target, Head: head,
		Slice: slice, CheckPollInterval: interval, Progress: reportWorktreeMergeCheckProgress(options.Progress, worktreeMergeCheckPhase(pullRequest)),
	})
	if err != nil {
		return result, err
	}
	switch result.Status {
	case PullRequestWaitPassed:
		return result, nil
	case PullRequestWaitPending:
		return result, fmt.Errorf("exact-head checks remain pending: %s; resume with wb worktree merge resume %s", result.Reason, receipt.ReceiptPath)
	default:
		return result, fmt.Errorf("exact-head checks failed: %s", result.Reason)
	}
}

func worktreeMergeCheckPhase(pullRequest string) string {
	if pullRequest != "" {
		return "candidate_checks"
	}
	return "target_checks"
}

func reportWorktreeMergeCheckProgress(reporter progress.Reporter, phase string) func(PullRequestWaitProgress) {
	if reporter == nil {
		return nil
	}
	return func(event PullRequestWaitProgress) {
		passed, pending, failed := 0, 0, 0
		for _, check := range event.Result.Checks {
			switch check.Bucket {
			case "pass", "skipping":
				passed++
			case "fail", "cancel":
				failed++
			default:
				pending++
			}
		}
		var state progress.State
		switch event.Result.Status {
		case PullRequestWaitPending:
			state = progress.Waiting
		case PullRequestWaitPassed:
			state = progress.Completed
		case PullRequestWaitFailed:
			state = progress.Failed
		default:
			state = progress.Running
		}
		detail := fmt.Sprintf("poll %d: %d passed, %d pending, %d failed", event.Observation, passed, pending, failed)
		if event.Result.StableObservations > 0 {
			detail += fmt.Sprintf("; stable %d/2", event.Result.StableObservations)
		}
		if event.NextPoll > 0 {
			detail += "; next poll in " + event.NextPoll.String()
		} else if strings.TrimSpace(event.Result.Reason) != "" {
			detail += "; " + event.Result.Reason
		}
		progress.Report(reporter, progress.Event{Operation: "worktree_merge", Phase: phase, State: state, Detail: detail})
	}
}

func reportWorktreeMergeQualityProgress(reporter progress.Reporter) func(quality.Progress) {
	if reporter == nil {
		return nil
	}
	return func(event quality.Progress) {
		var state progress.State
		if event.State == quality.ProgressStarted {
			state = progress.Started
		} else if event.Status == quality.StatusFailed {
			state = progress.Failed
		} else {
			state = progress.Completed
		}
		detail := strings.TrimSpace(event.Command)
		if event.Status != "" {
			detail += ": " + string(event.Status)
		}
		progress.Report(reporter, progress.Event{Operation: "worktree_merge", Phase: "validate_candidate", State: state, Detail: detail})
	}
}

func reportWorktreeMergeProgress(reporter progress.Reporter, phase string, state progress.State, detail string) {
	progress.Report(reporter, progress.Event{Operation: "worktree_merge", Phase: phase, State: state, Detail: detail})
}

func shortMergeRevision(revision string) string {
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}

func mergeExactPullRequest(ctx context.Context, receipt WorktreeMergeReceipt, options WorktreeMergeLandOptions) (string, error) {
	output, _, err := runCommand(ctx, options.Timeout, options.Retry, receipt.Candidate.Worktree, "gh", "api", "repos/"+receipt.Repository)
	if err != nil {
		return "", fmt.Errorf("read repository merge methods: %w", err)
	}
	var settings struct {
		AllowMerge  bool `json:"allow_merge_commit"`
		AllowSquash bool `json:"allow_squash_merge"`
		AllowRebase bool `json:"allow_rebase_merge"`
	}
	if err := json.Unmarshal([]byte(output), &settings); err != nil {
		return "", fmt.Errorf("decode repository merge methods: %w", err)
	}
	method := ""
	switch {
	case settings.AllowMerge:
		method = "--merge"
	case settings.AllowSquash:
		method = "--squash"
	case settings.AllowRebase:
		method = "--rebase"
	default:
		return "", fmt.Errorf("repository exposes no supported pull-request merge method")
	}
	if _, _, err := runCommand(ctx, options.Timeout, options.Retry, receipt.Candidate.Worktree, "gh", "pr", "merge", receipt.PullRequest,
		"--match-head-commit", receipt.Candidate.SHA, method); err != nil {
		return "", fmt.Errorf("merge exact pull-request head: %w", err)
	}
	serverLanding, merged, err := pullRequestLandingReceipt(ctx, receipt, options)
	if err != nil {
		return "", err
	}
	if !merged {
		return "", fmt.Errorf("pull request did not report a merged server result after merge command")
	}
	return serverLanding, nil
}

func pullRequestLandingReceipt(ctx context.Context, receipt WorktreeMergeReceipt, options WorktreeMergeLandOptions) (string, bool, error) {
	viewOutput, _, err := runCommand(ctx, options.Timeout, options.Retry, receipt.Candidate.Worktree, "gh", "pr", "view", receipt.PullRequest,
		"--repo", receipt.Repository, "--json", "state,mergedAt,mergeCommit,headRefOid,baseRefName")
	if err != nil {
		return "", false, fmt.Errorf("read pull-request landing receipt: %w", err)
	}
	var view struct {
		State       string `json:"state"`
		MergedAt    string `json:"mergedAt"`
		HeadRefOID  string `json:"headRefOid"`
		BaseRefName string `json:"baseRefName"`
		MergeCommit struct {
			OID string `json:"oid"`
		} `json:"mergeCommit"`
	}
	if err := json.Unmarshal([]byte(viewOutput), &view); err != nil {
		return "", false, fmt.Errorf("decode pull-request landing receipt: %w", err)
	}
	if view.BaseRefName != receipt.Target {
		return "", false, fmt.Errorf("pull-request landing receipt does not match target %s", receipt.Target)
	}
	if view.HeadRefOID != receipt.Candidate.SHA {
		advancesPublished, ancestorErr := isMergeAncestor(ctx, receipt.Candidate.Worktree, view.HeadRefOID, receipt.Candidate.SHA)
		if view.State == "MERGED" || receipt.PublishedCandidateSHA == "" || view.HeadRefOID != receipt.PublishedCandidateSHA || ancestorErr != nil || !advancesPublished {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("pull-request head %s does not match exact candidate %s or its recorded published predecessor %s", view.HeadRefOID, receipt.Candidate.SHA, receipt.PublishedCandidateSHA)
			}
			return "", false, ancestorErr
		}
	}
	if view.State != "MERGED" {
		return "", false, nil
	}
	if view.MergedAt == "" || view.MergeCommit.OID == "" {
		return "", false, fmt.Errorf("merged pull request omitted its time or server merge-result commit")
	}
	return view.MergeCommit.OID, true, nil
}

func syncCanonicalMergeTarget(ctx context.Context, canonical, target, landing string, timeout time.Duration, retry int) (string, error) {
	branch, _, err := runCommand(ctx, timeout, retry, canonical, "git", "branch", "--show-current")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(branch) != target {
		return "not_checked_out", nil
	}
	if err := requireCleanMergeWorktree(ctx, canonical); err != nil {
		return "blocked_dirty", fmt.Errorf("remote landed, but canonical target synchronization is blocked: %w", err)
	}
	if _, _, err := runCommand(ctx, timeout, retry, canonical, "git", "fetch", "--no-tags", "origin", "+refs/heads/"+target+":refs/remotes/origin/"+target); err != nil {
		return "blocked_fetch", err
	}
	if _, _, err := runCommand(ctx, timeout, retry, canonical, "git", "merge", "--ff-only", "refs/remotes/origin/"+target); err != nil {
		return "blocked_diverged", fmt.Errorf("remote landed, but canonical target cannot fast-forward: %w", err)
	}
	head, err := mergeRevision(ctx, canonical, "HEAD")
	if err != nil || head != landing {
		if err == nil {
			err = fmt.Errorf("canonical target is %s, exact remote target is %s", head, landing)
		}
		return "blocked_mismatch", err
	}
	return "fast_forwarded", nil
}

func cleanupWorktreeMergeAssets(ctx context.Context, projectsRoot string, receipt *WorktreeMergeReceipt) error {
	cleaned := make(map[string]bool, len(receipt.CleanedTasks))
	for _, task := range receipt.CleanedTasks {
		cleaned[task] = true
	}
	for _, task := range sortedUniqueMergeTasks(*receipt) {
		if cleaned[task] {
			continue
		}
		// Cleanup owns the single active -> terminal Work Log transition. Calling
		// LogFinalize first would make cleanup attempt the same immutable
		// transition twice and strand otherwise safe landed assets.
		outcome, err := worktrees.Cleanup(ctx, worktrees.CleanupOptions{
			ProjectsRoot: projectsRoot, Task: task, Base: receipt.Target, ExactRepository: receipt.Repository,
			AbsorbedBy: receipt.LandingSHA, MergeReceiptProofs: worktreeMergeCleanupProofs(*receipt, task),
			Apply: true, DeleteRemote: true, OlderThan: 0, Workers: 1,
		})
		if err != nil {
			return fmt.Errorf("cleanup task %s: %w", task, err)
		}
		receipt.CleanupReports = append(receipt.CleanupReports, outcome.ReportPath)
		for _, result := range outcome.Results {
			if !result.Applied {
				return fmt.Errorf("cleanup task %s remained unapplied: %s", task, result.Reason)
			}
		}
		receipt.CleanedTasks = append(receipt.CleanedTasks, task)
		if err := persistWorktreeMergeReceipt(*receipt); err != nil {
			return err
		}
	}
	return nil
}

func worktreeMergeCleanupProofs(receipt WorktreeMergeReceipt, task string) []worktrees.MergeReceiptCleanupProof {
	proofs := make([]worktrees.MergeReceiptCleanupProof, 0, len(receipt.Sources))
	for _, source := range receipt.Sources {
		if source.Task != task || !source.Merged {
			continue
		}
		proofs = append(proofs, worktrees.MergeReceiptCleanupProof{
			Repository: receipt.Repository, Target: receipt.Target,
			SourceTask: source.Task, SourceWorktree: source.Worktree, SourceBranch: source.Branch, SourceSHA: source.SHA,
			CandidateSHA: receipt.Candidate.SHA, LandingSHA: receipt.LandingSHA,
		})
	}
	return proofs
}

// PrepareWorktreeMergeRevert creates a fresh forward candidate which applies
// the inverse landing tree delta onto today's remote target. It never resets or
// force-pushes shared history.
func PrepareWorktreeMergeRevert(ctx context.Context, projectsRoot, input string, timeout time.Duration, retry int) (WorktreeMergeReceipt, error) {
	path, err := resolveWorktreeMergeReceiptPath(projectsRoot, input)
	if err != nil {
		return WorktreeMergeReceipt{}, err
	}
	receipt, err := readWorktreeMergeReceipt(path)
	if err != nil {
		return receipt, err
	}
	if receipt.Phase == WorktreeMergePhaseRevert {
		if receipt.Status == WorktreeMergePrepared {
			return receipt, nil
		}
		return receipt, fmt.Errorf("receipt already represents a forward revert with status %s", receipt.Status)
	}
	if receipt.PreviousTargetSHA == "" || receipt.LandingSHA == "" {
		return receipt, fmt.Errorf("receipt has no landed before/after target identity to revert")
	}
	revertOf := &WorktreeMergeRevertReceipt{
		PreviousTargetSHA: receipt.PreviousTargetSHA,
		LandingSHA:        receipt.LandingSHA,
		CandidateSHA:      receipt.Candidate.SHA,
	}
	task := "revert-" + receipt.ID
	prompt, err := writeWorktreeMergePrompt(receipt.Repository, receipt.Target, receipt.Sources)
	if err != nil {
		return receipt, err
	}
	defer func() { _ = os.Remove(prompt) }()
	created, err := worktrees.Create(ctx, []string{receipt.Repository}, worktrees.CreateOptions{
		ProjectsRoot: projectsRoot, Operation: task, Branch: "wb/revert/" + receipt.ID, BranchChosen: true, Base: receipt.Target,
		WorkLog: worktrees.WorkLogOptions{EffortID: task, RunID: task, Model: "unknown", AgentRuntime: "wb", OriginalPrompt: prompt, RequireOriginalPrompt: true},
	})
	if err != nil {
		return receipt, err
	}
	if len(created) != 1 {
		return receipt, fmt.Errorf("revert candidate creation returned %d repositories", len(created))
	}
	patchOutput, _, err := runCommand(ctx, timeout, retry, created[0].WorktreeDir, "git", "diff", "--binary", revertOf.PreviousTargetSHA, revertOf.LandingSHA)
	if err != nil {
		return receipt, err
	}
	patchFile, err := os.CreateTemp("", "wb-worktree-revert-*.patch")
	if err != nil {
		return receipt, err
	}
	patchPath := patchFile.Name()
	defer func() { _ = os.Remove(patchPath) }()
	if _, err := patchFile.WriteString(patchOutput); err != nil {
		_ = patchFile.Close()
		return receipt, err
	}
	if err := patchFile.Close(); err != nil {
		return receipt, err
	}
	if _, _, err := runCommand(ctx, timeout, retry, created[0].WorktreeDir, "git", "apply", "--check", "--3way", "--reverse", patchPath); err != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, fmt.Errorf("forward revert conflicts with current target: %w", err))
	}
	if _, _, err := runCommand(ctx, timeout, retry, created[0].WorktreeDir, "git", "apply", "--3way", "--reverse", "--index", patchPath); err != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
	}
	if _, _, err := runCommand(ctx, timeout, retry, created[0].WorktreeDir, "git", "commit", "-m", "revert: reverse worktree merge "+receipt.ID); err != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeConflict, err)
	}
	receipt.Phase = WorktreeMergePhaseRevert
	receipt.Status = WorktreeMergePrepared
	receipt.TargetSHA = created[0].BaseSHA
	receipt.Sources = nil
	receipt.Candidate = WorktreeMergeCandidate{Task: task, Worktree: created[0].WorktreeDir, Branch: created[0].Branch}
	receipt.Candidate.SHA, err = mergeRevision(ctx, created[0].WorktreeDir, "HEAD")
	receipt.RevertOf = revertOf
	receipt.Rebase = nil
	receipt.Route = WorktreeMergeRouteDecision{}
	receipt.PullRequest = ""
	receipt.PreviousTargetSHA = ""
	receipt.LandingSHA = ""
	receipt.CanonicalSync = ""
	receipt.Checks = PullRequestWaitResult{}
	receipt.Cleanup = false
	receipt.CleanupReports = nil
	receipt.CleanedTasks = nil
	receipt.Failure = ""
	receipt.UpdatedAt = time.Now().UTC()
	if err != nil {
		return receipt, err
	}
	if validationErr := validateWorktreeMergeCandidate(ctx, &receipt, timeout, retry, nil); validationErr != nil {
		return failWorktreeMergeReceipt(receipt, WorktreeMergeValidationFailed, fmt.Errorf("forward revert candidate validation failed: %w", validationErr))
	}
	if err := persistWorktreeMergeReceipt(receipt); err != nil {
		return receipt, err
	}
	return receipt, nil
}

// validateWorktreeMergeCandidate validates the candidate first. A passing
// candidate cannot regress a red target, so the expensive target snapshot is
// evaluated lazily only when candidate failure evidence needs comparison.
// Any new or changed candidate failure remains a hard gate.
func validateWorktreeMergeCandidate(ctx context.Context, receipt *WorktreeMergeReceipt, timeout time.Duration, retry int, reporter progress.Reporter) error {
	runOptions, err := quality.RepositoryRunOptions(receipt.Candidate.Worktree, quality.RunOptions{
		Timeout: timeout, Retry: retry, Progress: reportWorktreeMergeQualityProgress(reporter),
	})
	if err != nil {
		return fmt.Errorf("load candidate quality policy: %w", err)
	}
	receipt.Validation = quality.VerifyWithOptions(ctx, receipt.Repository, receipt.Candidate.Worktree,
		[]quality.Check{quality.CheckLint, quality.CheckTest, quality.CheckBuild, quality.CheckSpec},
		runOptions)
	receipt.Validation.Revision = receipt.Candidate.SHA
	receipt.Validation.WorkspaceClean = true
	if receipt.Validation.Status == quality.StatusPassed {
		receipt.BaselineValidation = quality.VerificationReport{
			Repository: receipt.Repository, Path: "git:" + receipt.TargetSHA, Revision: receipt.TargetSHA,
			WorkspaceClean: true, Status: quality.StatusSkipped,
			Results: []quality.VerificationEntry{{Status: quality.StatusSkipped,
				Detail: "candidate passed every configured local check; target baseline was not needed"}},
		}
		return nil
	}
	baseline, err := verifyWorktreeMergeTarget(ctx, receipt.Repository, receipt.Candidate.Worktree, receipt.TargetSHA, timeout, retry)
	if err != nil {
		return fmt.Errorf("capture exact target validation baseline after candidate failure: %w", err)
	}
	receipt.BaselineValidation = baseline
	if err := worktreeMergeValidationRegression(baseline, receipt.Validation); err != nil {
		return err
	}
	return nil
}

// verifyWorktreeMergeTarget materializes the exact fetched target revision in
// a temporary archive rather than trusting a mutable canonical checkout. This
// keeps the baseline tied to receipt.TargetSHA even while a candidate is being
// rebased for target drift.
func verifyWorktreeMergeTarget(ctx context.Context, repository, repositoryDir, targetSHA string, timeout time.Duration, retry int) (quality.VerificationReport, error) {
	targetSHA = strings.TrimSpace(targetSHA)
	if targetSHA == "" {
		return quality.VerificationReport{}, errors.New("target SHA is required for validation baseline")
	}
	temporary, err := os.MkdirTemp("", "wb-worktree-merge-target-*")
	if err != nil {
		return quality.VerificationReport{}, fmt.Errorf("create target validation snapshot: %w", err)
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	archivePath := filepath.Join(temporary, "target.tar")
	if _, _, err := runCommand(ctx, timeout, retry, repositoryDir, "git", "archive", "--format=tar", "--output="+archivePath, targetSHA); err != nil {
		return quality.VerificationReport{}, fmt.Errorf("archive target %s: %w", targetSHA, err)
	}
	snapshot := filepath.Join(temporary, "tree")
	if err := extractWorktreeMergeArchive(archivePath, snapshot); err != nil {
		return quality.VerificationReport{}, fmt.Errorf("materialize target %s: %w", targetSHA, err)
	}
	runOptions, err := quality.RepositoryRunOptions(snapshot, quality.RunOptions{Timeout: timeout, Retry: retry})
	if err != nil {
		return quality.VerificationReport{}, fmt.Errorf("load target quality policy: %w", err)
	}
	report := quality.VerifyWithOptions(ctx, repository, snapshot,
		[]quality.Check{quality.CheckLint, quality.CheckTest, quality.CheckBuild, quality.CheckSpec},
		runOptions)
	// The transient snapshot is intentionally removed before this durable
	// receipt is written. The exact revision remains the useful evidence.
	report.Path = "git:" + targetSHA
	report.Revision = targetSHA
	report.WorkspaceClean = true
	return report, nil
}

func extractWorktreeMergeArchive(archivePath, destination string) error {
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = archive.Close() }()
	reader := tar.NewReader(archive)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(header.Name)
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe archived path %q", header.Name)
		}
		path := filepath.Join(destination, name)
		switch header.Typeflag {
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
			// git archive emits PAX metadata before regular entries on some
			// platforms. The tar reader applies it to the following header.
			continue
		case tar.TypeDir:
			if err := os.MkdirAll(path, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, reader)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case tar.TypeSymlink:
			if filepath.IsAbs(header.Linkname) {
				return fmt.Errorf("unsafe archived symlink %q -> %q", header.Name, header.Linkname)
			}
			linkTarget := filepath.Clean(filepath.Join(filepath.Dir(path), header.Linkname))
			relativeTarget, err := filepath.Rel(destination, linkTarget)
			if err != nil || relativeTarget == ".." || strings.HasPrefix(relativeTarget, ".."+string(filepath.Separator)) {
				return fmt.Errorf("unsafe archived symlink %q -> %q", header.Name, header.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(header.Linkname, path); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported archived entry %q", header.Name)
		}
	}
}

func worktreeMergeValidationRegression(baseline, candidate quality.VerificationReport) error {
	baselineFailures := failedWorktreeMergeVerificationEntries(baseline)
	candidateFailures := failedWorktreeMergeVerificationEntries(candidate)
	if candidate.Status == quality.StatusFailed && len(candidateFailures) == 0 {
		return errors.New("candidate validation reported failure without failed check evidence")
	}
	matched := make([]bool, len(baselineFailures))
	for _, candidateFailure := range candidateFailures {
		found := false
		for index, baselineFailure := range baselineFailures {
			if !matched[index] && sameWorktreeMergeFailure(baselineFailure, candidateFailure) {
				matched[index] = true
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("candidate validation introduced or changed failure: %s %s %s", candidateFailure.Language, candidateFailure.Check, candidateFailure.Command)
		}
	}
	return nil
}

func failedWorktreeMergeVerificationEntries(report quality.VerificationReport) []quality.VerificationEntry {
	entries := make([]quality.VerificationEntry, 0, len(report.Results))
	for _, entry := range report.Results {
		if entry.Status == quality.StatusFailed {
			entries = append(entries, entry)
		}
	}
	return entries
}

func sameWorktreeMergeFailure(baseline, candidate quality.VerificationEntry) bool {
	return baseline.Language == candidate.Language && baseline.Module == candidate.Module && baseline.Check == candidate.Check &&
		baseline.Command == candidate.Command && normalizeWorktreeMergeFailureDetail(baseline.Detail) == normalizeWorktreeMergeFailureDetail(candidate.Detail)
}

func normalizeWorktreeMergeFailureDetail(detail string) string {
	// Quality command output can include the ephemeral checkout path. It is not
	// behavior, so compare a whitespace-normalized form after erasing absolute
	// paths. All command, check, module, and error text still has to match.
	fields := strings.Fields(detail)
	for index, field := range fields {
		if filepath.IsAbs(field) {
			fields[index] = "<workspace>"
		}
	}
	return strings.Join(fields, " ")
}

func activeRuleCount(pages [][]githubActiveBranchRule) int {
	count := 0
	for _, page := range pages {
		count += len(page)
	}
	return count
}

func worktreeMergeLaneID(repository, target string) string {
	hash := sha256.Sum256([]byte(repository + "\x00" + target))
	readable := strings.NewReplacer("/", "-", ".", "-", "_", "-").Replace(repository + "-" + target)
	readable = strings.Trim(readable, "-")
	if len(readable) > 42 {
		readable = readable[:42]
	}
	return "merge-" + readable + "-" + hex.EncodeToString(hash[:6])
}

func worktreeMergeOperationID(lane string, sources []WorktreeMergeSource) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(lane))
	for _, source := range sources {
		_, _ = hash.Write([]byte{'\x00'})
		_, _ = hash.Write([]byte(source.SHA))
	}
	return lane + "-" + hex.EncodeToString(hash.Sum(nil)[:6])
}

func mergeOperationSuffix(operation string) string {
	if index := strings.LastIndex(operation, "-"); index >= 0 && index+1 < len(operation) {
		return operation[index+1:]
	}
	return operation
}

func activeWorktreeMergeLaneReceipt(reportsDir, lane, except string) (*WorktreeMergeReceipt, error) {
	entries, err := os.ReadDir(reportsDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if entry.Name() != lane+".json" && !strings.HasPrefix(entry.Name(), lane+"-") {
			continue
		}
		path := filepath.Join(reportsDir, entry.Name())
		if filepath.Clean(path) == filepath.Clean(except) {
			continue
		}
		receipt, readErr := readWorktreeMergeReceipt(path)
		if readErr != nil {
			return nil, readErr
		}
		receiptLane := receipt.Lane
		if receiptLane == "" {
			receiptLane = worktreeMergeLaneID(receipt.Repository, receipt.Target)
		}
		if receiptLane == lane && receipt.Status != WorktreeMergeComplete {
			return &receipt, nil
		}
	}
	return nil, nil
}

func canRefreshWorktreeMergeReceipt(ctx context.Context, prior WorktreeMergeReceipt, sources []WorktreeMergeSource) (bool, error) {
	switch prior.Status {
	case WorktreeMergePreparing, WorktreeMergePrepared, WorktreeMergeConflict, WorktreeMergeValidationFailed, WorktreeMergeChecksFailed, WorktreeMergeChecksPending:
	default:
		return false, nil
	}
	if prior.LandingSHA != "" || prior.Candidate.Worktree == "" || prior.Candidate.Branch == "" || len(prior.Sources) != len(sources) {
		return false, nil
	}
	advanced := false
	for index := range sources {
		oldSource, newSource := prior.Sources[index], sources[index]
		if oldSource.Worktree != newSource.Worktree || oldSource.Branch != newSource.Branch || oldSource.Task != newSource.Task {
			return false, nil
		}
		if oldSource.SHA == newSource.SHA {
			continue
		}
		containsOld, err := isMergeAncestor(ctx, newSource.Worktree, oldSource.SHA, newSource.SHA)
		if err != nil {
			return false, err
		}
		if !containsOld {
			return false, nil
		}
		advanced = true
	}
	retrySameCandidate := !advanced && prior.Status == WorktreeMergeValidationFailed
	if (!advanced && !retrySameCandidate) || requireCleanMergeWorktree(ctx, prior.Candidate.Worktree) != nil {
		return false, nil
	}
	remote, _, err := runCommand(ctx, 0, 0, prior.Candidate.Worktree, "git", "ls-remote", "--heads", "origin", "refs/heads/"+prior.Candidate.Branch)
	if err != nil {
		return false, err
	}
	remote = strings.TrimSpace(remote)
	if prior.PullRequest == "" {
		return remote == "", nil
	}
	localHead, headErr := mergeRevision(ctx, prior.Candidate.Worktree, "HEAD")
	if headErr != nil || localHead != prior.Candidate.SHA {
		return false, headErr
	}
	published := prior.PublishedCandidateSHA
	if published == "" {
		published = prior.Candidate.SHA
	}
	return strings.HasPrefix(remote, published+"\t"), nil
}

// canPreparePostTargetRepair recognizes the one safe continuation after a
// remote landing: exact target CI failed, the same source worktrees advanced
// additively, and the retained candidate has not moved. Prepare then advances
// that candidate to the fetched landed target before integrating the repair.
// Other landed states still own the lane and fail closed.
func canPreparePostTargetRepair(ctx context.Context, prior WorktreeMergeReceipt, sources []WorktreeMergeSource) (bool, error) {
	if prior.Status != WorktreeMergePostTargetCIFailed || prior.LandingSHA == "" ||
		prior.Candidate.Worktree == "" || prior.Candidate.Branch == "" || prior.Candidate.SHA == "" ||
		len(prior.Sources) != len(sources) {
		return false, nil
	}
	advanced := false
	for index := range sources {
		oldSource, newSource := prior.Sources[index], sources[index]
		if oldSource.Worktree != newSource.Worktree || oldSource.Branch != newSource.Branch || oldSource.Task != newSource.Task {
			return false, nil
		}
		if oldSource.SHA == newSource.SHA {
			continue
		}
		containsOld, err := isMergeAncestor(ctx, newSource.Worktree, oldSource.SHA, newSource.SHA)
		if err != nil {
			return false, err
		}
		if !containsOld {
			return false, nil
		}
		advanced = true
	}
	if !advanced || requireCleanMergeWorktree(ctx, prior.Candidate.Worktree) != nil {
		return false, nil
	}
	localHead, err := mergeRevision(ctx, prior.Candidate.Worktree, "HEAD")
	if err != nil || localHead != prior.Candidate.SHA {
		return false, err
	}
	remote, _, err := runCommand(ctx, 0, 0, prior.Candidate.Worktree, "git", "ls-remote", "--heads", "origin", "refs/heads/"+prior.Candidate.Branch)
	if err != nil {
		return false, err
	}
	remote = strings.TrimSpace(remote)
	published := prior.PublishedCandidateSHA
	if published == "" {
		published = prior.Candidate.SHA
	}
	return remote == "" || strings.HasPrefix(remote, published+"\t"), nil
}

func writeWorktreeMergePrompt(repository, target string, sources []WorktreeMergeSource) (string, error) {
	file, err := os.CreateTemp("", "wb-worktree-merge-prompt-*.txt")
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
	fmt.Fprintf(&body, "WB mechanically prepares an integration candidate for %s target %s from these exact source heads:\n", repository, target)
	for _, source := range sources {
		fmt.Fprintf(&body, "- %s %s %s\n", source.Branch, source.SHA, source.Worktree)
	}
	if _, err := file.WriteString(body.String()); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func persistWorktreeMergeReceipt(receipt WorktreeMergeReceipt) error {
	if receipt.ReceiptPath == "" {
		return fmt.Errorf("merge receipt path is required")
	}
	if err := os.MkdirAll(filepath.Dir(receipt.ReceiptPath), 0o700); err != nil {
		return err
	}
	contents, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(receipt.ReceiptPath), ".merge-receipt-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, receipt.ReceiptPath)
}

func readWorktreeMergeReceipt(path string) (WorktreeMergeReceipt, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return WorktreeMergeReceipt{}, err
	}
	var receipt WorktreeMergeReceipt
	if err := json.Unmarshal(contents, &receipt); err != nil {
		return WorktreeMergeReceipt{}, fmt.Errorf("decode merge receipt %s: %w", path, err)
	}
	if receipt.SchemaVersion != WorktreeMergeSchemaVersion || receipt.ReceiptPath != path {
		return WorktreeMergeReceipt{}, fmt.Errorf("merge receipt %s has invalid identity", path)
	}
	return receipt, nil
}

func failWorktreeMergeReceipt(receipt WorktreeMergeReceipt, status WorktreeMergeStatus, failure error) (WorktreeMergeReceipt, error) {
	receipt.Status = status
	receipt.Failure = failure.Error()
	receipt.UpdatedAt = time.Now().UTC()
	if persistErr := persistWorktreeMergeReceipt(receipt); persistErr != nil {
		return receipt, fmt.Errorf("%w; persist failure receipt: %v", failure, persistErr)
	}
	return receipt, failure
}

func requireCleanMergeWorktree(ctx context.Context, path string) error {
	status, _, err := runCommand(ctx, 0, 0, path, "git", "status", "--porcelain=v1")
	if err != nil {
		return err
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("worktree is dirty: %s", strings.TrimSpace(status))
	}
	return nil
}

func recheckWorktreeMergeSources(ctx context.Context, sources []WorktreeMergeSource) error {
	for _, source := range sources {
		if err := requireCleanMergeWorktree(ctx, source.Worktree); err != nil {
			return fmt.Errorf("source %s changed during prepare: %w", source.Worktree, err)
		}
		head, err := mergeRevision(ctx, source.Worktree, "HEAD")
		if err != nil {
			return err
		}
		if head != source.SHA {
			return fmt.Errorf("source %s advanced from %s to %s during prepare", source.Worktree, source.SHA, head)
		}
	}
	return nil
}

func mergeRevision(ctx context.Context, path, revision string) (string, error) {
	output, _, err := runCommand(ctx, 0, 0, path, "git", "rev-parse", "--verify", revision+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func mergeTreeRevision(ctx context.Context, path, revision string) (string, error) {
	output, _, err := runCommand(ctx, 0, 0, path, "git", "rev-parse", "--verify", revision+"^{tree}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func worktreeMergeCandidateAbsorbed(ctx context.Context, path string, prior WorktreeMergeReceipt, remoteTarget string) (absorbed, graphContained bool, err error) {
	containsCandidate, err := isMergeAncestor(ctx, path, prior.Candidate.SHA, remoteTarget)
	if err != nil || containsCandidate {
		return containsCandidate, containsCandidate, err
	}
	if prior.PullRequest == "" || prior.PublishedCandidateSHA == "" || prior.PublishedCandidateSHA != prior.Candidate.SHA || prior.LandingSHA == "" {
		return false, false, nil
	}
	containsLanding, err := isMergeAncestor(ctx, path, prior.LandingSHA, remoteTarget)
	if err != nil || !containsLanding {
		return false, false, err
	}
	candidateTree, err := mergeTreeRevision(ctx, path, prior.Candidate.SHA)
	if err != nil {
		return false, false, fmt.Errorf("resolve prior candidate tree %s: %w", prior.Candidate.SHA, err)
	}
	landingTree, err := mergeTreeRevision(ctx, path, prior.LandingSHA)
	if err != nil {
		return false, false, fmt.Errorf("resolve prior landing tree %s: %w", prior.LandingSHA, err)
	}
	return candidateTree == landingTree, false, nil
}

func isMergeAncestor(ctx context.Context, path, ancestor, descendant string) (bool, error) {
	output, _, err := runCommand(ctx, 0, 0, path, "git", "merge-base", ancestor, descendant)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(output) == ancestor, nil
}

func validMergeBranch(ctx context.Context, path, branch string) bool {
	_, _, err := runCommand(ctx, 0, 0, path, "git", "check-ref-format", "--branch", branch)
	return err == nil
}

func sameWorktreeMergeSources(left, right []WorktreeMergeSource) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Worktree != right[index].Worktree || left[index].Branch != right[index].Branch || left[index].SHA != right[index].SHA {
			return false
		}
	}
	return true
}

func sortedUniqueMergeTasks(receipt WorktreeMergeReceipt) []string {
	seen := map[string]bool{}
	tasks := make([]string, 0, len(receipt.Sources)+1)
	for _, source := range receipt.Sources {
		if source.Task != "" && !seen[source.Task] {
			seen[source.Task] = true
			tasks = append(tasks, source.Task)
		}
	}
	if receipt.Candidate.Task != "" && !seen[receipt.Candidate.Task] {
		tasks = append(tasks, receipt.Candidate.Task)
	}
	sort.Strings(tasks)
	return tasks
}
