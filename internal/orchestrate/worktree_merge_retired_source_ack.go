package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// WorktreeMergeCleanedSourceProof is the audited evidence that stands in for
// a receipted source checkout an applied WB cleanup already retired. It binds
// the exact cleanup report (by digest), the cleaned head, and the sealed
// terminal Work Log claim base; containment in the current remote target is
// proved afresh by the acknowledgement that records it.
type WorktreeMergeCleanedSourceProof struct {
	Task                string `json:"task"`
	Worktree            string `json:"worktree"`
	Branch              string `json:"branch"`
	SHA                 string `json:"sha"`
	CleanupReportPath   string `json:"cleanup_report_path"`
	CleanupReportSHA256 string `json:"cleanup_report_sha256"`
	CleanupHeadSHA      string `json:"cleanup_head_sha"`
	ClaimBaseSHA        string `json:"claim_base_sha"`
}

// WorktreeMergeSupersedingTargetChecks records the later target head whose
// exact-head checks passed, superseding a red post-target CI receipt whose
// landed head can never turn green.
type WorktreeMergeSupersedingTargetChecks struct {
	Head       string                             `json:"head"`
	Status     githubchecks.PullRequestWaitStatus `json:"status"`
	Reason     string                             `json:"reason,omitempty"`
	ObservedAt time.Time                          `json:"observed_at"`
}

// targetChecksObserver observes exact-head checks on one target commit. It is
// the seam between the acknowledgement proof and GitHub.
type targetChecksObserver func(ctx context.Context, repository, target, head string) (githubchecks.PullRequestWaitResult, error)

const supersedingTargetChecksSlice = 2 * time.Minute

func observeSupersedingTargetChecks(ctx context.Context, repository, target, head string) (githubchecks.PullRequestWaitResult, error) {
	return githubchecks.WaitForCommitChecks(ctx, githubchecks.PullRequestWaitOptions{
		Repository: repository, Target: target, Head: head, AllowTargetDescendant: true,
		Slice: supersedingTargetChecksSlice, CheckPollInterval: 20 * time.Second,
	})
}

// sourceCheckoutRetired reports whether a post-target CI receipt's source
// checkout is gone. Only that receipt shape may substitute cleanup evidence.
func sourceCheckoutRetired(receipt WorktreeMergeReceipt, source WorktreeMergeSource) (bool, error) {
	if receipt.Status != WorktreeMergePostTargetCIFailed || source.Worktree == "" {
		return false, nil
	}
	if _, err := os.Lstat(source.Worktree); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("inspect receipted source %s: %w", source.Worktree, err)
	}
	return true, nil
}

// proveRetiredLandedFailureSource accepts a retired source only through its
// exact applied cleanup record and sealed terminal Work Log. Absence of the
// checkout is never evidence on its own.
func proveRetiredLandedFailureSource(ctx context.Context, run runner.Runner, readClaimBase func(string, worktrees.TerminalWorkLogExpectation) (string, error), projectsRoot, gitDir string, receipt WorktreeMergeReceipt, source WorktreeMergeSource) (WorktreeMergeCleanedSourceProof, error) {
	if source.Task == "" || source.Worktree == "" || source.Branch == "" || source.SHA == "" {
		return WorktreeMergeCleanedSourceProof{}, errors.New("receipt contains an incomplete source identity")
	}
	proof, err := worktrees.FindTerminalCleanupProof(projectsRoot, receipt.Repository, receipt.Target, source.Task, source.Worktree, source.Branch)
	if err != nil {
		return WorktreeMergeCleanedSourceProof{}, fmt.Errorf("retired source %s has no exact applied WB cleanup record: %w", source.Task, err)
	}
	// FindTerminalCleanupProof accepts only the latest applied, clean,
	// integrated cleanup with this exact identity, canonical clone, target and
	// a nonempty final head, whose worktree and branch are both gone.
	head := proof.Result.HeadSHA
	if head != source.SHA {
		contains, ancestorErr := isMergeAncestorWithRunner(ctx, run, gitDir, source.SHA, head)
		if ancestorErr != nil || !contains {
			return WorktreeMergeCleanedSourceProof{}, fmt.Errorf("cleaned head %s of retired source %s is not a proven descendant of receipted %s: %v", head, source.Task, source.SHA, ancestorErr)
		}
	}
	base, err := readClaimBase(projectsRoot, worktrees.TerminalWorkLogExpectation{
		Task: source.Task, Repository: receipt.Repository, Worktree: source.Worktree, Branch: source.Branch, FinalCommit: head,
	})
	if err != nil {
		return WorktreeMergeCleanedSourceProof{}, fmt.Errorf("validate removed terminal Work Log for retired source %s: %w", source.Task, err)
	}
	contains, err := isMergeAncestorWithRunner(ctx, run, gitDir, base, source.SHA)
	if err != nil || !contains {
		return WorktreeMergeCleanedSourceProof{}, fmt.Errorf("retired source %s at %s does not contain its immutable claim base %s: %v", source.Task, source.SHA, base, err)
	}
	return WorktreeMergeCleanedSourceProof{
		Task: source.Task, Worktree: source.Worktree, Branch: source.Branch, SHA: source.SHA,
		CleanupReportPath: proof.ReportPath, CleanupReportSHA256: proof.ReportSHA256, CleanupHeadSHA: head, ClaimBaseSHA: base,
	}, nil
}

