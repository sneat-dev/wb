package streams

import (
	"context"
	"fmt"
	"strings"
)

// EndOptions is one `wb stream end` invocation.
type EndOptions struct {
	Name string
	// Apply performs the removal. Without it the verb reports exactly what it
	// would do and changes nothing, so an operator can see which agent pull
	// requests would be closed before any of them are.
	Apply bool
	// Retarget moves still-open agent pull requests onto the member's base
	// instead of closing them. The default closes them, because GitHub's own
	// silent retarget is the hazard this verb exists to prevent and doing it
	// deliberately must be an explicit choice.
	Retarget bool
	// ForceUnabsorbed proceeds past the absorption guard. It is not a bypass
	// that hides anything: it requires Reason, records both in the event log,
	// and names every commit it is stepping over. Without it, an absorption
	// check WB could not run refuses — a check that cannot answer must not
	// pass.
	ForceUnabsorbed bool
	// Reason is mandatory with ForceUnabsorbed and is recorded verbatim.
	Reason string
	// KeepRemoteBranch leaves origin/stream/<name> in place. By default end
	// deletes it, because leaving it is scaffolding the verb claims to
	// remove.
	KeepRemoteBranch bool
}

// EndResult is what `stream end` did, or would do.
type EndResult struct {
	Stream  string            `json:"stream"`
	Applied bool              `json:"applied"`
	Members []EndMemberResult `json:"members"`
	// AgentPullRequests records what happened to every still-open pull
	// request that targeted a stream branch.
	AgentPullRequests []AgentPullRequestOutcome `json:"agent_pull_requests"`
	Errors            []string                  `json:"errors,omitempty"`
	// Forced names every finding `--force-unabsorbed` stepped over, and
	// ForcedReason the operator's recorded justification. Both are also
	// written to the event log, so the decision is auditable after the fact.
	Forced       []string `json:"forced,omitempty"`
	ForcedReason string   `json:"forced_reason,omitempty"`
}

// EndMemberResult is one member's retirement.
type EndMemberResult struct {
	Repository string `json:"repository"`
	Worktree   string `json:"worktree"`
	// RemoteBranchDeleted records that origin/stream/<name> is gone.
	RemoteBranchDeleted bool `json:"remote_branch_deleted"`
	// WorktreeRemoved is true when the existing cleanup path retired it.
	WorktreeRemoved bool `json:"worktree_removed"`
	// LeaseReleased is true when the stream lease record was cleared.
	LeaseReleased bool `json:"lease_released"`
	// DraftPullRequest is the member's own stream pull request, reported
	// rather than merged: ending a stream publishes, bumps and merges
	// nothing.
	DraftPullRequest int    `json:"draft_pull_request,omitempty"`
	DraftAction      string `json:"draft_action,omitempty"`
	Detail           string `json:"detail,omitempty"`
}

// AgentPullRequestOutcome is one agent pull request's disposition.
type AgentPullRequestOutcome struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
	URL        string `json:"url"`
	Action     string `json:"action"`
	Detail     string `json:"detail,omitempty"`
}

