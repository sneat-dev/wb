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
}

// EndMemberResult is one member's retirement.
type EndMemberResult struct {
	Repository string `json:"repository"`
	Worktree   string `json:"worktree"`
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
// Implements: dependency-streams#req:stream-end-restores-published-state,
// dependency-streams#req:stream-end-proves-absorption-and-removes-its-own-scaffolding,
// dependency-streams#req:stream-end-removes-every-stream-worktree.
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

	var unabsorbed []string
	for _, member := range stream.Members {
		subjects, err := engine.Git.CommitsNotIn(ctx, member.Worktree, member.Branch, "origin/"+member.Base)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", member.Repository, err))
			continue
		}
		if len(subjects) == 0 {
			continue
		}
		unabsorbed = append(unabsorbed, fmt.Sprintf("%s has %d commit(s) origin/%s has not absorbed: %s",
			member.Repository, len(subjects), member.Base, strings.Join(collapsePatchIdenticalSubjects(subjects), "; ")))
	}
	if len(unabsorbed) > 0 {
		return result, &Refusal{
			Code:       RefusalUnabsorbedWork,
			Message:    "stream " + stream.Name + " carries work the base has not absorbed: " + strings.Join(unabsorbed, " | "),
			Sanctioned: []string{"wb stream status " + stream.Name, "wb worktree merge <worktree> --route auto"},
		}
	}

	for _, member := range stream.Members {
		result.AgentPullRequests = append(result.AgentPullRequests, engine.retireAgentPullRequests(ctx, options, member)...)
		result.Members = append(result.Members, engine.retireMember(ctx, options, stream.Name, member))
	}

	if !options.Apply {
		return result, nil
	}
	ended := engine.now()
	if _, err := engine.Store.Update(stream.Name, func(current *Stream) error {
		current.EndedAt = &ended
		for index := range current.Members {
			current.Members[index].Lease = Lease{}
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
			Detail: "could not enumerate pull requests targeting " + member.Branch + ": " + err.Error(),
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
			outcome.Action = "retargeted"
			outcome.Detail = "onto " + member.Base
			if err := engine.GitHub.RetargetPullRequest(ctx, member.Worktree, pullRequest.Number, member.Base); err != nil {
				outcome.Action, outcome.Detail = "failed", err.Error()
			}
		default:
			outcome.Action = "closed"
			comment := "Closed by `wb stream end " + member.Branch + "`: the stream branch is being retired. " +
				"Reopen against " + member.Base + " if this work is still wanted."
			if err := engine.GitHub.ClosePullRequest(ctx, member.Worktree, pullRequest.Number, comment); err != nil {
				outcome.Action, outcome.Detail = "failed", err.Error()
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
		result.DraftAction = "closed"
		comment := "Closed by `wb stream end " + name + "`: the stream is ending. Ending a stream publishes, bumps and merges nothing."
		if err := engine.GitHub.ClosePullRequest(ctx, member.Worktree, member.PullRequest, comment); err != nil {
			result.DraftAction, result.Detail = "failed", err.Error()
		}
	}
	if !options.Apply {
		return result
	}
	if err := engine.Worktrees.Remove(ctx, name, member.Repository, member.Worktree); err != nil {
		result.Detail = strings.TrimSpace(result.Detail + " " + err.Error())
		return result
	}
	result.WorktreeRemoved = true
	result.LeaseReleased = true
	return result
}