// proveRetiredSourcesSuperseded runs once the current remote target is known.
// Every cleaned head must be in it, and a strictly later target head must
// have passed its exact-head checks: with the source gone, that green head is
// the only evidence the recorded failure was resolved rather than ignored.
func proveRetiredSourcesSuperseded(ctx context.Context, run runner.Runner, observe targetChecksObserver, gitDir string, receipt WorktreeMergeReceipt, retired []WorktreeMergeCleanedSourceProof, currentTarget string) (*WorktreeMergeSupersedingTargetChecks, error) {
	for _, proof := range retired {
		contains, err := isMergeAncestorWithRunner(ctx, run, gitDir, proof.CleanupHeadSHA, currentTarget)
		if err != nil || !contains {
			return nil, fmt.Errorf("current remote target %s does not contain cleaned head %s of retired source %s: %v", currentTarget, proof.CleanupHeadSHA, proof.Task, err)
		}
	}
	if currentTarget == receipt.LandingSHA {
		return nil, fmt.Errorf("current remote target %s is still the failed landing; a retired source needs a later target head whose checks pass", currentTarget)
	}
	result, err := observe(ctx, receipt.Repository, receipt.Target, currentTarget)
	if err != nil {
		return nil, fmt.Errorf("observe checks on superseding target head %s: %w", currentTarget, err)
	}
	if result.Head != currentTarget {
		return nil, fmt.Errorf("check observation for %s reported head %s", currentTarget, result.Head)
	}
	if result.Status != githubchecks.PullRequestWaitPassed {
		return nil, fmt.Errorf("superseding target head %s checks are %s, not passed (%s); wait with wb ci wait --repo %s --target %s --head %s, then retry",
			currentTarget, result.Status, result.Reason, receipt.Repository, receipt.Target, currentTarget)
	}
	if missing := failedChecksNotRepassed(receipt.Checks.Checks, result.Checks); len(result.Checks) == 0 || len(missing) > 0 {
		return nil, fmt.Errorf("superseding target head %s does not re-pass the receipt's failed checks %v (observed %d checks)", currentTarget, missing, len(result.Checks))
	}
	return &WorktreeMergeSupersedingTargetChecks{Head: currentTarget, Status: result.Status, Reason: result.Reason, ObservedAt: time.Now().UTC()}, nil
}

// failedChecksNotRepassed names every check the failed receipt recorded as
// failed or cancelled that did not execute and pass on the superseding head.
// A skipped or absent rerun proves nothing about the recorded failure.
func failedChecksNotRepassed(failed, observed []githubchecks.RemoteCheck) []string {
	passed := make(map[string]bool, len(observed))
	for _, check := range observed {
		if check.Bucket == "pass" {
			passed[githubchecks.NormalizeCheckName(check.Name)] = true
		}
	}
	var missing []string
	for _, check := range failed {
		if (check.Bucket == "fail" || check.Bucket == "cancel") && !passed[githubchecks.NormalizeCheckName(check.Name)] {
			missing = append(missing, check.Name)
		}
	}
	return missing
}

// writeRetiredSourceEvidenceID binds retired-source evidence into the
// acknowledgement ID under its own domain separator, leaving every older ID
// unchanged.
func writeRetiredSourceEvidenceID(hash interface{ Write([]byte) (int, error) }, ack WorktreeMergeLandedFailureAcknowledgement) {
	if len(ack.RetiredSources) == 0 && ack.SupersedingTargetChecks == nil {
		return
	}
	evidence, _ := json.Marshal(struct {
		Retired     []WorktreeMergeCleanedSourceProof     `json:"retired"`
		Superseding *WorktreeMergeSupersedingTargetChecks `json:"superseding"`
		Actor       string                                `json:"actor"`
		Reason      string                                `json:"reason"`
		RecordedAt  string                                `json:"recorded_at"`
	}{ack.RetiredSources, ack.SupersedingTargetChecks, ack.Actor, ack.Reason, ack.RecordedAt.UTC().Format(time.RFC3339Nano)})
	_, _ = hash.Write(append([]byte("retired_sources\x00"), evidence...))
}

// validateRetiredSourceEvidence checks a persisted acknowledgement's retired
// evidence is complete and consistent with its receipt. It authenticates the
// record; it does not re-prove Git custody.
func validateRetiredSourceEvidence(ack WorktreeMergeLandedFailureAcknowledgement, receipt WorktreeMergeReceipt) error {
	if len(ack.RetiredSources) == 0 && ack.SupersedingTargetChecks == nil {
		return nil
	}
	checks := ack.SupersedingTargetChecks
	if len(ack.RetiredSources) == 0 || checks == nil || receipt.Status != WorktreeMergePostTargetCIFailed ||
		checks.Status != githubchecks.PullRequestWaitPassed || checks.Head != ack.CurrentTargetSHA || checks.Head == receipt.LandingSHA ||
		checks.ObservedAt.IsZero() || ack.Actor == "" || ack.Reason == "" || ack.RecordedAt.IsZero() {
		return errors.New("incomplete retired-source evidence")
	}
	bySource := make(map[string]WorktreeMergeSource, len(receipt.Sources))
	for _, source := range receipt.Sources {
		bySource[source.Task] = source
	}
	seen := make(map[string]bool, len(ack.RetiredSources))
	for _, proof := range ack.RetiredSources {
		source, ok := bySource[proof.Task]
		if !ok || seen[proof.Task] || source.Worktree != proof.Worktree || source.Branch != proof.Branch || source.SHA != proof.SHA ||
			proof.CleanupReportPath == "" || len(proof.CleanupReportSHA256) != sha256.Size*2 || proof.CleanupHeadSHA == "" || proof.ClaimBaseSHA == "" {
			return fmt.Errorf("retired-source evidence for %q does not match the receipt", proof.Task)
		}
		seen[proof.Task] = true
	}
	return nil
}
