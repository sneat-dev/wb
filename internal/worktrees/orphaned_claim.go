package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

type orphanedClaimCandidate struct {
	home         string
	claim        workLogClaim
	terminalPath string
}

// workLogOrphanedEvidence is private authority evidence retained with the
// immutable terminal record. It says exactly what WB proved absent; it never
// claims that the vanished checkout's bytes or final commit were inspected.
type workLogOrphanedEvidence struct {
	Version            int       `json:"version"`
	ObservedAt         time.Time `json:"observed_at"`
	Actor              string    `json:"actor"`
	Reason             string    `json:"reason"`
	WorktreeAbsent     bool      `json:"worktree_absent"`
	RegistrationAbsent bool      `json:"registration_absent"`
	LocalBranchAbsent  bool      `json:"local_branch_absent"`
	RemoteBranchAbsent bool      `json:"remote_branch_absent"`
	TerminalAbsent     bool      `json:"terminal_absent"`
}

func abortOrphanedClaim(ctx context.Context, options AbortOptions) ([]AbortResult, error) {
	claimID := strings.TrimSpace(options.ClaimID)
	actor := strings.TrimSpace(options.Actor)
	reason := strings.TrimSpace(options.Reason)
	if !validClaimID(claimID) {
		return nil, fmt.Errorf("orphaned abort requires one exact --claim ID")
	}
	if actor == "" || len(actor) > 200 || strings.ContainsAny(actor, "\x00\r\n") {
		return nil, fmt.Errorf("orphaned abort requires a bounded single-line --actor")
	}
	if reason == "" || len(reason) > 1000 || strings.ContainsAny(reason, "\x00\r\n") {
		return nil, fmt.Errorf("orphaned abort requires a bounded single-line --reason")
	}
	if options.DeleteRemote {
		return nil, fmt.Errorf("orphaned abort proves the remote branch absent and never accepts --remote deletion authority")
	}
	if options.All {
		return nil, fmt.Errorf("orphaned abort always selects one exact --claim and does not accept --all")
	}
	projectsRoot, task, _, _, err := normalizeListOptions(ListOptions{ProjectsRoot: options.ProjectsRoot, Task: options.Task, Base: options.Base})
	if err != nil {
		return nil, err
	}
	if task == "" {
		return nil, fmt.Errorf("task is required")
	}
	options.ProjectsRoot = projectsRoot
	candidate, err := findOrphanedClaim(projectsRoot, task, claimID)
	if err != nil {
		return nil, err
	}
	if filter := strings.TrimSpace(options.Filter); filter != "" && abortRepositoryExcludedByFilter(filter, candidate.claim.Repository, candidate.claim.Worktree) {
		return nil, fmt.Errorf("exact orphaned claim %s does not match --filter %q", claimID, filter)
	}
	_, inspectErr := inspectOrphanedClaimAbsence(ctx, projectsRoot, candidate, actor, reason)
	result := orphanedAbortResult(projectsRoot, candidate, inspectErr)
	if inspectErr != nil {
		if options.Apply {
			return []AbortResult{result}, fmt.Errorf("claim %s is not orphaned: %w", claimID, inspectErr)
		}
		return []AbortResult{result}, nil
	}
	if !options.Apply {
		return []AbortResult{result}, nil
	}
	runDir, _, err := openWorkLogRun(candidate.home, candidate.claim.EffortID, candidate.claim.RunID, false)
	if err != nil {
		return []AbortResult{result}, fmt.Errorf("open orphaned claim run: %w", err)
	}
	defer func() { _ = runDir.Close() }()
	unlock, err := lockClaim(runDir, candidate.claim.ClaimID)
	if err != nil {
		return []AbortResult{result}, fmt.Errorf("lock orphaned claim: %w", err)
	}
	defer unlock()
	if options.beforeOrphanSeal != nil {
		options.beforeOrphanSeal()
	}
	evidence, err := inspectOrphanedClaimAbsence(ctx, projectsRoot, candidate, actor, reason)
	if err != nil {
		result.Eligible = false
		result.Reason = err.Error()
		return []AbortResult{result}, fmt.Errorf("orphaned claim safety changed under lock: %w", err)
	}
	if _, err := writeOrphanedWorkLogTerminal(candidate.home, runDir, candidate.claim, evidence); err != nil {
		return []AbortResult{result}, fmt.Errorf("seal orphaned Work Log claim: %w", err)
	}
	result.Applied = true
	result.Reason = "sealed from rechecked negative evidence; no vanished content was inspected"
	return []AbortResult{result}, nil
}

func orphanedAbortResult(projectsRoot string, candidate orphanedClaimCandidate, err error) AbortResult {
	canonical, _ := CanonicalRepositoryPath(projectsRoot, candidate.claim.Repository)
	result := AbortResult{ListResult: ListResult{
		Task: candidate.claim.Task, Repository: candidate.claim.Repository,
		CanonicalDir: canonical,
		WorktreeDir:  candidate.claim.Worktree, Branch: candidate.claim.Branch,
		Base: candidate.claim.Base, HeadSHA: "",
	}, Disposition: AbortOrphaned, Eligible: err == nil, WorktreeGone: true}
	if err != nil {
		result.Reason = err.Error()
	} else {
		result.Reason = "worktree path, Git registration, local branch, remote branch, and terminal record are absent"
	}
	return result
}

