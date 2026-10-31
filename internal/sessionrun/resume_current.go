package sessionrun

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// Current pickup never calls any launcher dependency. Bundle, all members and
// the caller PID slot are held in that order through attachment/finalization.
func resumeParkedCurrent(ctx context.Context, deps ResumeDependencies, request ResumeRequest, home string, store sessionpark.Store, lock *sessionpark.SourceLock, state sessionpark.State, now time.Time) (ResumeResult, error) {
	if deps.ResolveCurrent == nil || (deps.WithLocalCustody == nil && deps.WithCurrentCustody == nil) || deps.AttachLocal == nil || deps.WithCurrentTransition == nil || deps.MarkSourceResumed == nil {
		return ResumeResult{}, fmt.Errorf("current-chat pickup dependencies are unavailable; no agent was launched")
	}
	dir := filepath.Join(home, session.DirName)
	caller, found, err := deps.ResolveCurrent(dir)
	if err != nil {
		return ResumeResult{}, err
	}
	if !found || caller.NativeHarnessID == "" || caller.ProcessStartedAt.IsZero() {
		return ResumeResult{}, fmt.Errorf("no identifiable live registered caller; register this chat or explicitly choose --launch or --to <machine>; no agent was launched")
	}
	chain, err := store.CurrentPreparationsUnderLock(lock)
	if err != nil {
		return ResumeResult{}, err
	}
	hasPreparation := len(chain) > 0
	var prepared sessionpark.CurrentPreparation
	if hasPreparation {
		prepared = chain[len(chain)-1]
		for _, actor := range chain {
			if err := session.RequireCurrentCallerAvailable(dir, actor.Caller, state.Bundle.Source, state.Bundle.ParkedSessionID, actor.Successor.WBSessionID); err != nil {
				return ResumeResult{}, err
			}
			if err := session.RequireNoDeparture(dir, actor.Successor); err != nil {
				return ResumeResult{}, err
			}
		}
	}
	allowedSuccessor := ""
	if hasPreparation {
		allowedSuccessor = prepared.Successor.WBSessionID
	}
	if err := session.RequireCurrentCallerAvailable(dir, caller, state.Bundle.Source, state.Bundle.ParkedSessionID, allowedSuccessor); err != nil {
		return ResumeResult{}, err
	}
	if state.Status == sessionpark.StatusResumed {
		if !hasPreparation || (!prepared.CurrentCallerMatches(caller) && !request.RecoverCurrent) || state.Successor == nil || state.Successor.WBSessionID != prepared.Successor.WBSessionID {
			return ResumeResult{}, fmt.Errorf("parked session already has a different resume winner")
		}
	}
	attempts := make([]string, 0, len(chain))
	for _, node := range chain {
		attempts = append(attempts, string(node.Digest()))
	}
	withCustody := deps.WithCurrentCustody
	if withCustody == nil {
		withCustody = func(ctx context.Context, root string, bundle sessionpark.Bundle, attempts []string, proceed func(*worktrees.ParkedLocalCustody) error) error {
			if len(attempts) > 1 {
				return fmt.Errorf("current recovery requires chain-aware custody")
			}
			replay := ""
			if len(attempts) == 1 {
				replay = attempts[0]
			}
			return deps.WithLocalCustody(ctx, root, bundle, replay, proceed)
		}
	}
	var output ResumeResult
	err = withCustody(ctx, request.ProjectsRoot, state.Bundle, attempts, func(custody *worktrees.ParkedLocalCustody) error {
		if err := session.RequireCurrentCallerAvailable(dir, caller, state.Bundle.Source, state.Bundle.ParkedSessionID, allowedSuccessor); err != nil {
			return err
		}
		for _, actor := range chain {
			if err := session.RequireCurrentCallerAvailable(dir, actor.Caller, state.Bundle.Source, state.Bundle.ParkedSessionID, actor.Successor.WBSessionID); err != nil {
				return err
			}
			if err := session.RequireNoDeparture(dir, actor.Successor); err != nil {
				return err
			}
		}
		for _, ancestor := range chain[:max(0, len(chain)-1)] {
			if deps.ProveGenerationGone == nil {
				return fmt.Errorf("current recovery has no generation proof")
			}
			if err := deps.ProveGenerationGone(ancestor.Caller); err != nil {
				return err
			}
		}
		var p sessionpark.CurrentPreparation
		var continuationPath string
		var err error
		if request.RecoverCurrent {
			p, continuationPath, err = store.PrepareCurrentRecoveryUnderLock(lock, caller, custody.ResolvedWorktreeDirs(), now, deps.ProveGenerationGone)
		} else {
			p, continuationPath, err = store.PrepareCurrentWithUnstartedAcknowledgmentUnderLock(lock, caller, custody.ResolvedWorktreeDirs(), now, request.UnstartedLaunchAcknowledgment)
		}
		if err != nil {
			return err
		}
		output = ResumeResult{ParkedSessionID: state.Bundle.ParkedSessionID, Status: "awaiting_context_ack", TargetMachine: p.Successor.Machine,
			SuccessorWBSessionID: p.Successor.WBSessionID, MemberCount: len(state.Bundle.Worktrees), ContinuationPath: continuationPath, ContinuationDigest: p.ContinuationDigest, Replay: hasPreparation}
		if request.Acknowledge == "" {
			return nil
		}
		return deps.WithCurrentTransition(dir, p.Caller, p.Successor, func(commit func() error) error {
			if err := session.RequireCurrentCallerAvailable(dir, p.Caller, state.Bundle.Source, state.Bundle.ParkedSessionID, p.Successor.WBSessionID); err != nil {
				return err
			}
			// Bundle -> all member journals -> PID barrier also fences a park
			// committed after the initial replay inspection.
			for _, actor := range chain {
				if err := session.RequireCurrentCallerAvailable(dir, actor.Caller, state.Bundle.Source, state.Bundle.ParkedSessionID, actor.Successor.WBSessionID); err != nil {
					return err
				}
				if err := session.RequireNoDeparture(dir, actor.Successor); err != nil {
					return err
				}
			}
			if _, err := store.AcknowledgeCurrentUnderLock(lock, caller, request.Acknowledge); err != nil {
				return err
			}
			if err := deps.AttachLocal(ctx, custody, p.Successor, string(p.Digest()), 1); err != nil {
				return err
			}
			if err := commit(); err != nil {
				return err
			}
			final, err := store.ResumeUnderLock(lock, p.Successor, now)
			if err != nil {
				return err
			}
			if final.Status != sessionpark.StatusResumed || final.Successor == nil || final.Successor.WBSessionID != p.Successor.WBSessionID {
				return fmt.Errorf("current pickup has no exact final successor receipt")
			}
			if _, err := deps.MarkSourceResumed(dir, state.Bundle.Source, state.Bundle.ParkedSessionID, p.Successor.WBSessionID); err != nil {
				return err
			}
			if err := store.CompleteCurrentUnderLock(lock, p); err != nil {
				return err
			}
			output.Status = string(final.Status)
			return nil
		})
	})
	return output, err
}