// End removes a stream's own scaffolding and restores published state.
//
// It refuses while any live local link remains, because a stream that ended
// with a consumer still resolving an unpublished working tree has not restored
// published state at all; and it refuses a member whose branch carries work
// the base has not absorbed, by patch identity rather than by path listing.
// Before a stream branch could be deleted it enumerates every still-open pull
// request targeting it and closes or retargets each, because GitHub silently
// retargets such a pull request at the base when its base branch disappears —
// producing the misrouted pull request this Feature guards against with no
// operator mistake to blame.
//
// Ending publishes, bumps and merges nothing.
//
// Implements: dependency-streams#req:stream-end-proves-absorption-and-removes-its-own-scaffolding,
// dependency-streams#req:stream-end-removes-every-stream-worktree.
//
// `stream-end-restores-published-state` is only PARTLY implemented here. The
// REQ says end must remove every live link before delegating worktree removal;
// this verb refuses while one remains and names the exact command per link,
// which is a guard rather than the removal. Removing them inside `end` needs
// the local-link verb that lands in the propagate-local row — calling it from
// here would be a forward dependency on code this row does not contain. Until
// then, `one-verb-per-operation` is satisfied only for the refusal, and the
// operator still chains the undos by hand. That gap is deliberate and tracked,
// not an oversight.
func (engine *Engine) End(ctx context.Context, options EndOptions) (EndResult, error) {
	stream, err := engine.Store.Load(options.Name)
	if err != nil {
		return EndResult{}, err
	}
	result := EndResult{Stream: stream.Name, Applied: options.Apply}

	if live := stream.LiveLinks(); len(live) > 0 {
		names := make([]string, 0, len(live))
		undo := make([]string, 0, len(live))
		for _, entry := range live {
			names = append(names, fmt.Sprintf("%s → %s (%s %s)", entry.Member.Repository, entry.Link.LibraryRepository, entry.Link.Mechanism, entry.Link.Identity))
			undo = append(undo, "wb deps propagate local "+entry.Link.Library+" --to "+entry.Member.Worktree+" --undo")
		}
		return result, &Refusal{
			Code:       RefusalLiveLink,
			Message:    "stream " + stream.Name + " still holds live local links, so its consumers do not resolve published versions yet: " + strings.Join(names, ", "),
			Sanctioned: undo,
		}
	}

	// The absorption guard FAILS CLOSED. A comparison that could not run —
	// a stale or missing origin/<base>, a removed worktree, a timeout — is an
	// unknown, and an unknown must refuse rather than pass. This is the same
	// rule preflight already applies; before this fix an error here fell
	// through and the member's agent pull requests were closed and its
	// worktree removed on the strength of a check that never answered.
	var unabsorbed, unknown []string
	for _, member := range stream.Members {
		if member.Worktree == "" {
			// A member reserved but never published has nothing to absorb.
			continue
		}
		// Re-read origin first: a day-old clone would report work as
		// unabsorbed that the base already carries, and would refuse an end
		// that should succeed.
		if fetchErr := engine.Git.Fetch(ctx, member.Worktree); fetchErr != nil {
			unknown = append(unknown, fmt.Sprintf("%s: could not re-read origin: %s", member.Repository, RedactString(fetchErr.Error())))
			continue
		}
		commits, err := engine.Git.CommitsNotIn(ctx, member.Worktree, member.Branch, "origin/"+member.Base)
		if err != nil {
			unknown = append(unknown, fmt.Sprintf("%s: %s", member.Repository, RedactString(err.Error())))
			continue
		}
		if len(commits) == 0 {
			continue
		}
		unabsorbed = append(unabsorbed, fmt.Sprintf("%s has %d commit(s) origin/%s has not absorbed: %s",
			member.Repository, len(commits), member.Base, strings.Join(collapsePatchIdentical(commits), "; ")))
	}
	if len(unknown) > 0 && !options.ForceUnabsorbed {
		return result, &Refusal{
			Code: RefusalUnabsorbedWork,
			Message: "the absorption check could not run for every member, so `end` cannot prove their work landed: " +
				strings.Join(unknown, " | "),
			Sanctioned: []string{
				"wb stream status " + stream.Name,
				"wb stream end " + stream.Name + " --apply (retry once origin is reachable)",
				"wb stream end " + stream.Name + " --apply --force-unabsorbed --reason \"<why>\"",
			},
		}
	}
	if len(unabsorbed) > 0 && !options.ForceUnabsorbed {
		return result, &Refusal{
			Code:    RefusalUnabsorbedWork,
			Message: "stream " + stream.Name + " carries work the base has not absorbed: " + strings.Join(unabsorbed, " | "),
			Sanctioned: []string{
				"wb stream status " + stream.Name,
				"wb worktree merge <worktree> --route auto",
				"wb stream end " + stream.Name + " --apply --force-unabsorbed --reason \"<why>\"",
			},
		}
	}
	if options.ForceUnabsorbed && (len(unabsorbed) > 0 || len(unknown) > 0) {
		if strings.TrimSpace(options.Reason) == "" {
			return result, &Refusal{
				Code:       RefusalUsage,
				Message:    "--force-unabsorbed requires --reason so stepping over the absorption guard is auditable",
				Sanctioned: []string{"wb stream end " + stream.Name + " --apply --force-unabsorbed --reason \"<why>\""},
			}
		}
		result.Forced = append(append([]string(nil), unabsorbed...), unknown...)
		result.ForcedReason = options.Reason
		engine.record(stream.Name, Event{
			Stream: stream.Name, Verb: "stream end", Phase: "absorption", Outcome: "findings",
			RefusalCode: RefusalUnabsorbedWork,
			Detail:      "forced past the absorption guard: " + options.Reason,
			Evidence:    map[string]string{"stepped_over": strings.Join(result.Forced, " | ")},
		})
	}

	for _, member := range stream.Members {
		result.AgentPullRequests = append(result.AgentPullRequests, engine.retireAgentPullRequests(ctx, options, member)...)
		result.Members = append(result.Members, engine.retireMember(ctx, options, stream.Name, member))
	}

	if !options.Apply {
		return result, nil
	}
	ended := engine.now()
	// The lease is cleared only for members whose retirement actually
	// completed. Clearing every lease unconditionally made the report's
	// LeaseReleased:false contradict the state it described.
	released := map[string]bool{}
	for _, member := range result.Members {
		if member.LeaseReleased {
			released[strings.ToLower(member.Repository)] = true
		}
	}
	if _, err := engine.Store.Update(stream.Name, func(current *Stream) error {
		current.EndedAt = &ended
		current.Phase = PhaseEnded
		for index := range current.Members {
			if released[strings.ToLower(current.Members[index].Repository)] {
				current.Members[index].Lease = Lease{}
			}
		}
		return nil
	}); err != nil {
		return result, err
	}
	engine.record(stream.Name, Event{
		Stream: stream.Name, Verb: "stream end", Phase: "complete", Outcome: "success",
		Detail: fmt.Sprintf("%d members retired", len(result.Members)),
	})
	return result, nil
}