func findOrphanedClaim(projectsRoot, task, claimID string) (orphanedClaimCandidate, error) {
	resolution, err := wbhome.Resolve(projectsRoot)
	if err != nil {
		return orphanedClaimCandidate{}, err
	}
	seen := map[string]bool{}
	var found *orphanedClaimCandidate
	for _, layout := range resolution.Read {
		home := filepath.Clean(layout.Home)
		if seen[home] {
			continue
		}
		seen[home] = true
		matches, globErr := filepath.Glob(filepath.Join(home, "worklogs", "*", "runs", "*", "claims", claimID+".json"))
		if globErr != nil {
			return orphanedClaimCandidate{}, globErr
		}
		for _, path := range matches {
			var claim workLogClaim
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return orphanedClaimCandidate{}, fmt.Errorf("read orphaned claim %s: %w", path, readErr)
			}
			if err := json.Unmarshal(raw, &claim); err != nil {
				return orphanedClaimCandidate{}, fmt.Errorf("parse orphaned claim %s: %w", path, err)
			}
			if claim.Task != task || claim.ClaimID != claimID {
				continue
			}
			if err := validateOrphanedClaimIdentity(claim); err != nil {
				return orphanedClaimCandidate{}, fmt.Errorf("validate orphaned claim %s: %w", path, err)
			}
			candidate := orphanedClaimCandidate{home: home, claim: claim,
				terminalPath: filepath.Join(filepath.Dir(filepath.Dir(path)), "terminals", claimID+".json")}
			if found != nil {
				return orphanedClaimCandidate{}, fmt.Errorf("claim %s exists in more than one WB home", claimID)
			}
			found = &candidate
		}
	}
	if found == nil {
		return orphanedClaimCandidate{}, fmt.Errorf("active Work Log claim %s for task %q was not found", claimID, task)
	}
	return *found, nil
}

func validateOrphanedClaimIdentity(claim workLogClaim) error {
	if (claim.Version != 1 && claim.Version != 2) || claim.Lifecycle != "active" ||
		!validSafeSegment(claim.EffortID) || !validSafeSegment(claim.RunID) || !validClaimID(claim.ClaimID) ||
		claim.Task == "" || claim.Repository == "" || !filepath.IsAbs(claim.Worktree) || !validBranch(context.Background(), claim.Branch) ||
		!isGitObjectID(claim.BaseSHA) {
		return fmt.Errorf("immutable claim identity is incomplete or invalid")
	}
	wantID := workLogClaimID(claim.EffortID, CreateResult{Repository: claim.Repository, WorktreeDir: claim.Worktree, Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA})
	if claim.ParentClaimID != "" {
		if !validClaimID(claim.ParentClaimID) || claim.AgentID == "" {
			return fmt.Errorf("successor claim metadata is invalid")
		}
		switch claim.AcquiredVia {
		case "external_handoff":
			var err error
			wantID, err = expectedExternalClaimID(claim)
			if err != nil {
				return err
			}
		case "parked_session_resume":
			var err error
			wantID, err = expectedParkedSessionClaimID(claim)
			if err != nil {
				return err
			}
		case "handoff", "not_landed":
			if claim.Version == 2 {
				wantID = declaredSuccessorWorkLogClaimID(claim.ParentClaimID, claim.AgentID, claim.AcquiredVia,
					ClaimExecutionIdentity{Model: claim.Model, CLI: claim.CLI, Provider: claim.Provider})
			} else {
				wantID = successorWorkLogClaimID(claim.ParentClaimID, claim.AgentID, claim.AcquiredVia)
			}
		case "recycle_failed":
			wantID = successorWorkLogClaimID(claim.ParentClaimID, claim.AgentID, claim.AcquiredVia)
		default:
			return fmt.Errorf("successor claim acquisition %q is invalid", claim.AcquiredVia)
		}
	}
	if wantID != claim.ClaimID {
		return fmt.Errorf("immutable claim digest mismatch")
	}
	return nil
}

func inspectOrphanedClaimAbsence(ctx context.Context, projectsRoot string, candidate orphanedClaimCandidate, actor, reason string) (*workLogOrphanedEvidence, error) {
	if _, err := os.Lstat(candidate.claim.Worktree); err == nil {
		return nil, fmt.Errorf("claimed worktree path still exists at %s", candidate.claim.Worktree)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect claimed worktree path: %w", err)
	}
	canonical, err := CanonicalRepositoryPath(projectsRoot, candidate.claim.Repository)
	if err != nil {
		return nil, err
	}
	registered, err := worktreeStillRegistered(ctx, canonical, candidate.claim.Worktree)
	if err != nil {
		return nil, fmt.Errorf("inspect Git worktree registration: %w", err)
	}
	if registered {
		return nil, fmt.Errorf("claimed worktree remains registered with Git")
	}
	local, err := localBranchExists(ctx, canonical, candidate.claim.Branch)
	if err != nil {
		return nil, err
	}
	if local {
		return nil, fmt.Errorf("local branch refs/heads/%s still exists", candidate.claim.Branch)
	}
	remote, err := remoteBranchHead(ctx, canonical, candidate.claim.Branch)
	if err != nil {
		return nil, fmt.Errorf("inspect remote branch: %w", err)
	}
	if remote != "" {
		return nil, fmt.Errorf("remote branch refs/heads/%s still exists at %s", candidate.claim.Branch, remote)
	}
	if _, err := os.Lstat(candidate.terminalPath); err == nil {
		return nil, fmt.Errorf("terminal record already exists for claim %s", candidate.claim.ClaimID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect terminal record: %w", err)
	}
	return &workLogOrphanedEvidence{Version: 1, ObservedAt: time.Now().UTC(), Actor: actor, Reason: reason,
		WorktreeAbsent: true, RegistrationAbsent: true, LocalBranchAbsent: true,
		RemoteBranchAbsent: true, TerminalAbsent: true}, nil
}
