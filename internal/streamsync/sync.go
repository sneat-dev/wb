package streamsync

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// AgentBranch is one agent's branch on the stream, known from the stream's
// claims and recorded membership — the operator never names them.
type AgentBranch struct {
	Branch string `json:"branch"`
	// Agent is the claiming session, so a conflict report can name who has to
	// resolve it.
	Agent string `json:"agent,omitempty"`
	// InReview marks a branch whose review is open. Rebasing it invalidates
	// the review, so sync refuses by default.
	InReview bool `json:"in_review,omitempty"`
}

// Options is one `wb stream sync` invocation.
type Options struct {
	Stream string
	// Worktree is the stream worktree the bumps are applied in. Bumps need a
	// checkout because the toolchain has to refresh go.sum / pnpm-lock.yaml.
	Worktree string
	// Repository is the member being synced, for reporting.
	Repository string
	// Branch is `stream/<name>`; Base is what it rebases onto.
	Branch string
	Base   string
	// Libraries are the own-library targets in scope for this sync.
	Libraries []Library
	// AgentBranches are the stream's open agent branches.
	AgentBranches []AgentBranch
	// Verify runs the batch verification after applying.
	Verify bool
	// AllowMidReview proceeds past a branch whose review is open, with a
	// warning. Rebasing it invalidates that review.
	AllowMidReview bool
	// PushTrigger and PushReason justify a push. Empty means no push, which
	// is the normal outcome: sync's whole point is that bumps stay local.
	// A justified trigger performs a REAL push, verified against the remote.
	PushTrigger PushTrigger
	PushReason  string
	// RecordedRemoteHead is the stream head WB last recorded, used as the
	// --force-with-lease expectation.
	RecordedRemoteHead string
	// Timeout bounds each verification run.
	Timeout time.Duration
}

// Bump actions are contract: the report and the exit code branch on them.
const (
	// BumpApplied means one commit was written.
	BumpApplied = "bumped"
	// BumpAtTarget means the consumer already requires the target — the
	// normal outcome when Renovate landed it first.
	BumpAtTarget = "already-at-target"
	// BumpNotRequired means the consumer does not declare the library.
	BumpNotRequired = "not-required"
	// BumpUnreadableVersion means WB could not compare the declared version.
	// It is deliberately NOT "already-at-target": no commit is written either
	// way, but claiming the consumer is at target is an assertion WB cannot
	// support, and a false assurance is worse than no answer.
	BumpUnreadableVersion = "version-unreadable"
	// BumpFailed means the manifest edit or the lockfile refresh failed.
	BumpFailed = "failed"
)