// retireAgentPullRequests closes or retargets every still-open pull request
// whose base is the stream branch, before that branch could be deleted.
func (engine *Engine) retireAgentPullRequests(ctx context.Context, options EndOptions, member Member) []AgentPullRequestOutcome {
	pullRequests, err := engine.GitHub.OpenPullRequestsTargeting(ctx, member.Worktree, member.Branch)
	if err != nil {
		return []AgentPullRequestOutcome{{
			Repository: member.Repository, Action: "unknown",
			Detail: "could not enumerate pull requests targeting " + member.Branch + ": " + RedactString(err.Error()),
		}}
	}
	outcomes := make([]AgentPullRequestOutcome, 0, len(pullRequests))
	for _, pullRequest := range pullRequests {
		outcome := AgentPullRequestOutcome{
			Repository: member.Repository, Number: pullRequest.Number, URL: pullRequest.URL,
		}
		switch {
		case !options.Apply && options.Retarget:
			outcome.Action = "would-retarget"
			outcome.Detail = "onto " + member.Base
		case !options.Apply:
			outcome.Action = "would-close"
		case options.Retarget:
			// The action is written only after the port has re-read the pull
			// request and confirmed the new base. An exit code is not
			// evidence the effect landed.
			if err := engine.GitHub.RetargetPullRequest(ctx, member.Worktree, pullRequest.Number, member.Base); err != nil {
				outcome.Action, outcome.Detail = "failed", RedactString(err.Error())
			} else {
				outcome.Action, outcome.Detail = "retargeted", "onto "+member.Base
			}
		default:
			comment := "Closed by `wb stream end " + member.Branch + "`: the stream branch is being retired. " +
				"Reopen against " + member.Base + " if this work is still wanted."
			if err := engine.GitHub.ClosePullRequest(ctx, member.Worktree, pullRequest.Number, comment); err != nil {
				outcome.Action, outcome.Detail = "failed", RedactString(err.Error())
			} else {
				outcome.Action = "closed"
			}
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes
}

func (engine *Engine) retireMember(ctx context.Context, options EndOptions, name string, member Member) EndMemberResult {
	result := EndMemberResult{
		Repository: member.Repository, Worktree: member.Worktree,
		DraftPullRequest: member.PullRequest,
	}
	switch {
	case member.PullRequest == 0:
		result.DraftAction = "none"
	case !options.Apply:
		result.DraftAction = "would-close"
	default:
		comment := "Closed by `wb stream end " + name + "`: the stream is ending. Ending a stream publishes, bumps and merges nothing."
		if err := engine.GitHub.ClosePullRequest(ctx, member.Worktree, member.PullRequest, comment); err != nil {
			result.DraftAction, result.Detail = "failed", RedactString(err.Error())
		} else {
			result.DraftAction = "closed"
		}
	}
	if !options.Apply {
		return result
	}
	if member.Worktree == "" {
		// A member reserved but never published has no checkout to retire;
		// its lease is still released, which is the whole point of being able
		// to end a `creating` stream.
		result.LeaseReleased = true
		return result
	}
	// The remote stream branch goes before the local checkout: once the
	// worktree is gone there is no directory left to run the deletion from.
	// Deleting it is what makes "removes its own scaffolding" true of the
	// remote as well — and it is why the agent pull requests above had to be
	// closed or retargeted first, since GitHub silently retargets any that
	// are still open onto the base.
	if !options.KeepRemoteBranch {
		if err := engine.Git.DeleteRemoteBranch(ctx, member.Worktree, member.Branch); err != nil {
			result.Detail = strings.TrimSpace(result.Detail + " " + RedactString(err.Error()))
		} else {
			result.RemoteBranchDeleted = true
		}
	}
	if err := engine.Worktrees.Remove(ctx, name, member.Repository, member.Worktree); err != nil {
		result.Detail = strings.TrimSpace(result.Detail + " " + RedactString(err.Error()))
		return result
	}
	result.WorktreeRemoved = true
	result.LeaseReleased = true
	return result
}
