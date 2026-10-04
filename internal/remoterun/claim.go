package remoterun

import (
	"context"
	"fmt"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
)

func runRemoteClaim(deps Dependencies, projectsRoot, task, note string, takeOver, force, jsonOut bool, stale time.Duration) (ClaimResult, error) {
	if err := remotestate.ValidTaskName(task); err != nil {
		return ClaimResult{}, deps.ExitError(2, err.Error())
	}
	cfg, provider, err := loadRemote(deps, projectsRoot)
	if err != nil {
		return ClaimResult{}, err
	}
	login, err := deps.Login()
	if err != nil || login == "" {
		return ClaimResult{}, deps.ExitError(2, fmt.Sprintf("wb remote needs the GitHub login to key this machine's entry (gh auth status): %v", err))
	}
	mine := remotestate.Claim{
		SchemaVersion: remotestate.ClaimSchemaVersion,
		Task:          task,
		Login:         login,
		Machine:       cfg.Machine,
		ClaimedAt:     deps.Now(),
		Note:          note,
	}
	ctx := context.Background()

	outcome, err := provider.Claim(ctx, mine, remotestate.ClaimNormal, "")
	if err != nil {
		if !force {
			return ClaimResult{}, deps.ExitError(1, "claim "+task+": "+err.Error())
		}
		// The current claim file could not even be read (e.g. a newer schema
		// version); --force proceeds anyway, but there is no coherent holder
		// to name.
		return forceClaim(ctx, deps, provider, mine, "", jsonOut)
	}

	switch outcome.Kind {
	case remotestate.ClaimAcquired:
		return claimResult(outcome, fmt.Sprintf("claimed %s for %s → %s\n", task, mine.Holder(), outcome.Location))
	case remotestate.ClaimRefreshed:
		return claimResult(outcome, fmt.Sprintf("refreshed your remote claim on %s\n", task))
	default: // remotestate.ClaimHeld
		return handleClaimHeld(ctx, provider, deps, mine, outcome.Current, takeOver, force, jsonOut, stale)
	}
}

func handleClaimHeld(ctx context.Context, provider remotestate.Provider, deps Dependencies, mine, holder remotestate.Claim, takeOver, force, jsonOut bool, stale time.Duration) (ClaimResult, error) {
	machines, err := provider.List(ctx)
	if err != nil {
		return ClaimResult{}, deps.ExitError(1, "claim "+mine.Task+": read remote store: "+err.Error())
	}
	isStale := HolderStale(machines, holder.Login, holder.Machine, deps.Now(), stale)

	switch {
	case force:
		return forceClaim(ctx, deps, provider, mine, HolderDesc(mine, holder), jsonOut)
	case takeOver && isStale:
		outcome, err := provider.Claim(ctx, mine, remotestate.ClaimTakeOverStale, holder.Holder())
		if err != nil {
			return ClaimResult{}, deps.ExitError(1, "claim "+mine.Task+": "+err.Error())
		}
		if outcome.Kind == remotestate.ClaimHeld {
			// The holder judged stale above already changed hands (released
			// and reclaimed, or refreshed) before this call landed; the
			// provider refused the take-over and named the actual current
			// holder instead of replacing them.
			return ClaimResult{}, deps.ExitError(1, fmt.Sprintf("remote claim on %s changed to %s before the take-over landed; retry", mine.Task, HolderDesc(mine, outcome.Current)))
		}
		// Render from outcome.Previous, not the earlier-judged holder
		// variable: onLostRace's retry path can land a take-over against a
		// different previous state than what was judged stale up front.
		prev := holder
		if outcome.Previous != nil {
			prev = *outcome.Previous
		}
		hb := HeartbeatPhrase(machines, prev.Login, prev.Machine, deps.Now(), "never")
		text := fmt.Sprintf("took over %s from %s (their heartbeat: %s)\n", mine.Task, HolderDesc(mine, prev), hb)
		return claimResult(outcome, text)
	case takeOver:
		return ClaimResult{}, deps.ExitError(1, fmt.Sprintf("claim is fresh; ask %s to release, or use --force", HolderDesc(mine, holder)))
	default:
		hb := HeartbeatPhrase(machines, holder.Login, holder.Machine, deps.Now(), "never published")
		suffix := "it is fresh — ask them to release, or use --force"
		if isStale {
			suffix = "it is stale — retry with --take-over"
		}
		return ClaimResult{}, deps.ExitError(1, fmt.Sprintf("remote claim on %s is held by %s (heartbeat %s); %s", mine.Task, HolderDesc(mine, holder), hb, suffix))
	}
}

func forceClaim(ctx context.Context, deps Dependencies, provider remotestate.Provider, mine remotestate.Claim, holderLabel string, jsonOut bool) (ClaimResult, error) {
	outcome, err := provider.Claim(ctx, mine, remotestate.ClaimForce, "")
	if err != nil {
		return ClaimResult{}, deps.ExitError(1, "claim "+mine.Task+": "+err.Error())
	}
	text := ""
	if !jsonOut {
		if holderLabel == "" {
			text = fmt.Sprintf("OVERRIDING unreadable remote claim on %s\n", mine.Task)
		} else {
			text = fmt.Sprintf("OVERRIDING remote claim by %s on %s\n", holderLabel, mine.Task)
		}
	}
	return claimResult(outcome, text)
}
func claimResult(outcome remotestate.ClaimOutcome, text string) (ClaimResult, error) {
	return ClaimResult{Outcome: outcome, Text: text}, nil
}