// BumpResult is one library's outcome.
type BumpResult struct {
	Library Library `json:"library"`
	// Action is one of the Bump* constants.
	Action string `json:"action"`
	// Required is the version the consumer required after the rebase.
	Required string `json:"required,omitempty"`
	Commit   string `json:"commit,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// RebaseResult is one branch's rebase outcome.
type RebaseResult struct {
	Branch    string   `json:"branch"`
	Agent     string   `json:"agent,omitempty"`
	Rebased   bool     `json:"rebased"`
	Conflicts []string `json:"conflicts,omitempty"`
	Detail    string   `json:"detail,omitempty"`
}

// Result is the whole sync report.
type Result struct {
	Stream     string `json:"stream"`
	Repository string `json:"repository"`
	// BaseBefore and BaseAfter show what the rebase moved onto.
	BaseBefore string `json:"base_before,omitempty"`
	BaseAfter  string `json:"base_after,omitempty"`
	// StreamRebase is the stream branch's own rebase onto the base.
	StreamRebase RebaseResult `json:"stream_rebase"`
	// AgentRebases are the agent branches, reported per branch: a conflict in
	// one must not stop the others.
	AgentRebases []RebaseResult `json:"agent_rebases,omitempty"`
	Bumps        []BumpResult   `json:"bumps"`
	// Batch is the single verification over the resulting tree.
	Batch *BatchResult `json:"batch,omitempty"`
	// Unpushed is what the operator sees accumulating locally.
	Unpushed UnpushedReport `json:"unpushed"`
	// Push is set only when a trigger justified one AND the push landed. It
	// carries the SHA the remote actually holds.
	Push *PushDecision `json:"push,omitempty"`
	// PushSkipped states, in words, that the remote was left untouched.
	PushSkipped string   `json:"push_skipped,omitempty"`
	Errors      []string `json:"errors,omitempty"`
}

// Failed reports whether the sync needs attention.
func (result Result) Failed() bool {
	if len(result.Errors) > 0 {
		return true
	}
	for _, bump := range result.Bumps {
		// A bump whose manifest edit or lockfile refresh failed is a failure.
		// Reporting exit 0 told the operator all was well while the tree
		// carried a half-applied manifest.
		if bump.Action == BumpFailed {
			return true
		}
	}
	if result.Batch != nil && !result.Batch.Passed {
		return true
	}
	for _, rebase := range result.AgentRebases {
		if len(rebase.Conflicts) > 0 {
			return true
		}
	}
	return !result.StreamRebase.Rebased
}

// Engine performs sync against injected ports.
type Engine struct {
	Git      Git
	Bumper   Bumper
	Verifier Verifier
	CI       CIMechanisms
	Events   Events
	Now      func() time.Time
}

// Sync brings the stream branch current and applies the batch, locally.
//
// The ORDER is the mechanism, not an implementation detail:
//
//  1. fetch, so the base is live rather than a session-start snapshot;
//  2. rebase the stream branch onto the fresh base — never merge;
//  3. rebase every agent branch onto the new stream head, per-branch;
//  4. THEN compare each library's required version against the target.
//
// Step 4 after step 2 is what makes sync idempotent against Renovate: a bump
// Renovate already landed is present in the tree after the rebase, so the
// required version is already at target and no commit is written. Comparing
// first would write a duplicate bump on top of one that already landed.
func (engine *Engine) Sync(ctx context.Context, options Options) (Result, error) {
	result, err := engine.sync(ctx, options)
	// ONE event per invocation, on EVERY exit. Writing events only on the
	// success paths left a timeline in which syncs simply stopped happening,
	// with no record of the conflict or refusal that stopped them.
	engine.recordOutcome(options, result, err)
	return result, err
}

func (engine *Engine) sync(ctx context.Context, options Options) (Result, error) {
	result := Result{Stream: options.Stream, Repository: options.Repository}

	// Fences before side effects: a branch under review must not be rebased
	// silently, because a rebase invalidates the review that pinned it.
	if !options.AllowMidReview {
		var inReview []string
		for _, agent := range options.AgentBranches {
			if agent.InReview {
				inReview = append(inReview, agent.Branch+" ("+agent.Agent+")")
			}
		}
		if len(inReview) > 0 {
			return result, &Refusal{
				Code: "review-in-progress",
				Message: "rebasing a branch under review invalidates the review that pinned its patch set: " +
					strings.Join(inReview, ", "),
				Sanctioned: []string{
					"wb stream sync " + options.Stream + " --allow-mid-review",
					"wait for the review to record its verdict",
				},
			}
		}
	}
	if clean, err := engine.Git.IsClean(ctx, options.Worktree); err != nil {
		return result, err
	} else if !clean {
		return result, &Refusal{
			Code:       "dirty-worktree",
			Message:    options.Worktree + " has uncommitted changes; sync rebases and commits, so it will not run over them",
			Sanctioned: []string{"commit or stash the changes, then re-run sync"},
		}
	}

	if err := engine.Git.Fetch(ctx, options.Worktree); err != nil {
		return result, fmt.Errorf("re-read origin before rebasing: %w", err)
	}
	upstream := "origin/" + options.Base
	before, err := engine.Git.Head(ctx, options.Worktree, options.Branch)
	if err != nil {
		return result, err
	}
	result.BaseBefore = before

	// 2. The stream branch rebases onto the fresh base. Never a merge: a merge
	// commit on a stream branch is what `never-merge-commit-a-stream-branch`
	// exists to prevent.
	conflicts, err := engine.Git.Rebase(ctx, options.Worktree, options.Branch, upstream)
	result.StreamRebase = RebaseResult{Branch: options.Branch, Rebased: err == nil && len(conflicts) == 0, Conflicts: conflicts}
	if err != nil {
		result.StreamRebase.Detail = err.Error()
		_ = engine.Git.AbortRebase(ctx, options.Worktree)
		result.Errors = append(result.Errors, "rebase "+options.Branch+" onto "+upstream+": "+err.Error())
		return result, nil
	}
	if len(conflicts) > 0 {
		_ = engine.Git.AbortRebase(ctx, options.Worktree)
		result.Errors = append(result.Errors, "the stream branch conflicts with "+upstream+"; resolve it before the agent branches can be rebased")
		return result, nil
	}
	head, err := engine.Git.Head(ctx, options.Worktree, options.Branch)
	if err != nil {
		return result, err
	}
	result.BaseAfter = head
	engine.record(options, "rebase", "success", "rebased "+options.Branch+" onto "+upstream, map[string]string{
		"before": before, "after": head,
	})

	// 3. Agent branches, per branch. One conflict must not abort the others.
	for _, agent := range options.AgentBranches {
		rebase := RebaseResult{Branch: agent.Branch, Agent: agent.Agent}
		agentConflicts, agentErr := engine.Git.Rebase(ctx, options.Worktree, agent.Branch, options.Branch)
		switch {
		case agentErr != nil:
			rebase.Detail = agentErr.Error()
			_ = engine.Git.AbortRebase(ctx, options.Worktree)
		case len(agentConflicts) > 0:
			rebase.Conflicts = agentConflicts
			_ = engine.Git.AbortRebase(ctx, options.Worktree)
		default:
			rebase.Rebased = true
		}
		result.AgentRebases = append(result.AgentRebases, rebase)
	}
	// The stream branch is the one the bumps are committed on; a conflicted
	// agent rebase must not leave the worktree on someone else's branch.
	if len(options.AgentBranches) > 0 {
		if err := engine.Git.Checkout(ctx, options.Worktree, options.Branch); err != nil {
			return result, err
		}
	}

	// 4. Bumps, compared AFTER the rebase.
	for _, library := range options.Libraries {
		result.Bumps = append(result.Bumps, engine.bump(ctx, options, library))
	}

	if options.Verify {
		batch, err := engine.VerifyBatch(ctx, options, engine.bumpCommits(result))
		if err != nil {
			return result, err
		}
		result.Batch = &batch
	}

	ahead, err := engine.Git.CommitsAhead(ctx, options.Worktree, options.Branch, "origin/"+options.Branch)
	if err == nil {
		result.Unpushed = UnpushedReport{Repository: options.Repository, Branch: options.Branch, Commits: ahead}
	}

	// Nothing above pushed. A sync with no trigger says so rather than
	// leaving the operator to infer it from the absence of output.
	if options.PushTrigger == "" {
		result.PushSkipped = fmt.Sprintf(
			"the remote was left untouched: %d local commit(s) on %s, and a dependency bump is not a push trigger. "+
				"A push happens on landing, review, park, or --push --reason.",
			result.Unpushed.Commits, options.Branch)
		return result, nil
	}
	decision, err := JustifyPush(options.PushTrigger, options.PushReason)
	if err != nil {
		return result, err
	}
	// The push HAPPENS. Setting result.Push and writing a success event
	// without pushing reported an effect that did not exist.
	sha, pushErr := engine.Git.PushWithLease(ctx, options.Worktree, options.Branch, options.RecordedRemoteHead)
	if pushErr != nil {
		result.Errors = append(result.Errors, "push refused or failed: "+pushErr.Error())
		engine.record(options, "push", "findings", pushErr.Error(), map[string]string{
			"trigger": string(decision.Trigger), "reason": decision.Reason,
		})
		return result, nil
	}
	decision.SHA = sha
	result.Push = &decision
	engine.record(options, "push", "success", "pushed "+options.Branch, map[string]string{
		"trigger": string(decision.Trigger), "reason": decision.Reason, "sha": sha,
	})
	return result, nil
}

// bump writes at most one commit for one library.
func (engine *Engine) bump(ctx context.Context, options Options, library Library) BumpResult {
	outcome := BumpResult{Library: library}
	required, found, err := engine.Bumper.Required(ctx, options.Worktree, library)
	if err != nil {
		outcome.Action, outcome.Detail = "failed", err.Error()
		return outcome
	}
	outcome.Required = required
	if !found {
		outcome.Action = BumpNotRequired
		return outcome
	}
	if !comparable(required, library.Target) {
		outcome.Action = BumpUnreadableVersion
		outcome.Detail = fmt.Sprintf(
			"cannot compare declared %q against target %q, so no bump was written; WB will not rewrite a manifest on a comparison it could not make",
			required, library.Target)
		return outcome
	}
	// The comparison that makes sync idempotent: a version already at or above
	// the target gets no commit, so a bump Renovate landed is neither
	// rewritten nor reverted, and a second sync writes nothing at all.
	if !below(required, library.Target) {
		outcome.Action = BumpAtTarget
		return outcome
	}
	// The pre-apply head is captured so a failed apply can be undone. A
	// half-applied manifest left on disk makes the NEXT sync refuse as
	// dirty and tell the operator to commit a bump whose lockfile never
	// refreshed — the poisoned commit the batch model exists to prevent.
	before, headErr := engine.Git.Head(ctx, options.Worktree, "HEAD")
	if err := engine.Bumper.Apply(ctx, options.Worktree, library); err != nil {
		outcome.Action, outcome.Detail = BumpFailed, err.Error()
		engine.restoreAfterFailedBump(ctx, options, &outcome, before, headErr)
		return outcome
	}
	sha, committed, err := engine.Git.CommitAll(ctx, options.Worktree, BumpMessage(library, required))
	if err != nil {
		outcome.Action, outcome.Detail = BumpFailed, err.Error()
		engine.restoreAfterFailedBump(ctx, options, &outcome, before, headErr)
		return outcome
	}
	if !committed {
		// Apply changed nothing, so there is nothing to record. Writing an
		// empty commit here would make a re-run non-idempotent.
		outcome.Action = BumpAtTarget
		return outcome
	}
	outcome.Action, outcome.Commit = BumpApplied, sha
	engine.record(options, "bump", "success", BumpMessage(library, required), map[string]string{
		"library": library.Name, "from": required, "to": library.Target, "commit": sha,
	})
	return outcome
}

// BumpMessage formats one bump commit to the repository's own convention.
func BumpMessage(library Library, from string) string {
	return fmt.Sprintf("fix(deps): %s %s → %s", library.Name, from, library.Target)
}

// restoreAfterFailedBump returns the worktree to the state sync found it in.
//
// Leaving the partial edit behind is worse than the failed bump: the next sync
// refuses as dirty and its sanctioned command tells the operator to commit it.
func (engine *Engine) restoreAfterFailedBump(ctx context.Context, options Options, outcome *BumpResult, before string, headErr error) {
	if headErr != nil || before == "" {
		outcome.Detail += "; the pre-bump head could not be read, so the worktree was NOT restored — inspect it before re-running"
		return
	}
	if err := engine.Git.RestoreTo(ctx, options.Worktree, before); err != nil {
		outcome.Detail += "; the worktree could not be restored to " + before + ": " + err.Error()
		return
	}
	outcome.Detail += "; the worktree was restored to " + before
}

func (engine *Engine) bumpCommits(result Result) []Element {
	elements := make([]Element, 0, len(result.Bumps))
	for _, bump := range result.Bumps {
		if bump.Action != BumpApplied || bump.Commit == "" {
			continue
		}
		elements = append(elements, Element{
			Name: bump.Library.Name, SHA: bump.Commit,
			Description: BumpMessage(bump.Library, bump.Required),
		})
	}
	return elements
}

// recordOutcome writes the invocation's single terminal event.
func (engine *Engine) recordOutcome(options Options, result Result, err error) {
	outcome, detail := "success", ""
	evidence := map[string]string{}
	var refusal *Refusal
	switch {
	case errors.As(err, &refusal):
		outcome, detail = "refused", refusal.Message
		evidence["refusal_code"] = refusal.Code
	case err != nil:
		outcome, detail = "findings", err.Error()
	case result.Failed():
		outcome = "findings"
		detail = strings.Join(result.failureSummary(), "; ")
	default:
		detail = fmt.Sprintf("%d bump(s), %d agent branch(es)", len(result.Bumps), len(result.AgentRebases))
	}
	evidence["unpushed"] = strconv.Itoa(result.Unpushed.Commits)
	if result.Push != nil {
		evidence["pushed"] = result.Push.SHA
	}
	engine.record(options, "complete", outcome, detail, evidence)
}

// failureSummary names every reason the invocation needs attention, so the
// terminal event says which of them fired rather than only that one did.
func (result Result) failureSummary() []string {
	var reasons []string
	reasons = append(reasons, result.Errors...)
	for _, rebase := range result.AgentRebases {
		if len(rebase.Conflicts) > 0 {
			reasons = append(reasons, rebase.Branch+" conflicts: "+strings.Join(rebase.Conflicts, ", "))
		}
	}
	for _, bump := range result.Bumps {
		if bump.Action == BumpFailed {
			reasons = append(reasons, bump.Library.Name+" bump failed: "+bump.Detail)
		}
	}
	if result.Batch != nil && !result.Batch.Passed {
		reasons = append(reasons, "batch verification failed")
	}
	if !result.StreamRebase.Rebased {
		reasons = append(reasons, "the stream branch was not rebased")
	}
	return reasons
}

func (engine *Engine) record(options Options, phase, outcome, detail string, evidence map[string]string) {
	if engine.Events == nil {
		return
	}
	_ = engine.Events.Append(Event{
		Stream: options.Stream, Verb: "stream sync", Phase: phase,
		Repository: options.Repository, Outcome: outcome, Detail: detail, Evidence: evidence,
	})
}
